// Package runtimefsm 实现 T14 离线运行时 go 侧规则面（m3-spec §2，实现卡 #104）。
//
// 本文件=契约真身：Runtime 实现 tests/properties/contract.go 全部四个行为契约
// （RuntimeModel/IdentityModel/BudgetModel/DegradeModel，CI-1..CI-4 断言口径不变）
// ——tests/properties/real_runtime_test.go 在 -tags real 下绑定本类型，属性测试
// 即以同一断言口径跑真身（默认镜像保留作回归参照）。
//
// import 纪律（m3-spec §2 唯一例外，写进 #104 PR 模板）：白名单=标准库+
// tests/properties（仅类型——契约签名类型落在 model.go/contract.go 纯类型面；
// 断言本体在 ci1..ci4_test.go 的 _test 编译单元内，import 不可达，「被评代码
// import 评测断言实现」红线不破）。状态面（Arbiter/Offline）见 arbiter.go/
// offline.go，同样只依赖标准库+tests/properties 类型。
package runtimefsm

import (
	"fmt"

	"github.com/Cloudbird-Software/AI_Toy/tests/properties"
)

// Runtime 四档降级 FSM 契约真身：零值可用、方法全值接收器（纯函数式，状态
// 显式传参——testing/quick 随机状态推进兼容）。语义与 properties 镜像同源
// （tests/properties/model.go 为规格可执行样例；CH-01..08 对齐见 arbiter.go）。
type Runtime struct{}

// NewRuntime 构造并自检：降级矩阵/档位嵌套/水位表自洽（fail-closed——不自洽
// 的档位表在组装期即红，不留到运行面）。
func NewRuntime() (*Runtime, error) {
	r := Runtime{}
	if err := r.selfCheck(); err != nil {
		return nil, fmt.Errorf("runtimefsm: 自洽校验失败: %w", err)
	}
	return &r, nil
}

// selfCheck 三面自洽：①能力单调嵌套（剔除 CapScripted 模式位口径——预置脚
// 本模式是深降级形态位而非能力扩增，镜像同此：L2/L3 带 Scripted 而 L0/L1 不带，
// 严格位级嵌套不成立；非脚本位严格单调 L0⊇L1⊇L2⊇L3）；②水位表（L0/L1→Strict、
// L2/L3→Base，Safety 组件永不被故障压到 L1 以下——安全水位不降）；③降级
// 矩阵（每故障允许动作集非空、FailApply 从任意档位出发只降不升且水位不降）。
func (Runtime) selfCheck() error {
	// ① 档位能力单调嵌套（^CapScripted 口径：降档永不放大功能能力边界）。
	for t := properties.L0; t < properties.L3; t++ {
		hi := tierCaps(t+1) &^ properties.CapScripted
		lo := tierCaps(t) &^ properties.CapScripted
		if hi&lo != hi {
			return fmt.Errorf("档位能力非单调嵌套：TierCaps(%s) ⊄ TierCaps(%s)", t+1, t)
		}
	}
	// ①' 预置脚本模式位仅出现在深降级档（L2/L3——能力收缩形态，不属能力扩增）。
	for t := properties.L0; t <= properties.L1; t++ {
		if tierCaps(t).Has(properties.CapScripted) {
			return fmt.Errorf("档 %s 不应带预置脚本模式位（深降级形态）", t)
		}
	}
	// ② 水位表 + 每档绑定正确安全配置。
	for t := properties.L0; t <= properties.L3; t++ {
		var m properties.CompTierMap
		for i := range m {
			m[i] = t
		}
		want := properties.SafetyBase
		if t <= properties.L1 {
			want = properties.SafetyStrict
		}
		if got := safetyLevel(m); got != want {
			return fmt.Errorf("档 %s 安全水位 got=%v want=%v", t, got, want)
		}
		if !tierCaps(t).Has(properties.CapSafetyBase) {
			return fmt.Errorf("档 %s 无基础安全位（L3 也不可完全裸奔）", t)
		}
	}
	// ③ 降级矩阵自洽：全初始档位 × 全故障——只降不升、Safety 档位不被故障
	// 压过 L1（Safety 已在 L2/L3 的初始态保持不变——水位由 ② 的 Base 兜底）、
	// 水位不降、允许动作集非空。
	for init := properties.L0; init <= properties.L3; init++ {
		var m properties.CompTierMap
		for i := range m {
			m[i] = init
		}
		for f := properties.FailNone; f <= properties.FailNoResponse; f++ {
			next := failApply(m, f)
			for i := range next {
				if next[i] < m[i] {
					return fmt.Errorf("故障 %v 从档 %s 出现升档（违反只降不升）", f, init)
				}
			}
			if m[properties.CompSafety] <= properties.L1 && next[properties.CompSafety] > properties.L1 {
				return fmt.Errorf("故障 %v 把 Safety 压到 %s（安全水位不降红线）", f, next[properties.CompSafety])
			}
			if safetyLevel(next) < safetyLevel(m) {
				return fmt.Errorf("故障 %v 安全水位下降（CI-1 单调违反）", f)
			}
			if len(degradeMap(f)) == 0 {
				return fmt.Errorf("故障 %v 允许动作集为空（CI-4 预定义集不可为空）", f)
			}
		}
	}
	return nil
}

// ---- RuntimeModel（CI-1/CI-4 档位机）----

// FailApply 把故障作用到组件档位表上，返回新档位表（只降不升：档位数值只增
// 不减）。降级矩阵与 chaos CH-01/02/04/05/06/07/08 一一对应（CH-03 输出超长
// 由 loop 截断防线承载，M1 已落，不入档位矩阵；FailNoResponse=兜底行）。
func (Runtime) FailApply(m properties.CompTierMap, f properties.FailureType) properties.CompTierMap {
	return failApply(m, f)
}

// GlobalCapability 全局能力 = ∩ 各组件在其档位下的能力集（CI-1）。
func (Runtime) GlobalCapability(m properties.CompTierMap) properties.Capability {
	return globalCapability(m)
}

// SafetyLevel 从组件档位表推导当前安全水位（最严格者；仅由 CompSafety 自身
// 档位决定——其他组件降档不影响全局安全等级，CI-1 单调的核心约定）。
func (Runtime) SafetyLevel(m properties.CompTierMap) properties.SafetyWatermark {
	return safetyLevel(m)
}

// TierCaps 档位下组件理论能力上限（L0 全功能 → L3 仅预置脚本+基础安全）。
// 非 Safety 组件全档位带 SafetyStrict|SafetyBase 位（水位仅由 Safety 组件
// 独立决定）；MemoryRW 隐含 MemoryRO。
func (Runtime) TierCaps(t properties.Tier) properties.Capability {
	return tierCaps(t)
}

// ---- IdentityModel（CI-2 身份）----

// IdentityBinding 判定输出身份绑定是否合规：已验证→UserID 非空；未验证→空。
// 禁止半绑定（CI-2）。
func (Runtime) IdentityBinding(o properties.Output) bool {
	if o.Issuer.Verified {
		return o.Issuer.UserID != ""
	}
	return o.Issuer.UserID == ""
}

// T5Reject 声纹拒判 → 记忆通道转只读缓存（CI-2；已降级保持不变）。
func (Runtime) T5Reject(m properties.MemoryMode) properties.MemoryMode {
	return t5Reject(m)
}

// ---- BudgetModel（CI-3 预算）----

// BudgetTotal 计算 Σ分段 P95 − Σ并行重叠（重叠超总额裁剪、负值按 0 计；
// CI-3 要求结果 ≤ 1500ms）。
func (Runtime) BudgetTotal(segs []properties.BudgetSegment) int {
	return budgetTotal(segs)
}

// BudgetCheck 与基线对比判断预算状态：current.mean > baseline.mean+2σ 且
// 无划拨 → 红（BudgetRed）；超阈但有划拨 → 黄；否则绿。
func (Runtime) BudgetCheck(b, c properties.LatencySample, noReallocation bool) properties.BudgetStatus {
	return budgetCheck(b, c, noReallocation)
}

// MeanStd 返回采样均值与标准差（分母 N；空采样=0,0）。
func (Runtime) MeanStd(s properties.LatencySample) (mean, std float64) {
	return meanStd(s)
}

// ---- DegradeModel（CI-4 降级集）----

// DegradeMap 返回故障允许的降级动作集合（任何依赖失效行为 ∈ 预定义集）。
func (Runtime) DegradeMap(f properties.FailureType) map[properties.DegradeAction]bool {
	return degradeMap(f)
}

// AllowedAction 判断某故障下的动作是否在预定义降级集里。
func (Runtime) AllowedAction(f properties.FailureType, a properties.DegradeAction) bool {
	return degradeMap(f)[a]
}

// ---- 纯函数实现面（与 properties 镜像同语义；包内零状态）----

func tierCaps(t properties.Tier) properties.Capability {
	safetyFull := properties.CapSafetyStrict | properties.CapSafetyBase
	switch t {
	case properties.L0:
		return properties.CapCloudLLM | properties.CapCloudTTS | properties.CapLocalTTS |
			properties.CapMemoryRW | properties.CapMemoryRO | properties.CapVoiceprint |
			properties.CapIMU | safetyFull
	case properties.L1:
		return properties.CapCloudTTS | properties.CapLocalTTS | properties.CapMemoryRO |
			properties.CapVoiceprint | properties.CapIMU | safetyFull
	case properties.L2:
		return properties.CapLocalTTS | properties.CapMemoryRO | properties.CapScripted | safetyFull
	case properties.L3:
		return properties.CapScripted | safetyFull
	}
	return 0
}

func globalCapability(m properties.CompTierMap) properties.Capability {
	var acc properties.Capability = 0xFFFF
	for i := 0; i < int(properties.NumComponents); i++ {
		acc &= tierCaps(m[i])
	}
	return acc
}

func safetyLevel(m properties.CompTierMap) properties.SafetyWatermark {
	switch m[properties.CompSafety] {
	case properties.L0, properties.L1:
		return properties.SafetyStrict
	case properties.L2, properties.L3:
		return properties.SafetyBase
	}
	return properties.SafetyOff
}

func failApply(m properties.CompTierMap, f properties.FailureType) properties.CompTierMap {
	next := m
	set := func(c properties.Component, target properties.Tier) {
		if next[c] < target {
			next[c] = target
		}
	}
	switch f {
	case properties.FailNone:
		return next
	case properties.FailLLMConnect: // CH-01：云断 → LLM 落端侧 L2、Router 至少 L1
		set(properties.CompLLM, properties.L2)
		set(properties.CompRouter, properties.L1)
	case properties.FailTTSTimeout: // CH-02：云 TTS 超时 → 全端侧合成
		set(properties.CompTTS, properties.L2)
	case properties.FailMemoryWrite: // CH-04：记忆不可写 → 只读
		set(properties.CompMemory, properties.L2)
	case properties.FailVoiceprintReject: // CH-05：拒判 → 声纹降 L1（只读联动归 CI-2 面）
		set(properties.CompVoiceprint, properties.L1)
	case properties.FailIMUStorm: // CH-06：事件风暴 → IMU 限流
		set(properties.CompIMU, properties.L2)
	case properties.FailClockDrift: // CH-07：时钟漂移 → 时间类记忆停写
		set(properties.CompMemory, properties.L2)
	case properties.FailUpgradePartial: // CH-08：升级中断 → 非 Safety 统一 L2（Safety 不降）
		for i := range next {
			if properties.Component(i) == properties.CompSafety {
				continue
			}
			set(properties.Component(i), properties.L2)
		}
	case properties.FailNoResponse: // 兜底行：非 Safety→L3、Safety 至多 L1（保 Strict）
		for i := range next {
			if properties.Component(i) == properties.CompSafety {
				set(properties.CompSafety, properties.L1)
				continue
			}
			set(properties.Component(i), properties.L3)
		}
	}
	return next
}

func t5Reject(m properties.MemoryMode) properties.MemoryMode {
	switch m {
	case properties.MemReadWrite:
		return properties.MemReadOnly
	default:
		return m
	}
}

func budgetTotal(segs []properties.BudgetSegment) int {
	var sumP95, sumOver int
	for _, s := range segs {
		if s.P95 > 0 {
			sumP95 += s.P95
		}
		if s.Overlap > 0 {
			sumOver += s.Overlap
		}
	}
	if sumOver > sumP95 {
		sumOver = sumP95
	}
	return sumP95 - sumOver
}

func budgetCheck(b, c properties.LatencySample, noReallocation bool) properties.BudgetStatus {
	baseMean, baseStd := meanStd(b)
	curMean, _ := meanStd(c)
	if curMean > baseMean+2*baseStd {
		if noReallocation {
			return properties.BudgetRed
		}
		return properties.BudgetYellow
	}
	return properties.BudgetGreen
}

func meanStd(s properties.LatencySample) (float64, float64) {
	n := len(s.Values)
	if n == 0 {
		return 0, 0
	}
	var sum float64
	for _, v := range s.Values {
		sum += float64(v)
	}
	mean := sum / float64(n)
	var varSum float64
	for _, v := range s.Values {
		d := float64(v) - mean
		varSum += d * d
	}
	std := sqrt(varSum / float64(n))
	return mean, std
}

// sqrt 牛顿迭代开方（与镜像同法：确定性逐位复现，无依赖差异面）。
func sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	g := x
	for i := 0; i < 40; i++ {
		g = (g + x/g) / 2
	}
	return g
}

func degradeMap(f properties.FailureType) map[properties.DegradeAction]bool {
	m := map[properties.DegradeAction]bool{}
	switch f {
	case properties.FailNone:
		m[properties.ActNone] = true
	case properties.FailLLMConnect:
		m[properties.ActDropToL1] = true
		m[properties.ActDropToL2] = true
		m[properties.ActApology] = true
	case properties.FailTTSTimeout:
		m[properties.ActDropToL2] = true
		m[properties.ActDropToL3] = true
	case properties.FailMemoryWrite:
		m[properties.ActMemoryReadOnly] = true
		m[properties.ActApology] = true
	case properties.FailVoiceprintReject:
		m[properties.ActVoiceprintRetry] = true
		m[properties.ActMemoryReadOnly] = true
	case properties.FailIMUStorm:
		m[properties.ActIMUThrottle] = true
	case properties.FailClockDrift:
		m[properties.ActTimeFreeze] = true
	case properties.FailUpgradePartial:
		m[properties.ActRollback] = true
	case properties.FailNoResponse:
		m[properties.ActApology] = true
		m[properties.ActDropToL3] = true
	}
	return m
}
