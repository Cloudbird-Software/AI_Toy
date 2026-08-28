// 运行时镜像，实现落地后替换
// CI-1：任意故障序列 → 全局能力集 ⊆ 各组件档位能力交集；
// 安全配置=最严格者；降档安全水位单调不降（spec §8.2）。
package properties

import (
	"math/rand"
	"testing"
	"testing/quick"
)

// TestCI1_GlobalCapabilitySubset quick 属性：任何故障序列应用后，
// 全局能力集是每个组件档位能力的子集（即 GlobalCapability 实现自洽）。
func TestCI1_GlobalCapabilitySubset(t *testing.T) {
	prop := func(seed int64, nSteps int) bool {
		if nSteps < 0 {
			nSteps = -nSteps
		}
		nSteps = nSteps % 8 // 最多 7 步
		r := rand.New(rand.NewSource(seed))
		// 初始态：全部 L0
		var state CompTierMap
		for i := range state {
			state[i] = L0
		}
		for step := 0; step < nSteps; step++ {
			f := FailureType(r.Intn(int(FailNoResponse) + 1))
			state = FailApply(state, f)
		}
		global := state.GlobalCapability()
		// 全局能力必须是每个组件档位能力的子集
		for i := 0; i < int(NumComponents); i++ {
			compCap := TierCaps(state[i])
			if !compCap.Has(global) {
				return false
			}
		}
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("CI-1 全局能力⊆组件能力交集 失效: %v", err)
	}
}

// TestCI1_SafetyIsStrictest 表驱动：安全配置 = 最严格者。
func TestCI1_SafetyIsStrictest(t *testing.T) {
	cases := []struct {
		name  string
		tiers CompTierMap
		want  SafetyWatermark
	}{
		{
			name:  "全L0严格安全",
			tiers: CompTierMap{L0, L0, L0, L0, L0, L0, L0},
			want:  SafetyStrict,
		},
		{
			name:  "Safety组件L2其余L0 → 全局只剩SafetyBase",
			tiers: CompTierMap{L0, L0, L0, L0, L0, L0, L2},
			want:  SafetyBase,
		},
		{
			name:  "全L2基础安全",
			tiers: CompTierMap{L2, L2, L2, L2, L2, L2, L2},
			want:  SafetyBase,
		},
		{
			name:  "全L3只有脚本（含基础安全）",
			tiers: CompTierMap{L3, L3, L3, L3, L3, L3, L3},
			want:  SafetyBase, // L3 有 CapSafetyBase
		},
		{
			name:  "Safety组件L3其余L0 → 全局 SafetyBase（L0∩L3安全位只有Base共通）",
			tiers: CompTierMap{L0, L0, L0, L0, L0, L0, L3},
			want:  SafetyBase,
		},
		{
			name:  "LLM落L1、Safety仍L0 → SafetyStrict（Safety决定）",
			tiers: CompTierMap{L1, L1, L0, L0, L0, L0, L0},
			want:  SafetyStrict,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.tiers.SafetyLevel()
			if got != c.want {
				t.Errorf("got %v want %v", got, c.want)
			}
			// 再校验：SafetyLevel 同时兼容 Capability 调用链时不应矛盾
			g := c.tiers.GlobalCapability()
			if c.tiers[CompSafety] <= L1 && g.SafetyLevel() != SafetyStrict {
				t.Errorf("全局能力里 SafetyStrict 丢失: %v", g)
			}
		})
	}
}

// TestCI1_WatermarkMonotonic quick 属性：按任意故障序列推进，
// 安全水位严格单调不降（不会从 Strict 掉回 Base 或 Off）。
func TestCI1_WatermarkMonotonic(t *testing.T) {
	prop := func(seed int64, nSteps int) bool {
		if nSteps < 0 {
			nSteps = -nSteps
		}
		nSteps = (nSteps % 12) + 1 // 1~12 步
		r := rand.New(rand.NewSource(seed))
		var state CompTierMap
		for i := range state {
			state[i] = L0
		}
		prevLevel := state.SafetyLevel()
		for step := 0; step < nSteps; step++ {
			f := FailureType(r.Intn(int(FailNoResponse) + 1))
			state = FailApply(state, f)
			curLevel := state.SafetyLevel()
			// 水位必须不降
			if curLevel < prevLevel {
				return false
			}
			prevLevel = curLevel
		}
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("CI-1 安全水位单调不降 失效: %v", err)
	}
}

// TestCI1_NoUpgradeDuringFailure 表驱动边界：几类典型故障序列下档位不回升。
func TestCI1_NoUpgradeDuringFailure(t *testing.T) {
	start := CompTierMap{L0, L0, L0, L0, L0, L0, L0}
	cases := []struct {
		name    string
		seq     []FailureType
		wantCap Capability // 期望最终全局能力位
	}{
		{
			name:    "空序列",
			seq:     nil,
			wantCap: TierCaps(L0), // 全组件L0求交=L0
		},
		{
			name: "LLM断连 → Router=L1, LLM=L2, 其他L0；全局取交集",
			seq:  []FailureType{FailLLMConnect},
			// Router(L1) = {CloudTTS, LocalTTS, MemoryRO, Voiceprint, IMU, safetyFull}
			// LLM(L2)    = {LocalTTS, MemoryRO, Scripted, safetyFull}
			// 其他(L0)   = 所有功能位 + safetyFull
			// 三元交集   = LocalTTS | MemoryRO | safetyFull
			wantCap: CapLocalTTS | CapMemoryRO | CapSafetyStrict | CapSafetyBase,
		},
		{
			name: "NoResponse → 非安全=L3, Safety=L1；交集 = safetyFull（两档共有安全位）",
			seq:  []FailureType{FailNoResponse},
			// TierCaps(L3) = Scripted | safetyFull
			// TierCaps(L1) = CloudTTS | LocalTTS | MemoryRO | Voiceprint | IMU | safetyFull
			// ∩ 只留 safetyFull
			wantCap: CapSafetyStrict | CapSafetyBase,
		},
		{
			name: "LLM断连→TTS超时→记忆不可写 串列 不升档",
			seq:  []FailureType{FailLLMConnect, FailTTSTimeout, FailMemoryWrite},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			state := start
			for _, f := range c.seq {
				next := FailApply(state, f)
				if !MonotonicNonUpgrade(state, next) {
					t.Errorf("故障 %v 后出现升档（违反单调）", f)
				}
				state = next
			}
			if c.wantCap != 0 {
				got := state.GlobalCapability()
				if got != c.wantCap {
					t.Errorf("全局能力 got=%v want=%v", got, c.wantCap)
				}
			}
		})
	}
}
