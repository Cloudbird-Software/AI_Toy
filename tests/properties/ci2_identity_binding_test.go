// 运行时镜像，实现落地后替换
// CI-2：任何输出可回溯唯一身份；T5 拒判瞬间记忆通道转只读缓存（spec §8.2）。
package properties

import (
	"fmt"
	"math/rand"
	"testing"
	"testing/quick"
)

// TestCI2_UniqueIdentityBinding quick 属性：任何 Output 对象，
// 身份绑定要么已验证且 UserID 非空，要么未验证且 UserID 为空。
// 不允许半绑定（Verified=true 空串 或 Verified=false 非空）。
func TestCI2_UniqueIdentityBinding(t *testing.T) {
	// 属性：对于任意构造的 Output，IdentityBinding() 真值 ↔ 绑定符合规则
	// （已验证→UserID 非空；未验证→UserID 空）。
	prop := func(seed int64, nOutputs int) bool {
		if nOutputs < 0 {
			nOutputs = -nOutputs
		}
		nOutputs = (nOutputs % 50) + 1
		r := rand.New(rand.NewSource(seed))
		for i := 0; i < nOutputs; i++ {
			verified := r.Intn(2) == 0
			// uid 策略：
			//   已验证：3/4 非空（合法），1/4 空（非法半绑定）
			//   未验证：3/4 空（合法匿名），1/4 非空（非法泄漏）
			var uid string
			if verified {
				if r.Intn(4) == 0 {
					uid = ""
				} else {
					uid = fmt.Sprintf("u%d", r.Intn(10000))
				}
			} else {
				if r.Intn(4) == 0 {
					uid = fmt.Sprintf("leak%d", r.Intn(10000))
				} else {
					uid = ""
				}
			}
			o := Output{
				ID:      fmt.Sprintf("o%d", i),
				Issuer:  Identity{UserID: uid, Verified: verified},
				Content: "hi",
			}
			want := (verified && uid != "") || (!verified && uid == "")
			if o.IdentityBinding() != want {
				return false
			}
		}
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("CI-2 身份绑定不变量 失效: %v", err)
	}
}

// TestCI2_OutputTraceability 表驱动：身份绑定正反例。
func TestCI2_OutputTraceability(t *testing.T) {
	cases := []struct {
		name   string
		out    Output
		wantOK bool
	}{
		{
			name:   "已验证+非空ID → 合法",
			out:    Output{ID: "o1", Issuer: Identity{UserID: "u100", Verified: true}, Content: "hi"},
			wantOK: true,
		},
		{
			name:   "未验证+空串 → 合法（匿名访客）",
			out:    Output{ID: "o2", Issuer: Identity{UserID: "", Verified: false}, Content: "who?"},
			wantOK: true,
		},
		{
			name:   "已验证但UserID空 → 非法半绑定",
			out:    Output{ID: "o3", Issuer: Identity{UserID: "", Verified: true}, Content: "hmm"},
			wantOK: false,
		},
		{
			name:   "未验证但有UserID → 非法泄漏",
			out:    Output{ID: "o4", Issuer: Identity{UserID: "u501", Verified: false}, Content: "hi"},
			wantOK: false,
		},
		{
			name:   "输出ID非空但身份完全零值 → 合法（访客输出无身份）",
			out:    Output{ID: "o5", Issuer: Identity{}, Content: "hi"},
			wantOK: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.out.IdentityBinding()
			if got != c.wantOK {
				t.Errorf("IdentityBinding()=%v want %v  out=%+v", got, c.wantOK, c.out)
			}
		})
	}
}

// TestCI2_T5RejectSwitchesMemoryToReadOnly quick 属性：T5 声纹拒判后，
// 记忆通道从 MemReadWrite 转为 MemReadOnly；对已经只读/禁用的保持不变。
func TestCI2_T5RejectSwitchesMemoryToReadOnly(t *testing.T) {
	prop := func(seed int64, nSteps int) bool {
		if nSteps < 0 {
			nSteps = -nSteps
		}
		nSteps = (nSteps % 10) + 1
		r := rand.New(rand.NewSource(seed))
		mode := MemoryMode(r.Intn(3))
		// 应用多次 T5 拒判
		for i := 0; i < nSteps; i++ {
			mode = T5Reject(mode)
		}
		// 结果必须是 MemReadOnly 或 MemDisabled（不会回到读写）
		return mode == MemReadOnly || mode == MemDisabled
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("CI-2 T5拒判→记忆只读 失效: %v", err)
	}
}

// TestCI2_T5RejectBoundary 表驱动：T5 拒判的边界态。
func TestCI2_T5RejectBoundary(t *testing.T) {
	cases := []struct {
		name string
		in   MemoryMode
		want MemoryMode
	}{
		{"ReadWrite→ReadOnly", MemReadWrite, MemReadOnly},
		{"ReadOnly→保持ReadOnly", MemReadOnly, MemReadOnly},
		{"Disabled→保持Disabled", MemDisabled, MemDisabled},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := T5Reject(c.in)
			if got != c.want {
				t.Errorf("T5Reject(%v)=%v want %v", c.in, got, c.want)
			}
		})
	}
}

// TestCI2_BindingAfterT5Chain 表驱动：在组件级档位上叠加 T5 拒判 → 记忆保持只读。
func TestCI2_BindingAfterT5Chain(t *testing.T) {
	state := CompTierMap{L0, L0, L0, L0, L0, L0, L0}
	// 应用声纹拒判故障
	state = FailApply(state, FailVoiceprintReject)
	g := state.GlobalCapability()
	// 记忆组件至少得有 MemoryRO
	if !TierCaps(state[CompMemory]).Has(CapMemoryRO) {
		t.Errorf("记忆组件档位 %v 不包含只读能力", state[CompMemory])
	}
	// 全局能力不能有 MemoryRW（组件 Memory 档位升了）
	if g.Has(CapMemoryRW) && !TierCaps(state[CompMemory]).Has(CapMemoryRW) {
		// 若组件内存已无RW，而全局还含RW则矛盾——GlobalCapability会排除
		t.Errorf("全局能力不应含 MemoryRW：%v", g)
	}
}
