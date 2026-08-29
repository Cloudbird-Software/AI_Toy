// 属性测试（m2-spec §11 三件套之二，testing/quick；spec §5 属性清单）：
// P1 安全盒（任意 Mood fuzz——含垃圾标签/NaN 强度——输出恒在盒内）；P2 强度
// 单调（同标签+同 seed：动作集不随强度变、逐动作幅度单调不降）；P3 确定性
// 回放（同 Mood+同 seed 跨实例逐字段全等）；P4 静默强制（silent=true 恒 nil
// 且锁存 IdleTick）；P5 idle 连续性（仿真时钟扫描：微动作间隔 ≤90s——
// T12-G1-01 调度逻辑面）。命名与 AGENTS.md「本地命令」的 -run Property 匹配。
package motionmap

import (
	"testing"
	"testing/quick"
)

// inBox 安全盒断言（T12-G0-01 口径的函数化）：字段合法、每组恰一动作、
// 互斥集内恰一、组内 Σ ≤ GroupDuty、全局 Σ ≤ GlobalAmpSum。
func inBox(out []Action, l Limits) bool {
	if len(out) == 0 {
		return true
	}
	perGroup := map[string]int{}
	groupSum := map[string]int{}
	sum := 0
	for _, a := range out {
		if a.ID == "" || a.Group == "" || a.Amp > AmpMax {
			return false
		}
		perGroup[a.Group]++
		if perGroup[a.Group] > 1 {
			return false // 每组恰一
		}
		groupSum[a.Group] += int(a.Amp)
		if groupSum[a.Group] > int(l.GroupDuty[a.Group]) {
			return false // 组内 Σ 超 GroupDuty
		}
		sum += int(a.Amp)
	}
	if sum > int(l.GlobalAmpSum) {
		return false
	}
	for _, set := range l.MutexGroups {
		n := 0
		for _, a := range out {
			for _, g := range set {
				if a.Group == g {
					n++
					break
				}
			}
		}
		if n > 1 {
			return false // 互斥集内多动作
		}
	}
	return true
}

// TestPropertySafetyBox P1 安全盒：任意（含垃圾）Mood+任意 seed 的 Map 输出
// 恒在安全盒内（T12-G0-01 的 fuzz 面——quick 的 string 可产任意标签、float64
// 可产 NaN/±Inf）。
func TestPropertySafetyBox(t *testing.T) {
	f := func(label string, intensity float64, seed int64) bool {
		m, err := NewMapper(DefaultTable(), DefaultLimits())
		if err != nil {
			t.Errorf("DefaultTable/DefaultLimits 被拒：%v", err)
			return false
		}
		return inBox(m.Map(Mood{Label: label, Intensity: intensity}, false, seed), DefaultLimits())
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 300}); err != nil {
		t.Errorf("P1 安全盒被违反：%v", err)
	}
}

// TestPropertyIntensityMonotone P2 强度单调：同标签+同 seed 下强度↑——动作
// 集（ID 序列）不变、逐动作幅度单调不降、Σ 单调不降（min(单调升,常量配额)
// 的构造保证）。
func TestPropertyIntensityMonotone(t *testing.T) {
	f := func(label string, i1, i2 float64, seed int64) bool {
		lo, hi := i1, i2
		if lo > hi {
			lo, hi = hi, lo
		}
		lo, hi = clamp01(lo), clamp01(hi)
		m, err := NewMapper(DefaultTable(), DefaultLimits())
		if err != nil {
			t.Errorf("DefaultTable/DefaultLimits 被拒：%v", err)
			return false
		}
		o1, o2 := m.Map(Mood{Label: label, Intensity: lo}, false, seed), m.Map(Mood{Label: label, Intensity: hi}, false, seed)
		if len(o1) != len(o2) {
			t.Logf("选择集随强度变化（须只依赖 label+seed）：%v vs %v", o1, o2)
			return false
		}
		s1, s2 := 0, 0
		for k := range o1 {
			if o1[k].ID != o2[k].ID || o1[k].Group != o2[k].Group {
				t.Logf("动作身份随强度变化：%+v vs %+v", o1[k], o2[k])
				return false
			}
			if o2[k].Amp < o1[k].Amp {
				t.Logf("强度 %.3f→%.3f 动作 %s 幅度降 %d→%d", lo, hi, o1[k].ID, o1[k].Amp, o2[k].Amp)
				return false
			}
			s1 += int(o1[k].Amp)
			s2 += int(o2[k].Amp)
		}
		return s2 >= s1
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 300}); err != nil {
		t.Errorf("P2 强度单调被违反：%v", err)
	}
}

// TestPropertyDeterministicReplay P3 确定性回放：同 Mood+同 seed 在两个独立
// Mapper 实例（同配置）输出逐字段全等；同实例重复调用亦全等（含 IdleTick）。
func TestPropertyDeterministicReplay(t *testing.T) {
	f := func(label string, intensity float64, seed, atMs int64) bool {
		run := func() ([]Action, []Action) {
			m, err := NewMapper(DefaultTable(), DefaultLimits())
			if err != nil {
				t.Errorf("DefaultTable/DefaultLimits 被拒：%v", err)
				return nil, nil
			}
			return m.Map(Mood{Label: label, Intensity: intensity}, false, seed), m.IdleTick(atMs, seed)
		}
		m1, i1 := run()
		m2, i2 := run()
		if len(m1) != len(m2) || len(i1) != len(i2) {
			return false
		}
		for k := range m1 {
			if m1[k] != m2[k] {
				t.Logf("Map 分歧[%d]：%+v vs %+v", k, m1[k], m2[k])
				return false
			}
		}
		for k := range i1 {
			if i1[k] != i2[k] {
				t.Logf("IdleTick 分歧[%d]：%+v vs %+v", k, i1[k], i2[k])
				return false
			}
		}
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("P3 确定性回放被违反：%v", err)
	}
}

// TestPropertySilentOverrides P4 静默强制：任意 Mood+任意 seed 下 silent=true
// 恒 nil，且锁存使 IdleTick 恒 nil（T12-G0-02 属性面——优先级高于一切映射）。
func TestPropertySilentOverrides(t *testing.T) {
	f := func(label string, intensity float64, seed, atMs int64) bool {
		m, err := NewMapper(DefaultTable(), DefaultLimits())
		if err != nil {
			t.Errorf("DefaultTable/DefaultLimits 被拒：%v", err)
			return false
		}
		if out := m.Map(Mood{Label: label, Intensity: intensity}, true, seed); out != nil {
			t.Logf("静默下 Map 非 nil：%v", out)
			return false
		}
		if out := m.IdleTick(atMs, seed); out != nil {
			t.Logf("静默锁存后 IdleTick 非 nil：%v", out)
			return false
		}
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("P4 静默强制被违反：%v", err)
	}
}

// TestPropertyIdleContinuity P5 idle 连续性：仿真时钟 6h@1s 扫描（3 档 seed），
// 相邻非空输出间隔 ≤90s 且全部输出在安全盒内（BI-12.2 微动作永不停止的
// 调度逻辑面；真机 24h 面归 T12-G1-01 门禁 debt）。
func TestPropertyIdleContinuity(t *testing.T) {
	f := func(seed int64) bool {
		m, err := NewMapper(DefaultTable(), DefaultLimits())
		if err != nil {
			t.Errorf("DefaultTable/DefaultLimits 被拒：%v", err)
			return false
		}
		l := DefaultLimits()
		const totalMs, stepMs, maxGapMs = 6 * 3600_000, 1000, 90_000
		last := int64(-1)
		for atMs := int64(0); atMs <= totalMs; atMs += stepMs {
			out := m.IdleTick(atMs, seed)
			if !inBox(out, l) {
				t.Logf("atMs=%d idle 输出越安全盒：%v", atMs, out)
				return false
			}
			if len(out) > 0 {
				if last >= 0 && atMs-last > maxGapMs {
					t.Logf("idle 间隔 %dms > 90s（seed=%d）", atMs-last, seed)
					return false
				}
				last = atMs
			}
		}
		return true
	}
	for _, seed := range []int64{1, 2, 3} {
		if !f(seed) {
			t.Errorf("P5 idle 连续性被违反（seed=%d）", seed)
		}
	}
	if err := quick.Check(func(seed int64) bool { return f(((seed % 1000) + 1000) % 1000) }, &quick.Config{MaxCount: 30}); err != nil {
		t.Errorf("P5 idle 连续性被违反：%v", err)
	}
}
