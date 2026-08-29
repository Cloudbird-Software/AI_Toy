// 表驱动单测（m2-spec §11 三件套之一）：NewEngine 校验面（半衰期/规则覆盖/
// 步长上限/标签带不重叠+全覆盖）、OnEvent 方向与夹紧、DecayTo 单调时钟与
// 基线回归、Ignore 零增量、未知 Kind/越界强度鲁棒性。
package emotion

import (
	"math"
	"strings"
	"testing"
)

func mustEngine(t *testing.T, cfg Config) *Engine {
	t.Helper()
	e, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return e
}

// TestNewEngineValidation 校验面表驱动：每个坏配置须被拒绝（唯一 error 出口）。
func TestNewEngineValidation(t *testing.T) {
	badRule := func(mut func(Config) Config) Config { return mut(DefaultConfig()) }
	cases := []struct {
		name string
		cfg  Config
		want string // 期望错误片段（空串=须报错即可）
	}{
		{"快半衰期零", badRule(func(c Config) Config { c.HalfLifeFastMs = 0; return c }), "HalfLifeFastMs"},
		{"快半衰期负", badRule(func(c Config) Config { c.HalfLifeFastMs = -1; return c }), "HalfLifeFastMs"},
		{"慢半衰期零", badRule(func(c Config) Config { c.HalfLifeSlowMs = 0; return c }), "HalfLifeSlowMs"},
		{"规则缺 Kind", badRule(func(c Config) Config { c.Rules = c.Rules[:KindCount-1]; return c }), "覆盖"},
		{"规则重复", badRule(func(c Config) Config { c.Rules = append(c.Rules, Rule{K: Praise, DV: 0.1}); return c }), "重复"},
		{"规则 Kind 越界", badRule(func(c Config) Config { c.Rules[0].K = Kind(99); return c }), "越界"},
		{"步长超上限", badRule(func(c Config) Config { c.Rules[1].DV = 0.31; return c }), "步长"},
		{"增量 NaN", badRule(func(c Config) Config { c.Rules[1].DA = math.NaN(); return c }), "NaN"},
		{"标签带空", badRule(func(c Config) Config { c.LabelTable = nil; return c }), "LabelTable"},
		{"标签重复", badRule(func(c Config) Config { c.LabelTable[1].Label = "sad"; return c }), "重复"},
		{"标签矩形 Min≥Max", badRule(func(c Config) Config { c.LabelTable[0].MaxV = 0; return c }), "矩形非法"},
		{"标签矩形越界", badRule(func(c Config) Config { c.LabelTable[0].MinA = -0.1; return c }), "矩形非法"},
		{"标签带重叠", badRule(func(c Config) Config { c.LabelTable[1].MinV = 0.3; return c }), "恰 1"},
		{"标签带缺口", badRule(func(c Config) Config { c.LabelTable[1].MinA = 0.45; c.LabelTable[1].MaxA = 0.65; return c }), "恰 1"},
	}
	for _, tc := range cases {
		_, err := NewEngine(tc.cfg)
		if err == nil {
			t.Fatalf("%s：坏配置未被拒绝", tc.name)
		}
		if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s：错误信息 %q 不含 %q", tc.name, err.Error(), tc.want)
		}
	}
	if _, err := NewEngine(DefaultConfig()); err != nil {
		t.Fatalf("DefaultConfig 被拒：%v", err)
	}
}

// TestOnEventDeltaAndClamp 事件增量表驱动：从基线出发的单事件终态（方向+
// 幅度）与强度截断（>1 按 1、NaN 按 0）。
func TestOnEventDeltaAndClamp(t *testing.T) {
	e := mustEngine(t, DefaultConfig())
	got := e.OnEvent(Event{K: Praise, Intensity: 1})
	// Praise ΔV=+0.25 ΔA=+0.15 ΔC=+0.20：基线 0.5 → (0.75, 0.65, 0.70)
	if got.Valence != 0.75 || got.Arousal != 0.65 || got.Closeness != 0.70 {
		t.Fatalf("Praise 全强度增量错：got (%.3f,%.3f,%.3f) want (0.75,0.65,0.70)",
			got.Valence, got.Arousal, got.Closeness)
	}
	// (V=0.75,A=0.65) 落 V∈[0.6,1)×A∈[0.65,1] → excited（半开端闭边界）
	if got.Label != "excited" {
		t.Fatalf("Praise 全强度标签错：got %q want excited", got.Label)
	}
	// 夹紧上界：连推全强度 Praise 至各维触顶（唤醒最慢：0.5+0.15×4=1.1→1）
	for i := 0; i < 5; i++ {
		got = e.OnEvent(Event{K: Praise, Intensity: 1})
	}
	if got.Valence != 1 || got.Arousal != 1 || got.Closeness != 1 {
		t.Fatalf("正性事件连推未触顶夹紧：got (%.3f,%.3f,%.3f) want 全 1",
			got.Valence, got.Arousal, got.Closeness)
	}
	if got.Label != "excited" {
		t.Fatalf("触顶标签错：got %q want excited", got.Label)
	}
	// 负性夹紧下界：全强度 Criticize×N → Valence 触底 0
	e2 := mustEngine(t, DefaultConfig())
	for i := 0; i < 8; i++ {
		got = e2.OnEvent(Event{K: Criticize, Intensity: 1})
	}
	if got.Valence != 0 {
		t.Fatalf("负性事件连推未触底夹紧：Valence=%.3f want 0", got.Valence)
	}
	if got.Label != "sad" && got.Label != "scared" && got.Label != "angry" {
		t.Fatalf("触底标签 %q 不在负性三带", got.Label)
	}
	// 强度越界/NaN：>1 截 1、NaN 截 0（状态不动的合法对照=NaN→零增量）
	e3 := mustEngine(t, DefaultConfig())
	g3 := e3.OnEvent(Event{K: Play, Intensity: 7})
	if g3.Valence != 0.75 || g3.Arousal != 0.8 || g3.Closeness != 0.7 {
		t.Fatalf("Intensity=7 未按 1 截断：got (%.3f,%.3f,%.3f)", g3.Valence, g3.Arousal, g3.Closeness)
	}
	g3 = e3.OnEvent(Event{K: Play, Intensity: math.NaN()})
	if g3.Valence != 0.75 || g3.Arousal != 0.8 || g3.Closeness != 0.7 {
		t.Fatalf("NaN Intensity 未按 0 处理（状态须不变）：got (%.3f,%.3f,%.3f)",
			g3.Valence, g3.Arousal, g3.Closeness)
	}
}

// TestIgnoreAndUnknownKindNoState 无关事件与未知枚举零增量（spec §4：日志/
// 心跳/噪声不改状态；未知 Kind 不 panic）。
func TestIgnoreAndUnknownKindNoState(t *testing.T) {
	e := mustEngine(t, DefaultConfig())
	e.OnEvent(Event{K: Hug, Intensity: 1}) // 先离开基线
	before := e.State()
	for _, k := range []Kind{Ignore, Kind(99), Kind(-3)} {
		got := e.OnEvent(Event{K: k, Intensity: 1})
		if got != before {
			t.Fatalf("Kind %d 改了状态：%v → %v", k, before, got)
		}
	}
}

// TestDecayToMonotonicClock DecayTo 单调时钟：迟到调用整体 no-op（状态不变）；
// 推进后各维严格向基线回归且夹在 [0,1]。
func TestDecayToMonotonicClock(t *testing.T) {
	e := mustEngine(t, DefaultConfig())
	e.OnEvent(Event{K: ToySnatched, Intensity: 1}) // 负性高唤醒
	e.DecayTo(10_000)
	s1 := e.State()
	late := e.DecayTo(5_000) // 早于已推进时刻：no-op
	if late != s1 {
		t.Fatalf("迟到 DecayTo 改了状态：%v → %v", s1, late)
	}
	s2 := e.DecayTo(20_000)
	if math.Abs(s2.Arousal-0.5) >= math.Abs(s1.Arousal-0.5) {
		t.Fatalf("唤醒（快变量）未向基线回归：|Δ| %.4f → %.4f",
			math.Abs(s1.Arousal-0.5), math.Abs(s2.Arousal-0.5))
	}
	if s2.Valence < 0 || s2.Valence > 1 || s2.Arousal < 0 || s2.Arousal > 1 || s2.Closeness < 0 || s2.Closeness > 1 {
		t.Fatalf("衰减越界 [0,1]：%v", s2)
	}
	// 超长静置：全部回基线（凸组合极限）
	s3 := e.DecayTo(1 << 40)
	if math.Abs(s3.Valence-0.5) > 1e-12 || math.Abs(s3.Arousal-0.5) > 1e-12 || math.Abs(s3.Closeness-0.5) > 1e-12 {
		t.Fatalf("超长静置未回基线：%v", s3)
	}
	if s3.Label != "calm" {
		t.Fatalf("基线标签错：got %q want calm", s3.Label)
	}
}

// TestLabelBands 标签带表驱动：关键 (V,A) 点 → 期望儿童 9 类标签（含边界
// 半开归属）。
func TestLabelBands(t *testing.T) {
	e := mustEngine(t, DefaultConfig())
	cases := []struct {
		v, a float64
		want string
	}{
		{0.5, 0.5, "calm"},      // 中心
		{0.2, 0.2, "sad"},       // 低愉悦低唤醒
		{0.2, 0.5, "scared"},    // 低愉悦中唤醒
		{0.2, 0.9, "angry"},     // 低愉悦高唤醒
		{0.5, 0.2, "sleepy"},    // 中愉悦低唤醒
		{0.5, 0.9, "surprised"}, // 中愉悦高唤醒
		{0.9, 0.2, "content"},   // 高愉悦低唤醒
		{0.9, 0.5, "happy"},     // 高愉悦中唤醒
		{0.9, 0.9, "excited"},   // 高愉悦高唤醒
		{0.4, 0.35, "calm"},     // 左闭边界：0.4/0.35 归中带
		{1, 1, "excited"},       // 右端闭：1 归高端带
	}
	for _, tc := range cases {
		if got := e.labelFor(tc.v, tc.a); got != tc.want {
			t.Errorf("labelFor(%.2f,%.2f)=%q want %q", tc.v, tc.a, got, tc.want)
		}
	}
}
