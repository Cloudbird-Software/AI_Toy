// 运行时镜像，实现落地后替换
// CI-4：依赖失效 → 行为 ∈ 预定义降级集；无响应 >10s 不道歉 = 违反（spec §8.2）。
package properties

import (
	"math/rand"
	"testing"
	"testing/quick"
)

// TestCI4_FailureActionInPredefinedSet quick 属性：任何 (故障, 动作) 对，
// 只要 AllowedAction 判定合法，动作必须在 DegradeMap 的预定义集合内；
// 同时验证 DegradeMap 对所有故障类型至少返回 1 个合法动作。
func TestCI4_FailureActionInPredefinedSet(t *testing.T) {
	prop := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))
		for f := FailNone; f <= FailNoResponse; f++ {
			allowed := DegradeMap(f)
			if len(allowed) == 0 {
				return false
			}
			// 随机挑 5 个动作，验证合法性判定一致
			for k := 0; k < 5; k++ {
				a := DegradeAction(r.Intn(int(ActApology) + 1))
				if AllowedAction(f, a) != allowed[a] {
					return false
				}
			}
		}
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("CI-4 降级动作属于预定义集 失效: %v", err)
	}
}

// TestCI4_PredefinedDegradeSet 表驱动：每类故障的允许动作正反例。
func TestCI4_PredefinedDegradeSet(t *testing.T) {
	cases := []struct {
		name    string
		failure FailureType
		action  DegradeAction
		allowed bool
	}{
		// FailNone → 只能 ActNone
		{"无故障不允许降档", FailNone, ActDropToL1, false},
		{"无故障=ActNone", FailNone, ActNone, true},
		// FailLLMConnect
		{"LLM断连→降L1", FailLLMConnect, ActDropToL1, true},
		{"LLM断连→降L2", FailLLMConnect, ActDropToL2, true},
		{"LLM断连→道歉", FailLLMConnect, ActApology, true},
		{"LLM断连不允许回滚", FailLLMConnect, ActRollback, false},
		// FailTTSTimeout
		{"TTS超时→降L2", FailTTSTimeout, ActDropToL2, true},
		{"TTS超时→降L3", FailTTSTimeout, ActDropToL3, true},
		{"TTS超时不允许IMU限流", FailTTSTimeout, ActIMUThrottle, false},
		// FailMemoryWrite
		{"记忆不可写→只读", FailMemoryWrite, ActMemoryReadOnly, true},
		// FailVoiceprintReject
		{"声纹拒判→重试+不确定", FailVoiceprintReject, ActVoiceprintRetry, true},
		// FailIMUStorm
		{"IMU风暴→限流", FailIMUStorm, ActIMUThrottle, true},
		{"IMU风暴不允许降L3", FailIMUStorm, ActDropToL3, false},
		// FailClockDrift
		{"时钟漂移→时间类记忆停写", FailClockDrift, ActTimeFreeze, true},
		// FailUpgradePartial
		{"升级中断→回滚", FailUpgradePartial, ActRollback, true},
		{"升级中断不允许声纹重试", FailUpgradePartial, ActVoiceprintRetry, false},
		// FailNoResponse
		{"无响应>10s→道歉", FailNoResponse, ActApology, true},
		{"无响应>10s→降L3", FailNoResponse, ActDropToL3, true},
		{"无响应>10s不允许只ActNone", FailNoResponse, ActNone, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := AllowedAction(c.failure, c.action)
			if got != c.allowed {
				t.Errorf("AllowedAction(%v,%v)=%v want %v", c.failure, c.action, got, c.allowed)
			}
		})
	}
}

// noResponseCompliant 是「FailNoResponse 后实现给出的 (动作, Output) 是否合规」的判定，
// 供 CI-4 quick 属性验证。规则：
//   - 动作必须在 DegradeMap(FailNoResponse) 集合内
//   - ActApology 必须搭配 Apologize==true 的输出
//   - ActDropToL3 对 Apologize 无硬性要求
//   - ActNone 永远不合规（静默违反）
//
// 这个函数是运行时镜像内的参考判定；未来落地实现的行为应等价于它。
func noResponseCompliant(a DegradeAction, out Output) bool {
	if !AllowedAction(FailNoResponse, a) {
		return false
	}
	if a == ActNone {
		return false
	}
	if a == ActApology && !out.Apologize {
		return false
	}
	return true
}

// TestCI4_NoResponseMustApologize quick 属性：
// 对任意 (动作, 输出) 对，noResponseCompliant 的判定真值等于规则的推导结果。
// 等价地说：noResponseCompliant 不应误报或漏报。
func TestCI4_NoResponseMustApologize(t *testing.T) {
	prop := func(seed int64, nTrials int) bool {
		if nTrials < 0 {
			nTrials = -nTrials
		}
		nTrials = (nTrials % 40) + 1
		r := rand.New(rand.NewSource(seed))
		for i := 0; i < nTrials; i++ {
			action := DegradeAction(r.Intn(int(ActApology) + 1))
			apologizeOut := r.Intn(2) == 0
			out := Output{
				ID:        "o",
				Issuer:    Identity{UserID: "u", Verified: true},
				Content:   "...",
				Apologize: apologizeOut,
			}
			// 由规则手算期望值
			inSet := AllowedAction(FailNoResponse, action)
			noneCase := (action == ActNone)
			apologyCase := (action == ActApology && !out.Apologize)
			want := inSet && !noneCase && !apologyCase
			got := noResponseCompliant(action, out)
			if got != want {
				return false
			}
		}
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("CI-4 无响应>10s不道歉=违反 失效: %v", err)
	}
}

// TestCI4_NoResponseApologyBoundary 表驱动：无响应道歉边界。
func TestCI4_NoResponseApologyBoundary(t *testing.T) {
	cases := []struct {
		name   string
		action DegradeAction
		out    Output
		wantOK bool
	}{
		{
			name:   "ActNone → 违反",
			action: ActNone,
			out:    Output{ID: "o1", Content: "..."},
			wantOK: false,
		},
		{
			name:   "ActApology 但输出没道歉 → 违反",
			action: ActApology,
			out:    Output{ID: "o2", Content: "没道歉", Apologize: false},
			wantOK: false,
		},
		{
			name:   "ActApology + 道歉输出 → 合规",
			action: ActApology,
			out:    Output{ID: "o3", Content: "对不起…", Apologize: true},
			wantOK: true,
		},
		{
			name:   "ActDropToL3 + 有输出 → 合规（L3档响应即可）",
			action: ActDropToL3,
			out:    Output{ID: "o4", Content: "我好像卡住了…", Apologize: false},
			wantOK: true,
		},
		{
			name:   "ActDropToL3 + 道歉 → 合规",
			action: ActDropToL3,
			out:    Output{ID: "o5", Content: "抱歉…", Apologize: true},
			wantOK: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// 按 FailNoResponse 判定：合法动作 且 (ActApology→Apologize=true) 且 (ActNone=违反)
			var ok bool
			if AllowedAction(FailNoResponse, c.action) {
				if c.action == ActNone {
					ok = false
				} else if c.action == ActApology {
					ok = c.out.Apologize
				} else {
					ok = true
				}
			}
			if ok != c.wantOK {
				t.Errorf("got OK=%v want %v  action=%v out=%+v", ok, c.wantOK, c.action, c.out)
			}
		})
	}
}

// TestCI4_FailApplyProducesAllowedActions quick 属性：FailApply 对任意初始状态
// 应用任意故障，结果保持单调不降（只降档/不升档）。
// 还验证几类可观察强不变量：
//   - FailNoResponse 之后所有组件至少是 L3（即都等于 L3，因为 L3 是最低档位）
//   - FailUpgradePartial 之后任一组件若原 < L2 则至少 L2
//   - FailLLMConnect 之后 CompLLM 至少 L2，CompRouter 至少 L1
func TestCI4_FailApplyProducesAllowedActions(t *testing.T) {
	prop := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))
		// 随机初始组件档位（每组件 L0..L3）
		var init CompTierMap
		for i := range init {
			init[i] = Tier(r.Intn(4))
		}
		f := FailureType(r.Intn(int(FailNoResponse) + 1))
		next := FailApply(init, f)
		// 1) 必须保持单调不升档（数值不减）
		if !MonotonicNonUpgrade(init, next) {
			return false
		}
		// 2) 故障语义断言（注意：Safety 组件为了水位单调，在 FailUpgradePartial 不降级；
		//    在 FailNoResponse 中最多降到 L1）
		switch f {
		case FailNoResponse:
			for i := range next {
				if Component(i) == CompSafety {
					if next[i] < L1 {
						return false
					}
				} else if next[i] != L3 {
					return false
				}
			}
		case FailUpgradePartial:
			// Safety 除外，其他组件原 < L2 → 至少 L2
			for i := range init {
				if Component(i) == CompSafety {
					continue
				}
				if init[i] < L2 && next[i] < L2 {
					return false
				}
			}
		case FailLLMConnect:
			if init[CompLLM] < L2 && next[CompLLM] != L2 {
				return false
			}
			if init[CompRouter] < L1 && next[CompRouter] != L1 {
				return false
			}
		case FailTTSTimeout:
			if init[CompTTS] < L2 && next[CompTTS] != L2 {
				return false
			}
		case FailMemoryWrite, FailClockDrift:
			if init[CompMemory] < L2 && next[CompMemory] != L2 {
				return false
			}
		case FailIMUStorm:
			if init[CompIMU] < L2 && next[CompIMU] != L2 {
				return false
			}
		case FailVoiceprintReject:
			if init[CompVoiceprint] < L1 && next[CompVoiceprint] != L1 {
				return false
			}
		}
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 300}); err != nil {
		t.Errorf("CI-4 FailApply 产生合规结果 失效: %v", err)
	}
}
