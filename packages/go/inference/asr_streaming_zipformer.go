// Streaming Zipformer 中英双语流式转写引擎（sherpa-onnx 生态 k2-fsa 导出档）：
// sherpa-onnx-streaming-zipformer-bilingual-zh-en-2023-02-20
// （Apache-2.0；基座 pfluo/k2fsa-zipformer-chinese-english-mixed，
// icefall pruned_transducer_stateless7_streaming 训练；encoder int8 + decoder fp32
// + joiner int8），经 yalue/onnxruntime_go 绑定驱动（与 FireRedASR2 同装配惯例）。
// 解码语义对齐 sherpa-onnx OnlineRecognizer transducer 贪心（Python ORT 原型
// 与 FireRed golden 逐字对拍，见 asr_streaming_test.go）：
//
//	wav(PCM16) → [-1,1] 量纲 → fbank(80, knf 口径, 流式分帧, 无 CMVN——该模型
//    训练即归一化波形的裸 log-mel，与 FireRedASR2 int16 量纲相反, 见 pcm16ToUnitDomain)
//	  → encoder 固定块：元数据 T=39 / decode_chunk_len=32，即首块 39 新帧、
//	    后续每块 = 上块尾 7 帧（输入 4:1 下采样左上下文）+ 32 新帧，
//	    出 8 个下采样位置（encoder_out [1,8,512]），分层缓存 Run 间回喂
//	    （cached_{len,avg,key,val,val2,conv1,2} × 5 栈，new_* 输出直接接管为下块输入）
//	  → 逐位置 transducer 贪心：joiner(enc_frame, decoder_out) argmax，
//	    非 blank(<blk>=0) 发射 token 并滑动 decoder 上下文窗 y=[y₀,y₁]→[y₁,tok]
//	    （context_size=2，初始 [0,0]），decoder 输出 [1,512]
//	  → Finish 尾帧不足一块时零填充到 39 帧后编码定稿
//
// 实现 ASREngine（Recognize 签名不变）：整句入、按 40ms 块走流式路径出整句文本
// ——与增量消费方（FeedChunk/Finish）完全同代码路径，PoC 实测口径即可上线口径。
// FireRedASR2（asr.go）保留为备选实现，二者可经同一接口互换。
// 模型/库缺失时构造不失败——降级桩（同 FireRedASR2 惯例），Err()/InFallback() 查询。
// 限制（PoC 口径）：仅 16kHz；贪心解码；端点检测由消费方（VAD hangover）驱动，
// Finish() 只做定稿刷新，不含静音判定。
package inference

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	ort "github.com/yalue/onnxruntime_go"
)

const (
	// zipformerBlank 转导出 blank token id（tokens.txt 首行 <blk> 0）。
	zipformerBlank = int64(0)
	// feedChunkBytes Recognize 内部喂入步长：40ms（640 样本 × PCM16 2B）。
	feedChunkBytes = fbankSampleRate * 2 * 40 / 1000
	// feedChunkMs 与 feedChunkBytes 对应的块时长（实时节奏口径对齐用）。
	feedChunkMs = 40 * time.Millisecond
)

// StreamingZipformer 流式识别引擎（实现 ASREngine；单线程驱动，同 T3 契约不加锁）。
type StreamingZipformer struct {
	fb     *fbank
	stream *fbankStream
	tokens map[int64]string
	vocab  int
	encDim int // encoder_out 尾维（512，取自 ONNX 声明）
	chunkT int // encoder 固定输入帧数（39，取自 x 声明）
	reuseT int // 每块复用的左上下文帧数（7 = T - decode_chunk_len）

	encSess *ort.DynamicAdvancedSession
	decSess *ort.DynamicAdvancedSession
	joiSess *ort.DynamicAdvancedSession

	encInNames  []string
	encOutNames []string
	encDecl     []ort.InputOutputInfo // 缓存输入声明（造零值初始缓存用）
	encCache    []ort.Value           // 与 encInNames[1:] 对齐（Run 后被 new_* 输出接管）
	x           *ort.Tensor[float32]  // [1, chunkT, 80]

	decY   *ort.Tensor[int64]   // [1,2] 上下文窗
	decOut *ort.Tensor[float32] // [1,512]
	jEnc   *ort.Tensor[float32] // joiner encoder_out 输入 [1,512]
	logit  *ort.Tensor[float32] // [1,vocab]

	// 流式状态
	feats   []float32 // 待消费特征（行主序）
	tail    []float32 // 上块尾 reuseT 帧（首块前 nil）
	started bool      // 已编码过至少一块
	toks    []int64   // 已发射 token（部分文本 = join(toks)）

	// 桩 fallback（模型/库不可用时非 nil）
	stub    *fireRedStub
	initErr error

	// 观测面（当前轮：Reset 起）
	turnBegin    time.Time
	firstTokenAt time.Time
	encWall      time.Duration
	joinWall     time.Duration
	decWall      time.Duration
	finWall      time.Duration
}

// NewStreamingZipformer 构造流式 ASR 引擎（encoder/decoder/joiner 为 ONNX 模型路径，
// tokens.txt 取 decoder 同目录；签名风格与 NewFireRedASR2 一致）。
// 初始化失败不报错——降级桩实现，原因经 Err() 查询。
func NewStreamingZipformer(encoderPath, decoderPath, joinerPath string) *StreamingZipformer {
	z := &StreamingZipformer{}
	z.initErr = z.init(encoderPath, decoderPath, joinerPath)
	if z.initErr != nil {
		z.stub = &fireRedStub{}
		z.destroyPartial()
	}
	return z
}

// DefaultStreamingASRModelDir 定位流式 zipformer 模型目录：
// env T14_STREAM_ASR_MODEL_DIR → 数据集落盘路径。
func DefaultStreamingASRModelDir() string {
	if p := os.Getenv("T14_STREAM_ASR_MODEL_DIR"); p != "" {
		return p
	}
	return "/root/workspace/datasets/models/streaming-zipformer-zh-en-2023-02-20"
}

func (z *StreamingZipformer) init(encoderPath, decoderPath, joinerPath string) error {
	if err := initORT(); err != nil {
		return fmt.Errorf("inference: zipformer: onnxruntime 初始化失败: %w", err)
	}
	if z.fb = newFbank(); z.fb == nil {
		return fmt.Errorf("inference: zipformer: fbank 初始化失败")
	}
	z.stream = newFbankStream(z.fb)
	var err error
	if z.tokens, z.vocab, err = loadTokens(filepath.Join(filepath.Dir(decoderPath), "tokens.txt")); err != nil {
		return err
	}
	if err := z.initEncoder(encoderPath); err != nil {
		return err
	}
	opts, err := newSessionOpts(0)
	if err != nil {
		return err
	}
	defer opts.Destroy()
	z.decSess, err = ort.NewDynamicAdvancedSession(decoderPath,
		[]string{"y"}, []string{"decoder_out"}, opts)
	if err != nil {
		return fmt.Errorf("inference: zipformer: 加载 decoder %s: %w", decoderPath, err)
	}
	z.joiSess, err = ort.NewDynamicAdvancedSession(joinerPath,
		[]string{"encoder_out", "decoder_out"}, []string{"logit"}, opts)
	if err != nil {
		return fmt.Errorf("inference: zipformer: 加载 joiner %s: %w", joinerPath, err)
	}
	if z.decY, err = ort.NewTensor[int64](ort.Shape{1, 2}, []int64{0, 0}); err != nil {
		return err
	}
	if z.decOut, err = ort.NewTensor[float32](ort.Shape{1, int64(z.encDim)}, make([]float32, z.encDim)); err != nil {
		return err
	}
	if z.jEnc, err = ort.NewTensor[float32](ort.Shape{1, int64(z.encDim)}, make([]float32, z.encDim)); err != nil {
		return err
	}
	// logit 尾维取 joiner 声明（int8 导出可能带量化尾巴：6254→6257），
	// argmax 限扫 tokens 词表范围、越界按 blank 终结（见 joinArgmax）。
	logitDim := z.vocab
	if _, jouts, err := ort.GetInputOutputInfo(joinerPath); err != nil {
		return fmt.Errorf("inference: zipformer: 读 joiner 声明: %w", err)
	} else {
		for _, o := range jouts {
			if o.Name == "logit" && len(o.Dimensions) == 2 && o.Dimensions[1] > 0 {
				logitDim = int(o.Dimensions[1])
			}
		}
	}
	if z.logit, err = ort.NewTensor[float32](ort.Shape{1, int64(logitDim)}, make([]float32, logitDim)); err != nil {
		return err
	}
	return z.resetTurn()
}

// initEncoder 按 ONNX 声明装配 encoder 会话与初始缓存：io 名字公式化
// （cached_{len,avg,key,val,val2,conv1,2}_{i}，与 new_* 输出同相对序，Run 后按下标
// 接管回喂）；形状免元数据直接取自声明（GetInputOutputInfo 免会话探明）。
// 块语义经 Python 原型实证（见文件头）：首块 T=39 帧、后续每块 7 复用 + 32 新帧。
func (z *StreamingZipformer) initEncoder(encoderPath string) error {
	ins, outs, err := ort.GetInputOutputInfo(encoderPath)
	if err != nil {
		return fmt.Errorf("inference: zipformer: 读 encoder 声明: %w", err)
	}
	for i := range ins {
		if ins[i].Name != "x" {
			continue
		}
		if len(ins[i].Dimensions) != 3 {
			return fmt.Errorf("inference: zipformer: x 形状 %v 非 [N,T,C]", ins[i].Dimensions)
		}
		z.chunkT = int(ins[i].Dimensions[1])
		if int(ins[i].Dimensions[2]) != fbankNumBins {
			return fmt.Errorf("inference: zipformer: x 尾维 %d ≠ 80（非 80 维 fbank 导出？）", ins[i].Dimensions[2])
		}
	}
	if z.chunkT == 0 {
		return fmt.Errorf("inference: zipformer: 未找到输入 x（非流式 zipformer 导出？）")
	}
	z.reuseT = 7 // T - decode_chunk_len(32)，该导出档固定值（元数据 decode_chunk_len=32）
	for _, o := range outs {
		if o.Name == "encoder_out" {
			z.encDim = int(o.Dimensions[len(o.Dimensions)-1])
		}
	}
	if z.encDim <= 0 {
		return fmt.Errorf("inference: zipformer: encoder_out 声明异常")
	}
	numStacks := 0
	for _, in := range ins {
		if strings.HasPrefix(in.Name, "cached_len_") {
			numStacks++
		}
	}
	if numStacks == 0 {
		return fmt.Errorf("inference: zipformer: 未发现 cached_len_* 输入（非流式 zipformer 导出？）")
	}
	z.encInNames = []string{"x"}
	z.encOutNames = []string{"encoder_out"}
	for _, kind := range []string{"len", "avg", "key", "val", "val2", "conv1", "conv2"} {
		for i := 0; i < numStacks; i++ {
			z.encInNames = append(z.encInNames, fmt.Sprintf("cached_%s_%d", kind, i))
			z.encOutNames = append(z.encOutNames, fmt.Sprintf("new_cached_%s_%d", kind, i))
		}
	}
	z.encDecl = make([]ort.InputOutputInfo, 0, len(z.encInNames)-1)
	for _, in := range ins {
		if strings.HasPrefix(in.Name, "cached_") {
			z.encDecl = append(z.encDecl, in)
		}
	}
	opts, err := newSessionOpts(0)
	if err != nil {
		return err
	}
	defer opts.Destroy()
	z.encSess, err = ort.NewDynamicAdvancedSession(encoderPath, z.encInNames, z.encOutNames, opts)
	if err != nil {
		return fmt.Errorf("inference: zipformer: 加载 encoder %s: %w", encoderPath, err)
	}
	if z.x, err = ort.NewTensor[float32](ort.Shape{1, int64(z.chunkT), int64(fbankNumBins)},
		make([]float32, z.chunkT*fbankNumBins)); err != nil {
		return err
	}
	return z.resetEncoderCache()
}

// resetEncoderCache 按 ONNX 声明造零值初始缓存（声明中的字面维度照抄，动态维度取 1）。
// cached_len_* 为 int64、其余 float32，与该导出档声明一致。
func (z *StreamingZipformer) resetEncoderCache() error {
	z.destroyEncoderCache()
	z.encCache = make([]ort.Value, 0, len(z.encDecl))
	for _, in := range z.encDecl {
		shape := make(ort.Shape, len(in.Dimensions))
		n := int64(1)
		for i, d := range in.Dimensions {
			if d <= 0 {
				d = 1 // 动态维（N、缓存帧容量按声明字面值缺席时）
			}
			shape[i] = d
			n *= d
		}
		var v ort.Value
		var err error
		if in.DataType == ort.TensorElementDataTypeInt64 {
			v, err = ort.NewTensor[int64](shape, make([]int64, n))
		} else {
			v, err = ort.NewTensor[float32](shape, make([]float32, n))
		}
		if err != nil {
			return err
		}
		z.encCache = append(z.encCache, v)
	}
	return nil
}

func (z *StreamingZipformer) destroyEncoderCache() {
	for _, v := range z.encCache {
		if v != nil {
			v.Destroy()
		}
	}
	z.encCache = nil
}

// resetTurn 清流式状态并复位 decoder/encoder 缓存（新一轮话轮起点，时钟从此起）。
func (z *StreamingZipformer) resetTurn() error {
	z.feats = z.feats[:0]
	z.tail = nil
	z.started = false
	z.toks = z.toks[:0]
	z.turnBegin = time.Now()
	z.firstTokenAt = time.Time{}
	z.encWall, z.joinWall, z.decWall, z.finWall = 0, 0, 0, 0
	z.decY.GetData()[0], z.decY.GetData()[1] = 0, 0
	// decoder_out 与 y=[0,0] 对齐（blank 起步投影）
	if err := z.decSess.Run([]ort.Value{z.decY}, []ort.Value{z.decOut}); err != nil {
		return fmt.Errorf("inference: zipformer: decoder 复位: %w", err)
	}
	return z.resetEncoderCache()
}

// Reset 结束当前话轮并复位流式状态（消费方在端点定稿后调用；时钟重启）。
func (z *StreamingZipformer) Reset() error {
	if z.stub != nil {
		return nil
	}
	return z.resetTurn()
}

// Recognize 实现 ASREngine：PCM16LE 单声道 16kHz 整句 → 中文文本（内部按 40ms 块
// 走流式路径 + Finish 定稿；▁ 与 sherpa-onnx 输出口径一致保留原样）。
func (z *StreamingZipformer) Recognize(stream []byte) (string, error) {
	if z.stub != nil {
		return z.stub.Recognize(stream)
	}
	if err := z.Reset(); err != nil {
		return "", err
	}
	for off := 0; off < len(stream); off += feedChunkBytes {
		end := off + feedChunkBytes
		if end > len(stream) {
			end = len(stream)
		}
		if _, err := z.FeedChunk(stream[off:end]); err != nil {
			return "", err
		}
	}
	return z.Finish()
}

// FeedChunk 增量喂入 PCM16 音频（任意步长；内部满一块 39/32 帧即编码解码一次），
// 返回当前部分文本（话轮内单调增长）。
func (z *StreamingZipformer) FeedChunk(pcm16 []byte) (string, error) {
	if z.stub != nil {
		return z.stub.Recognize(pcm16)
	}
	z.stream.push(pcm16ToUnitDomain(pcm16), &z.feats)
	if err := z.drainPending(); err != nil {
		return "", err
	}
	return z.Text(), nil
}

// pcm16ToUnitDomain PCM16LE → [-1,1] 量纲 float32（icefall/sherpa 流式模型训练口径，
// 即 torchaudio 加载的归一化波形——与 FireRedASR2 的 int16 量纲相差 2·ln32768≈20.7
// 的 log-mel 偏移，两引擎不可互喂，实测见 reports/eval/T14/latency-report.md）。
func pcm16ToUnitDomain(data []byte) []float32 {
	n := len(data) / 2
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		out[i] = float32(int16(binary.LittleEndian.Uint16(data[2*i:]))) / 32768
	}
	return out
}

// drainPending 编码解码所有满块：首块 39 新帧，后续每块 7 复用 + 32 新帧。
func (z *StreamingZipformer) drainPending() error {
	frames := z.pendingFrames()
	for {
		need := z.chunkT
		if z.started {
			need = z.chunkT - z.reuseT
		}
		if frames < need {
			return nil
		}
		chunk := make([]float32, z.chunkT*fbankNumBins)
		used := need
		if z.started {
			copy(chunk, z.tail)
			copy(chunk[z.reuseT*fbankNumBins:], z.feats)
		} else {
			copy(chunk, z.feats)
		}
		z.tail = append(z.tail[:0], chunk[(z.chunkT-z.reuseT)*fbankNumBins:]...)
		z.started = true
		copy(z.feats, z.feats[used*fbankNumBins:])
		z.feats = z.feats[:len(z.feats)-used*fbankNumBins]
		frames -= used
		if err := z.encodeDecodeChunk(chunk); err != nil {
			return err
		}
	}
}

// encodeDecodeChunk 编码一块特征（x + 缓存回喂，new_* 输出接管为下块缓存）
// 并对每个输出位置做 transducer 贪心。
func (z *StreamingZipformer) encodeDecodeChunk(chunk []float32) error {
	copy(z.x.GetData(), chunk)
	inputs := make([]ort.Value, 0, len(z.encInNames))
	inputs = append(inputs, z.x)
	inputs = append(inputs, z.encCache...)
	outs := make([]ort.Value, len(z.encOutNames))
	encBegin := time.Now()
	if err := z.encSess.Run(inputs, outs); err != nil {
		return fmt.Errorf("inference: zipformer: encoder Run: %w", err)
	}
	z.encWall += time.Since(encBegin)
	encOut := outs[0]
	defer encOut.Destroy()
	z.destroyEncoderCache()
	z.encCache = outs[1:]
	encData := encOut.(*ort.Tensor[float32]).GetData()
	for t := 0; t*z.encDim < len(encData); t++ {
		copy(z.jEnc.GetData(), encData[t*z.encDim:(t+1)*z.encDim])
		best, err := z.joinArgmax()
		if err != nil {
			return err
		}
		for best != zipformerBlank {
			z.toks = append(z.toks, best)
			if z.firstTokenAt.IsZero() {
				z.firstTokenAt = time.Now()
			}
			y := z.decY.GetData()
			y[0], y[1] = y[1], best
			decBegin := time.Now()
			if err := z.decSess.Run([]ort.Value{z.decY}, []ort.Value{z.decOut}); err != nil {
				return fmt.Errorf("inference: zipformer: decoder Run: %w", err)
			}
			z.decWall += time.Since(decBegin)
			if best, err = z.joinArgmax(); err != nil {
				return err
			}
		}
	}
	return nil
}

// joinArgmax 当前 (enc_frame, decoder_out) 的最优 token（贪心；int8 joiner 的
// 量化尾巴维度在 tokens 词表之外，越界最优视作 blank 终结本帧）。
func (z *StreamingZipformer) joinArgmax() (int64, error) {
	begin := time.Now()
	if err := z.joiSess.Run([]ort.Value{z.jEnc, z.decOut}, []ort.Value{z.logit}); err != nil {
		return 0, fmt.Errorf("inference: zipformer: joiner Run: %w", err)
	}
	z.joinWall += time.Since(begin)
	logits := z.logit.GetData()
	if len(logits) > z.vocab {
		logits = logits[:z.vocab]
	}
	best, bestV := int64(0), float32(-math.MaxFloat32)
	for i, v := range logits {
		if v > bestV {
			bestV, best = v, int64(i)
		}
	}
	return best, nil
}

// Finish 端点定稿：剩余不足一块的尾帧零填充到整块后编码解码，返回最终文本
// （无剩余尾帧时直接返回当前文本；耗时即定稿延迟观测面 finWall）。
func (z *StreamingZipformer) Finish() (string, error) {
	if z.stub != nil {
		return z.stub.Recognize(nil)
	}
	begin := time.Now()
	defer func() { z.finWall += time.Since(begin) }()
	if frames := z.pendingFrames(); frames > 0 || !z.started {
		chunk := make([]float32, z.chunkT*fbankNumBins) // 零填充
		off := 0
		if z.started {
			off = copy(chunk, z.tail)
		}
		copy(chunk[off:], z.feats)
		z.tail = append(z.tail[:0], chunk[(z.chunkT-z.reuseT)*fbankNumBins:]...)
		z.started = true
		z.feats = z.feats[:0]
		if err := z.encodeDecodeChunk(chunk); err != nil {
			return "", err
		}
	}
	return z.Text(), nil
}

// Text 当前部分文本（话轮内单调增长）。
func (z *StreamingZipformer) Text() string {
	var b strings.Builder
	for _, id := range z.toks {
		b.WriteString(z.tokens[id])
	}
	return b.String()
}

// FirstTokenLatency 自话轮起点（Reset）到首个非 blank token 发射的墙钟；
// 未出字返回 false。
func (z *StreamingZipformer) FirstTokenLatency() (time.Duration, bool) {
	if z.firstTokenAt.IsZero() {
		return 0, false
	}
	return z.firstTokenAt.Sub(z.turnBegin), true
}

// Walls 当前轮累计推理墙钟（encoder/joiner/decoder；RTF 分解观测面）。
func (z *StreamingZipformer) Walls() (enc, join, dec time.Duration) {
	return z.encWall, z.joinWall, z.decWall
}

// FinishWall 最近一次 Finish 的墙钟（定稿延迟观测面）。
func (z *StreamingZipformer) FinishWall() time.Duration { return z.finWall }

// Err 构造期错误（nil=真推理就绪；非 nil=已降级桩）。
func (z *StreamingZipformer) Err() error { return z.initErr }

// InFallback 是否运行在桩降级模式。
func (z *StreamingZipformer) InFallback() bool { return z.stub != nil }

// Destroy 释放会话（进程退出面调用一次即可）。
func (z *StreamingZipformer) Destroy() error {
	if z.encSess != nil {
		if err := z.encSess.Destroy(); err != nil {
			return err
		}
	}
	z.destroyPartial()
	return nil
}

func (z *StreamingZipformer) destroyPartial() {
	for _, s := range []*ort.DynamicAdvancedSession{z.encSess, z.decSess, z.joiSess} {
		if s != nil {
			s.Destroy()
		}
	}
	z.encSess, z.decSess, z.joiSess = nil, nil, nil
	z.destroyEncoderCache()
	for _, t := range []*ort.Tensor[float32]{z.x, z.decOut, z.jEnc, z.logit} {
		if t != nil {
			t.Destroy()
		}
	}
	if z.decY != nil {
		z.decY.Destroy()
	}
	z.x, z.decY, z.decOut, z.jEnc, z.logit = nil, nil, nil, nil, nil
}

func (z *StreamingZipformer) pendingFrames() int { return len(z.feats) / fbankNumBins }
