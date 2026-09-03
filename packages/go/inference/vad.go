// SileroVAD 真推理引擎：silero-vad v5.x ONNX（MIT，silero_vad.onnx）经
// yalue/onnxruntime_go 绑定驱动（与 T3 vap 同一装配惯例）。
//
// 导出契约（Python ORT 实测确认，见 m2-prep.result.md）：
//   - input [N, T] float32：16kHz 下 T=576（512+64 上下文；T=512 时模型输出恒
//     近零概率——v5 契约以 576 为一帧，帧时长 36ms）；
//   - state [2, N, 128] float32（h/c 合并态，首帧零初始化）→ stateN 滚动；
//   - sr 标量 int64=16000；output [N, 1] 语音概率。
//
// 音频量纲：与 ASR 相反，Silero 期望 [-1,1] 归一化浮点（PCM16/32768）。
// 模型/库缺失时构造不失败——引擎降级为 M1 桩（vad_stub.go），
// Err()/InFallback() 暴露降级原因（消费方面向接口，行为不变）。
package inference

import (
	"encoding/binary"
	"fmt"

	ort "github.com/yalue/onnxruntime_go"
)

const (
	// VADFrameSamples Silero v5 16kHz 一帧样本数（36ms）。
	VADFrameSamples = 576
	// VADDefaultThreshold 语音判定阈值。
	VADDefaultThreshold = 0.5
	// vadEndHangoverFrames 连续低于阈值 N 帧才判 VoiceEnd（≈0.58s，滤语音间短停顿）。
	vadEndHangoverFrames = 16
)

// SileroVAD 语音活动检测引擎（实现 VADEngine；单线程驱动，同 T3 契约不加锁）。
type SileroVAD struct {
	threshold float32

	sess  *ort.DynamicAdvancedSession
	in    *ort.Tensor[float32] // input [1,576]
	st    *ort.Tensor[float32] // state [2,1,128]（当前态，与 stOut 逐步滚动）
	stOut *ort.Tensor[float32] // stateN
	sr    *ort.Scalar[int64]   // sr 标量（零维 shape 走绑定 Scalar 类型）
	out   *ort.Tensor[float32] // output [1,1]

	buf       []float32 // 未凑满一帧的 PCM 样本缓冲
	speaking  bool
	lowStreak int     // 连续低于阈值帧数
	utterPeak float32 // 当前语音段峰值概率

	// 桩 fallback（模型/库不可用时非 nil）
	stub    *sileroStub
	initErr error
}

// NewSileroVAD 构造 VAD 引擎（modelPath 为 ONNX 模型路径，签名与 M1 桩一致）。
// 初始化失败不报错——降级桩实现，原因经 Err() 查询。
func NewSileroVAD(modelPath string) *SileroVAD {
	v := &SileroVAD{threshold: VADDefaultThreshold}
	v.initErr = v.init(modelPath)
	if v.initErr != nil {
		v.stub = &sileroStub{}
		v.destroyPartial()
	}
	return v
}

func (v *SileroVAD) init(modelPath string) error {
	if err := initORT(); err != nil {
		return fmt.Errorf("inference: vad: onnxruntime 初始化失败: %w", err)
	}
	opts, err := newSessionOpts(0)
	if err != nil {
		return err
	}
	defer opts.Destroy()
	v.sess, err = ort.NewDynamicAdvancedSession(modelPath,
		[]string{"input", "state", "sr"},
		[]string{"output", "stateN"}, opts)
	if err != nil {
		return fmt.Errorf("inference: vad: 加载模型 %s: %w", modelPath, err)
	}
	mk := func(t **ort.Tensor[float32], shape ...int64) error {
		*t, err = ort.NewTensor[float32](ort.Shape(shape), make([]float32, dimOf(shape)))
		return err
	}
	if err := mk(&v.in, 1, VADFrameSamples); err != nil {
		return err
	}
	if err := mk(&v.st, 2, 1, 128); err != nil {
		return err
	}
	if err := mk(&v.stOut, 2, 1, 128); err != nil {
		return err
	}
	if err := mk(&v.out, 1, 1); err != nil {
		return err
	}
	v.sr, err = ort.NewScalar[int64](fbankSampleRate)
	if err != nil {
		return err
	}
	return nil
}

func (v *SileroVAD) destroyPartial() {
	for _, t := range []*ort.Tensor[float32]{v.in, v.st, v.stOut, v.out} {
		if t != nil {
			t.Destroy()
		}
	}
	if v.sr != nil {
		v.sr.Destroy()
	}
	if v.sess != nil {
		v.sess.Destroy()
	}
	v.sess = nil
}

// PushFrame 推送 PCM16LE 单声道 16kHz 音频帧，返回本批推理产生的最后一个 VAD 事件。
// 语义：概率首过阈值 → VADVoiceStart（Score=触发帧概率）；
// 连续 vadEndHangoverFrames 帧低于阈值 → VADVoiceEnd（Score=该段峰值概率）。
// 推理错误按无事件处理（接口无错误返回面；Err 面仅覆盖构造期）。
func (v *SileroVAD) PushFrame(data []byte) VADEvent {
	if v.stub != nil {
		return v.stub.PushFrame(data)
	}
	samples := pcm16ToFloat32(data)
	if len(samples) == 0 {
		return VADEvent{Kind: VADNone}
	}
	v.buf = append(v.buf, samples...)
	ev := VADEvent{Kind: VADNone}
	for len(v.buf) >= VADFrameSamples {
		copy(v.in.GetData(), v.buf[:VADFrameSamples])
		leftover := len(v.buf) - VADFrameSamples
		copy(v.buf, v.buf[VADFrameSamples:])
		v.buf = v.buf[:leftover]
		p, err := v.runFrame()
		if err != nil {
			return VADEvent{Kind: VADNone}
		}
		if e := v.stepEvent(p); e.Kind != VADNone {
			ev = e
		}
	}
	return ev
}

// SpeechProbabilities 离线整段推理：返回每个 576 样本帧的语音概率。
// 使用独立 scratch 状态（不影响/不依赖 PushFrame 流式状态），供评测与测试面使用。
func (v *SileroVAD) SpeechProbabilities(pcm []byte) ([]float32, error) {
	if v.stub != nil {
		return nil, v.initErr
	}
	samples := pcm16ToFloat32(pcm)
	probs := make([]float32, 0, len(samples)/VADFrameSamples)
	stA, err := ort.NewTensor[float32](ort.Shape{2, 1, 128}, make([]float32, 2*128))
	if err != nil {
		return nil, err
	}
	defer stA.Destroy()
	stB, err := ort.NewTensor[float32](ort.Shape{2, 1, 128}, make([]float32, 2*128))
	if err != nil {
		return nil, err
	}
	defer stB.Destroy()
	st, stOut := stA, stB
	for off := 0; off+VADFrameSamples <= len(samples); off += VADFrameSamples {
		copy(v.in.GetData(), samples[off:off+VADFrameSamples])
		err := v.sess.Run([]ort.Value{v.in, st, v.sr}, []ort.Value{v.out, stOut})
		if err != nil {
			return nil, fmt.Errorf("inference: vad: ORT Run: %w", err)
		}
		st, stOut = stOut, st
		probs = append(probs, v.out.GetData()[0])
	}
	return probs, nil
}

// runFrame 单帧 ORT 推理并滚动 state（out→in）。
func (v *SileroVAD) runFrame() (float32, error) {
	err := v.sess.Run([]ort.Value{v.in, v.st, v.sr}, []ort.Value{v.out, v.stOut})
	if err != nil {
		return 0, fmt.Errorf("inference: vad: ORT Run: %w", err)
	}
	v.st, v.stOut = v.stOut, v.st
	return v.out.GetData()[0], nil
}

// stepEvent 帧级概率 → 事件状态机。
func (v *SileroVAD) stepEvent(p float32) VADEvent {
	if p >= v.threshold {
		v.lowStreak = 0
		if !v.speaking {
			v.speaking = true
			v.utterPeak = p
			return VADEvent{Kind: VADVoiceStart, Score: p}
		}
		if p > v.utterPeak {
			v.utterPeak = p
		}
		return VADEvent{Kind: VADNone}
	}
	if !v.speaking {
		return VADEvent{Kind: VADNone}
	}
	v.lowStreak++
	if v.lowStreak >= vadEndHangoverFrames {
		v.speaking = false
		v.lowStreak = 0
		return VADEvent{Kind: VADVoiceEnd, Score: v.utterPeak}
	}
	return VADEvent{Kind: VADNone}
}

// Threshold 当前语音判定阈值。
func (v *SileroVAD) Threshold() float32 { return v.threshold }

// SetThreshold 调整语音判定阈值。
func (v *SileroVAD) SetThreshold(t float32) { v.threshold = t }

// Err 构造期错误（nil=真推理就绪；非 nil=已降级桩）。
func (v *SileroVAD) Err() error { return v.initErr }

// InFallback 是否运行在 M1 桩降级模式。
func (v *SileroVAD) InFallback() bool { return v.stub != nil }

// Reset 清空流式状态（新会话起点；模型保留）。
func (v *SileroVAD) Reset() {
	if v.stub != nil {
		v.stub = &sileroStub{}
		return
	}
	for _, t := range []*ort.Tensor[float32]{v.st, v.stOut} {
		d := t.GetData()
		for i := range d {
			d[i] = 0
		}
	}
	v.buf = nil
	v.speaking, v.lowStreak, v.utterPeak = false, 0, 0
}

// Destroy 释放会话（进程退出面调用一次即可）。
func (v *SileroVAD) Destroy() error {
	if v.sess == nil {
		return nil
	}
	return v.sess.Destroy()
}

// pcm16ToFloat32 PCM16LE → [-1,1] float32（Silero 输入量纲）。
func pcm16ToFloat32(data []byte) []float32 {
	n := len(data) / 2
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		out[i] = float32(int16(binary.LittleEndian.Uint16(data[2*i:]))) / 32768
	}
	return out
}

// pcm16ToInt16Domain PCM16LE → int16 量纲 float32（FireRedASR2 fbank 输入量纲，见 fbank.go）。
func pcm16ToInt16Domain(data []byte) []float32 {
	n := len(data) / 2
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		out[i] = float32(int16(binary.LittleEndian.Uint16(data[2*i:])))
	}
	return out
}
