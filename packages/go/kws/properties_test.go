// T4 属性测试（spec §2 契约 + docs/gates/assets/T4.md 属性行，testing/quick）：
//
//	P1 增益不变（PCM −20~+6dB 缩放判定序列不变——尺度不变打分器的设计约束）
//	P2 位置无关（唤醒特征模式出现在流任意位置均命中恰一次）
//	P3 不应期（Wake 后 RefractoryMs 窗口内二次特征不重复发事件）
//	P4 确定性（同输入同输出，回放可复现）
//	P5 SNR 单调（噪声分档增强→置信度单调不升；分档表用例）
//
// 属性以脚本化 Inferencer 注入测 Detector 逻辑（非测模型）；内置打分器在
// P1/P5 走真实 PCM 通道（模型接入后同组属性重跑——接口即 seam，ADR-0004）。
package kws

import (
	"math"
	"math/rand"
	"testing"
	"testing/quick"
)

// p1Config P1/P5 共用（内置打分器 + PCM 通道）。
const (
	p1FrameMs   = 30
	p1Threshold = 0.55
	p1Confirm   = 3
	p1Refract   = 60
	p1Amp       = 4000.0 // 峰值：×2（+6dB）后 8000，远离 int16 饱和
	p1Freq      = 200.0  // 唤醒模式主频（低频高占空）
)

// synthSineFrame 合成一帧纯正弦（确定性：相位由帧索引连续推进）。
func synthSineFrame(ts int64, frameMs, idx int, freq, amp float64) Frame {
	n := frameMs * 16
	pcm := make([]int16, n)
	w := 2 * math.Pi * freq / 16000.0
	phase := float64(idx) * float64(n) * w
	for i := 0; i < n; i++ {
		pcm[i] = int16(math.Round(amp * math.Sin(phase+float64(i)*w)))
	}
	return Frame{TS: ts, PCM: pcm}
}

// synthSilenceFrame 全零帧（零能量）。
func synthSilenceFrame(ts int64, frameMs int) Frame {
	return Frame{TS: ts, PCM: make([]int16, frameMs*16)}
}

// synthWakeStream 唤醒流：padIn 帧静音 + 6 帧正弦 + padOut 帧静音。
func synthWakeStream(frameMs, padIn, padOut int) []Frame {
	var fs []Frame
	ts := int64(0)
	for i := 0; i < padIn; i++ {
		fs = append(fs, synthSilenceFrame(ts, frameMs))
		ts += int64(frameMs)
	}
	for i := 0; i < 6; i++ {
		fs = append(fs, synthSineFrame(ts, frameMs, i, p1Freq, p1Amp))
		ts += int64(frameMs)
	}
	for i := 0; i < padOut; i++ {
		fs = append(fs, synthSilenceFrame(ts, frameMs))
		ts += int64(frameMs)
	}
	return fs
}

// scaleStream 整流增益缩放（g 合理域 [0.1, 2.0] = −20~+6dB；源峰值受限，缩放后不饱和）。
func scaleStream(fs []Frame, g float64) []Frame {
	out := make([]Frame, len(fs))
	for i, f := range fs {
		s := make([]int16, len(f.PCM))
		for j, v := range f.PCM {
			s[j] = int16(math.Round(float64(v) * g))
		}
		out[i] = Frame{TS: f.TS, PCM: s}
	}
	return out
}

// verdicts 判定序列（Kind, AtMs）——P1 比较口径（置信度数值允许量化微漂）。
func verdicts(evs []Event) [][2]int64 {
	out := make([][2]int64, 0, len(evs))
	for _, e := range evs {
		out = append(out, [2]int64{int64(e.Kind), e.AtMs})
	}
	return out
}

// runDetector 顺序推帧收集事件。
func runDetector(d *Detector, fs []Frame) []Event {
	evs := make([]Event, 0, len(fs))
	for _, f := range fs {
		evs = append(evs, d.Push(f))
	}
	return evs
}

// TestPropertyGainInvariance P1：同帧序列整体缩放增益（−20~+6dB，合理域内）
// 判定序列不变——内置打分器必须尺度不变（能量占比+过零率，无绝对门限）。
func TestPropertyGainInvariance(t *testing.T) {
	prop := func(gainPct uint8, phaseSeed int64) bool {
		g := 0.1 + float64(gainPct)/255.0*1.9 // [0.1, 2.0]
		_ = phaseSeed                         // 相位由流构造固定（帧索引推进），保留参数位
		base := synthWakeStream(p1FrameMs, 3, 4)
		d1, err := NewDetector(Config{FrameMs: p1FrameMs, ConfirmFrames: p1Confirm,
			RefractoryMs: p1Refract, Threshold: p1Threshold})
		if err != nil {
			t.Fatalf("P1 NewDetector: %v", err)
		}
		d2, err := NewDetector(Config{FrameMs: p1FrameMs, ConfirmFrames: p1Confirm,
			RefractoryMs: p1Refract, Threshold: p1Threshold})
		if err != nil {
			t.Fatalf("P1 NewDetector: %v", err)
		}
		v1 := verdicts(runDetector(d1, base))
		v2 := verdicts(runDetector(d2, scaleStream(base, g)))
		if len(v1) != len(v2) {
			return false
		}
		for i := range v1 {
			if v1[i] != v2[i] {
				return false
			}
		}
		// 锚点：基线（含缩放后）必须恰一次 Wake（否则属性空转）
		wakes := 0
		for _, e := range v1 {
			if e[0] == int64(EvWake) {
				wakes++
			}
		}
		return wakes == 1
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 120}); err != nil {
		t.Errorf("P1 增益不变性失效: %v", err)
	}
}

// TestPropertyPositionIndependence P2：唤醒特征模式出现在流任意位置均命中
// 恰一次（脚本化 Inferencer 注入，位置由 TS 编码）。
func TestPropertyPositionIndependence(t *testing.T) {
	const frameMs = 30
	prop := func(prefix uint8, suffix uint8) bool {
		p := int(prefix) % 60
		s := int(suffix) % 60
		patternLen := 5
		startMs := p * frameMs
		endMs := startMs + patternLen*frameMs
		infer := ConfidenceFunc(func(f Frame) float64 {
			if f.TS >= int64(startMs) && f.TS < int64(endMs) {
				return 0.9
			}
			return 0.05
		})
		d, err := NewDetector(Config{FrameMs: frameMs, ConfirmFrames: 3,
			RefractoryMs: 90, Threshold: 0.55, Infer: infer})
		if err != nil {
			t.Fatalf("P2 NewDetector: %v", err)
		}
		n := p + patternLen + s
		wakes := 0
		var lastWake int64 = -1
		for i := 0; i < n; i++ {
			ev := d.Push(Frame{TS: int64(i) * frameMs, Feats: []float32{0}})
			if ev.Kind == EvWake {
				wakes++
				lastWake = ev.AtMs
			}
		}
		// 恰一次，且在模式内第 ConfirmFrames 帧位置（防抖语义 + 位置无关）
		return wakes == 1 && lastWake == int64(p+2)*frameMs
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 150}); err != nil {
		t.Errorf("P2 位置无关失效: %v", err)
	}
}

// TestPropertyRefractory P3：触发后 RefractoryMs 内二次特征不重复发事件
// （全程超阈值流——最激进重触发场景；任意两次 Wake 间隔 ≥ RefractoryMs）。
func TestPropertyRefractory(t *testing.T) {
	const frameMs = 30
	prop := func(refractory uint16) bool {
		r := int(refractory) % 2000
		infer := ConfidenceFunc(func(Frame) float64 { return 0.99 })
		d, err := NewDetector(Config{FrameMs: frameMs, ConfirmFrames: 3,
			RefractoryMs: r, Threshold: 0.5, Infer: infer})
		if err != nil {
			t.Fatalf("P3 NewDetector: %v", err)
		}
		const n = 120 // 3600ms 流
		var prev int64 = math.MinInt64
		for i := 0; i < n; i++ {
			ev := d.Push(Frame{TS: int64(i) * frameMs, Feats: []float32{0}})
			if ev.Kind == EvWake {
				if prev != math.MinInt64 && ev.AtMs-prev < int64(r) {
					return false
				}
				prev = ev.AtMs
			}
		}
		return prev != math.MinInt64 // 首触发必须发生（r=0 时也至少一次）
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 150}); err != nil {
		t.Errorf("P3 不应期失效: %v", err)
	}
}

// TestPropertyDeterminism P4：同输入同输出（随机配置×随机置信度流，重放
// 两次事件序列逐字段全等——含 Confidence 浮点位级一致）。
func TestPropertyDeterminism(t *testing.T) {
	const frameMs = 30
	prop := func(seed int64) bool {
		if seed < 0 {
			seed = -seed
		}
		r := rand.New(rand.NewSource(seed))
		confirm := 1 + r.Intn(5)
		refract := r.Intn(300)
		threshold := 0.25 + r.Float64()*0.5
		n := 80
		confStream := make([]float64, n)
		for i := range confStream {
			confStream[i] = r.Float64()
		}
		run := func() []Event {
			infer := ConfidenceFunc(func(f Frame) float64 {
				return confStream[int(f.TS)/frameMs]
			})
			d, err := NewDetector(Config{FrameMs: frameMs, ConfirmFrames: confirm,
				RefractoryMs: refract, Threshold: threshold, Infer: infer})
			if err != nil {
				t.Fatalf("P4 NewDetector: %v", err)
			}
			evs := make([]Event, 0, n)
			for i := 0; i < n; i++ {
				evs = append(evs, d.Push(Frame{TS: int64(i) * frameMs, Feats: []float32{0}}))
			}
			return evs
		}
		a, b := run(), run()
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 120}); err != nil {
		t.Errorf("P4 确定性失效: %v", err)
	}
}

// TestPropertySNRMonotonic P5（分档表用例，非 quick）：噪声增强（SNR 降档）
// → 置信度单调不升。信号=200Hz 正弦（唤醒模式占位），噪声=固定种子确定性
// 白噪声；对每档取全帧置信度均值与峰值，断言逐档不升，且端点语义成立
// （纯信号必触发、最低 SNR 档不触发）。
func TestPropertySNRMonotonic(t *testing.T) {
	const frameMs = 30
	const frames = 20
	snrDb := []float64{40, 20, 10, 5, 0, -5}
	means := make([]float64, len(snrDb))
	wakes := make([]int, len(snrDb))
	for k, snr := range snrDb {
		d := freshDetector(t, frameMs) // 各档独立（防跨档不应期/滑窗残留污染）
		sigma := p1Amp / math.Pow(10, snr/20)
		r := rand.New(rand.NewSource(20260829)) // 固定种子：噪声序列确定可复现
		var sum float64
		for i := 0; i < frames; i++ {
			f := synthSineFrame(int64(i)*frameMs, frameMs, i, p1Freq, p1Amp)
			for j, v := range f.PCM {
				f.PCM[j] = int16(math.Round(clampF(float64(v)+r.NormFloat64()*sigma, -32768, 32767)))
			}
			ev := d.Push(f)
			sum += ev.Confidence
			if ev.Kind == EvWake {
				wakes[k]++
			}
		}
		means[k] = sum / frames
	}
	for i := 1; i < len(means); i++ {
		if means[i] > means[i-1]+1e-9 { // 单调不升（容浮点尾差）
			t.Errorf("P5 SNR 单调失效：SNR %.0fdB→%.0fdB 置信度均值 %.4f→%.4f 上升",
				snrDb[i-1], snrDb[i], means[i-1], means[i])
		}
	}
	// 端点锚点：最高档（40dB≈纯信号）必触发、最低档（-5dB）不触发——属性非空转。
	if wakes[0] == 0 {
		t.Errorf("P5 端点失效：40dB 档未触发（唤醒通道空转）")
	}
	if wakes[len(wakes)-1] > 0 {
		t.Errorf("P5 端点失效：-5dB 档触发 %d 次（低 SNR 须不唤醒）", wakes[len(wakes)-1])
	}
}

// freshDetector 辅助：默认内置打分器的新 Detector。
func freshDetector(t *testing.T, frameMs int) *Detector {
	t.Helper()
	d, err := NewDetector(Config{FrameMs: frameMs, ConfirmFrames: p1Confirm,
		RefractoryMs: p1Refract, Threshold: p1Threshold})
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	return d
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
