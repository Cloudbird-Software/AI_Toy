// FireRedASR2 真推理引擎：sherpa-onnx 导出的 FireRedASR2-AED int8 ONNX
// （Apache 2.0，encoder+decoder 两阶段）经 yalue/onnxruntime_go 绑定驱动
// （与 T3 vap 同一装配惯例）。解码语义对齐 sherpa-onnx v1.12.13
// OfflineRecognizerFireRedAsrImpl + OfflineFireRedAsrGreedySearchDecoder：
//
//	wav(PCM16) → int16 量纲 fbank(80, knf 口径, 见 fbank.go)
//	  → encoder 元数据全局 CMVN (x-mean)*inv_stddev
//	  → encoder(x[T,80], x_len) → cross_k/v [16,1,T,1280]
//	  → decoder 逐 token 贪心：tokens[1,1] + self KV cache [16,1,C,20,64]
//	    + offset 位置偏移，argmax 至 <eos>（sos=3/eos=4 取自模型元数据）。
//
// 模型/库缺失时构造不失败——引擎降级为 M1 桩（asr_stub.go），
// Err()/InFallback() 暴露降级原因（消费方面向接口，行为不变）。
// 限制（PoC 口径）：仅 16kHz；非流式整句识别；贪心解码。
package inference

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	ort "github.com/yalue/onnxruntime_go"
)

const (
	// fireredCacheLen 自注意 KV cache 长度（token 上限）。sherpa 用 max_len=1024
	// （2×85MB float）；本机 7.4G 内存 PoC 取 256（≈42s 音频），并作为解码步数硬顶。
	fireredCacheLen = 256
	// fireredVocabDefault vocab 兜底值（tokens.txt 行数为准：FireRedASR2 = 8667）。
	fireredVocabDefault = 8667
)

// FireRedASR2 流式识别引擎（实现 ASREngine；单线程驱动，同 T3 契约不加锁）。
type FireRedASR2 struct {
	fb       *fbank
	tokens   map[int64]string
	vocab    int
	mean     []float32
	inv      []float32
	sos, eos int64
	maxLen   int

	encSess *ort.DynamicAdvancedSession
	decSess *ort.DynamicAdvancedSession

	token  *ort.Tensor[int64]   // [1,1]
	offset *ort.Tensor[int64]   // [1]
	logits *ort.Tensor[float32] // [1,1,vocab]
	kcIn   *ort.Tensor[float32] // self k cache [16,1,C,20,64]
	vcIn   *ort.Tensor[float32]
	kcOut  *ort.Tensor[float32] // 出侧缓存（步间与 in 侧指针互换，零拷贝）
	vcOut  *ort.Tensor[float32]

	// 桩 fallback（模型/库不可用时非 nil）
	stub    *fireRedStub
	initErr error

	// RTF 观测面（最近一次 Recognize 的 encoder/decoder 推理墙钟）
	lastEncWall, lastDecWall time.Duration
}

// NewFireRedASR2 构造 ASR 引擎（encoder/decoder 为 ONNX 模型路径，签名与 M1 桩一致；
// tokens.txt 取 decoder 同目录）。初始化失败不报错——降级桩实现，原因经 Err() 查询。
func NewFireRedASR2(encoderPath, decoderPath string) *FireRedASR2 {
	f := &FireRedASR2{}
	f.initErr = f.init(encoderPath, decoderPath)
	if f.initErr != nil {
		f.stub = &fireRedStub{}
		f.destroyPartial()
	}
	return f
}

func (f *FireRedASR2) init(encoderPath, decoderPath string) error {
	if err := initORT(); err != nil {
		return fmt.Errorf("inference: asr: onnxruntime 初始化失败: %w", err)
	}
	var err error
	if f.fb = newFbank(); f.fb == nil {
		return fmt.Errorf("inference: asr: fbank 初始化失败")
	}
	f.tokens, f.vocab, err = loadTokens(filepath.Join(filepath.Dir(decoderPath), "tokens.txt"))
	if err != nil {
		return err
	}
	if f.sos, f.eos, f.maxLen, f.mean, f.inv, err = loadEncoderMeta(encoderPath); err != nil {
		return err
	}
	opts, err := newSessionOpts(0)
	if err != nil {
		return err
	}
	defer opts.Destroy()
	f.encSess, err = ort.NewDynamicAdvancedSession(encoderPath,
		[]string{"x", "x_len"},
		[]string{"n_layer_cross_k", "n_layer_cross_v"}, opts)
	if err != nil {
		return fmt.Errorf("inference: asr: 加载 encoder %s: %w", encoderPath, err)
	}
	f.decSess, err = ort.NewDynamicAdvancedSession(decoderPath,
		[]string{"tokens", "in_n_layer_self_k_cache", "in_n_layer_self_v_cache",
			"n_layer_cross_k", "n_layer_cross_v", "offset"},
		[]string{"logits", "out_n_layer_self_k_cache", "out_n_layer_self_v_cache"}, opts)
	if err != nil {
		return fmt.Errorf("inference: asr: 加载 decoder %s: %w", decoderPath, err)
	}
	nl, nh, hd := 16, 20, 64
	cacheShape := ort.Shape{int64(nl), 1, fireredCacheLen, int64(nh), int64(hd)}
	mkF := func(t **ort.Tensor[float32], s ort.Shape) error {
		*t, err = ort.NewTensor[float32](s, make([]float32, s.FlattenedSize()))
		return err
	}
	if err := mkF(&f.kcIn, cacheShape); err != nil {
		return err
	}
	if err := mkF(&f.vcIn, cacheShape); err != nil {
		return err
	}
	if err := mkF(&f.kcOut, cacheShape); err != nil {
		return err
	}
	if err := mkF(&f.vcOut, cacheShape); err != nil {
		return err
	}
	if f.token, err = ort.NewTensor[int64](ort.Shape{1, 1}, make([]int64, 1)); err != nil {
		return err
	}
	if f.offset, err = ort.NewTensor[int64](ort.Shape{1}, make([]int64, 1)); err != nil {
		return err
	}
	if f.logits, err = ort.NewTensor[float32](ort.Shape{1, 1, int64(f.vocab)}, make([]float32, f.vocab)); err != nil {
		return err
	}
	return nil
}

func (f *FireRedASR2) destroyPartial() {
	for _, t := range []*ort.Tensor[float32]{f.kcIn, f.vcIn, f.kcOut, f.vcOut, f.logits} {
		if t != nil {
			t.Destroy()
		}
	}
	for _, t := range []*ort.Tensor[int64]{f.token, f.offset} {
		if t != nil {
			t.Destroy()
		}
	}
	if f.encSess != nil {
		f.encSess.Destroy()
	}
	if f.decSess != nil {
		f.decSess.Destroy()
	}
	f.encSess, f.decSess = nil, nil
}

// Recognize 识别 PCM16LE 单声道 16kHz 整句音频为中文文本（贪心解码，
// 句子Piece 的 ▁ 保留原样——与 sherpa-onnx 输出口径一致）。
func (f *FireRedASR2) Recognize(stream []byte) (string, error) {
	if f.stub != nil {
		return f.stub.Recognize(stream)
	}
	// fbank：int16 量纲（见 fbank.go 量纲纪律）
	feats, numFrames, err := f.fb.compute(pcm16ToInt16Domain(stream))
	if err != nil {
		return "", fmt.Errorf("inference: asr: fbank: %w", err)
	}
	applyCMVN(feats, f.mean, f.inv)
	shape := ort.Shape{1, int64(numFrames), fbankNumBins}
	x, err := ort.NewTensor[float32](shape, feats)
	if err != nil {
		return "", err
	}
	defer x.Destroy()
	xLen, err := ort.NewTensor[int64](ort.Shape{1}, []int64{int64(numFrames)})
	if err != nil {
		return "", err
	}
	defer xLen.Destroy()
	outs := []ort.Value{nil, nil}
	encBegin := time.Now()
	if err := f.encSess.Run([]ort.Value{x, xLen}, outs); err != nil {
		return "", fmt.Errorf("inference: asr: encoder Run: %w", err)
	}
	f.lastEncWall = time.Since(encBegin)
	crossK, crossV := outs[0], outs[1]
	defer func() {
		crossK.Destroy()
		crossV.Destroy()
	}()
	text, err := f.decode(crossK, crossV, numFrames)
	if err != nil {
		return "", err
	}
	return text, nil
}

// decode 贪心解码（sherpa 口径：sos 起点、argmax、eos 停、offset 逐步 +1；
// KV cache in/out 双缓冲指针互换，避免每步 40MB 拷贝）。
func (f *FireRedASR2) decode(crossK, crossV ort.Value, numFrames int) (string, error) {
	maxSteps := numFrames * 6 / 100 // ≈6 token/s 上限
	if maxSteps > f.maxLen/2 {
		maxSteps = f.maxLen / 2
	}
	if maxSteps > fireredCacheLen {
		maxSteps = fireredCacheLen
	}
	for i := range f.kcIn.GetData() {
		f.kcIn.GetData()[i] = 0
	}
	for i := range f.vcIn.GetData() {
		f.vcIn.GetData()[i] = 0
	}
	f.token.GetData()[0] = f.sos
	f.offset.GetData()[0] = 0
	var ids []int64
	kc, vc := f.kcIn, f.vcIn
	kcOut, vcOut := f.kcOut, f.vcOut
	logits := f.logits.GetData()
	decBegin := time.Now()
	defer func() { f.lastDecWall = time.Since(decBegin) }()
	for step := 0; step < maxSteps; step++ {
		err := f.decSess.Run(
			[]ort.Value{f.token, kc, vc, crossK, crossV, f.offset},
			[]ort.Value{f.logits, kcOut, vcOut})
		if err != nil {
			return "", fmt.Errorf("inference: asr: decoder Run(步 %d): %w", step, err)
		}
		best, bestV := int64(0), float32(-math.MaxFloat32)
		for i, v := range logits {
			if v > bestV {
				bestV, best = v, int64(i)
			}
		}
		if best == f.eos {
			break
		}
		ids = append(ids, best)
		f.token.GetData()[0] = best
		f.offset.GetData()[0]++
		kc, kcOut = kcOut, kc
		vc, vcOut = vcOut, vc
	}
	var b strings.Builder
	for _, id := range ids {
		b.WriteString(f.tokens[id])
	}
	return b.String(), nil
}

// applyCMVN 元数据全局 CMVN：每帧 (x[k]-mean[k])*inv[k]（sherpa ApplyCMVN 口径）。
func applyCMVN(feats []float32, mean, inv []float32) {
	d := len(mean)
	for off := 0; off+d <= len(feats); off += d {
		for k := 0; k < d; k++ {
			feats[off+k] = (feats[off+k] - mean[k]) * inv[k]
		}
	}
}

// loadTokens 解析 tokens.txt（每行「symbol id」；symbol 理论上可含空格，
// 以最后一个空白分隔字段为 id）。返回映射与 vocab 大小。
func loadTokens(path string) (map[int64]string, int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("inference: asr: 读 tokens.txt: %w", err)
	}
	tokens := make(map[int64]string, fireredVocabDefault)
	maxID := 0
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		id, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil {
			continue
		}
		sym := strings.TrimSuffix(line, " "+fields[len(fields)-1])
		tokens[int64(id)] = sym
		if id > maxID {
			maxID = id
		}
	}
	if len(tokens) == 0 {
		return nil, 0, fmt.Errorf("inference: asr: tokens.txt 空: %s", path)
	}
	return tokens, maxID + 1, nil
}

// loadEncoderMeta 读 encoder ONNX 元数据（sos/eos/max_len/cmvn_*，
// 经 ORT ModelMetadata——与 sherpa InitEncoder 同源）。
func loadEncoderMeta(encoderPath string) (sos, eos int64, maxLen int, mean, inv []float32, err error) {
	if err := initORT(); err != nil {
		return 0, 0, 0, nil, nil, err
	}
	opts, err := newSessionOpts(0)
	if err != nil {
		return 0, 0, 0, nil, nil, err
	}
	defer opts.Destroy()
	sess, err := ort.NewDynamicAdvancedSession(encoderPath,
		[]string{"x", "x_len"},
		[]string{"n_layer_cross_k", "n_layer_cross_v"}, opts)
	if err != nil {
		return 0, 0, 0, nil, nil, fmt.Errorf("inference: asr: 加载 encoder %s: %w", encoderPath, err)
	}
	defer sess.Destroy()
	md, err := sess.GetModelMetadata()
	if err != nil {
		return 0, 0, 0, nil, nil, err
	}
	defer md.Destroy()
	lookup := func(key string) (string, error) {
		v, ok, err := md.LookupCustomMetadataMap(key)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("inference: asr: encoder 元数据缺 %s（非 FireRedASR2 导出？）", key)
		}
		return v, nil
	}
	readInt := func(key string) (int64, error) {
		v, err := lookup(key)
		if err != nil {
			return 0, err
		}
		return strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	}
	if sos, err = readInt("sos"); err != nil {
		return 0, 0, 0, nil, nil, err
	}
	if eos, err = readInt("eos"); err != nil {
		return 0, 0, 0, nil, nil, err
	}
	ml, err := readInt("max_len")
	if err != nil {
		return 0, 0, 0, nil, nil, err
	}
	maxLen = int(ml)
	if mean, err = parseFloatVec(lookup, "cmvn_mean"); err != nil {
		return 0, 0, 0, nil, nil, err
	}
	if inv, err = parseFloatVec(lookup, "cmvn_inv_stddev"); err != nil {
		return 0, 0, 0, nil, nil, err
	}
	return sos, eos, maxLen, mean, inv, nil
}

func parseFloatVec(lookup func(string) (string, error), key string) ([]float32, error) {
	v, err := lookup(key)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(v, ",")
	out := make([]float32, 0, len(parts))
	for _, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if err != nil {
			return nil, fmt.Errorf("inference: asr: 元数据 %s 解析: %w", key, err)
		}
		out = append(out, float32(f))
	}
	return out, nil
}

// Err 构造期错误（nil=真推理就绪；非 nil=已降级桩）。
func (f *FireRedASR2) Err() error { return f.initErr }

// LastStageWalls 最近一次 Recognize 的 encoder/decoder 推理墙钟（RTF 观测面）。
func (f *FireRedASR2) LastStageWalls() (enc, dec time.Duration) { return f.lastEncWall, f.lastDecWall }

// InFallback 是否运行在 M1 桩降级模式。
func (f *FireRedASR2) InFallback() bool { return f.stub != nil }

// Destroy 释放会话（进程退出面调用一次即可）。
func (f *FireRedASR2) Destroy() error {
	var errs []error
	if f.encSess != nil {
		errs = append(errs, f.encSess.Destroy())
	}
	if f.decSess != nil {
		errs = append(errs, f.decSess.Destroy())
	}
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
