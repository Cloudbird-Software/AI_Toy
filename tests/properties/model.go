// 运行时镜像，实现落地后替换
// package properties：tests/properties 下本地最小运行时镜像（spec §11.1：Go 侧用
// testing/quick 做运行时镜像）。纯数据结构 + 纯函数操作，不 import packages/*。
// 涵盖：档位 L0-L3、组件能力集、身份绑定、分段延迟预算、故障→降级映射。
//
// 镜像→真身替换流程（IR #65，审计漏洞 #3「镜像永不替换」的封口）：
//  1. 属性测试一律经 contract.go 的行为契约接口驱动（RuntimeModel/IdentityModel/
//     BudgetModel/DegradeModel），默认注入本文件的镜像实现（MirrorRuntime 等）；
//  2. 实现 packages/go/runtime-fsm（四档降级 FSM）等真实包，满足上述契约
//     （方法签名与本文件镜像函数一致，状态显式传参）；
//  3. go test -tags real ./tests/properties/... 编译通过——real_runtime_test.go 的
//     接口绑定在空壳期必编译失败（强制函数），编译通过即接线完成；
//  4. 属性测试即以同一断言口径跑真身（两份驱动：默认镜像 / -tags real 真身），
//     镜像保留作回归参照与规格可执行样例，不被删除。
package properties

// Tier 档位枚举，L0 全功能云档 → L3 纯离线最低档。
type Tier int

const (
	L0 Tier = iota // 全功能：云 LLM + 云 TTS + 记忆读写 + 声纹 + IMU
	L1             // 云 TTS + 本地语义兜底 + 记忆只读
	L2             // 端侧离线：本地 TTS + 本地对话模型 + 记忆只读
	L3             // 最低档：预置脚本 + 无状态响应
)

// String 返回档位可读名。
func (t Tier) String() string {
	switch t {
	case L0:
		return "L0"
	case L1:
		return "L1"
	case L2:
		return "L2"
	case L3:
		return "L3"
	}
	return "?"
}

// Capability 组件能力标签（位枚举，可组合）。
type Capability uint16

const (
	CapCloudLLM     Capability = 1 << iota // 云大模型
	CapCloudTTS                            // 云 TTS
	CapLocalTTS                            // 端侧 TTS
	CapMemoryRW                            // 记忆可读写
	CapMemoryRO                            // 记忆只读缓存
	CapVoiceprint                          // 声纹识别
	CapIMU                                 // IMU 微动作
	CapSafetyStrict                        // 严格安全策略
	CapSafetyBase                          // 基础安全策略
	CapScripted                            // 预置脚本模式
)

// TierCaps 各档位下组件理论能力上限（spec §8.2 CI-1）。
// 注意：更高档位数值 = 更低能力（L0全功能，L3仅脚本）。
// 非 Safety 组件的档位：所有档位都具有 SafetyStrict | SafetyBase 位（水位仅由
// CompSafety 组件独立决定，其他组件降档不影响全局安全等级——CI-1 单调的核心约定）。
// 记忆语义：MemoryRW 隐含 MemoryRO（可写自然可读）。
func TierCaps(t Tier) Capability {
	// 通用安全位：非 Safety 组件的所有档位下全开。Safety 组件独立逻辑由 SafetyLevel
	// 基于 Safety 组件档位推导（见 SafetyLevel 注释），也可以在该位掩码里区分等级。
	safetyFull := CapSafetyStrict | CapSafetyBase
	switch t {
	case L0:
		return CapCloudLLM | CapCloudTTS | CapLocalTTS | CapMemoryRW | CapMemoryRO |
			CapVoiceprint | CapIMU | safetyFull
	case L1:
		return CapCloudTTS | CapLocalTTS | CapMemoryRO | CapVoiceprint | CapIMU | safetyFull
	case L2:
		return CapLocalTTS | CapMemoryRO | CapScripted | safetyFull
	case L3:
		return CapScripted | safetyFull
	}
	return 0
}

// Has 判断 c 是否包含子能力。
func (c Capability) Has(sub Capability) bool { return c&sub == sub }

// Intersect 取能力交集。
func (c Capability) Intersect(o Capability) Capability { return c & o }

// Component 组件名枚举。
type Component int

const (
	CompRouter Component = iota
	CompLLM
	CompTTS
	CompMemory
	CompVoiceprint
	CompIMU
	CompSafety
	NumComponents
)

// ComponentName 返回可读组件名。
func ComponentName(c Component) string {
	switch c {
	case CompRouter:
		return "Router"
	case CompLLM:
		return "LLM"
	case CompTTS:
		return "TTS"
	case CompMemory:
		return "Memory"
	case CompVoiceprint:
		return "Voiceprint"
	case CompIMU:
		return "IMU"
	case CompSafety:
		return "Safety"
	}
	return "?"
}

// CompDefaultTier 组件各自默认档位（初始 L0），可独立降档。
type CompTierMap [NumComponents]Tier

// GlobalCapability 按 CI-1：全局能力 = ∩ 各组件在其档位下的能力集。
func (m CompTierMap) GlobalCapability() Capability {
	var acc Capability = 0xFFFF // 全 1，逐组件求交
	for i := 0; i < int(NumComponents); i++ {
		acc = acc.Intersect(TierCaps(m[i]))
	}
	return acc
}

// SafetyWatermark 安全配置严格度水位：数值越大越严格。CI-1 要求单调不降。
type SafetyWatermark int

const (
	SafetyOff    SafetyWatermark = 0
	SafetyBase   SafetyWatermark = 1
	SafetyMid    SafetyWatermark = 2
	SafetyStrict SafetyWatermark = 3
)

// SafetyLevel 从组件档位表推导当前安全水位（spec §8.2 CI-1：安全配置=最严格者）。
// 仅由 CompSafety 组件自身档位决定，避免被其他组件的能力交集掩盖。
// 水位：L0/L1 → Strict；L2 → Base；L3 → Base（L3 也给基础安全，防止完全裸奔）。
// 若需严格表达「L3 下 SafetyOff」，调用方可手动扩展。
func (m CompTierMap) SafetyLevel() SafetyWatermark {
	switch m[CompSafety] {
	case L0, L1:
		return SafetyStrict
	case L2:
		return SafetyBase
	case L3:
		return SafetyBase
	}
	return SafetyOff
}

// SafetyLevel（Capability 接收器旧版，兼容 GlobalCapability 调用链；
// 实现上等价 SafetyStrict 位优先，否则 Base，否则 Off。）
func (g Capability) SafetyLevel() SafetyWatermark {
	if g.Has(CapSafetyStrict) {
		return SafetyStrict
	}
	if g.Has(CapSafetyBase) {
		return SafetyBase
	}
	return SafetyOff
}

// Identity 用户身份标识。
type Identity struct {
	UserID   string // 稳定身份 ID（声纹匹配后绑定）
	Verified bool   // 是否已通过 T5 验证
}

// Output 任何对外输出对象（含可回溯身份）。
type Output struct {
	ID        string   // 输出唯一 ID（UUID 风格，运行时镜像内用短串）
	Issuer    Identity // 输出绑定的身份（即使不确定也带未验证身份或空）
	Content   string   // 载荷文本
	Apologize bool     // 是否包含道歉声明（CI-4 用）
}

// IdentityBinding CI-2：任何输出可回溯唯一身份。
// 绑定规则：Verified=true 时 Issuer.UserID 非空；未验证时必须空串，不许半绑定。
func (o Output) IdentityBinding() bool {
	if o.Issuer.Verified {
		return o.Issuer.UserID != ""
	}
	return o.Issuer.UserID == ""
}

// MemoryMode 记忆通道模式。
type MemoryMode int

const (
	MemReadWrite MemoryMode = iota // 正常读写
	MemReadOnly                    // 只读缓存（T5 拒判等触发）
	MemDisabled                    // 完全不可用
)

// T5Reject 模拟 T5 声纹拒判事件 → 瞬间把记忆通道转为只读缓存（CI-2）。
func T5Reject(m MemoryMode) MemoryMode {
	switch m {
	case MemReadWrite:
		return MemReadOnly
	default:
		return m // 已降级则保持
	}
}

// BudgetSegment 分段延迟预算（毫秒）。spec §8.2 CI-3：Σ分段 P95 − 并行重叠 ≤ 1500。
type BudgetSegment struct {
	Name    string
	P95     int // 毫秒 P95
	Overlap int // 与上一段并行重叠毫秒数（可被扣除）
}

// BudgetTotal 计算 ΣP95 − Σ并行重叠。CI-3 要求结果 ≤ 1500ms。
func BudgetTotal(segs []BudgetSegment) int {
	var sumP95, sumOver int
	for _, s := range segs {
		if s.P95 < 0 {
			sumP95 += 0
		} else {
			sumP95 += s.P95
		}
		if s.Overlap < 0 {
			sumOver += 0
		} else {
			sumOver += s.Overlap
		}
	}
	// 重叠不能超过总 P95
	if sumOver > sumP95 {
		sumOver = sumP95
	}
	return sumP95 - sumOver
}

// LatencySample 统计口径：多次采样用于 2σ 劣化判定。
type LatencySample struct {
	Values []int // 单次总延迟采样
}

// MeanStd 返回均值与标准差（分母 N，运行时镜像用简化版）。
func (s LatencySample) MeanStd() (float64, float64) {
	if len(s.Values) == 0 {
		return 0, 0
	}
	var sum float64
	for _, v := range s.Values {
		sum += float64(v)
	}
	mean := sum / float64(len(s.Values))
	var varSum float64
	for _, v := range s.Values {
		d := float64(v) - mean
		varSum += d * d
	}
	std := varSum / float64(len(s.Values))
	// 手工开方（避免 math 依赖）：牛顿迭代
	sqrt := std
	if sqrt > 0 {
		for i := 0; i < 20; i++ {
			sqrt = (sqrt + std/sqrt) / 2
		}
	} else {
		sqrt = 0
	}
	return mean, sqrt
}

// BudgetStatus 预算状态。
type BudgetStatus int

const (
	BudgetGreen BudgetStatus = iota
	BudgetYellow
	BudgetRed // >2σ 劣化且无划拨
)

// BudgetCheck CI-3：与基线对比判断预算状态。
// baseline 为基线采样，current 为当前采样。若 current.mean > baseline.mean+2σ 且
// noReallocation（无划拨）→ BudgetRed。
func BudgetCheck(baseline, current LatencySample, noReallocation bool) BudgetStatus {
	_, baseStd := baseline.MeanStd()
	baseMean, _ := baseline.MeanStd()
	curMean, _ := current.MeanStd()
	threshold := baseMean + 2*baseStd
	if curMean > threshold && noReallocation {
		return BudgetRed
	}
	if curMean > threshold {
		return BudgetYellow
	}
	return BudgetGreen
}

// FailureType 故障类型枚举。
type FailureType int

const (
	FailNone             FailureType = iota
	FailLLMConnect                   // 云 LLM 断连
	FailTTSTimeout                   // TTS 超时/首包失败
	FailMemoryWrite                  // 记忆存储不可写
	FailVoiceprintReject             // 声纹拒判
	FailIMUStorm                     // IMU 事件风暴
	FailClockDrift                   // 时钟漂移
	FailUpgradePartial               // 升级中断
	FailNoResponse                   // 无响应 >10s
)

// DegradeAction 降级动作（CI-4 预定义降级集）。
type DegradeAction int

const (
	ActNone            DegradeAction = iota
	ActDropToL1                      // 降至 L1：云 TTS + 本地语义兜底
	ActDropToL2                      // 降至 L2：全端侧
	ActDropToL3                      // 降至 L3：预置脚本
	ActMemoryReadOnly                // 记忆只读
	ActVoiceprintRetry               // 声纹重试+明示不确定
	ActIMUThrottle                   // IMU 限流聚合
	ActTimeFreeze                    // 时间类记忆停写
	ActRollback                      // 原子回滚
	ActApology                       // 道歉声明
)

// DegradeMap 故障 → 允许的降级动作集合。CI-4 要求任何依赖失效行为 ∈ 预定义集。
func DegradeMap(f FailureType) map[DegradeAction]bool {
	m := map[DegradeAction]bool{}
	switch f {
	case FailNone:
		m[ActNone] = true
	case FailLLMConnect:
		m[ActDropToL1] = true
		m[ActDropToL2] = true
		m[ActApology] = true
	case FailTTSTimeout:
		m[ActDropToL2] = true
		m[ActDropToL3] = true
	case FailMemoryWrite:
		m[ActMemoryReadOnly] = true
		m[ActApology] = true
	case FailVoiceprintReject:
		m[ActVoiceprintRetry] = true
		m[ActMemoryReadOnly] = true
	case FailIMUStorm:
		m[ActIMUThrottle] = true
	case FailClockDrift:
		m[ActTimeFreeze] = true
	case FailUpgradePartial:
		m[ActRollback] = true
	case FailNoResponse:
		m[ActApology] = true
		m[ActDropToL3] = true
	}
	return m
}

// AllowedAction 判断某故障下的动作是否在预定义降级集里。
func AllowedAction(f FailureType, a DegradeAction) bool {
	return DegradeMap(f)[a]
}

// FailApply 把故障作用到组件档位表上，返回新档位表。
// 单调不变：新档位不高于原档位（即 L0→L1→L2→L3 单向，数值只增不减）。
// 任何赋值都通过 maxTier(当前, 目标) 保证不升档（不把更大数值往小调）。
func FailApply(m CompTierMap, f FailureType) CompTierMap {
	next := m
	set := func(c Component, target Tier) {
		if next[c] < target {
			next[c] = target
		}
	}
	switch f {
	case FailNone:
		return next
	case FailLLMConnect:
		if next[CompLLM] < L2 {
			set(CompLLM, L2)
			set(CompRouter, L1) // 仅当 Router<L1 时才设 L1，避免「从 L3 强制回升到 L1」的升档
		} else {
			// CompLLM 已经>=L2，但仍可能需要让 Router 至少降到 L1。
			set(CompRouter, L1)
		}
	case FailTTSTimeout:
		set(CompTTS, L2)
	case FailMemoryWrite:
		set(CompMemory, L2)
	case FailVoiceprintReject:
		set(CompVoiceprint, L1)
	case FailIMUStorm:
		set(CompIMU, L2)
	case FailClockDrift:
		set(CompMemory, L2)
	case FailUpgradePartial:
		// 非 Safety 组件统一降到 L2；Safety 组件保持不降级（CI-1 水位单调）
		for i := range next {
			if Component(i) == CompSafety {
				continue
			}
			set(Component(i), L2)
		}
	case FailNoResponse:
		// 非 Safety 组件统一降到 L3；Safety 组件最多降到 L1（保持 Strict 水位）
		for i := range next {
			if Component(i) == CompSafety {
				set(CompSafety, L1)
				continue
			}
			set(Component(i), L3)
		}
	}
	return next
}

// MonotonicNonUpgrade 验证 new 每个组件档位都 ≤ old（数值越大越低档，即降档或不变）。
// CI-1：降档安全水位单调不降 ↔ 档位只增不减。
func MonotonicNonUpgrade(old, next CompTierMap) bool {
	for i := 0; i < int(NumComponents); i++ {
		if next[i] < old[i] { // 数值变小 = 升档（恢复），这里不允许在故障应用后发生
			return false
		}
	}
	return true
}
