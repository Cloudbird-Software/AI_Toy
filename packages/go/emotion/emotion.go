// Package emotion —— T7 情绪引擎（M2，IR #92 / m2-spec §4 包契约 C，路径 A）。
//
// OCC 显式规则 + 低维动力学：情绪事件进（Event），三维情绪状态出（State，
// 愉悦度×唤醒度+亲密度，全域 [0,1]）。纯 Go、无 IO、无随机、无墙钟——
// 全符号可断言：同事件序列+同初始态 → 同终态（随机只许在表达层=动作带权
// 采样，归 motion-map）。依赖纪律：import 白名单=标准库。
//
// 动力学（离散时间步进，快慢双半衰期指数衰减）：
//
//	OnEvent：Δ三维 = Rule 增量 × Intensity，夹紧 [0,1]（单维步长 |Δ|≤MaxStep
//	  ——T7-G1-02 单轮跳变 ≤0.3 由构造保证）
//	DecayTo(atMs)：各维向基线 0.5 指数回归——唤醒=快半衰期、亲密=慢半衰期、
//	  愉悦=两者几何平均；除基线无不动点（无吸收态，T7-G1-03 可恢复性）
//
// 错误语义：仅 NewEngine 校验返回 error；OnEvent/DecayTo 任意调用序不
// panic、状态永不 NaN（Intensity 越界/NaN 截断、未知 Kind 零增量）。
package emotion

import (
	"errors"
	"fmt"
	"math"
)

// Kind OCC 规则事件枚举（≥12 种）。零值 Ignore=无关事件（心跳/日志/噪声，
// 零增量不改状态——spec §4 属性）。
type Kind int8

const (
	Ignore      Kind = iota // 无关事件（心跳/日志）
	Praise                  // 被表扬
	Criticize               // 被批评
	Hug                     // 被拥抱
	ToySnatched             // 玩具被抢
	Alone                   // 独处
	Greet                   // 被问候
	Play                    // 一起玩
	Scared                  // 受惊
	Soothed                 // 被安抚
	Story                   // 听故事
	Bedtime                 // 睡前仪式
	Interrupted             // 说话被孩子打断
	MissParent              // 孩子想念爸妈
	Tickles                 // 挠痒痒
	Bored                   // 无聊
)

// KindCount 枚举总数（NewEngine 规则覆盖校验上界）。
const KindCount = 16

// MaxStep 单事件单维增量绝对值上限（T7-G1-02 单轮跳变 ≤0.3 的构造保证）。
const MaxStep = 0.3

// baseline 三维中性基线（衰减不动点、静置回归目标）。
var baseline = [3]float64{0.5, 0.5, 0.5}

// Event 情绪事件。Intensity∈[0,1]（越界截断、NaN→0）。
type Event struct {
	K         Kind
	Intensity float64
}

// State 情绪状态：三维全域 [0,1]（0.5=中性）；Label=儿童 9 类（8–10 类带内，
// spec §4）。只读快照——调用方不得修改后回灌（无 setter）。
type State struct {
	Valence   float64 // 愉悦度（1=愉悦 0=不悦）
	Arousal   float64 // 唤醒度（快变量；1=激动 0=平静）
	Closeness float64 // 亲密度（慢变量；1=亲近 0=疏远）
	Label     string  // 儿童 9 类标签（V×A 平面标签带）
}

// Rule OCC 规则：事件 → 单位强度增量（Δ愉悦,Δ唤醒,Δ亲密）。每维 |Δ| ≤
// MaxStep（NewEngine 校验）；正性事件对应维 Δ≥0、负性对称（T7-G1-01 方向面）。
type Rule struct {
	K          Kind
	DV, DA, DC float64
}

// LabelBand 儿童情绪标签带：(Valence×Arousal) 平面上的半开矩形 [Min,Max)
// （Max=1 端闭）；带间不重叠、合并覆盖全平面（NewEngine 网格探针校验）。
type LabelBand struct {
	Label      string
	MinV, MaxV float64
	MinA, MaxA float64
}

// Config 引擎配置（零值不可用——规则/半衰期/标签带须显式给全，
// DefaultConfig 提供对齐 spec 的缺省）。
type Config struct {
	Rules          []Rule      // 须覆盖全部 Kind（每 Kind 恰一条）
	HalfLifeFastMs int64       // 唤醒（快变量）半衰期 ms，>0
	HalfLifeSlowMs int64       // 亲密（慢变量）半衰期 ms，>0
	LabelTable     []LabelBand // 儿童 8–10 类标签带
}

// DefaultConfig 缺省配置：16 规则（方向对齐儿童情绪语义——表扬↑愉悦↑亲密、
// 玩具被抢↓愉悦↑唤醒等）、快半衰期 90s（唤醒）/慢半衰期 8min（亲密）、
// 愉悦半衰期=几何平均 ≈3.5min、9 类标签（V×A 平面 3×3 半开带）。慢半衰期
// 保证 ≤30min 静置回基线 ±0.1（最慢维亲密：0.5×2^(−30/8)≈0.037）。
func DefaultConfig() Config {
	rules := []Rule{
		{Ignore, 0, 0, 0},
		{Praise, +0.25, +0.15, +0.20},
		{Criticize, -0.25, +0.15, -0.15},
		{Hug, +0.20, -0.20, +0.25},
		{ToySnatched, -0.30, +0.25, -0.20},
		{Alone, -0.15, -0.20, -0.10},
		{Greet, +0.15, +0.20, +0.10},
		{Play, +0.25, +0.30, +0.20},
		{Scared, -0.25, +0.30, 0},
		{Soothed, +0.20, -0.30, +0.20},
		{Story, +0.20, -0.15, 0},
		{Bedtime, +0.15, -0.30, 0},
		{Interrupted, -0.20, +0.15, -0.05},
		{MissParent, -0.20, -0.10, +0.05},
		{Tickles, +0.20, +0.30, +0.15},
		{Bored, -0.10, -0.15, -0.05},
	}
	bands := []LabelBand{ // V×A 平面 3×3：V∈[0,0.4)/[0.4,0.6)/[0.6,1]，A 同构
		{Label: "sad", MinV: 0, MaxV: 0.4, MinA: 0, MaxA: 0.35},
		{Label: "scared", MinV: 0, MaxV: 0.4, MinA: 0.35, MaxA: 0.65},
		{Label: "angry", MinV: 0, MaxV: 0.4, MinA: 0.65, MaxA: 1},
		{Label: "sleepy", MinV: 0.4, MaxV: 0.6, MinA: 0, MaxA: 0.35},
		{Label: "calm", MinV: 0.4, MaxV: 0.6, MinA: 0.35, MaxA: 0.65},
		{Label: "surprised", MinV: 0.4, MaxV: 0.6, MinA: 0.65, MaxA: 1},
		{Label: "content", MinV: 0.6, MaxV: 1, MinA: 0, MaxA: 0.35},
		{Label: "happy", MinV: 0.6, MaxV: 1, MinA: 0.35, MaxA: 0.65},
		{Label: "excited", MinV: 0.6, MaxV: 1, MinA: 0.65, MaxA: 1},
	}
	return Config{Rules: rules, HalfLifeFastMs: 90_000, HalfLifeSlowMs: 480_000, LabelTable: bands}
}

// Engine OCC 情绪引擎。单流串行使用（无并发共享——与 kws/turntaking 同
// 资产定性）；OnEvent/DecayTo 同步推进并返回当前状态快照。
type Engine struct {
	rules  [KindCount]Rule
	hlFast float64 // 唤醒半衰期 ms
	hlSlow float64 // 亲密半衰期 ms
	hlMid  float64 // 愉悦半衰期 ms（快慢几何平均）
	bands  []LabelBand

	v, a, c float64 // 三维状态（恒 ∈[0,1]）
	lastMs  int64   // 已推进到的时刻（DecayTo 单调；早于它的调用为 no-op）
}

// NewEngine 构造引擎：仅此处校验配置——半衰期>0、规则恰覆盖全 Kind、每维
// 步长 |Δ|≤MaxStep、标签带合法且不重叠+全覆盖（21×21 网格探针每点恰命中
// 一带）。任一违反返回 error。
func NewEngine(cfg Config) (*Engine, error) {
	if cfg.HalfLifeFastMs <= 0 {
		return nil, errors.New("emotion: HalfLifeFastMs 须 > 0（唤醒快变量半衰期）")
	}
	if cfg.HalfLifeSlowMs <= 0 {
		return nil, errors.New("emotion: HalfLifeSlowMs 须 > 0（亲密慢变量半衰期）")
	}
	var rules [KindCount]Rule
	seen := map[Kind]bool{}
	for _, r := range cfg.Rules {
		if r.K < 0 || int(r.K) >= KindCount {
			return nil, fmt.Errorf("emotion: 规则 Kind %d 越界 [0,%d)", r.K, KindCount)
		}
		if seen[r.K] {
			return nil, fmt.Errorf("emotion: 规则重复覆盖 Kind %d（须每 Kind 恰一条）", r.K)
		}
		if math.Abs(r.DV) > MaxStep || math.Abs(r.DA) > MaxStep || math.Abs(r.DC) > MaxStep {
			return nil, fmt.Errorf("emotion: Kind %d 步长超上限（|Δ| 须 ≤%.1f，got ΔV=%.3f ΔA=%.3f ΔC=%.3f）",
				r.K, MaxStep, r.DV, r.DA, r.DC)
		}
		if math.IsNaN(r.DV) || math.IsNaN(r.DA) || math.IsNaN(r.DC) {
			return nil, fmt.Errorf("emotion: Kind %d 增量含 NaN", r.K)
		}
		seen[r.K] = true
		rules[r.K] = r
	}
	if len(seen) != KindCount {
		return nil, fmt.Errorf("emotion: 规则未覆盖全部 Kind（须 %d 条覆盖 0..%d，got %d 条）",
			KindCount, KindCount-1, len(seen))
	}
	if err := validateBands(cfg.LabelTable); err != nil {
		return nil, err
	}
	return &Engine{
		rules:  rules,
		hlFast: float64(cfg.HalfLifeFastMs),
		hlSlow: float64(cfg.HalfLifeSlowMs),
		hlMid:  math.Sqrt(float64(cfg.HalfLifeFastMs) * float64(cfg.HalfLifeSlowMs)),
		bands:  cfg.LabelTable,
		v:      baseline[0], a: baseline[1], c: baseline[2],
	}, nil
}

// OnEvent 施加一个事件：Δ三维 = 规则增量×Intensity 后夹紧 [0,1]，返回推进
// 后状态。任意 Intensity/Kind（含越界/NaN/未知枚举）不 panic、永不 NaN；
// Ignore 与未知 Kind 零增量。
func (e *Engine) OnEvent(ev Event) State {
	i := clamp01(ev.Intensity)
	var r Rule
	if ev.K >= 0 && int(ev.K) < KindCount {
		r = e.rules[ev.K]
	}
	e.v = clamp01(e.v + r.DV*i)
	e.a = clamp01(e.a + r.DA*i)
	e.c = clamp01(e.c + r.DC*i)
	return e.state()
}

// DecayTo 衰减推进到时刻 atMs（离散时间步进）：各维向基线指数回归（唤醒=
// 快半衰期、亲密=慢半衰期、愉悦=几何平均）。atMs 不早于已推进时刻才生效
// （单调时钟——迟到调用整体 no-op，状态不变）；返回推进后状态。静置时到
// 基线距离单调不减回（李雅普诺夫式），除基线无不动点。
func (e *Engine) DecayTo(atMs int64) State {
	if atMs > e.lastMs {
		dt := float64(atMs - e.lastMs)
		e.v = decayDim(e.v, e.hlMid, dt)
		e.a = decayDim(e.a, e.hlFast, dt)
		e.c = decayDim(e.c, e.hlSlow, dt)
		e.lastMs = atMs
	}
	return e.state()
}

// State 返回当前状态快照（只读语义）。
func (e *Engine) State() State { return e.state() }

func (e *Engine) state() State {
	return State{Valence: e.v, Arousal: e.a, Closeness: e.c, Label: e.labelFor(e.v, e.a)}
}

// decayDim 单维指数回归基线：x → 0.5 + (x−0.5)·exp(−ln2·dt/halfLife)。
// x∈[0,1] 时凸组合仍在 [0,1]；dt>0 且 x≠0.5 时严格向基线移动（无吸收态）。
func decayDim(x, halfLifeMs, dtMs float64) float64 {
	return baseline[0] + (x-baseline[0])*math.Exp(-math.Ln2*dtMs/halfLifeMs)
}

// labelFor 从 (V,A) 查标签带。NewEngine 已校验全覆盖不重叠——循环必命中；
// 兜底返回首带（不 panic）。
func (e *Engine) labelFor(v, a float64) string {
	for _, b := range e.bands {
		if inBand(b, v, a) {
			return b.Label
		}
	}
	return e.bands[0].Label
}

// inBand 半开矩形判定（[Min,Max)，Max=1 端闭——V/A∈[0,1] 端点唯一归属）。
func inBand(b LabelBand, v, a float64) bool {
	return inHalfOpen(v, b.MinV, b.MaxV) && inHalfOpen(a, b.MinA, b.MaxA)
}

func inHalfOpen(x, lo, hi float64) bool {
	return x >= lo && (x < hi || (x == 1 && hi == 1))
}

// validateBands 标签带校验：非空/标签唯一/矩形合法（含 NaN 拒绝——NaN 比较
// 恒 false 自然落入此分支）；21×21 网格探针每点恰命中一带（不重叠+全覆盖，
// spec「标签带不重叠校验」的完备口径）。
func validateBands(bands []LabelBand) error {
	if len(bands) == 0 {
		return errors.New("emotion: LabelTable 不得为空（儿童 8–10 类）")
	}
	seen := map[string]bool{}
	for _, b := range bands {
		if b.Label == "" {
			return errors.New("emotion: 标签带 Label 不得为空")
		}
		if seen[b.Label] {
			return fmt.Errorf("emotion: 标签带 %s 重复", b.Label)
		}
		seen[b.Label] = true
		ok := b.MinV >= 0 && b.MaxV <= 1 && b.MinV < b.MaxV &&
			b.MinA >= 0 && b.MaxA <= 1 && b.MinA < b.MaxA
		if !ok { // NaN 端点使比较失败——同支拒绝
			return fmt.Errorf("emotion: 标签带 %s 矩形非法 [%.3f,%.3f)×[%.3f,%.3f)（须 ⊆[0,1] 且 Min<Max）",
				b.Label, b.MinV, b.MaxV, b.MinA, b.MaxA)
		}
	}
	for i := 0; i <= 20; i++ {
		for j := 0; j <= 20; j++ {
			v, a := float64(i)/20, float64(j)/20
			hits := 0
			for _, b := range bands {
				if inBand(b, v, a) {
					hits++
				}
			}
			if hits != 1 {
				return fmt.Errorf("emotion: 标签带在 (V=%.2f,A=%.2f) 命中 %d 带（须恰 1——不重叠且全覆盖）", v, a, hits)
			}
		}
	}
	return nil
}

// clamp01 夹紧 [0,1]（NaN→0、+Inf→1、−Inf→0）——状态永不 NaN 的统一入口
// （事件强度越界截断同用）。
func clamp01(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
