// T14 属性测试（m3-spec §2 属性行 + docs/gates/assets/T14.md，testing/quick）：
//
//	P1 只降不升+水位单调：任意故障序列下档位数值不减、安全水位不降。
//	P2 能力上界⊆档安全配置：任意档位表的全局能力恒含 SafetyBase；
//	   水位=Strict 时恒含 SafetyStrict（CI-1 单调约定）。
//	P3 确定性回放：同故障序列在两个 Arbiter 产生同一档位表与同一迁移记录。
//	P4 网络恢复活性：任意故障序列后网络恢复→非 Safety 组件回 L0；
//	   全量恢复→全局 L0（有限时间回 L0 的构造面+NetDown 归零）。
//	P5 离线产出通道封闭（编造=0 构造保证）：任意 query 的应答 ∈ 本地知识集
//	   ∪ {边界声明话术}——不存在第三种产出（拒绝话术含档位边界声明）。
//	P6 事务性切档：OnFault/OnRecover 的迁移记录=前后档位表差分（逐组件对账，
//	   无中间态）；故障动作 ∈ CI-4 预定义降级集。
package runtimefsm

import (
	"strings"
	"testing"
	"testing/quick"

	"github.com/Cloudbird-Software/AI_Toy/tests/properties"
)

// propFaultSeq quick 生成的故障序列（元素 mod 9 → FailureType；-1 终止符冗余
// 由长度控制——切片长度即序列长度）。
type propFaultSeq struct {
	Faults []int8
}

// toFailures 抽象序列 → FailureType 序列（mod 9 落合法域，负数先取正）。
func (s propFaultSeq) toFailures() []properties.FailureType {
	out := make([]properties.FailureType, 0, len(s.Faults))
	for _, f := range s.Faults {
		v := int(f) % 9
		if v < 0 {
			v += 9
		}
		out = append(out, properties.FailureType(v))
	}
	return out
}

// TestP1MonotonicNonUpgrade quick：任意故障序列档位数值不减+安全水位不降。
func TestP1MonotonicNonUpgrade(t *testing.T) {
	r := Runtime{}
	prop := func(s propFaultSeq) bool {
		m := allTierMap(properties.L0)
		wm := r.SafetyLevel(m)
		for _, f := range s.toFailures() {
			next := r.FailApply(m, f)
			for i, v := range next {
				if v < m[i] {
					return false // 升档（数值变小）
				}
			}
			if r.SafetyLevel(next) < wm {
				return false // 水位下降
			}
			m, wm = next, r.SafetyLevel(next)
		}
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 300}); err != nil {
		t.Errorf("P1 只降不升/水位单调 失效: %v", err)
	}
}

// TestP2CapabilitySubsetSafetyConfig quick：任意档位表全局能力 ⊆ 档安全配置
// （SafetyBase 恒在；Strict 水位下 SafetyStrict 恒在）。
func TestP2CapabilitySubsetSafetyConfig(t *testing.T) {
	r := Runtime{}
	prop := func(tiers [int(properties.NumComponents)]int8) bool {
		var m properties.CompTierMap
		for i, v := range tiers {
			t := properties.Tier(int(v) % 4)
			if t < 0 {
				t += 4
			}
			m[i] = t
		}
		caps := r.GlobalCapability(m)
		if !caps.Has(properties.CapSafetyBase) {
			return false
		}
		if r.SafetyLevel(m) == properties.SafetyStrict && !caps.Has(properties.CapSafetyStrict) {
			return false
		}
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 300}); err != nil {
		t.Errorf("P2 能力上界⊆档安全配置 失效: %v", err)
	}
}

// TestP3DeterministicReplay quick：同故障序列两 Arbiter 同终态同轨迹。
func TestP3DeterministicReplay(t *testing.T) {
	prop := func(s propFaultSeq, seedMs int64) bool {
		run := func() (properties.CompTierMap, []Transition) {
			a := NewArbiter()
			var trs []Transition
			at := seedMs
			if at < 0 {
				at = -at
			}
			for i, f := range s.toFailures() {
				trs = append(trs, a.OnFault(f, at+int64(i)*10)...)
			}
			return a.CompTiers(), trs
		}
		m1, t1 := run()
		m2, t2 := run()
		if m1 != m2 {
			return false
		}
		if len(t1) != len(t2) {
			return false
		}
		for i := range t1 {
			if t1[i] != t2[i] {
				return false
			}
		}
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("P3 确定性回放 失效: %v", err)
	}
}

// TestP4RecoverLiveness quick：任意故障序列后——网络恢复把非 Safety 组件全部
// 回 L0（Safety 可能停在 L1）；全量恢复全局 L0。
func TestP4RecoverLiveness(t *testing.T) {
	prop := func(s propFaultSeq) bool {
		a := NewArbiter()
		for i, f := range s.toFailures() {
			a.OnFault(f, int64(i)*10)
		}
		a.OnRecover(RecoverNetwork, 1_000_000)
		comps := a.CompTiers()
		for i := range comps {
			if properties.Component(i) == properties.CompSafety {
				continue
			}
			if comps[i] != properties.L0 {
				return false // 非 Safety 未回 L0（活性违反）
			}
		}
		a.OnRecover(RecoverAll, 2_000_000)
		return a.Tier() == int(properties.L0)
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("P4 网络恢复回 L0 活性 失效: %v", err)
	}
}

// propOffline propOffline 规则面固定知识集（P5 断言的产出域上界）。
var propOffline = NewOffline(map[string]string{
	"晚安":          "晚安，做个好梦",
	"陪我玩":         "好呀，我们玩什么呢",
	"how are you": "Fine",
})

// TestP5OfflineChannelClosure quick：任意 query 应答 ∈ known 值集 ∪ {边界
// 声明话术}（编造=0 的构造封闭面）；拒绝话术含「离线」边界声明与档位口径。
func TestP5OfflineChannelClosure(t *testing.T) {
	values := map[string]bool{"晚安，做个好梦": true, "好呀，我们玩什么呢": true, "Fine": true}
	prop := func(q string, tier int8) bool {
		ti := 2 + ((int(tier)%2)+2)%2 // 落 {2,3}（L2/L3 两档话术）
		resp, refused := propOffline.Answer(q, ti)
		if refused {
			boundary := BoundaryL2
			if ti != 2 {
				boundary = BoundaryL3
			}
			want := "这个问题我现在离线答不了，" + boundary + "。等我连上网再告诉你，好吗？"
			return resp == want && strings.Contains(resp, "离线")
		}
		return values[resp] // 命中值必来自知识集
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 300}); err != nil {
		t.Errorf("P5 离线产出通道封闭 失效: %v", err)
	}
}

// propEvent P6 抽象事件（导出字段——quick 生成器约束）。
type propEvent struct {
	F    int8
	Rec  int8 // mod 3：0=故障 1=网络恢复 2=全量恢复
	AtMs int64
}

// TestP6TransactionalSwitch quick：任意事件序列（故障/恢复交错）迁移记录=
// 前后档位表差分（逐组件对账，事务性——无中间态）；故障迁移动作 ∈ 预定义集。
func TestP6TransactionalSwitch(t *testing.T) {
	prop := func(evs []propEvent) bool {
		a := NewArbiter()
		for _, e := range evs {
			before := a.CompTiers()
			var trs []Transition
			f := properties.FailureType(((int(e.F) % 9) + 9) % 9)
			switch mod := ((int(e.Rec) % 3) + 3) % 3; {
			case mod == 0:
				trs = a.OnFault(f, e.AtMs)
			case mod == 1:
				trs = a.OnRecover(RecoverNetwork, e.AtMs)
			default:
				trs = a.OnRecover(RecoverAll, e.AtMs)
			}
			after := a.CompTiers()
			// 差分对账：迁移集合 == {组件: before≠after}。
			diff := map[properties.Component]bool{}
			for i := range before {
				if before[i] != after[i] {
					diff[properties.Component(i)] = true
				}
			}
			if len(trs) != len(diff) {
				return false
			}
			seen := map[properties.Component]bool{}
			for _, tr := range trs {
				c := tr.Comp
				if seen[c] || !diff[c] {
					return false // 重复/无变化组件产出迁移（中间态泄漏）
				}
				if tr.From != before[c] || tr.To != after[c] {
					return false
				}
				if tr.Fault != properties.FailNone && !degradeMap(tr.Fault)[tr.Action] {
					return false // 动作 ∉ CI-4 预定义集
				}
				seen[c] = true
			}
		}
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("P6 事务性切档 失效: %v", err)
	}
}
