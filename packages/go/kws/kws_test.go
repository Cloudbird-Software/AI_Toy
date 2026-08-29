// T4 表驱动单测（spec §2 契约 / m1-spec §6 三件套之一）：配置校验、防抖、
// 不应期、滑窗置信度、Feats 直供通道、ConfidenceFunc 注入、TierBudget 预留、
// 内置打分器档位。错误语义：仅 NewDetector 返回 error；Push 对畸变输入按零
// 能量帧处理（EvNone），永不 error/panic。
package kws

import (
	"math"
	"math/rand"
	"testing"
)

// pushAll 顺序推帧（TS 由序号×FrameMs 生成）：cfg.Infer 为 nil 时按 confs
// 脚本化注入（Feats 直供通道），非 nil 时沿用注入实现（confs 仅决定帧数）。
func pushAll(t *testing.T, cfg Config, confs ...float64) []Event {
	t.Helper()
	c := cfg
	if c.Infer == nil {
		c.Infer = ConfidenceFunc(func(f Frame) float64 {
			i := int(f.TS) / c.FrameMs
			if i < 0 || i >= len(confs) {
				return 0
			}
			return confs[i]
		})
	}
	d, err := NewDetector(c)
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	evs := make([]Event, 0, len(confs))
	for i := range confs {
		evs = append(evs, d.Push(Frame{TS: int64(i) * int64(c.FrameMs), Feats: []float32{0}}))
	}
	return evs
}

func baseCfg() Config {
	return Config{FrameMs: 30, ConfirmFrames: 3, RefractoryMs: 60, Threshold: 0.5}
}

func TestNewDetectorValidation(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"零值 FrameMs→默认 30", Config{ConfirmFrames: 1, Threshold: 0.5}, false},
		{"FrameMs 负", Config{FrameMs: -30, ConfirmFrames: 1, Threshold: 0.5}, true},
		{"FrameMs 超 200", Config{FrameMs: 201, ConfirmFrames: 1, Threshold: 0.5}, true},
		{"FrameMs=200 边界", Config{FrameMs: 200, ConfirmFrames: 1, Threshold: 0.5}, false},
		{"ConfirmFrames=0", Config{FrameMs: 30, ConfirmFrames: 0, Threshold: 0.5}, true},
		{"ConfirmFrames=1 边界", Config{FrameMs: 30, ConfirmFrames: 1, Threshold: 0.5}, false},
		{"RefractoryMs 负", Config{FrameMs: 30, ConfirmFrames: 1, RefractoryMs: -1, Threshold: 0.5}, true},
		{"RefractoryMs=0 合法", Config{FrameMs: 30, ConfirmFrames: 1, RefractoryMs: 0, Threshold: 0.5}, false},
		{"Threshold<0", Config{FrameMs: 30, ConfirmFrames: 1, Threshold: -0.01}, true},
		{"Threshold>1", Config{FrameMs: 30, ConfirmFrames: 1, Threshold: 1.01}, true},
		{"Threshold 边界 0/1", Config{FrameMs: 30, ConfirmFrames: 1, Threshold: 0}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := NewDetector(tc.cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望 error，got nil（cfg=%+v）", tc.cfg)
				}
				return
			}
			if err != nil {
				t.Fatalf("意外 error: %v", err)
			}
			if d == nil {
				t.Fatal("NewDetector 返回 nil Detector")
			}
		})
	}
}

func TestDebounceConfirmFrames(t *testing.T) {
	// 连续 3 帧超阈才触发；序列中断则防抖计数重置。
	evs := pushAll(t, baseCfg(), 0.9, 0.9, 0.9)
	if len(evs) != 3 || evs[0].Kind != EvNone || evs[1].Kind != EvNone || evs[2].Kind != EvWake {
		t.Fatalf("防抖：前 2 帧 EvNone、第 3 帧 EvWake，got %+v", evs)
	}
	// 中断重置：高低交替永不触发。
	evs = pushAll(t, baseCfg(), 0.9, 0.2, 0.9, 0.2, 0.9, 0.2)
	for i, e := range evs {
		if e.Kind != EvNone {
			t.Fatalf("防抖中断重置失效：帧 %d 发 Wake", i)
		}
	}
	// 恰在阈值上的帧不计（「超阈值」= 严格 >）。
	evs = pushAll(t, baseCfg(), 0.5, 0.5, 0.5, 0.5)
	for i, e := range evs {
		if e.Kind != EvNone {
			t.Fatalf("阈值等于不算超阈：帧 %d 发 Wake", i)
		}
	}
}

func TestRefractorySuppression(t *testing.T) {
	// ConfirmFrames=1、RefractoryMs=60（=2 帧）：Wake@0 → 抑制 [0,60)（帧 0、1），
	// TS=60 到期即解除重新计防抖 → 帧 2 可再触发；任意两次 Wake 间隔 ≥ RefractoryMs。
	cfg := Config{FrameMs: 30, ConfirmFrames: 1, RefractoryMs: 60, Threshold: 0.5}
	evs := pushAll(t, cfg, 0.9, 0.9, 0.9, 0.9, 0.9)
	wantKinds := []EventKind{EvWake, EvNone, EvWake, EvNone, EvWake}
	for i, k := range wantKinds {
		if evs[i].Kind != k {
			t.Fatalf("不应期序列：帧 %d 期望 %v got %v（全序列 %+v）", i, k, evs[i].Kind, evs)
		}
	}
	var prev int64 = -1
	for _, e := range evs {
		if e.Kind != EvWake {
			continue
		}
		if prev >= 0 && e.AtMs-prev < 60 {
			t.Fatalf("Wake 间隔 %d < RefractoryMs=60", e.AtMs-prev)
		}
		prev = e.AtMs
	}
	// EvNone 帧（噪声）不重置不应期计时时钟：帧 1 低置信噪声后，帧 2（TS=60 到期）
	// 仍立即触发——抑制时长严格由 TS 推进决定。
	evs = pushAll(t, cfg, 0.9, 0.2, 0.9, 0.9)
	if evs[0].Kind != EvWake || evs[2].Kind != EvWake {
		t.Fatalf("不应期计时应与帧内容无关：got %+v", evs)
	}
	// RefractoryMs=0 = 无不应期（ConfirmFrames=1 时每帧超阈即 Wake）。
	cfg0 := Config{FrameMs: 30, ConfirmFrames: 1, RefractoryMs: 0, Threshold: 0.5}
	evs = pushAll(t, cfg0, 0.9, 0.9, 0.9)
	for i, e := range evs {
		if e.Kind != EvWake {
			t.Fatalf("RefractoryMs=0 须无抑制：帧 %d 期望 EvWake got %v", i, e.Kind)
		}
	}
}

func TestSlidingWindowConfidence(t *testing.T) {
	// EvNone 时 Confidence=当帧滑窗峰值（可观测）：峰值在窗内滞留，窗出即衰减。
	evs := pushAll(t, baseCfg(), 0.9, 0.1, 0.1, 0.1, 0.1)
	// 帧 0：窗=[0.9] 峰 0.9；帧 1：窗=[0.9,0.1] 峰 0.9；帧 2：窗=[0.9,0.1,0.1] 峰 0.9；帧 3：窗滚出 → 0.1
	if evs[1].Kind != EvNone || evs[1].Confidence < 0.89 {
		t.Fatalf("滑窗峰值滞留失效：帧 1 conf=%.4f（期望≈0.9）", evs[1].Confidence)
	}
	if evs[3].Confidence > 0.11 {
		t.Fatalf("滑窗滚出失效：帧 3 conf=%.4f（期望≈0.1）", evs[3].Confidence)
	}
	// EvWake 时 Confidence = 触发帧滑窗峰值。
	cfg2 := Config{FrameMs: 30, ConfirmFrames: 2, RefractoryMs: 60, Threshold: 0.5}
	evs2 := pushAll(t, cfg2, 0.7, 0.9)
	if evs2[1].Kind != EvWake || evs2[1].Confidence < 0.89 {
		t.Fatalf("EvWake 置信度须为滑窗峰值：got %+v", evs2[1])
	}
}

func TestPushMalformedFramesNeverPanic(t *testing.T) {
	// 畸变输入（空 PCM+空 Feats / nil PCM / 短帧 / 常量帧 / 极值帧）→ 零能量 EvNone，不 panic。
	d, err := NewDetector(baseCfg())
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	bad := []Frame{
		{TS: 0},
		{TS: 30, PCM: nil},
		{TS: 60, PCM: []int16{}},
		{TS: 90, PCM: []int16{100}},
		{TS: 120, PCM: make([]int16, 3)},
		{TS: 150, PCM: []int16{32767, -32768, 32767, -32768}},
	}
	for _, f := range bad {
		ev := d.Push(f) // 不 panic 即通过编译期断言
		if ev.Kind != EvNone {
			t.Fatalf("畸变帧须 EvNone：TS=%d got %v", f.TS, ev.Kind)
		}
	}
}

func TestFeatsPassthrough(t *testing.T) {
	// Feats 非空 → 跳过包内预处理直供（默认打分器取 Feats[0] 并 clip [0,1]）。
	cases := []struct {
		feats []float32
		want  float64
	}{
		{[]float32{0.75}, 0.75},
		{[]float32{1.5}, 1},
		{[]float32{-0.3}, 0},
		{[]float32{0}, 0},
	}
	for _, tc := range cases {
		d, err := NewDetector(baseCfg())
		if err != nil {
			t.Fatalf("NewDetector: %v", err)
		}
		ev := d.Push(Frame{TS: 0, Feats: tc.feats})
		if math.Abs(ev.Confidence-tc.want) > 1e-6 {
			t.Fatalf("Feats 直供：feats=%v 期望 conf=%.4f got %.4f", tc.feats, tc.want, ev.Confidence)
		}
	}
}

func TestConfidenceFuncInjection(t *testing.T) {
	// ConfidenceFunc 注入点：普通函数直接作 Inferencer（无需定义类型）。
	calls := 0
	infer := ConfidenceFunc(func(f Frame) float64 {
		calls++
		return 0.99
	})
	cfg := Config{FrameMs: 30, ConfirmFrames: 2, RefractoryMs: 60, Threshold: 0.5, Infer: infer}
	evs := pushAll(t, cfg, 0.99, 0.99)
	if evs[1].Kind != EvWake {
		t.Fatalf("ConfidenceFunc 注入失效：未触发")
	}
	if calls != 2 {
		t.Fatalf("ConfidenceFunc 调用计数：期望 2 got %d", calls)
	}
}

func TestDefaultInferencerScoring(t *testing.T) {
	// 内置打分器（能量+过零启发式，尺度不变）：唤醒模式占位（低频正弦）高分、
	// 白噪声低分、静音零分——三档序关系（无唤醒语义，仅通道可观测）。
	sine := synthSineFrame(0, 30, 0, 200, 4000)
	noise := whiteNoiseFrame(1, 30, 1, 20260829)
	silent := synthSilenceFrame(2, 30)
	h := heuristicInferencer{}
	sc := map[string]float64{"sine": h.Infer(sine), "noise": h.Infer(noise), "silence": h.Infer(silent)}
	if sc["sine"] <= sc["noise"] {
		t.Errorf("内置打分器序失效：正弦 %.4f ≤ 白噪声 %.4f", sc["sine"], sc["noise"])
	}
	if sc["silence"] != 0 {
		t.Errorf("静音须 0：got %.4f", sc["silence"])
	}
	if sc["sine"] < 0.55 { // Threshold=0.55 时正弦须可触发（P5 端点前提）
		t.Errorf("正弦占位分数过低：%.4f（唤醒通道空转风险）", sc["sine"])
	}
	if sc["noise"] >= 0.55 {
		t.Errorf("白噪声分数过高：%.4f（误唤醒风险）", sc["noise"])
	}
}

func TestTierBudgetDefaultMirror(t *testing.T) {
	// T14 档位镜像（M1 预留不接线）：nil=默认表；四档非零且端侧递减（L0⊇L1⊇L2⊇L3 嵌套的预算面）。
	d, err := NewDetector(baseCfg())
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	prev := math.MaxInt
	for tier := 0; tier <= 3; tier++ {
		lim := d.MemLimitBytes(tier)
		if lim <= 0 {
			t.Fatalf("档位 %d 内存上限须 >0：got %d", tier, lim)
		}
		if lim > prev {
			t.Fatalf("端侧预算须随档位递减：L%d=%d > L%d=%d", tier, lim, tier-1, prev)
		}
		prev = lim
	}
	if lim := d.MemLimitBytes(4); lim != 0 {
		t.Fatalf("越界档位须 0：got %d", lim)
	}
	// 注入自定义 Budget 透传。
	custom := fixedBudget{bytes: 123456}
	d2, err := NewDetector(Config{FrameMs: 30, ConfirmFrames: 1, Threshold: 0.5, Budget: custom})
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	for tier := 0; tier <= 3; tier++ {
		if got := d2.MemLimitBytes(tier); got != 123456 {
			t.Fatalf("自定义 Budget 透传失效：L%d got %d", tier, got)
		}
	}
}

type fixedBudget struct{ bytes int }

func (b fixedBudget) KWSMemLimitBytes(int) int { return b.bytes }

// whiteNoiseFrame 确定性白噪声帧（固定种子）。
func whiteNoiseFrame(ts int64, frameMs int, sigma float64, seed int64) Frame {
	r := rand.New(rand.NewSource(seed))
	pcm := make([]int16, frameMs*16)
	for i := range pcm {
		pcm[i] = int16(math.Round(r.NormFloat64() * sigma))
	}
	return Frame{TS: ts, PCM: pcm}
}
