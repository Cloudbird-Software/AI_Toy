// T5 表驱动单测（m3-spec §5 包契约 D 的断言本体；门禁口径在 gates_test.go）。
package voiceprint

import (
	"math"
	"math/rand"
	"testing"
)

// hookFunc 函数式 MemoryReadOnly 适配器（拒判→只读联动桩）。
type hookFunc func(RejectEvent)

// OnVoiceprintReject 实现 MemoryReadOnly。
func (h hookFunc) OnVoiceprintReject(ev RejectEvent) { h(ev) }

// unitVec 确定性单位向量（维度 d、种子 s——测试合成生成器，与打分器解耦）。
func unitVec(d int, s int64) Feat {
	r := rand.New(rand.NewSource(s))
	v := make(Feat, d)
	var norm float64
	for i := range v {
		v[i] = r.NormFloat64()
		norm += v[i] * v[i]
	}
	n := math.Sqrt(norm)
	for i := range v {
		v[i] /= n
	}
	return v
}

// jitter 向量加确定性扰动后归一（同人不同「内容」的合成代理）。
func jitter(v Feat, sigma float64, s int64) Feat {
	r := rand.New(rand.NewSource(s))
	out := make(Feat, len(v))
	var norm float64
	for i := range out {
		out[i] = v[i] + r.NormFloat64()*sigma
		norm += out[i] * out[i]
	}
	n := math.Sqrt(norm)
	for i := range out {
		out[i] /= n
	}
	return out
}

// enrollTwo 注册两名成员（远距簇单测口径），返回两成员基向量。
func enrollTwo(t *testing.T, e *Engine, d int) (Feat, Feat) {
	t.Helper()
	a, b := unitVec(d, 101), unitVec(d, 202)
	for _, m := range []struct {
		uid  string
		base Feat
	}{{"mom", a}, {"kid", b}} {
		fs := []Feat{jitter(m.base, 0.02, 1), jitter(m.base, 0.02, 2), jitter(m.base, 0.02, 3)}
		if err := e.Enroll(m.uid, fs); err != nil {
			t.Fatalf("Enroll(%s): %v", m.uid, err)
		}
	}
	return a, b
}

func TestNewEngineConfigValidation(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		ok   bool
	}{
		{"零值可用（默认 0.9/3）", Config{}, true},
		{"合法自定义", Config{Threshold: 0.8, MinEnroll: 4}, true},
		{"阈值超上界", Config{Threshold: 1.01}, false},
		{"阈值负数", Config{Threshold: -0.1}, false},
		{"MinEnroll<1", Config{MinEnroll: -1}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewEngine(c.cfg)
			if (err == nil) != c.ok {
				t.Errorf("NewEngine(%+v) err=%v, want ok=%v", c.cfg, err, c.ok)
			}
		})
	}
	// 零值默认语义
	e, err := NewEngine(Config{})
	if err != nil {
		t.Fatalf("零值构造: %v", err)
	}
	if e.cfg.Threshold != defaultThreshold || e.cfg.MinEnroll != defaultMinEnroll {
		t.Errorf("零值默认未生效: %+v", e.cfg)
	}
}

func TestEnrollValidation(t *testing.T) {
	base := unitVec(8, 1)
	good := []Feat{jitter(base, 0.01, 1), jitter(base, 0.01, 2), jitter(base, 0.01, 3)}
	cases := []struct {
		name string
		uid  string
		fs   []Feat
		ok   bool
	}{
		{"空 uid 拒绝", "", good, false},
		{"句数不足拒绝", "u", good[:2], false},
		{"空特征拒绝", "u", []Feat{{}, {}, {}}, false},
		{"NaN 特征拒绝", "u", []Feat{{1, math.NaN()}, {1, 0}, {0, 1}}, false},
		{"正常注册通过", "u", good, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, _ := NewEngine(Config{})
			err := e.Enroll(c.uid, c.fs)
			if (err == nil) != c.ok {
				t.Errorf("Enroll(%s) err=%v, want ok=%v", c.name, err, c.ok)
			}
		})
	}
	// 重复注册拒绝（同引擎同 uid）
	e, _ := NewEngine(Config{})
	if err := e.Enroll("u", good); err != nil {
		t.Fatalf("首注册: %v", err)
	}
	if err := e.Enroll("u", good); err == nil {
		t.Errorf("重复注册未拒绝")
	}
	// 维度不一致拒绝（先注册 8 维，再注册 4 维成员）
	other := unitVec(4, 9)
	if err := e.Enroll("b", []Feat{other, other, other}); err == nil {
		t.Errorf("跨维度注册未拒绝")
	}
	// MinEnroll=1 单句注册通过（下限可配）
	e3, _ := NewEngine(Config{MinEnroll: 1})
	if err := e3.Enroll("solo", []Feat{unitVec(4, 5)}); err != nil {
		t.Errorf("MinEnroll=1 单句注册被误拒: %v", err)
	}
}

func TestVerifyDecisions(t *testing.T) {
	e, _ := NewEngine(Config{Threshold: 0.9, MinEnroll: 3})
	a, b := enrollTwo(t, e, 24)
	nanFeat := make(Feat, 24)
	nanFeat[7] = math.NaN()

	cases := []struct {
		name       string
		f          Feat
		wantUID    string
		wantReject bool
	}{
		{"成员 A 新句→绑定", jitter(a, 0.03, 11), "mom", false},
		{"成员 B 新句→绑定", jitter(b, 0.03, 12), "kid", false},
		{"陌生人→拒判不冒认", unitVec(24, 999), "", true},
		{"维度不符→拒判", make(Feat, 16), "", true},
		{"NaN 特征→拒判", nanFeat, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := e.Verify(c.f)
			if d.Rejected != c.wantReject {
				t.Errorf("Verify Rejected=%v want %v（score=%.4f）", d.Rejected, c.wantReject, d.Score)
			}
			if d.UserID != c.wantUID {
				t.Errorf("Verify UserID=%q want %q", d.UserID, c.wantUID)
			}
			if d.Rejected && d.UserID != "" {
				t.Errorf("拒判冒认成员 %q（CI-2 禁半绑定）", d.UserID)
			}
		})
	}
	// 空库拒判（分数 0）
	empty, _ := NewEngine(Config{})
	if d := empty.Verify(unitVec(8, 1)); !d.Rejected || d.UserID != "" || d.Score != 0 {
		t.Errorf("空库未拒判: %+v", d)
	}
}

func TestVerifyRejectEventHook(t *testing.T) {
	cases := []struct {
		name     string
		feat     func(a, b Feat) Feat
		wantEvts int
	}{
		{"拒判一次→恰一事件", func(a, b Feat) Feat { return unitVec(24, 777) }, 1},
		{"识别成功→零事件", func(a, b Feat) Feat { return jitter(a, 0.03, 21) }, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, _ := NewEngine(Config{Threshold: 0.9})
			a, b := enrollTwo(t, e, 24)
			n := 0
			e.BindReadOnly(hookFunc(func(ev RejectEvent) {
				n++
				if ev.Threshold != 0.9 {
					t.Errorf("事件阈值镜像错误: %v", ev)
				}
			}))
			e.Verify(c.feat(a, b))
			if n != c.wantEvts {
				t.Errorf("拒判事件数=%d want %d", n, c.wantEvts)
			}
		})
	}
	// 连续拒判逐次事件（一一对应：N 次拒判=N 事件）
	e, _ := NewEngine(Config{Threshold: 0.9})
	enrollTwo(t, e, 24)
	n := 0
	e.BindReadOnly(hookFunc(func(RejectEvent) { n++ }))
	for i := 0; i < 5; i++ {
		e.Verify(unitVec(24, int64(1000+i)))
	}
	if n != 5 {
		t.Errorf("连续拒判事件数=%d want 5", n)
	}
	// 解绑后零副作用
	e.BindReadOnly(nil)
	e.Verify(unitVec(24, 2000))
	if n != 5 {
		t.Errorf("解绑后仍产事件（n=%d）", n)
	}
}

func TestEvaluateWorkingPoint(t *testing.T) {
	e, _ := NewEngine(Config{Threshold: 0.9})
	a, b := unitVec(24, 1), unitVec(24, 2)
	if err := e.Enroll("a", []Feat{jitter(a, 0.02, 1), jitter(a, 0.02, 2), jitter(a, 0.02, 3)}); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if err := e.Enroll("b", []Feat{jitter(b, 0.02, 4), jitter(b, 0.02, 5), jitter(b, 0.02, 6)}); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	trials := []Trial{
		{A: jitter(a, 0.02, 7), B: jitter(a, 0.02, 8), SameSpeaker: true},    // genuine 过阈
		{A: jitter(a, 0.02, 9), B: unitVec(24, 999), SameSpeaker: true},      // genuine 被拒→miss
		{A: jitter(a, 0.02, 10), B: jitter(b, 0.02, 11), SameSpeaker: false}, // imposter 拒→不计
	}
	rep := e.Evaluate(trials)
	if rep.Trials != 3 || rep.Misses != 1 || rep.FalseAlarms != 0 {
		t.Errorf("Evaluate=%+v want {3,1,0}", rep)
	}
	// 评估通道零副作用（不触发 MemoryReadOnly）
	n := 0
	e.BindReadOnly(hookFunc(func(RejectEvent) { n++ }))
	e.Evaluate(trials)
	if n != 0 {
		t.Errorf("评估通道触发拒判事件 %d 次（须零副作用）", n)
	}
}

func TestDefaultScorer(t *testing.T) {
	cases := []struct {
		name string
		a, b Feat
		want float64
		tol  float64
	}{
		{"同向→1", Feat{1, 0, 0}, Feat{2, 0, 0}, 1, 1e-12},
		{"反向→0", Feat{1, 0, 0}, Feat{-1, 0, 0}, 0, 1e-12},
		{"正交→0.5", Feat{1, 0}, Feat{0, 1}, 0.5, 1e-12},
		{"缩放不变", Feat{0.3, 0.4}, Feat{30, 40}, 1, 1e-12},
		{"零向量→0.5", Feat{0, 0}, Feat{1, 1}, 0.5, 1e-12},
		{"维度不符→0", Feat{1}, Feat{1, 1}, 0, 0},
		{"空→0", nil, Feat{1}, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DefaultScorer(c.a, c.b)
			if math.Abs(got-c.want) > c.tol {
				t.Errorf("DefaultScorer=%.6f want %.6f", got, c.want)
			}
		})
	}
	if DefaultScorer(Feat{math.NaN()}, Feat{1}) != 0 {
		t.Errorf("NaN 输入未按 0 处理")
	}
	// 平分确定性：两成员同分时绑定字典序最小 uid（map 遍历序无关）
	e, _ := NewEngine(Config{Threshold: 0.5})
	same := unitVec(16, 55)
	if err := e.Enroll("zeta", []Feat{same, same, same}); err != nil {
		t.Fatalf("Enroll zeta: %v", err)
	}
	if err := e.Enroll("alpha", []Feat{same, same, same}); err != nil {
		t.Fatalf("Enroll alpha: %v", err)
	}
	for i := 0; i < 3; i++ {
		if d := e.Verify(same); d.UserID != "alpha" {
			t.Errorf("平分绑定非确定（第 %d 次=%q）", i, d.UserID)
		}
	}
}
