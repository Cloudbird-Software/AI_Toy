// guard —— T6-G0-03 软件双保险输出指令边界盒（m3-spec §6「硬件熔断双保险」
// 的软件面）。冲击等级（ImpactLevel，Detector 观测面产出）×输出动作（电机
// 占空比/摆臂角度/声音三列）矩阵表驱动：Guard.Clamp 把任意指令钳入该冲击
// 等级边界行——任何软件 bug 产出的指令（越界/负值/NaN/±Inf）经 Clamp 后恒
// 在盒内（Violation==false），severe 行恒停马达静音。固件层（packages/
// native/firmware-imu）=独立保险（数据表安全值，真机 L5）：软件 bug 不可
// 越过固件，本层非唯一保险（包 AGENTS.md 禁令）。
//
// loop 组装面（m3-spec §1）：每帧读 Detector.ImpactLevel() → Guard.Clamp
// （desired→下发）；EvFall 触发的 ImpactSevere 保持 fallHoldMs=2s（摔落
// 保护窗）即「≤2s 停马达静音」的实现面。
package imu

import "math"

// ImpactLevel 冲击等级（Guard 矩阵行键；输入侧重力偏离观测→输出侧边界行）。
type ImpactLevel int8

const (
	ImpactNone   ImpactLevel = 0 // 平静（常态：满动作预算）
	ImpactLight  ImpactLevel = 1 // 轻微冲击（微动作降载）
	ImpactMedium ImpactLevel = 2 // 中等冲击（动作受限）
	ImpactSevere ImpactLevel = 3 // 剧烈/摔落（停马达静音——severe 行）
)

// ImpactLevel 数量（矩阵行数；LevelOf/越界下标的地板）。
const nImpactLevels = 4

// 分带常量（重力偏离 g 口径；Detector 观测面 Activity/Violence/冲击分级带
// 与 Guard 行键同源）。真机标定属 L5（固件/台架面，不在此暴露）。
const (
	lightDevG  = 0.6 // 轻微冲击带下沿（重力偏离 ≥此值=ImpactLight）
	mediumDevG = 1.0 // 中等冲击带下沿
	severeDevG = 1.5 // 剧烈冲击带下沿（Violence 满格口径）
)

// LevelOf 重力偏离分带（矩阵行键取值面）。NaN/负值/低于 lightDevG →
// ImpactNone（非法观测按最低带——fail-safe 不放大）。
func LevelOf(dev float64) ImpactLevel {
	if !(dev >= lightDevG) { // NaN 亦落此支
		return ImpactNone
	}
	switch {
	case dev >= severeDevG:
		return ImpactSevere
	case dev >= mediumDevG:
		return ImpactMedium
	default:
		return ImpactLight
	}
}

// Cmd 输出指令（马达+声音面；loop 从 T12 动作面组装 desired，过 Guard.Clamp
// 后下发）。占空比/角度为非负物理量（0=停转/回中）；非法值（NaN/±Inf/负）
// 由 Clamp 归零（畸变指令按最保守输出处理，永不 panic）。
type Cmd struct {
	Duty     float64 // 电机占空比 [0, dutyMax]（0=停转）
	AngleDeg float64 // 摆臂目标角度绝对值 deg（0=回中）
	Sound    bool    // 声音输出允许位（severe 行强制静音）
}

// 数据表安全值（M3 规则面占位口径：占空比 ≤1.0、摆臂角度 ≤120°、severe 行
// 停马达静音；真机数据表安全值=固件层独立持有 L5——本层为镜像上限，非唯一
// 保险）。矩阵任何行的上限不得超出此组（表驱动穷举断言面，T6-G0-03）。
const (
	datasheetDutyMax  = 1.0
	datasheetAngleMax = 120.0
)

// guardRow 边界行：该冲击等级下的输出动作上限（矩阵行=冲击等级，列=占空比/
// 角度/声音三动作面，表驱动）。
type guardRow struct {
	dutyMax  float64
	angleMax float64
	sound    bool
}

// Guard 软件层输出指令边界盒（冲击等级×输出动作矩阵）。构造后只读（零值
// 可用=默认矩阵；NewGuard 显式构造）。单流串行使用（不加锁——对齐 kws
// 资产卡定性）。
type Guard struct {
	rows [nImpactLevels]guardRow
}

// NewGuard 默认边界盒（表驱动矩阵本体；穷举面即此表——T6-G0-03 断言按行
// 全枚举）。行语义：平静=满预算；轻微/中等=逐级降载；剧烈（摔落保护窗）
// =停马达静音（duty 0/角度 0/无声）。
func NewGuard() *Guard {
	return &Guard{rows: [nImpactLevels]guardRow{
		ImpactNone:   {dutyMax: 1.0, angleMax: 120, sound: true},
		ImpactLight:  {dutyMax: 0.8, angleMax: 90, sound: true},
		ImpactMedium: {dutyMax: 0.5, angleMax: 60, sound: true},
		ImpactSevere: {dutyMax: 0, angleMax: 0, sound: false},
	}}
}

// Row 该冲击等级的边界行（矩阵观测面：穷举/审计/报告用；越界下标→severe
// 行——未知等级按最严边界，fail-safe）。
func (g *Guard) Row(lv ImpactLevel) Cmd {
	r := g.row(lv)
	return Cmd{Duty: r.dutyMax, AngleDeg: r.angleMax, Sound: r.sound}
}

// Clamp 把任意指令钳入该冲击等级边界行（纯函数：同 (lv,cmd) 同输出——确定
// 性属性的实现面）。占空比/角度：非法（NaN/±Inf/负）→0、越界→上限；声音：
// 行允许且指令请求才放行。severe 行恒停马达静音——任何软件 bug 的指令值
// 经本面后均无法越界（T6-G0-03 motor_bound_violation_count==0 的实现面）。
func (g *Guard) Clamp(lv ImpactLevel, c Cmd) Cmd {
	r := g.row(lv)
	return Cmd{
		Duty:     clampBound(c.Duty, r.dutyMax),
		AngleDeg: clampBound(c.AngleDeg, r.angleMax),
		Sound:    c.Sound && r.sound,
	}
}

// Violation 指令是否越出该冲击等级边界行（Clamp 后恒 false；非法值/越界值
// /severe 行任何马达输出均=越界——门禁断言的判定面，勿在实现内调用自证）。
func (g *Guard) Violation(lv ImpactLevel, c Cmd) bool {
	r := g.row(lv)
	if !validBound(c.Duty, r.dutyMax) || !validBound(c.AngleDeg, r.angleMax) {
		return true
	}
	return c.Sound && !r.sound
}

// row 行取值（越界下标→severe 行，fail-safe）。
func (g *Guard) row(lv ImpactLevel) guardRow {
	if lv < ImpactNone || lv > ImpactSevere {
		return g.rows[ImpactSevere]
	}
	return g.rows[lv]
}

// clampBound 物理量钳入 [0,max]：非法（NaN/±Inf）与负值→0，越界→max。
func clampBound(v, max float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	if v > max {
		return max
	}
	return v
}

// validBound 物理量合法域 [0,max]（NaN/±Inf 不合法）。
func validBound(v, max float64) bool {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return false
	}
	return v >= 0 && v <= max
}
