// 表驱动单测（m2-spec §11 三件套之一）：NewMapper 校验面（表完备/互斥组无
// 交叠/上限自洽）、Map 选一/互斥/配额截断/强度缩放/未知标签回落/静默锁存、
// IdleTick 确定性与安全盒。
package motionmap

import (
	"math"
	"strings"
	"testing"
)

func mustMapper(t *testing.T, tab Table, l Limits) *Mapper {
	t.Helper()
	m, err := NewMapper(tab, l)
	if err != nil {
		t.Fatalf("NewMapper: %v", err)
	}
	return m
}

// TestNewMapperValidation 校验面表驱动：每个坏配置须被拒绝（唯一 error 出口）。
func TestNewMapperValidation(t *testing.T) {
	type mcase struct {
		name   string
		tab    Table
		limits Limits
		want   string // 期望错误片段
	}
	bad := func(name, want string, mut func(Table, Limits) (Table, Limits)) mcase {
		tab, l := mut(DefaultTable(), DefaultLimits())
		return mcase{name: name, tab: tab, limits: l, want: want}
	}
	minimal := Table{DefaultLabel: {{ID: "sway", Amp: 8, Group: "body"}}}
	cases := []mcase{
		bad("表空", "Table 不得为空", func(tb Table, l Limits) (Table, Limits) { return Table{}, l }),
		bad("缺中性默认行", "缺中性默认行", func(tb Table, l Limits) (Table, Limits) { delete(tb, DefaultLabel); return tb, l }),
		bad("行为空", "行为空", func(tb Table, l Limits) (Table, Limits) { tb["calm"] = nil; return tb, l }),
		bad("动作 ID 空", "ID 为空", func(tb Table, l Limits) (Table, Limits) {
			tb["calm"] = []Action{{ID: "", Amp: 8, Group: "body"}}
			return tb, l
		}),
		bad("动作 Group 空", "Group 为空", func(tb Table, l Limits) (Table, Limits) {
			tb["calm"] = []Action{{ID: "sway", Amp: 8, Group: ""}}
			return tb, l
		}),
		bad("动作幅度超上限", "幅度", func(tb Table, l Limits) (Table, Limits) {
			tb["calm"] = []Action{{ID: "sway", Amp: 101, Group: "body"}}
			return tb, l
		}),
		bad("全局和零", "GlobalAmpSum", func(tb Table, l Limits) (Table, Limits) { l.GlobalAmpSum = 0; return tb, l }),
		bad("全局和超上限", "GlobalAmpSum", func(tb Table, l Limits) (Table, Limits) { l.GlobalAmpSum = 101; return tb, l }),
		bad("GroupDuty nil", "GroupDuty 不得为 nil", func(tb Table, l Limits) (Table, Limits) { l.GroupDuty = nil; return tb, l }),
		bad("表组缺上限", "缺 GroupDuty 上限", func(tb Table, l Limits) (Table, Limits) { delete(l.GroupDuty, "head"); return tb, l }),
		{"idle 组缺上限", minimal, Limits{GroupDuty: map[string]uint8{"body": 50},
			MutexGroups: [][]string{{"body"}}, GlobalAmpSum: 100}, "缺 GroupDuty 上限"},
		bad("duty 值超上限", "超上限", func(tb Table, l Limits) (Table, Limits) { l.GroupDuty["body"] = 101; return tb, l }),
		bad("duty 键空", "空组名", func(tb Table, l Limits) (Table, Limits) { l.GroupDuty[""] = 40; return tb, l }),
		bad("互斥集空", "互斥集", func(tb Table, l Limits) (Table, Limits) { l.MutexGroups = [][]string{{"head"}, {}}; return tb, l }),
		bad("互斥集空组名", "空组名", func(tb Table, l Limits) (Table, Limits) { l.MutexGroups = [][]string{{"head", ""}}; return tb, l }),
		bad("集内组重复", "重复", func(tb Table, l Limits) (Table, Limits) { l.MutexGroups = [][]string{{"head", "head"}}; return tb, l }),
		bad("组跨集交叠", "交叠", func(tb Table, l Limits) (Table, Limits) {
			l.MutexGroups = [][]string{{"head"}, {"head", "body"}}
			return tb, l
		}),
		{"互斥组缺上限", minimal, Limits{GroupDuty: map[string]uint8{"body": 50, "face": 40},
			MutexGroups: [][]string{{"body"}, {"legs"}}, GlobalAmpSum: 100}, "互斥组"},
	}
	for _, tc := range cases {
		_, err := NewMapper(tc.tab, tc.limits)
		if err == nil {
			t.Fatalf("%s：坏配置未被拒绝", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s：错误信息 %q 不含 %q", tc.name, err.Error(), tc.want)
		}
	}
	if _, err := NewMapper(DefaultTable(), DefaultLimits()); err != nil {
		t.Fatalf("DefaultTable/DefaultLimits 被拒：%v", err)
	}
	if _, err := NewMapper(minimal, Limits{GroupDuty: map[string]uint8{"body": 50, "face": 40},
		MutexGroups: [][]string{{"body"}}, GlobalAmpSum: 100}); err != nil {
		t.Fatalf("最小合法配置被拒：%v", err)
	}
}

// TestMapStructureAndAmps 结构面：每标签恰每组一动作（组序=行首现序）、幅度=
// 行内该 ID 的基幅度（缺省配置无截断）、输出在安全盒内。
func TestMapStructureAndAmps(t *testing.T) {
	m := mustMapper(t, DefaultTable(), DefaultLimits())
	rowIDs := map[string]map[string]uint8{} // label → ID → 基幅度（组唯一）
	for label, row := range DefaultTable() {
		ids := map[string]uint8{}
		for _, a := range row {
			ids[a.ID] = a.Amp
		}
		rowIDs[label] = ids
	}
	for _, label := range []string{"excited", "happy", "content", "calm", "sleepy", "sad", "scared", "angry", "surprised"} {
		got := m.Map(Mood{Label: label, Intensity: 1}, false, 42)
		perGroup := map[string]int{}
		sum := 0
		for _, a := range got {
			base, ok := rowIDs[label][a.ID]
			if !ok {
				t.Fatalf("%s：动作 %s 不在行内", label, a.ID)
			}
			if a.Amp != base {
				t.Fatalf("%s：动作 %s 幅度 %d ≠ 基幅度 %d（缺省配置无截断）", label, a.ID, a.Amp, base)
			}
			perGroup[a.Group]++
			sum += int(a.Amp)
		}
		for g, n := range perGroup {
			if n != 1 {
				t.Fatalf("%s：组 %s 出现 %d 个动作（须每组恰一）", label, g, n)
			}
		}
		if sum > int(DefaultLimits().GlobalAmpSum) {
			t.Fatalf("%s：ΣAmp=%d 超全局上限", label, sum)
		}
	}
}

// TestMapExactSleepy 精确值：单候选/组的行（sleepy）无选择歧义——组序=行序
// （head,body,face）、全强度幅度=基幅度、半强度=round(base/2)。
func TestMapExactSleepy(t *testing.T) {
	m := mustMapper(t, DefaultTable(), DefaultLimits())
	got := m.Map(Mood{Label: "sleepy", Intensity: 1}, false, 7)
	want := []Action{{ID: "droop", Amp: 12, Group: "head"}, {ID: "settle", Amp: 8, Group: "body"}, {ID: "lids", Amp: 10, Group: "face"}}
	if len(got) != len(want) {
		t.Fatalf("sleepy 输出数 %d ≠ %d", len(got), len(want))
	}
	for k := range want {
		if got[k] != want[k] {
			t.Fatalf("sleepy[%d] = %+v want %+v", k, got[k], want[k])
		}
	}
	got = m.Map(Mood{Label: "sleepy", Intensity: 0.5}, false, 7)
	want = []Action{{ID: "droop", Amp: 6, Group: "head"}, {ID: "settle", Amp: 4, Group: "body"}, {ID: "lids", Amp: 5, Group: "face"}}
	for k := range want {
		if got[k] != want[k] {
			t.Fatalf("sleepy 半强度[%d] = %+v want %+v", k, got[k], want[k])
		}
	}
}

// TestMapUnknownLabelFallback 未知标签→中性默认行：输出 ID 全部来自 calm 行
// （sway/settle/soft）；空标签同路径；强度越界/NaN 不 panic。
func TestMapUnknownLabelFallback(t *testing.T) {
	m := mustMapper(t, DefaultTable(), DefaultLimits())
	calmIDs := map[string]bool{"sway": true, "settle": true, "soft": true}
	for _, label := range []string{"???", "", "EXCITED"} {
		got := m.Map(Mood{Label: label, Intensity: 0.8}, false, 3)
		if len(got) == 0 {
			t.Fatalf("未知标签 %q 输出为空（须回落默认行）", label)
		}
		for _, a := range got {
			if !calmIDs[a.ID] {
				t.Fatalf("未知标签 %q 动作 %s 不在默认行", label, a.ID)
			}
		}
	}
	got := m.Map(Mood{Label: "???", Intensity: math.NaN()}, false, 3)
	for _, a := range got {
		if a.Amp != 0 {
			t.Fatalf("NaN 强度须零幅度：%+v", a)
		}
	}
	m.Map(Mood{Label: "???", Intensity: 7}, false, 3) // >1 截 1：不 panic
}

// TestMapSafetyTruncation 配额截断精确值：组上限与全局上限双绑定——
// 组内比例缩放（60→duty 30 半幅）再全局比例缩放（60→G 40 三分之二），
// 终幅=min(round(base×i), floor(配额))。
func TestMapSafetyTruncation(t *testing.T) {
	tab := Table{DefaultLabel: {{ID: "move", Amp: 60, Group: "body"}, {ID: "mimic", Amp: 60, Group: "face"}}}
	l := Limits{GroupDuty: map[string]uint8{"body": 30, "face": 30},
		MutexGroups: [][]string{{"body"}, {"face"}}, GlobalAmpSum: 40}
	m := mustMapper(t, tab, l)
	got := m.Map(Mood{Label: "calm", Intensity: 1}, false, 0)
	// 配额=floor(60×0.5×(40/60))=20；round(60×1)=60→截 20
	if len(got) != 2 || got[0] != (Action{ID: "move", Amp: 20, Group: "body"}) || got[1] != (Action{ID: "mimic", Amp: 20, Group: "face"}) {
		t.Fatalf("全强度截断错：got %+v", got)
	}
	got = m.Map(Mood{Label: "calm", Intensity: 0.25}, false, 0)
	// round(60×0.25)=15 ≤ 配额 20 → 保留
	if got[0].Amp != 15 || got[1].Amp != 15 {
		t.Fatalf("低强度须免截断：got %+v", got)
	}
}

// TestMapGroupDutyCap 单动作超组上限：quota=floor(base×duty/base)=duty，
// 全强度幅度截至组上限。
func TestMapGroupDutyCap(t *testing.T) {
	tab := Table{DefaultLabel: {{ID: "nod", Amp: 20, Group: "head"}}}
	l := Limits{GroupDuty: map[string]uint8{"head": 15, "body": 50, "face": 40},
		MutexGroups: [][]string{{"head"}}, GlobalAmpSum: 100}
	m := mustMapper(t, tab, l)
	got := m.Map(Mood{Label: "calm", Intensity: 1}, false, 0)
	if len(got) != 1 || got[0].Amp != 15 {
		t.Fatalf("组上限截断错：got %+v（want nod/15）", got)
	}
}

// TestMapMutexFilter 跨组互斥：集 {head,body} 保基幅度大者；平手取 ID 字典
// 序小者；非集内组不受影响。
func TestMapMutexFilter(t *testing.T) {
	tab := Table{DefaultLabel: {{ID: "nod", Amp: 20, Group: "head"}, {ID: "bounce", Amp: 30, Group: "body"}}}
	l := Limits{GroupDuty: map[string]uint8{"head": 40, "body": 50, "face": 40},
		MutexGroups: [][]string{{"head", "body"}, {"face"}}, GlobalAmpSum: 100}
	m := mustMapper(t, tab, l)
	got := m.Map(Mood{Label: "calm", Intensity: 1}, false, 0)
	if len(got) != 1 || got[0].ID != "bounce" {
		t.Fatalf("互斥须保幅度大者：got %+v（want bounce）", got)
	}
	// 平手：等幅度 20/20 → ID 字典序小者 aa 胜
	tab2 := Table{DefaultLabel: {{ID: "zz", Amp: 20, Group: "head"}, {ID: "aa", Amp: 20, Group: "body"}}}
	m2 := mustMapper(t, tab2, l)
	got = m2.Map(Mood{Label: "calm", Intensity: 1}, false, 0)
	if len(got) != 1 || got[0].ID != "aa" {
		t.Fatalf("互斥平手须取 ID 字典序小者：got %+v（want aa）", got)
	}
}

// TestMapSilentLatch 静默锁存：silent=true 恒 nil 且锁存（IdleTick 同口径
// nil）；silent=false 解锁；锁存对后续任意情绪生效。
func TestMapSilentLatch(t *testing.T) {
	m := mustMapper(t, DefaultTable(), DefaultLimits())
	if got := m.Map(Mood{Label: "excited", Intensity: 1}, true, 0); got != nil {
		t.Fatalf("静默须 nil：got %+v", got)
	}
	if got := m.IdleTick(4000, 0); got != nil {
		t.Fatalf("静默锁存后 IdleTick 须 nil：got %+v", got)
	}
	if got := m.Map(Mood{Label: "angry", Intensity: 1}, true, 99); got != nil {
		t.Fatalf("静默态任意情绪须 nil：got %+v", got)
	}
	if got := m.Map(Mood{Label: "excited", Intensity: 1}, false, 0); len(got) == 0 {
		t.Fatalf("解锁后须恢复输出")
	}
	if got := m.IdleTick(4000, 0); len(got) == 0 {
		t.Fatalf("解锁后 IdleTick 须恢复输出")
	}
}

// TestMapDeterminismAndVariety 确定性与防机械重复：同 (mood,seed) 跨实例/
// 跨调用逐字段全等；变 seed 在多候选行上产生不同选择（≥2 种输出）。
func TestMapDeterminismAndVariety(t *testing.T) {
	m1 := mustMapper(t, DefaultTable(), DefaultLimits())
	m2 := mustMapper(t, DefaultTable(), DefaultLimits())
	mood := Mood{Label: "excited", Intensity: 0.9}
	a, b := m1.Map(mood, false, 7), m2.Map(mood, false, 7)
	if len(a) != len(b) {
		t.Fatalf("跨实例输出数不等：%d vs %d", len(a), len(b))
	}
	for k := range a {
		if a[k] != b[k] {
			t.Fatalf("跨实例分歧[%d]：%+v vs %+v", k, a[k], b[k])
		}
	}
	if c := m1.Map(mood, false, 7); len(c) != len(a) {
		t.Fatalf("同实例重放输出数不等")
	} else {
		for k := range c {
			if c[k] != a[k] {
				t.Fatalf("同实例重放分歧[%d]：%+v vs %+v", k, c[k], a[k])
			}
		}
	}
	distinct := map[string]bool{}
	for seed := int64(0); seed < 16; seed++ {
		out := m1.Map(mood, false, seed)
		key := ""
		for _, x := range out {
			key += x.ID + ","
		}
		distinct[key] = true
	}
	if len(distinct) < 2 {
		t.Fatalf("变 seed 未防机械重复：16 个 seed 仅 %d 种输出", len(distinct))
	}
}

// TestIdleTick idle 微动作：呼吸恒在（三角波幅度 ∈[3,8]）；眨眼按窗哈希
// 出现/缺席两态皆有；安全盒恒成立；负 atMs 按 0 处理；同 (atMs,seed) 同输出。
func TestIdleTick(t *testing.T) {
	m := mustMapper(t, DefaultTable(), DefaultLimits())
	l := DefaultLimits()
	a, b := m.IdleTick(12_345, 5), m.IdleTick(12_345, 5)
	if len(a) != len(b) {
		t.Fatalf("IdleTick 重放输出数不等")
	}
	for k := range a {
		if a[k] != b[k] {
			t.Fatalf("IdleTick 重放分歧[%d]：%+v vs %+v", k, a[k], b[k])
		}
	}
	if got := m.IdleTick(-5, 5); len(got) == 0 || got[0].ID != "breathe" {
		t.Fatalf("负 atMs 须按 0 处理且呼吸恒在：got %+v", got)
	}
	blinkSeen, noBlink, breathSeen := false, false, false
	for atMs := int64(0); atMs < 40_000; atMs += 500 {
		out := m.IdleTick(atMs, 9)
		if len(out) == 0 {
			t.Fatalf("atMs=%d 输出为空（呼吸恒在）", atMs)
		}
		sum := 0
		groups := map[string]int{}
		for _, x := range out {
			if x.ID == "breathe" {
				breathSeen = true
				if x.Amp < idleBreathMinAmp || x.Amp > idleBreathMaxAmp {
					t.Fatalf("呼吸幅度越界 [3,8]：%d", x.Amp)
				}
			}
			if x.ID == "blink" {
				blinkSeen = true
			}
			groups[x.Group]++
			sum += int(x.Amp)
		}
		if sum > int(l.GlobalAmpSum) {
			t.Fatalf("atMs=%d idle ΣAmp=%d 超全局上限", atMs, sum)
		}
		for g, n := range groups {
			if n > 1 {
				t.Fatalf("atMs=%d 组 %s %d 个动作", atMs, g, n)
			}
			if sum := sumOfGroup(out, g); sum > int(l.GroupDuty[g]) {
				t.Fatalf("atMs=%d 组 %s Σ=%d 超上限", atMs, g, sum)
			}
		}
		if len(out) == 1 {
			noBlink = true // 仅呼吸的窗（眨眼缺席面）
		}
	}
	if !breathSeen || !blinkSeen || !noBlink {
		t.Fatalf("idle 微动作面不完备：breath=%v blink=%v 无眨眼窗=%v", breathSeen, blinkSeen, noBlink)
	}
}

func sumOfGroup(out []Action, g string) int {
	s := 0
	for _, x := range out {
		if x.Group == g {
			s += int(x.Amp)
		}
	}
	return s
}
