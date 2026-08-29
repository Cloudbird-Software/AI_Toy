// Package turntaking 实现端侧话轮管理 FSM（T3，m1-spec §3 包契约 B）：
// VAD 事件流进 → 话轮动作（开麦/关麦/话轮终点/打断停播）出。纯同步、无 IO、
// 不 panic；AtMs 非单调的事件整体丢弃（迟到帧不回放）。
//
// 转移表（docs/m1-spec.md §3，7 行穷举；未列组合=自转移零动作）：
//
//	| 态        | 事件                    | 后继      | 动作                       |
//	|-----------|-------------------------|-----------|----------------------------|
//	| Idle      | OnWake / EvVoiceStart   | Listening | ActOpenMic                 |
//	| Listening | EvVoiceStart            | Listening | —（清尾静音计时）          |
//	| Listening | EvVoiceEnd 后静音≥SilenceMs | Idle | ActTurnEnd+ActCloseMic |
//	| Listening | 累计≥MaxTurnMs          | Idle      | ActTurnEnd+ActCloseMic（防挂起）|
//	| Listening | OnSpeakStart            | Speaking  | —                          |
//	| Speaking  | EvVoiceStart            | Listening | ActStopTTS+ActOpenMic（打断）|
//	| Speaking  | EvVoiceEnd / OnSpeakDone| Speaking/Idle | —/—                  |
//
// 路径选择（实现卡 #80，记录于 PR）：① Idle+OnSpeakStart→Speaking 为放行转移
// （表外补格）——spec §1 闭环要求 ActTurnEnd（→Idle）后驱动注入 SpeakRequest、
// OnSpeakStart 开播入 Speaking，不放行则打断链（BI-3.2）在集成流不可达；
// ② Listening 行4 累计截断先于行2 自转移判定（VAD 永不静音也照样防挂起）；
// ③ 尾静音基准=话轮起点或最近语音事件（唤醒后不开口的话轮同样按 SilenceMs
// 收口，与 MaxTurnMs 双保险）；EvVoiceStart 即清静音计时、EvVoiceEnd 重起算。
package turntaking

import (
	"fmt"
	"math"
)

// State 话轮 FSM 状态（StIdle 为零值）。
type State int8

const (
	StIdle      State = iota // 空闲（零值）：无进行中话轮，麦关
	StListening              // 倾听：用户话轮进行中（唤醒或用户语音开麦）
	StSpeaking               // 播报：TTS 播出中，可被打断
)

// String 便于测试与日志可读。
func (s State) String() string {
	switch s {
	case StIdle:
		return "Idle"
	case StListening:
		return "Listening"
	case StSpeaking:
		return "Speaking"
	}
	return fmt.Sprintf("State(%d)", int8(s))
}

// VADEventKind VAD 事件类别（EvNone 为零值）。
type VADEventKind int8

const (
	EvNone       VADEventKind = iota // 静音观测帧（无语音活动）
	EvVoiceStart                     // 用户开始说话
	EvVoiceEnd                       // 用户停止说话（尾静音自此刻起算）
)

// VADEvent VAD 事件（AtMs 采集时刻 ms；调用方尽力单调，非单调事件被丢弃）。
type VADEvent struct {
	Kind VADEventKind
	AtMs int64
}

// ActionKind FSM 输出动作类别（ActNone 为零值）。
type ActionKind int8

const (
	ActNone     ActionKind = iota // 无动作（零值）
	ActOpenMic                    // 开麦（进入 Listening）
	ActCloseMic                   // 关麦（话轮结束）
	ActTurnEnd                    // 话轮终点（驱动注入 SpeakRequest）
	ActStopTTS                    // 停止 TTS 播报（打断，BI-3.2）
)

// Action 一次输出动作：AtMs=动作时刻，同步于触发事件（打断=同步单步，逻辑延迟 0）。
type Action struct {
	Kind ActionKind
	AtMs int64
}

// TierPolicy T14 档位镜像（语义镜像 tests/properties 契约的 RuntimeModel.TierCaps，
// 本包不 import tests/**——防「对着考卷优化」）；M1 预留不接线（ADR-0004 桩约定）。
type TierPolicy interface {
	// SilenceBudgetMs 返回档位下的尾静音预算 ms。
	SilenceBudgetMs(tier int) int
}

// flatTierPolicy M1 默认档位表：各档同预算（=SilenceMs，不引入任何档位差异化
// 数值——档位真身由 runtime-fsm 落地后注入替换）。
type flatTierPolicy struct{ budgetMs int }

// SilenceBudgetMs 实现 TierPolicy。
func (p flatTierPolicy) SilenceBudgetMs(tier int) int { return p.budgetMs }

// Config FSM 配置。三项时间参数均 >0；BargeInWindow ∈ [1,300]ms（打断响应契约
// 上限，T3-G0-01；链路实测延迟 M2 硬件计时）。
type Config struct {
	SilenceMs     int        // Listening 尾静音达到该时长 → 话轮终点
	MaxTurnMs     int        // Listening 话轮累计达到该时长 → 强制截断（防挂起）
	BargeInWindow int        // 打断响应窗口（契约上限 300ms）
	Policy        TierPolicy // T14 档位镜像；nil=默认表（M1 预留不接线）
}

// FSM 话轮状态机（单流串行无并发——资产卡口径，不加锁）。
type FSM struct {
	cfg   Config
	state State

	lastMs         int64 // 已见最大 AtMs（非单调丢弃基准）
	turnStartMs    int64 // 当前话轮起点（Listening 累计计时基准）
	silenceBaseMs  int64 // 尾静音基准（silenceRunning 时有效）
	silenceRunning bool  // 尾静音计时是否在跑（语音进行中清零）
	voiceActive    bool  // 用户语音进行中（EvVoiceStart 后未 EvVoiceEnd）

	policy           TierPolicy
	bargeInLatencyMs int64 // 最近一次打断的逻辑延迟（noBargeIn=尚未发生）
}

const (
	// bargeInWindowMaxMs 打断响应契约上限（T3-G0-01：≤300ms）。
	bargeInWindowMaxMs = 300
	// noBargeIn 尚无打断发生时 BargeInLatencyMs 的哨兵值。
	noBargeIn int64 = -1
)

// NewFSM 构造话轮 FSM：校验配置（错误仅此处返回，任意调用序此后不 error 不 panic）。
func NewFSM(cfg Config) (*FSM, error) {
	if cfg.SilenceMs <= 0 {
		return nil, fmt.Errorf("turntaking: SilenceMs 须 >0（尾静音门限，got %d）", cfg.SilenceMs)
	}
	if cfg.MaxTurnMs <= 0 {
		return nil, fmt.Errorf("turntaking: MaxTurnMs 须 >0（话轮上限防挂起，got %d）", cfg.MaxTurnMs)
	}
	if cfg.BargeInWindow < 1 || cfg.BargeInWindow > bargeInWindowMaxMs {
		return nil, fmt.Errorf("turntaking: BargeInWindow 须 ∈[1,%d]ms（打断响应契约，got %d）",
			bargeInWindowMaxMs, cfg.BargeInWindow)
	}
	policy := cfg.Policy
	if policy == nil {
		policy = flatTierPolicy{budgetMs: cfg.SilenceMs}
	}
	return &FSM{cfg: cfg, state: StIdle, lastMs: math.MinInt64,
		bargeInLatencyMs: noBargeIn, policy: policy}, nil
}

// State 返回当前状态。
func (f *FSM) State() State { return f.state }

// BargeInLatencyMs 返回最近一次打断的逻辑响应延迟（EvVoiceStart 输入→ActStopTTS
// 输出，同步单步恒为 0 ≤ BargeInWindow；noBargeIn=-1 表示尚未发生）。链路实测
// 延迟（麦克风→VAD→FSM→TTS 停止）归 M2 硬件计时，本值即 T3-G0-01 的逻辑面证据。
func (f *FSM) BargeInLatencyMs() int64 { return f.bargeInLatencyMs }

// OnWake 唤醒事件：Idle→Listening（开麦）；其余态自转移。
func (f *FSM) OnWake(atMs int64) []Action {
	if !f.accept(atMs) {
		return nil
	}
	if f.state == StIdle {
		f.enterListening(atMs, false)
		return []Action{{Kind: ActOpenMic, AtMs: atMs}}
	}
	return nil
}

// OnVAD VAD 事件：主转移入口（转移表见包注释）。
func (f *FSM) OnVAD(ev VADEvent) []Action {
	if ev.Kind != EvNone && ev.Kind != EvVoiceStart && ev.Kind != EvVoiceEnd {
		return nil // 畸变类别：按无事件处理（任意调用序不 panic）
	}
	if !f.accept(ev.AtMs) {
		return nil // 迟到帧不回放
	}
	switch f.state {
	case StIdle:
		if ev.Kind == EvVoiceStart { // 行1右：用户语音直接开麦
			f.enterListening(ev.AtMs, true)
			return []Action{{Kind: ActOpenMic, AtMs: ev.AtMs}}
		}
	case StListening:
		return f.onVADListening(ev)
	case StSpeaking:
		if ev.Kind == EvVoiceStart { // 行6：打断（BI-3.2）
			f.bargeInLatencyMs = 0 // 同步单步：逻辑延迟 0
			f.enterListening(ev.AtMs, true)
			return []Action{{Kind: ActStopTTS, AtMs: ev.AtMs}, {Kind: ActOpenMic, AtMs: ev.AtMs}}
		}
		// 行7/未列：Speaking+EvVoiceEnd/EvNone=自转移（TTS 播报不受 VAD 静音影响）
	}
	return nil
}

// OnSpeakStart TTS 开播：Listening→Speaking（行5）；Idle→Speaking 为放行转移
// （spec §1 闭环：TurnEnd 后驱动注入 SpeakRequest 开播）；Speaking 自转移。
func (f *FSM) OnSpeakStart(atMs int64) []Action {
	if !f.accept(atMs) {
		return nil
	}
	if f.state == StIdle || f.state == StListening {
		f.state = StSpeaking
	}
	return nil
}

// OnSpeakDone TTS 播完：Speaking→Idle（行7）；其余态自转移。
func (f *FSM) OnSpeakDone(atMs int64) []Action {
	if !f.accept(atMs) {
		return nil
	}
	if f.state == StSpeaking {
		f.state = StIdle
	}
	return nil
}

// accept 单调门：早于已见最大值的 AtMs 整体丢弃（等值不算非单调，须处理）。
func (f *FSM) accept(atMs int64) bool {
	if atMs < f.lastMs {
		return false
	}
	f.lastMs = atMs
	return true
}

// onVADListening Listening 态 VAD 转移（行2/3/4）。
func (f *FSM) onVADListening(ev VADEvent) []Action {
	// 行4（防挂起）先判：累计自话轮起点起算；VAD 永不静音（持续 VoiceStart/
	// VoiceEnd 翻转）也照样截断——任意 VAD 事件都推进累计观测。
	if ev.AtMs-f.turnStartMs >= int64(f.cfg.MaxTurnMs) {
		return f.endTurn(ev.AtMs)
	}
	switch ev.Kind {
	case EvVoiceStart: // 行2：语音中/停顿内复讲——清尾静音计时
		f.voiceActive = true
		f.silenceRunning = false
	case EvVoiceEnd: // 行3前缀：尾静音自 VoiceEnd 重起算（此刻静音 0，终点由后续观测触发）
		f.voiceActive = false
		f.silenceRunning = true
		f.silenceBaseMs = ev.AtMs
	case EvNone:
		if f.voiceActive { // VAD 漏发 VoiceEnd：自首个静音观测帧保守起算
			f.voiceActive = false
			f.silenceRunning = true
			f.silenceBaseMs = ev.AtMs
		}
		if f.silenceRunning && ev.AtMs-f.silenceBaseMs >= int64(f.cfg.SilenceMs) { // 行3
			return f.endTurn(ev.AtMs)
		}
	}
	return nil
}

// enterListening 进入 Listening 并开启新话轮计时：speaking=用户正在说话
// （EvVoiceStart 入口，静音不跑）；false=唤醒入口（话轮起点即静音基准）。
func (f *FSM) enterListening(atMs int64, speaking bool) {
	f.state = StListening
	f.turnStartMs = atMs
	f.voiceActive = speaking
	f.silenceRunning = !speaking
	if !speaking {
		f.silenceBaseMs = atMs
	}
}

// endTurn 话轮终点（行3/行4 同一出口）：Idle+ActTurnEnd+ActCloseMic。
func (f *FSM) endTurn(atMs int64) []Action {
	f.state = StIdle
	f.voiceActive = false
	f.silenceRunning = false
	return []Action{{Kind: ActTurnEnd, AtMs: atMs}, {Kind: ActCloseMic, AtMs: atMs}}
}
