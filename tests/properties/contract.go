// 契约层：镜像 API 的行为接口（IR #65 / 审计漏洞 #3——镜像永不替换风险）。
//
// 属性测试一律通过本文件的行为接口驱动被测模型，不再直连 model.go 的镜像函数：
//   - 默认构建：注入镜像实现（MirrorRuntime 等，行为与直调镜像完全一致）；
//   - -tags real：real_runtime_test.go 把注入点切到 packages/go/runtime-fsm 真身。
//     当前该包为空壳，-tags real 必然编译失败——这是刻意的强制函数：真实实现
//     落地之日该 tag 必须编译通过，属性测试方可切换真身（详见该文件头注释）。
//
// 接口按镜像职责拆四个（签名 = model.go 现有镜像函数签名；状态显式传参，保持纯函数式，
// 与 testing/quick 的随机状态推进兼容）：
//
//	RuntimeModel  CI-1/CI-4 档位机：故障迁移、能力交集、安全水位
//	IdentityModel CI-2 身份：输出身份绑定、T5 拒判→记忆只读
//	BudgetModel   CI-3 预算：分段延迟总额、2σ 劣化判定
//	DegradeModel  CI-4 降级集：故障→允许动作
//
// 镜像→真身替换流程：
//  1. 实现 packages/go/runtime-fsm（四档降级 FSM）等真实包，满足下列契约；
//  2. go test -tags real ./tests/properties/... 编译通过（「包未实现」编译错误消失
//     即接线完成；契约若分域落在别的包，同步调整 real_runtime_test.go 的绑定目标）；
//  3. 属性测试两份驱动同一断言口径：默认镜像、-tags real 真身。
package properties

// RuntimeModel CI-1/CI-4 档位机行为契约（状态显式传参，纯函数式）。
type RuntimeModel interface {
	// FailApply 把故障作用到组件档位表上，返回新档位表（只降不升）。
	FailApply(m CompTierMap, f FailureType) CompTierMap
	// GlobalCapability 返回全局能力 = ∩ 各组件在其档位下的能力集。
	GlobalCapability(m CompTierMap) Capability
	// SafetyLevel 从组件档位表推导当前安全水位（最严格者，单调不降）。
	SafetyLevel(m CompTierMap) SafetyWatermark
	// TierCaps 返回档位下组件理论能力上限。
	TierCaps(t Tier) Capability
}

// IdentityModel CI-2 身份行为契约。
type IdentityModel interface {
	// IdentityBinding 判定输出身份绑定是否合规（已验证→UserID 非空；未验证→空）。
	IdentityBinding(o Output) bool
	// T5Reject 模拟 T5 声纹拒判 → 记忆通道转只读缓存。
	T5Reject(m MemoryMode) MemoryMode
}

// BudgetModel CI-3 延迟预算行为契约。
type BudgetModel interface {
	// BudgetTotal 计算 Σ分段 P95 − Σ并行重叠（CI-3 要求 ≤ 1500ms）。
	BudgetTotal(segs []BudgetSegment) int
	// BudgetCheck 与基线对比判断预算状态（>2σ 劣化且无划拨 → 红）。
	BudgetCheck(baseline, current LatencySample, noReallocation bool) BudgetStatus
	// MeanStd 返回采样均值与标准差（分母 N）。
	MeanStd(s LatencySample) (mean, std float64)
}

// DegradeModel CI-4 降级集行为契约。
type DegradeModel interface {
	// DegradeMap 返回故障允许的降级动作集合（任何依赖失效行为 ∈ 预定义集）。
	DegradeMap(f FailureType) map[DegradeAction]bool
	// AllowedAction 判断某故障下的动作是否在预定义降级集里。
	AllowedAction(f FailureType, a DegradeAction) bool
}

// ---- 镜像实现（委托 model.go 纯函数；satisfies 编译断言见文件尾） ----

// MirrorRuntime 镜像档位机：model.go 镜像函数的接口化包装。
type MirrorRuntime struct{}

// FailApply 镜像实现：委托包级 FailApply。
func (MirrorRuntime) FailApply(m CompTierMap, f FailureType) CompTierMap { return FailApply(m, f) }

// GlobalCapability 镜像实现：委托 CompTierMap.GlobalCapability。
func (MirrorRuntime) GlobalCapability(m CompTierMap) Capability { return m.GlobalCapability() }

// SafetyLevel 镜像实现：委托 CompTierMap.SafetyLevel。
func (MirrorRuntime) SafetyLevel(m CompTierMap) SafetyWatermark { return m.SafetyLevel() }

// TierCaps 镜像实现：委托包级 TierCaps。
func (MirrorRuntime) TierCaps(t Tier) Capability { return TierCaps(t) }

// MirrorIdentity 镜像身份模型。
type MirrorIdentity struct{}

// IdentityBinding 镜像实现：委托 Output.IdentityBinding。
func (MirrorIdentity) IdentityBinding(o Output) bool { return o.IdentityBinding() }

// T5Reject 镜像实现：委托包级 T5Reject。
func (MirrorIdentity) T5Reject(m MemoryMode) MemoryMode { return T5Reject(m) }

// MirrorBudget 镜像预算模型。
type MirrorBudget struct{}

// BudgetTotal 镜像实现：委托包级 BudgetTotal。
func (MirrorBudget) BudgetTotal(segs []BudgetSegment) int { return BudgetTotal(segs) }

// BudgetCheck 镜像实现：委托包级 BudgetCheck。
func (MirrorBudget) BudgetCheck(baseline, current LatencySample, noReallocation bool) BudgetStatus {
	return BudgetCheck(baseline, current, noReallocation)
}

// MeanStd 镜像实现：委托 LatencySample.MeanStd。
func (MirrorBudget) MeanStd(s LatencySample) (float64, float64) { return s.MeanStd() }

// MirrorDegrade 镜像降级集模型。
type MirrorDegrade struct{}

// DegradeMap 镜像实现：委托包级 DegradeMap。
func (MirrorDegrade) DegradeMap(f FailureType) map[DegradeAction]bool { return DegradeMap(f) }

// AllowedAction 镜像实现：委托包级 AllowedAction。
func (MirrorDegrade) AllowedAction(f FailureType, a DegradeAction) bool {
	return AllowedAction(f, a)
}

// satisfies 编译断言：镜像必须完整实现行为契约（接口与镜像签名漂移在编译期暴露）。
var (
	_ RuntimeModel  = MirrorRuntime{}
	_ IdentityModel = MirrorIdentity{}
	_ BudgetModel   = MirrorBudget{}
	_ DegradeModel  = MirrorDegrade{}
)

// ---- 测试驱动注入点（默认镜像；-tags real 时由 real_runtime_test.go 覆盖为真身） ----

var (
	testRuntime  RuntimeModel  = MirrorRuntime{}
	testIdentity IdentityModel = MirrorIdentity{}
	testBudget   BudgetModel   = MirrorBudget{}
	testDegrade  DegradeModel  = MirrorDegrade{}
)
