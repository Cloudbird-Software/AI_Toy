// Package motionmap —— T12 情绪→表情/动作映射（M2，IR #92 / m2-spec §5 包契约 D，路径 A）。
//
// 显式映射表（情绪标签→候选动作）+ 种子驱动带权采样 + 安全盒硬截断：Mood 进
// （镜像 emotion.State 观测面——零 import，由 loop 搬运）、动作集出。纯 Go、
// 无 IO、无墙钟、无 goroutine——Map 同步直返（T12-G1-03「并行旁路不等 TTS」
// 的构造保证）；全部「随机」=fnv-1a 确定性哈希（同 Mood+同 seed → 同动作序列，
// 回放可复现；变 seed 防机械重复）。依赖纪律：import 白名单=标准库。
//
// 安全盒（T12-G0-01，任意输入 0 越界）：
//
//	选一：每组带权选一个候选动作；互斥集内保基幅度最大者（各取一）
//	配额：组内比例缩放至 GroupDuty、全局比例缩放至 GlobalAmpSum
//	终幅：min(round(base×Intensity), floor(配额))——强度↑幅度单调不降且
//	  ΣAmp ≤ GlobalAmpSum、组内 Σ ≤ GroupDuty 恒成立（floor 只降不升）
//
// 静默强制（T12-G0-02）：silent=true 优先级高于一切映射，Map 恒返 nil 并
// 锁存静默态——IdleTick 同口径返 nil（Map(silent=false) 解锁）。错误语义：
// 仅 NewMapper 校验返回 error；Map/IdleTick 任意输入不越界不 panic（未知
// 标签→中性默认行、Intensity 越界/NaN 截断）。
package motionmap

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

// AmpMax 幅度标尺上限（Action.Amp 定义域 0..100）。
const AmpMax uint8 = 100

// DefaultLabel 中性默认行键：未知标签的回落行（表完备校验要求存在）。
const DefaultLabel = "calm"

// 内置 idle 微动作组（NewMapper 上限自洽校验须覆盖——安全盒无缺口）。
const (
	GroupFace = "face" // 眨眼（微表情）
	GroupBody = "body" // 呼吸（躯干微起伏）
)

// idle 微动作参数（仿真时钟确定性调度；T12-G1-01 调度逻辑面）。
const (
	idleBreathPeriodMs int64 = 4000 // 呼吸周期（三角波调制）
	idleBlinkWindowMs  int64 = 4000 // 眨眼判定窗
	idleBreathMinAmp         = 3    // 呼吸幅度下限
	idleBreathMaxAmp         = 8    // 呼吸幅度上限
	idleBlinkAmp             = 10   // 眨眼幅度
	idleBlinkPct             = 70   // 窗内眨眼概率（%）
)

// Mood 情绪观测面（镜像 emotion.State：Label+Intensity 由 loop 搬运，本包
// 零 import emotion）。Intensity∈[0,1]（越界截断、NaN→0）。
type Mood struct {
	Label     string
	Intensity float64
}

// Action 动作指令：Amp 0..100（0=保持位）；Group=互斥组（head/face/body…）。
type Action struct {
	ID    string
	Amp   uint8
	Group string
}

// Limits 安全盒上限：GroupDuty=组内幅度和上限；MutexGroups=互斥组集合（集内
// 至多一个动作，组不得跨集交叠）；GlobalAmpSum=全集幅度和上限。
type Limits struct {
	GroupDuty    map[string]uint8
	MutexGroups  [][]string
	GlobalAmpSum uint8
}

// Table 情绪标签→候选动作行（带权采样防机械重复；可 diff、设计师可编辑）。
type Table map[string][]Action

// Mapper 情绪→动作映射器。单流串行使用（与 emotion.Engine 同资产定性——
// loop 单线程驱动；无锁）。Map 会锁存 silent 态供 IdleTick 同口径消费。
type Mapper struct {
	table  Table
	limits Limits
	silent bool
}

// DefaultTable 缺省动作库（对齐 emotion 儿童标签 9 类；方向类=动作库设计
// 文档：正高/正低/负高/负低，每行动作只取本类——T12-G1-02 方向表同源）。
func DefaultTable() Table {
	return Table{
		"excited":   {{ID: "bounce", Amp: 30, Group: "body"}, {ID: "wiggle", Amp: 20, Group: "body"}, {ID: "smile", Amp: 25, Group: "face"}, {ID: "nod", Amp: 20, Group: "head"}},
		"happy":     {{ID: "smile", Amp: 25, Group: "face"}, {ID: "sway", Amp: 15, Group: "body"}, {ID: "bounce", Amp: 12, Group: "body"}, {ID: "tilt", Amp: 15, Group: "head"}},
		"content":   {{ID: "sway", Amp: 10, Group: "body"}, {ID: "soft", Amp: 12, Group: "face"}, {ID: "tilt", Amp: 8, Group: "head"}, {ID: "nod", Amp: 6, Group: "head"}},
		"calm":      {{ID: "sway", Amp: 8, Group: "body"}, {ID: "settle", Amp: 6, Group: "body"}, {ID: "soft", Amp: 8, Group: "face"}},
		"sleepy":    {{ID: "droop", Amp: 12, Group: "head"}, {ID: "settle", Amp: 8, Group: "body"}, {ID: "lids", Amp: 10, Group: "face"}},
		"sad":       {{ID: "down", Amp: 18, Group: "head"}, {ID: "droop", Amp: 15, Group: "body"}, {ID: "frown", Amp: 10, Group: "face"}},
		"scared":    {{ID: "shrink", Amp: 20, Group: "body"}, {ID: "still", Amp: 10, Group: "body"}, {ID: "low", Amp: 15, Group: "head"}, {ID: "wide", Amp: 12, Group: "face"}},
		"angry":     {{ID: "stomp", Amp: 25, Group: "body"}, {ID: "shake", Amp: 20, Group: "head"}, {ID: "frown", Amp: 20, Group: "face"}, {ID: "glare", Amp: 15, Group: "face"}},
		"surprised": {{ID: "back", Amp: 20, Group: "head"}, {ID: "wide", Amp: 20, Group: "face"}, {ID: "jolt", Amp: 18, Group: "body"}, {ID: "still", Amp: 10, Group: "body"}},
	}
}

// DefaultLimits 缺省安全盒：单动作幅度 ≤100 恒成立，组上限 head/face 40、
// body 50（表内最大基幅度之上留余量）；互斥组=每组至多一动作（伺服组物理
// 约束）；全局 ΣAmp ≤100。
func DefaultLimits() Limits {
	return Limits{
		GroupDuty:    map[string]uint8{"head": 40, "face": 40, "body": 50},
		MutexGroups:  [][]string{{"head"}, {"face"}, {"body"}},
		GlobalAmpSum: 100,
	}
}

// NewMapper 构造映射器：仅此处校验配置——表非空/默认行存在/行与动作字段
// 合法（ID、Group 非空，Amp≤100）/GlobalAmpSum∈[1,100]/GroupDuty 覆盖表内
// 全部组+内置 idle 组（上限自洽：无上限组=安全盒缺口）/互斥集非空、集内无
// 重复、组不跨集交叠且组有上限。任一违反返回 error。
func NewMapper(t Table, l Limits) (*Mapper, error) {
	if len(t) == 0 {
		return nil, errors.New("motionmap: Table 不得为空")
	}
	if _, ok := t[DefaultLabel]; !ok {
		return nil, fmt.Errorf("motionmap: Table 缺中性默认行 %q（未知标签回落）", DefaultLabel)
	}
	for label, row := range t {
		if len(row) == 0 {
			return nil, fmt.Errorf("motionmap: 标签 %q 行为空（须 ≥1 候选动作）", label)
		}
		for _, a := range row {
			if a.ID == "" {
				return nil, fmt.Errorf("motionmap: 标签 %q 动作 ID 为空", label)
			}
			if a.Group == "" {
				return nil, fmt.Errorf("motionmap: 标签 %q 动作 %s 的 Group 为空", label, a.ID)
			}
			if a.Amp > AmpMax {
				return nil, fmt.Errorf("motionmap: 标签 %q 动作 %s 幅度 %d 超上限（须 ≤%d）", label, a.ID, a.Amp, AmpMax)
			}
		}
	}
	if l.GlobalAmpSum < 1 || l.GlobalAmpSum > AmpMax {
		return nil, fmt.Errorf("motionmap: GlobalAmpSum=%d 非法（须 ∈[1,%d]——0=永久禁动请用 silent 态）", l.GlobalAmpSum, AmpMax)
	}
	if l.GroupDuty == nil {
		return nil, errors.New("motionmap: GroupDuty 不得为 nil（安全盒须覆盖全部组）")
	}
	need := map[string]bool{GroupFace: true, GroupBody: true} // 内置 idle 组
	for _, row := range t {
		for _, a := range row {
			need[a.Group] = true
		}
	}
	for g := range need {
		if _, ok := l.GroupDuty[g]; !ok {
			return nil, fmt.Errorf("motionmap: 组 %q 缺 GroupDuty 上限（上限自洽：无上限组=安全盒缺口）", g)
		}
	}
	for g, d := range l.GroupDuty {
		if g == "" {
			return nil, errors.New("motionmap: GroupDuty 含空组名")
		}
		if d > AmpMax {
			return nil, fmt.Errorf("motionmap: GroupDuty[%q]=%d 超上限（须 ≤%d）", g, d, AmpMax)
		}
	}
	seenSet := map[string]int{}
	for si, set := range l.MutexGroups {
		if len(set) == 0 {
			return nil, fmt.Errorf("motionmap: 互斥集 %d 为空", si)
		}
		inSet := map[string]bool{}
		for _, g := range set {
			if g == "" {
				return nil, fmt.Errorf("motionmap: 互斥集 %d 含空组名", si)
			}
			if inSet[g] {
				return nil, fmt.Errorf("motionmap: 互斥集 %d 内组 %q 重复", si, g)
			}
			inSet[g] = true
			if prev, dup := seenSet[g]; dup {
				return nil, fmt.Errorf("motionmap: 组 %q 跨互斥集交叠（集 %d 与 %d）", g, prev, si)
			}
			seenSet[g] = si
			if _, ok := l.GroupDuty[g]; !ok {
				return nil, fmt.Errorf("motionmap: 互斥组 %q 缺 GroupDuty 上限", g)
			}
		}
	}
	return &Mapper{table: t, limits: l}, nil
}

// Map 情绪→动作集（同步直返）：silent=true 恒 nil（最高优先级，锁存静默态
// 供 IdleTick）；未知标签→中性默认行；带权选一→互斥过滤→配额截断→强度
// 缩放。任意 Label/Intensity（含 NaN/越界/垃圾串）不 panic、输出恒在安全盒。
func (m *Mapper) Map(mood Mood, silent bool, seed int64) []Action {
	m.silent = silent
	if silent {
		return nil
	}
	i := clamp01(mood.Intensity)
	row, ok := m.table[mood.Label]
	if !ok {
		row = m.table[DefaultLabel]
	}
	picked := pickPerGroup(row, mood.Label, seed) // 每组带权选一（行序）
	picked = m.applyMutex(picked)                 // 互斥集各取一（基幅度最大）
	return scaleToBox(picked, i, m.limits)        // 配额+强度→终幅
}

// IdleTick 待机微动作（呼吸/眨眼，仿真时钟）：呼吸恒在（三角波幅度调制，
// BI-12.2 微动作永不停止的构造面——除非静默）；眨眼按窗哈希 70% 概率出现。
// 二者同受 GroupDuty/GlobalAmpSum 硬截断（安全盒）；atMs<0 按 0 处理（不
// panic）。同 (atMs,seed) 同输出（确定性回放）。
func (m *Mapper) IdleTick(atMs int64, seed int64) []Action {
	if m.silent {
		return nil
	}
	if atMs < 0 {
		atMs = 0
	}
	phase := float64(atMs%idleBreathPeriodMs) / float64(idleBreathPeriodMs)
	tri := 1 - math.Abs(2*phase-1) // 三角波 [0,1]
	breath := float64(idleBreathMinAmp) + float64(idleBreathMaxAmp-idleBreathMinAmp)*tri
	capBody := math.Min(float64(m.limits.GroupDuty[GroupBody]), float64(m.limits.GlobalAmpSum))
	breathAmp := uint8(math.Floor(math.Min(breath, capBody)))
	out := []Action{{ID: "breathe", Amp: breathAmp, Group: GroupBody}}
	w := atMs / idleBlinkWindowMs
	if hashKey(strconv.FormatInt(seed, 10), strconv.FormatInt(w, 10))%100 < idleBlinkPct {
		remain := float64(m.limits.GlobalAmpSum) - float64(breathAmp)
		if remain < 0 {
			remain = 0
		}
		capFace := math.Min(float64(m.limits.GroupDuty[GroupFace]), remain)
		blinkAmp := uint8(math.Floor(math.Min(float64(idleBlinkAmp), capFace)))
		if blinkAmp > 0 {
			out = append(out, Action{ID: "blink", Amp: blinkAmp, Group: GroupFace})
		}
	}
	return out
}

// pickPerGroup 按组聚合行内候选（组序=行内首次出现序），每组带权选一
// （权重=Amp+1——零幅度候选保有基础权；哈希=fnv(label,group,seed)——
// 同 (label,seed) 同选、变 seed 防机械重复）。返回保持组序。
func pickPerGroup(row []Action, label string, seed int64) []Action {
	var groups []string
	byGroup := map[string][]Action{}
	for _, a := range row {
		if _, seen := byGroup[a.Group]; !seen {
			groups = append(groups, a.Group)
		}
		byGroup[a.Group] = append(byGroup[a.Group], a)
	}
	seedStr := strconv.FormatInt(seed, 10)
	var picked []Action
	for _, g := range groups {
		cands := byGroup[g]
		h := hashKey(label, g, seedStr)
		picked = append(picked, cands[pickWeighted(cands, h)])
	}
	return picked
}

// pickWeighted 带权选择：h 对总权重取模后顺序累减定位（确定性）。
func pickWeighted(actions []Action, h uint64) int {
	total := uint64(0)
	for _, a := range actions {
		total += uint64(a.Amp) + 1
	}
	r := h % total
	for k, a := range actions {
		w := uint64(a.Amp) + 1
		if r < w {
			return k
		}
		r -= w
	}
	return len(actions) - 1 // 浮点防御兜底（不可达）
}

// applyMutex 互斥集过滤：集内保基幅度最大者（平手取 ID 字典序小者），
// 其余丢弃。集序不影响结果（各集独立判丢弃）。
func (m *Mapper) applyMutex(picked []Action) []Action {
	drop := make([]bool, len(picked))
	for _, set := range m.limits.MutexGroups {
		in := map[string]bool{}
		for _, g := range set {
			in[g] = true
		}
		best := -1
		for k := range picked {
			if !in[picked[k].Group] || drop[k] {
				continue
			}
			if best < 0 || betterMutex(picked[k], picked[best]) {
				best = k
			}
		}
		if best < 0 {
			continue
		}
		for k := range picked {
			if k != best && in[picked[k].Group] && !drop[k] {
				drop[k] = true
			}
		}
	}
	out := picked[:0:0]
	for k, a := range picked {
		if !drop[k] {
			out = append(out, a)
		}
	}
	return out
}

// betterMutex 互斥淘汰序：基幅度大者胜；平手 ID 字典序小者胜（确定性）。
func betterMutex(a, b Action) bool {
	if a.Amp != b.Amp {
		return a.Amp > b.Amp
	}
	return a.ID < b.ID
}

// scaleToBox 配额+强度缩放（安全盒核心）：组内基幅度比例缩放至 GroupDuty、
// 全局再比例缩放至 GlobalAmpSum；终幅 = min(round(base×i), floor(配额))。
// floor 只降不升 → ΣAmp ≤ GlobalAmpSum、组内 Σ ≤ GroupDuty 恒成立；
// min(单调升, 常量) 单调 → 强度↑幅度单调不降（T12 属性）。
func scaleToBox(picked []Action, intensity float64, l Limits) []Action {
	groupSum := map[string]float64{}
	all := 0.0
	for _, a := range picked {
		groupSum[a.Group] += float64(a.Amp)
		all += float64(a.Amp)
	}
	groupScale := map[string]float64{}
	for g, s := range groupSum {
		groupScale[g] = 1
		if duty := float64(l.GroupDuty[g]); s > duty {
			groupScale[g] = duty / s
		}
	}
	globalScale := 1.0
	scaled := 0.0
	for _, a := range picked {
		scaled += float64(a.Amp) * groupScale[a.Group]
	}
	if scaled > float64(l.GlobalAmpSum) {
		globalScale = float64(l.GlobalAmpSum) / scaled
	}
	out := make([]Action, len(picked))
	for k, a := range picked {
		quota := math.Floor(float64(a.Amp) * groupScale[a.Group] * globalScale)
		amp := math.Round(float64(a.Amp) * intensity)
		if amp > quota {
			amp = quota
		}
		if amp < 0 {
			amp = 0
		}
		out[k] = Action{ID: a.ID, Amp: uint8(amp), Group: a.Group}
	}
	return out
}

// hashKey fnv-1a 确定性哈希（种子驱动采样的唯一「随机」面——无 math/rand，
// 纯函数可回放）。段间掺分隔字节防跨段拼接歧义。
func hashKey(parts ...string) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for _, p := range parts {
		for i := 0; i < len(p); i++ {
			h ^= uint64(p[i])
			h *= prime64
		}
		h ^= 0x1f
		h *= prime64
	}
	return h
}

// clamp01 夹紧 [0,1]（NaN→0、+Inf→1、−Inf/负→0）——Intensity 越界/NaN 的
// 统一入口（与 emotion.Event.Intensity 同语义）。
func clamp01(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
