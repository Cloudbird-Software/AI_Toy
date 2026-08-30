// T14 表驱动穷举单测（m3-spec §2 资产卡必做：四档×全事件全表断言后继合法/
// 无死锁/L0 可达/每档绑定正确安全配置；AGENTS.md 本包禁令行）。
package runtimefsm

import (
	"reflect"
	"testing"

	"github.com/Cloudbird-Software/AI_Toy/tests/properties"
)

// allTierMap 全组件同档的档位表。
func allTierMap(t properties.Tier) properties.CompTierMap {
	var m properties.CompTierMap
	for i := range m {
		m[i] = t
	}
	return m
}

// TestTierCapsMonotonicNesting 档位能力单调嵌套（^CapScripted 口径——镜像
// 语义：预置脚本模式位是 L2/L3 深降级形态位，不属能力扩增；降档永不放大
// 功能能力边界——AGENTS.md 禁令的镜像同源断言）。
func TestTierCapsMonotonicNesting(t *testing.T) {
	r := Runtime{}
	for hi := properties.L0; hi < properties.L3; hi++ {
		hiCaps := r.TierCaps(hi+1) &^ properties.CapScripted
		loCaps := r.TierCaps(hi) &^ properties.CapScripted
		if hiCaps&loCaps != hiCaps {
			t.Fatalf("TierCaps(%s) ⊄ TierCaps(%s)（能力嵌套破坏）", hi+1, hi)
		}
	}
	for _, lo := range []properties.Tier{properties.L0, properties.L1} {
		if r.TierCaps(lo).Has(properties.CapScripted) {
			t.Fatalf("档 %s 不应带预置脚本模式位（深降级形态）", lo)
		}
	}
	for _, hi := range []properties.Tier{properties.L2, properties.L3} {
		if !r.TierCaps(hi).Has(properties.CapScripted) {
			t.Fatalf("档 %s 应带预置脚本模式位（深降级形态）", hi)
		}
	}
}

// TestTierSafetyBinding 每档绑定正确安全配置：L0/L1→Strict、L2/L3→Base
// （L3 也不可完全裸奔——CapSafetyBase 位恒在）。
func TestTierSafetyBinding(t *testing.T) {
	r := Runtime{}
	want := []properties.SafetyWatermark{properties.SafetyStrict, properties.SafetyStrict,
		properties.SafetyBase, properties.SafetyBase}
	for tier := properties.L0; tier <= properties.L3; tier++ {
		m := allTierMap(tier)
		if got := r.SafetyLevel(m); got != want[tier] {
			t.Fatalf("档 %s 安全水位 got=%v want=%v", tier, got, want[tier])
		}
		if !r.TierCaps(tier).Has(properties.CapSafetyBase) {
			t.Fatalf("档 %s 无基础安全位", tier)
		}
	}
}

// TestFSMFullMatrixExhaustive 资产卡必做全表穷举：四初始档 × 全事件（8 故障
// +空事件）×全组件——后继合法（档位 ∈[L0,L3]、只降不升、CompSafety 不被压到
// L1 以下、水位不降）、无死锁（每故障允许动作集非空）、L0 可达（RecoverAll
// 从任意后继态回全 L0）。
func TestFSMFullMatrixExhaustive(t *testing.T) {
	r := Runtime{}
	for init := properties.L0; init <= properties.L3; init++ {
		for f := properties.FailNone; f <= properties.FailNoResponse; f++ {
			m := allTierMap(init)
			next := r.FailApply(m, f)
			for i, v := range next {
				if v < properties.L0 || v > properties.L3 {
					t.Fatalf("初始档 %s × 故障 %v：组件 %d 后继档 %s 越界", init, f, i, v)
				}
				if v < m[i] {
					t.Fatalf("初始档 %s × 故障 %v：组件 %d 升档（%s→%s，违反只降不升）",
						init, f, i, m[i], v)
				}
			}
			if m[properties.CompSafety] <= properties.L1 && next[properties.CompSafety] > properties.L1 {
				t.Fatalf("初始档 %s × 故障 %v：Safety 压到 %s（安全水位不降红线）",
					init, f, next[properties.CompSafety])
			}
			if r.SafetyLevel(next) < r.SafetyLevel(m) {
				t.Fatalf("初始档 %s × 故障 %v：安全水位下降", init, f)
			}
			if f != properties.FailNone && len(r.DegradeMap(f)) == 0 {
				t.Fatalf("故障 %v 允许动作集为空（死锁/未定义行为）", f)
			}
			// L0 可达（活性）：任意后继态经全量恢复回全 L0。
			a := NewArbiter()
			a.comps = next
			a.OnRecover(RecoverAll, int64(next[0])+1000)
			if got := a.Tier(); got != int(properties.L0) {
				t.Fatalf("初始档 %s × 故障 %v：RecoverAll 后全局档=%d（L0 不可达）", init, f, got)
			}
		}
	}
}

// TestFailApplyCanonicalMatrix 降级矩阵真值表（L0 全云起步，行=8 行 chaos 对齐
// CH-01/02/04/05/06/07/08+兜底；CH-03 由 loop 截断承载不入矩阵）。
func TestFailApplyCanonicalMatrix(t *testing.T) {
	r := Runtime{}
	type tc struct {
		name   string
		f      properties.FailureType
		expect properties.CompTierMap
	}
	base := allTierMap(properties.L0)
	with := func(pairs ...[2]int) properties.CompTierMap {
		m := base
		for _, p := range pairs {
			m[properties.Component(p[0])] = properties.Tier(p[1])
		}
		return m
	}
	cases := []tc{
		{"FailNone 恒等", properties.FailNone, base},
		{"CH-01 云断", properties.FailLLMConnect, with(
			[2]int{int(properties.CompLLM), int(properties.L2)}, [2]int{int(properties.CompRouter), int(properties.L1)})},
		{"CH-02 TTS 超时", properties.FailTTSTimeout, with(
			[2]int{int(properties.CompTTS), int(properties.L2)})},
		{"CH-04 记忆不可写", properties.FailMemoryWrite, with(
			[2]int{int(properties.CompMemory), int(properties.L2)})},
		{"CH-05 拒判", properties.FailVoiceprintReject, with(
			[2]int{int(properties.CompVoiceprint), int(properties.L1)})},
		{"CH-06 IMU 风暴", properties.FailIMUStorm, with(
			[2]int{int(properties.CompIMU), int(properties.L2)})},
		{"CH-07 时钟漂移", properties.FailClockDrift, with(
			[2]int{int(properties.CompMemory), int(properties.L2)})},
		{"CH-08 升级中断", properties.FailUpgradePartial, with(
			[2]int{int(properties.CompRouter), int(properties.L2)}, [2]int{int(properties.CompLLM), int(properties.L2)},
			[2]int{int(properties.CompTTS), int(properties.L2)}, [2]int{int(properties.CompMemory), int(properties.L2)},
			[2]int{int(properties.CompVoiceprint), int(properties.L2)}, [2]int{int(properties.CompIMU), int(properties.L2)})},
		{"兜底 无响应", properties.FailNoResponse, with(
			[2]int{int(properties.CompRouter), int(properties.L3)}, [2]int{int(properties.CompLLM), int(properties.L3)},
			[2]int{int(properties.CompTTS), int(properties.L3)}, [2]int{int(properties.CompMemory), int(properties.L3)},
			[2]int{int(properties.CompVoiceprint), int(properties.L3)}, [2]int{int(properties.CompIMU), int(properties.L3)},
			[2]int{int(properties.CompSafety), int(properties.L1)})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := r.FailApply(base, c.f); got != c.expect {
				t.Fatalf("got=%v want=%v", got, c.expect)
			}
		})
	}
}

// TestDegradeMapNonEmptyPerFailure 每故障允许动作集非空（CI-4 预定义集不可
// 为空——含兜底行）。
func TestDegradeMapNonEmptyPerFailure(t *testing.T) {
	r := Runtime{}
	for f := properties.FailNone; f <= properties.FailNoResponse; f++ {
		if len(r.DegradeMap(f)) == 0 {
			t.Fatalf("故障 %v 允许动作集为空", f)
		}
	}
	if !r.AllowedAction(properties.FailLLMConnect, properties.ActDropToL1) {
		t.Fatal("CH-01 应允许 ActDropToL1")
	}
	if r.AllowedAction(properties.FailNone, properties.ActDropToL3) {
		t.Fatal("FailNone 不应允许降档动作")
	}
}

// ---- Arbiter 状态面 ----

// TestArbiterFaultTransitions OnFault 迁移记录=档位表差分（事务性——一次
// 全部生效，From/To 逐组件对账）。
func TestArbiterFaultTransitions(t *testing.T) {
	a := NewArbiter()
	trs := a.OnFault(properties.FailLLMConnect, 1000)
	want := map[properties.Component][2]properties.Tier{
		properties.CompLLM:    {properties.L0, properties.L2},
		properties.CompRouter: {properties.L0, properties.L1},
	}
	if len(trs) != len(want) {
		t.Fatalf("迁移数 %d ≠ %d（%v）", len(trs), len(want), trs)
	}
	for _, tr := range trs {
		w, ok := want[tr.Comp]
		if !ok || tr.From != w[0] || tr.To != w[1] {
			t.Fatalf("迁移 %+v 与期望 %+v 不符", tr, w)
		}
		if tr.Action != properties.ActDropToL1 {
			t.Fatalf("CH-01 主降级动作 got=%v want=ActDropToL1", tr.Action)
		}
	}
	if got := a.Tier(); got != int(properties.L2) {
		t.Fatalf("全局档=%d want 2", got)
	}
}

// TestArbiterRecoverLiveness 网络恢复活性：断网→有限时间回 L0；恢复幂等
// （重复恢复零迁移）；再断网再恢复仍可达（非一次性事件）。
func TestArbiterRecoverLiveness(t *testing.T) {
	a := NewArbiter()
	a.OnFault(properties.FailLLMConnect, 1000)
	if a.Tier() != int(properties.L2) {
		t.Fatalf("断网后档=%d want 2", a.Tier())
	}
	if a.NetDownMs(1500) != 500 {
		t.Fatalf("断网时长=%d want 500", a.NetDownMs(1500))
	}
	trs := a.OnRecover(RecoverNetwork, 2000)
	if len(trs) == 0 {
		t.Fatal("网络恢复应产出迁移记录")
	}
	if a.Tier() != int(properties.L0) {
		t.Fatalf("恢复后档=%d want 0（活性违反）", a.Tier())
	}
	if a.NetDownMs(3000) != 0 {
		t.Fatalf("恢复后断网时长应归零，got %d", a.NetDownMs(3000))
	}
	// 幂等：已恢复组件零迁移。
	if trs := a.OnRecover(RecoverNetwork, 3000); len(trs) != 0 {
		t.Fatalf("重复恢复产出迁移 %v（应幂等）", trs)
	}
	// 再断再恢复（恢复不是一次性事件）。
	a.OnFault(properties.FailLLMConnect, 4000)
	if a.Tier() != int(properties.L2) {
		t.Fatalf("再断网后档=%d want 2", a.Tier())
	}
	a.OnRecover(RecoverNetwork, 5000)
	if a.Tier() != int(properties.L0) {
		t.Fatalf("再恢复后档=%d want 0", a.Tier())
	}
}

// TestArbiterLateEventClamp 迟到事件钳制（仿真时钟单调——与 loop FSM 对齐）。
func TestArbiterLateEventClamp(t *testing.T) {
	a := NewArbiter()
	a.OnFault(properties.FailIMUStorm, 5000)
	trs := a.OnFault(properties.FailTTSTimeout, 3000) // 迟到：钳到 5000
	for _, tr := range trs {
		if tr.AtMs != 5000 {
			t.Fatalf("迟到事件未钳制：AtMs=%d want 5000", tr.AtMs)
		}
	}
}

// TestArbiterSafetyNotTouchedByNetworkRecover 网络恢复不碰 Safety 档（非
// 网络域组件）；RecoverAll 全量回 L0。
func TestArbiterSafetyNotTouchedByNetworkRecover(t *testing.T) {
	a := NewArbiter()
	a.OnFault(properties.FailNoResponse, 100) // Safety→L1（兜底行保 Strict）
	before := a.CompTiers()[properties.CompSafety]
	a.OnRecover(RecoverNetwork, 200)
	if got := a.CompTiers()[properties.CompSafety]; got != before {
		t.Fatalf("网络恢复改动 Safety 档 %s→%s", before, got)
	}
	a.OnRecover(RecoverAll, 300)
	if got := a.CompTiers()[properties.CompSafety]; got != properties.L0 {
		t.Fatalf("全量恢复后 Safety=%s want L0", got)
	}
}

// ---- Offline 规则面 ----

// TestOfflineAnswer 表驱动：命中/未命中/规范化检索/档位话术。
func TestOfflineAnswer(t *testing.T) {
	o := NewOffline(map[string]string{
		"小狗为什么摇尾巴":    "因为它们开心呀",
		"晚安":          "晚安，做个好梦",
		"how are you": "Fine",
	})
	cases := []struct {
		name    string
		q       string
		tier    int
		wantAns string
		refused bool
	}{
		{"命中", "小狗为什么摇尾巴", 2, "因为它们开心呀", false},
		{"命中·规范化空白+大小写", "  How   are You ", 2, "Fine", false},
		{"未命中→L2 边界", "恐龙为什么会灭绝", 2, "这个问题我现在离线答不了，" + BoundaryL2 + "。等我连上网再告诉你，好吗？", true},
		{"未命中→L3 边界", "恐龙为什么会灭绝", 3, "这个问题我现在离线答不了，" + BoundaryL3 + "。等我连上网再告诉你，好吗？", true},
		{"空知识集全拒绝", "", 2, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, refused := o.Answer(c.q, c.tier)
			if refused != c.refused {
				t.Fatalf("refused=%v want %v（got=%q）", refused, c.refused, got)
			}
			if c.wantAns != "" && got != c.wantAns {
				t.Fatalf("got=%q want=%q", got, c.wantAns)
			}
		})
	}
	if o.Size() != 3 {
		t.Fatalf("Size=%d want 3", o.Size())
	}
	if !o.Known("晚安") || o.Known("不存在的问题") {
		t.Fatal("Known 观测面口径错误")
	}
}

// TestNewOfflineDefensiveCopy 构造后外部改写 known 不影响内部（只读视图）。
func TestNewOfflineDefensiveCopy(t *testing.T) {
	src := map[string]string{"k": "v"}
	o := NewOffline(src)
	src["k"] = "改写"
	if got, _ := o.Answer("k", 2); got != "v" {
		t.Fatalf("防御性拷贝失效：got=%q", got)
	}
}

// TestNewRuntimeSelfCheck 自洽校验绿（构造即校验面）。
func TestNewRuntimeSelfCheck(t *testing.T) {
	if _, err := NewRuntime(); err != nil {
		t.Fatalf("自洽校验失败: %v", err)
	}
}

// TestArbiterCompTiersSnapshot CompTiers 返回快照（外部改写不影响内部）。
func TestArbiterCompTiersSnapshot(t *testing.T) {
	a := NewArbiter()
	snap := a.CompTiers()
	snap[properties.CompLLM] = properties.L3
	if a.CompTiers()[properties.CompLLM] != properties.L0 {
		t.Fatal("CompTiers 非值拷贝")
	}
}

// TestRecoverScopeString 作用域可读名（报告口径）。
func TestRecoverScopeString(t *testing.T) {
	got := []string{RecoverNone.String(), RecoverNetwork.String(), RecoverAll.String()}
	want := []string{"none", "network", "all"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%v want=%v", got, want)
	}
}
