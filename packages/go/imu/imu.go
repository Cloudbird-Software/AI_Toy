// Package imu —— T6 IMU 感知规则面（M3，IR #106 / m3-spec §6，ADR-0006）。
// 法典卡面包=packages/go/imu+packages/native/firmware-imu（固件熔断 M3 不动）；
// 本包为软件层：三态事件检测（拿起/静置/风暴滤除）+ 摔落剖面检出 + 输出指令
// 边界盒（Guard，软件双保险——T6-G0-03；固件层=独立保险，本层非唯一保险）。
//
// 纯 Go 流式检测：加速度样本进（Sample，50Hz 名义、合成曲线回放=台架代理），
// 事件出（Event）。同步、无 IO、无墙钟——全部判定为样本序列的确定性函数
// （同曲线同事件，P5；时间基状态机=重采样/时移不变性的实现面）。
// 依赖纪律：import 白名单=标准库；不 import loop/Arbiter（事件由 loop 搬运：
// EvPickup→emotion.Greet、EvIdle→静默抑制、EvFall→停马达（Guard severe 行）、
// EvStorm→Arbiter.OnFault（CH-06 无动作风暴），m3-spec §1）。
//
// 三态语义（m3-spec §6）：
//
//	拿起=滑窗幅值 ≥PickupThreshG（重力偏离）+去抖（DebounceMs 内持续复确认）
//	  →EvPickup；活动期一次（重臂=静默 DebounceMs 后再拿起才再发）
//	静置=QuietMs 无运动→EvIdle（深度安静态：静默抑制信号——loop 停自发输出/
//	  计划任务停发；持续静置每 IdleMs 心跳重发维持抑制；任意运动即解除）
//	风暴=阈值上穿速率 ≥StormPerSec→EvStorm 限流聚合（窗口内至多 1/s；风暴
//	  期 EvPickup/EvFall 滤除——高频冲击序列不误触发，输入带内平静自动恢复）
//	摔落=自由落体剖面（幅值骤降 <freeFallG →冲击尖峰 ≥FallThreshG）→EvFall
//	  （≤2s 停马达静音：事件即发于冲击样本+severe 保持 fallHoldMs=2s 保护窗）
//
// 错误语义：仅 NewDetector 校验配置返回 error；Push 对畸变输入（NaN/±Inf）
// 按中性重力样本处理（EvNone），永不 error/panic（对齐 kws 纪律）。
package imu

import (
	"errors"
	"math"
)

// Sample 一帧加速度样本（单位 g）。AtMs 单调不减由调用方保证；方向任意
// （检测只依赖总幅值 ‖a‖——玩具姿态任意；重力偏离=|‖a‖−1|）。
type Sample struct {
	AtMs       int64
	Ax, Ay, Az float64
}

// EventKind 事件类型（零值=EvNone——驱动层可零值初始化事件缓冲）。
type EventKind int8

const (
	EvNone   EventKind = 0
	EvPickup EventKind = 1 // 拿起（loop→emotion.Greet 活物感）
	EvIdle   EventKind = 2 // 静置超时（深度安静态：静默抑制信号）
	EvFall   EventKind = 3 // 摔落（≤2s 停马达静音——Guard severe 行）
	EvStorm  EventKind = 4 // 风暴限流聚合（CH-06，loop→Arbiter.OnFault）
)

// Event 检测结果。Conf∈[0,1]（事件语义置信度：拿起=活动强度、摔落=冲击
// 烈度、风暴=上穿速率、静置=满窗 1.0）；EvNone 时=当前活动得分（连续观测
// 面——调用方拿活动流做阈值调谐/噪声带标定，对齐 kws 置信度口径）。
type Event struct {
	Kind EventKind
	AtMs int64
	Conf float64
}

// Config 检测器配置。零值可用性：全部数值字段零值取默认（PickupThreshG
// 0.30/FallThreshG 2.0/DebounceMs 250/QuietMs 2000/IdleMs 60000/StormPerSec
// 10）。去抖为时间基（DebounceMs；50Hz 名义下 ≈N 帧——重采样不变性前提）。
type Config struct {
	PickupThreshG float64 // 拿起判定阈值（重力偏离 g，>0；幅值偏离 ≥此值=活动）
	FallThreshG   float64 // 摔落冲击尖峰阈值（总幅值 g，>1；自由落体后的冲击 ≥此值=摔落）
	DebounceMs    int     // 拿去去抖窗 ms（≥1）：活动持续此时长才发 EvPickup
	IdleMs        int     // 静置心跳周期 ms（≥QuietMs）：深度安静中每此时长重发 EvIdle
	StormPerSec   int     // 风暴上穿速率阈值（次/s，≥2）：1s 窗内上穿 ≥此值=风暴
	QuietMs       int     // 静置超时 ms（≥1）：无运动持续此时长→EvIdle（静默抑制）
}

// 内部冻结常量（规则面口径；真机标定属 L5——固件/台架面，不在 Config 暴露）。
const (
	freeFallG     = 0.35 // 自由落体判定：总幅值 <此值（真自由落体 ≈0g）
	minFreeFallMs = 100  // 自由落体最短持续（区分瞬时凹陷噪声；≥5cm 落体）
	maxFreeFallMs = 2000 // 自由落体最长持续（超时复位——长挂零=传感器故障面）
	fallHoldMs    = 2000 // 摔落保护窗：severe 冲击保持（停马达静音 ≤2s 口径）
	stormWinMs    = 1000 // 风暴速率滑窗
	activityWinMs = 500  // 活动/剧烈置信度观测窗

	winCap      = 64   // 观测窗容量（超率时窗缩短——统计仍有界且单调）
	crossingCap = 1024 // 上穿时刻环容量（风暴计数饱和仍 ≥阈值——判定不受影响）
)

// 默认配置（产品面工作点）。
const (
	defaultPickupThreshG = 0.30
	defaultFallThreshG   = 2.00
	defaultDebounceMs    = 250
	defaultQuietMs       = 2000
	defaultIdleMs        = 60000
	defaultStormPerSec   = 10
)

// Detector 三态流式检测器。单流串行使用（不加锁——对齐 kws 资产卡定性），
// Push 同步返回当帧事件；全部内部状态有界（观测窗 ≤winCap、上穿环
// ≤crossingCap——常驻内存不随流长增长）。
type Detector struct {
	cfg Config

	lastAt   int64 // 最近样本时刻（ImpactLevel 观测面）
	wasAbove bool  // 上一样本是否超拿起阈值（上穿沿检测）

	// 拿起面
	activeStart  int64 // 活动持续 streak 起点（-1=无；≥DebounceMs 达去抖）
	pickupDone   bool  // 本活动期已发 EvPickup（重臂前不重发）
	quietRearmAt int64 // 重臂静默计时起点（-1=无；静默 ≥DebounceMs→重臂）

	// 静置面
	quietStart int64 // 无运动 streak 起点（-1=无）
	idleFired  bool  // 本静置期已发 EvIdle（深度安静态）
	lastIdleAt int64 // 上次 EvIdle 时刻（心跳周期基准）

	// 摔落面
	ffStart       int64 // 自由落体 streak 起点（-1=无）
	ffLast        int64 // 自由落体 streak 末帧时刻（-1=无；streak 跨度=ffLast-ffStart）
	postFallUntil int64 // 摔落保护窗到期时刻（math.MinInt64=无；内=severe）

	// 风暴面
	stormOn     bool
	lastStormAt int64
	crossings   []int64 // 阈值上穿时刻（时间升序；≤crossingCap 有界）

	// 观测面（活动得分/剧烈置信度/冲击等级带）
	win []winPt
}

// winPt 观测窗样本点。
type winPt struct {
	at  int64
	dev float64
}

// NewDetector 构造检测器：仅此处校验配置（零值取默认；PickupThreshG∈(0,5]/
// FallThreshG∈(1,16]/DebounceMs≥1/QuietMs≥1/IdleMs≥QuietMs/StormPerSec≥2）。
func NewDetector(cfg Config) (*Detector, error) {
	if cfg.PickupThreshG == 0 {
		cfg.PickupThreshG = defaultPickupThreshG
	}
	if !(cfg.PickupThreshG > 0 && cfg.PickupThreshG <= 5) {
		return nil, errors.New("imu: PickupThreshG 须 ∈ (0, 5]（重力偏离 g）")
	}
	if cfg.FallThreshG == 0 {
		cfg.FallThreshG = defaultFallThreshG
	}
	if !(cfg.FallThreshG > 1 && cfg.FallThreshG <= 16) {
		return nil, errors.New("imu: FallThreshG 须 ∈ (1, 16]（冲击尖峰总幅值 g）")
	}
	if cfg.DebounceMs == 0 {
		cfg.DebounceMs = defaultDebounceMs
	}
	if cfg.DebounceMs < 1 {
		return nil, errors.New("imu: DebounceMs 须 ≥ 1")
	}
	if cfg.QuietMs == 0 {
		cfg.QuietMs = defaultQuietMs
	}
	if cfg.QuietMs < 1 {
		return nil, errors.New("imu: QuietMs 须 ≥ 1")
	}
	if cfg.IdleMs == 0 {
		cfg.IdleMs = defaultIdleMs
	}
	if cfg.IdleMs < cfg.QuietMs {
		return nil, errors.New("imu: IdleMs 须 ≥ QuietMs（心跳不早于静置超时）")
	}
	if cfg.StormPerSec == 0 {
		cfg.StormPerSec = defaultStormPerSec
	}
	if cfg.StormPerSec < 2 {
		return nil, errors.New("imu: StormPerSec 须 ≥ 2（低于常态活动率即误报风暴）")
	}
	return &Detector{cfg: cfg, activeStart: -1, quietRearmAt: -1, quietStart: -1,
		lastIdleAt: -1, ffStart: -1, ffLast: -1, postFallUntil: math.MinInt64, lastStormAt: -1}, nil
}

// Push 推入一帧样本，返回当帧事件（至多一个；优先级：风暴>摔落>拿起>静置
// ——风暴期滤除拿起/摔落，m3-spec §6「限流聚合」）。畸变输入（NaN/±Inf 任
// 分量）按中性重力样本处理，永不 error/panic。
func (d *Detector) Push(s Sample) Event {
	ax, ay, az := s.Ax, s.Ay, s.Az
	if !finite(ax) || !finite(ay) || !finite(az) {
		ax, ay, az = 0, 0, 1 // 中性重力（畸变输入按静置处理）
	}
	mag := math.Sqrt(ax*ax + ay*ay + az*az)
	dev := math.Abs(mag - 1)
	d.lastAt = s.AtMs

	// 观测窗更新（活动得分/剧烈置信度/冲击等级带的数据面）
	d.pushWindow(s.AtMs, dev)

	above := dev >= d.cfg.PickupThreshG
	if above && !d.wasAbove {
		d.crossings = append(d.crossings, s.AtMs)
	}
	d.wasAbove = above

	inFreeFall := mag < freeFallG
	switch {
	case inFreeFall:
		d.onFreeFall(s)
	case above:
		d.onActive(s, mag)
	default:
		d.onQuiet(s)
	}

	// 事件裁决（优先级：风暴>摔落>拿起>静置）
	if ev, ok := d.stormEvent(s); ok {
		return ev
	}
	if ev, ok := d.fallEvent(s, mag); ok {
		return ev
	}
	if ev, ok := d.pickupEvent(s, dev); ok {
		return ev
	}
	if ev, ok := d.idleEvent(s); ok {
		return ev
	}
	return Event{Kind: EvNone, AtMs: s.AtMs, Conf: d.Activity()}
}

// onActive 活动样本状态面（拿起 streak 累积；静置/自由落体/重臂计时复位）。
// ffStart 仅在非冲击活动帧上作废：冲击候选帧（mag ≥FallThreshG）保留给
// fallEvent 消费（自由落体剖面以冲击尖峰收尾——状态更新先于事件裁决，此处
// 清除将使摔落永不触发）；非冲击活动=剖面被运动打断，作废。
func (d *Detector) onActive(s Sample, mag float64) {
	d.quietStart = -1 // 无运动断
	d.idleFired = false
	d.lastIdleAt = -1
	d.quietRearmAt = -1
	if mag < d.cfg.FallThreshG {
		d.ffStart = -1 // 常规活动帧：落体剖面作废（冲击帧由 fallEvent 收尾复位）
	}
	if d.activeStart < 0 {
		d.activeStart = s.AtMs
	}
}

// onQuiet 静置样本状态面（无运动 streak 累积；拿起 streak 断、重臂计时累积）。
func (d *Detector) onQuiet(s Sample) {
	d.activeStart = -1
	d.ffStart = -1
	if d.quietStart < 0 {
		d.quietStart = s.AtMs
	}
	if d.quietRearmAt < 0 {
		d.quietRearmAt = s.AtMs
	}
	if s.AtMs-d.quietRearmAt >= int64(d.cfg.DebounceMs) {
		d.pickupDone = false // 重臂：静默满去抖窗→下次拿起可再发 EvPickup
	}
}

// onFreeFall 自由落体样本状态面（落体 streak 累积；拿起/静置复位）。
func (d *Detector) onFreeFall(s Sample) {
	d.quietStart = -1
	d.idleFired = false
	d.lastIdleAt = -1
	d.activeStart = -1 // 落体非拿起
	d.quietRearmAt = -1
	if d.ffStart < 0 || s.AtMs-d.ffStart > maxFreeFallMs {
		d.ffStart = s.AtMs // 起点或超时复位（长挂零=传感器故障面，不持剖面）
	}
	d.ffLast = s.AtMs
}

// fallEvent 摔落裁决：自由落体 streak 跨度（ffLast−ffStart——真实落体时长，
// 不含冲击帧）≥minFreeFallMs 且冲击尖峰 ≥FallThreshG →EvFall（即发于冲击
// 样本——检出延迟=0；保护窗 fallHoldMs 内 severe）。
func (d *Detector) fallEvent(s Sample, mag float64) (Event, bool) {
	if d.stormOn {
		return Event{}, false // 风暴期滤除（高频冲击序列不误触发）
	}
	if mag < d.cfg.FallThreshG || d.ffStart < 0 || d.ffLast-d.ffStart < minFreeFallMs {
		return Event{}, false
	}
	ev := Event{Kind: EvFall, AtMs: s.AtMs, Conf: clip01((mag - 1) / severeDevG)}
	d.ffStart = -1
	d.postFallUntil = s.AtMs + fallHoldMs
	d.activeStart = -1 // 冲击后残余振动不接拿起 streak（保护窗内不发 EvPickup）
	d.pickupDone = false
	return ev, true
}

// pickupEvent 拿起裁决：活动持续 ≥DebounceMs 且本活动期未发且非风暴/非
// 摔落保护窗→EvPickup（拿起瞬间打招呼，每活动期恰一次）。
func (d *Detector) pickupEvent(s Sample, dev float64) (Event, bool) {
	if d.activeStart < 0 || d.pickupDone || d.stormOn {
		return Event{}, false
	}
	if s.AtMs <= d.postFallUntil {
		return Event{}, false // 摔落保护窗内不问候（弹跳/残余振动面）
	}
	if s.AtMs-d.activeStart < int64(d.cfg.DebounceMs) {
		return Event{}, false
	}
	d.pickupDone = true
	return Event{Kind: EvPickup, AtMs: s.AtMs,
		Conf: clip01(dev / (2 * d.cfg.PickupThreshG))}, true
}

// idleEvent 静置裁决：无运动 ≥QuietMs→EvIdle（静置超时=静默抑制信号）；
// 此后持续静置每 IdleMs 心跳重发（计划任务停发的持续维持信号）。任意
// 运动即由状态面解除（拿起即醒）。
func (d *Detector) idleEvent(s Sample) (Event, bool) {
	if d.quietStart < 0 {
		return Event{}, false
	}
	if !d.idleFired {
		if s.AtMs-d.quietStart < int64(d.cfg.QuietMs) {
			return Event{}, false
		}
		d.idleFired = true
		d.lastIdleAt = s.AtMs
		return Event{Kind: EvIdle, AtMs: s.AtMs, Conf: 1}, true
	}
	if s.AtMs-d.lastIdleAt >= int64(d.cfg.IdleMs) {
		d.lastIdleAt = s.AtMs
		return Event{Kind: EvIdle, AtMs: s.AtMs, Conf: 1}, true
	}
	return Event{}, false
}

// stormEvent 风暴裁决：1s 窗内阈值上穿 ≥StormPerSec→EvStorm（上升沿即发；
// 持续期至多 1/s 限流重发；带内平静自动恢复）。风暴期拿起/摔落滤除（聚合）。
func (d *Detector) stormEvent(s Sample) (Event, bool) {
	d.evictCrossings(s.AtMs)
	n := len(d.crossings)
	if n >= d.cfg.StormPerSec {
		if !d.stormOn {
			d.stormOn = true
			d.lastStormAt = s.AtMs
			return Event{Kind: EvStorm, AtMs: s.AtMs,
				Conf: clip01(float64(n) / (2 * float64(d.cfg.StormPerSec)))}, true
		}
		if s.AtMs-d.lastStormAt >= stormWinMs {
			d.lastStormAt = s.AtMs
			return Event{Kind: EvStorm, AtMs: s.AtMs,
				Conf: clip01(float64(n) / (2 * float64(d.cfg.StormPerSec)))}, true
		}
		return Event{}, false
	}
	d.stormOn = false
	return Event{}, false
}

// pushWindow 观测窗维护（时间过期+容量驱逐——内存有界；统计单调的前提）。
func (d *Detector) pushWindow(at int64, dev float64) {
	d.win = append(d.win, winPt{at: at, dev: dev})
	if len(d.win) > winCap {
		d.win = d.win[1:]
	}
	for len(d.win) > 0 && d.win[0].at < at-activityWinMs {
		d.win = d.win[1:]
	}
}

// evictCrossings 上穿环维护（1s 窗过期+容量驱逐；计数饱和仍 ≥阈值）。
func (d *Detector) evictCrossings(now int64) {
	for len(d.crossings) > 0 && d.crossings[0] < now-stormWinMs {
		d.crossings = d.crossings[1:]
	}
	if len(d.crossings) > crossingCap {
		d.crossings = d.crossings[len(d.crossings)-crossingCap:]
	}
}

// Activity 当前活动得分 ∈[0,1]：观测窗重力偏离均值/lightDevG 线性映射
// （幅值单调增→单调不降的实现面——P1；loop 观测面）。
func (d *Detector) Activity() float64 {
	if len(d.win) == 0 {
		return 0
	}
	sum := 0.0
	for _, p := range d.win {
		sum += p.dev
	}
	return clip01(sum / float64(len(d.win)) / lightDevG)
}

// Violence 当前剧烈置信度 ∈[0,1]：观测窗重力偏离峰值/severeDevG 线性映射
// （幅值单调增→单调不降的实现面——P1；loop 观测面）。
func (d *Detector) Violence() float64 {
	peak := 0.0
	for _, p := range d.win {
		if p.dev > peak {
			peak = p.dev
		}
	}
	return clip01(peak / severeDevG)
}

// ImpactLevel 当前冲击等级（Guard 行键）：摔落保护窗内=Severe（停马达静音）；
// 否则观测窗峰值分带（LevelOf）。loop 组装面：读此值→Guard.Clamp 输出指令。
func (d *Detector) ImpactLevel() ImpactLevel {
	if d.lastAt <= d.postFallUntil {
		return ImpactSevere
	}
	peak := 0.0
	for _, p := range d.win {
		if p.dev > peak {
			peak = p.dev
		}
	}
	return LevelOf(peak)
}

// finite 有限值判定。
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// clip01 夹取 [0,1]（NaN→0——非法观测按最低置信处理）。
func clip01(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
