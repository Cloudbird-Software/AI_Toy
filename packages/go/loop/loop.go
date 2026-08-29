// Package loop —— L1 演示闭环组装器（M1 收官，IR #87 / docs/m1-spec.md §1）。
//
// 三资产事件管道：音频帧进（kws.Detector）→ 唤醒（EvWake）→ 话轮 FSM
// （turntaking.FSM：开麦/关麦/话轮终点/打断）→ 话轮终点产文（Responder，
// M2=ASR+LLM+T15+记忆）→ TTS 路由（tts.Router：拦截/缓存/云端/端侧/降级）→
// 音频块出（PumpSpeak）。本包即 ADR-0004 预留的「驱动层」：Event/VADEvent/
// Request 平凡类型由本包搬运，三资产包互不 import 的结构不动；Responder 是
// spec §1「SpeakRequest.Text 由驱动注入」的接口化落位（M1 注入脚本桩，
// M2 换真实管道只换注入不改结构）。
//
// 驱动模型：单流串行同步——PushAudioFrame/PushVAD 推进输入面，PumpSpeak 按
// 调用方节奏逐块交付音频（块间可注入打断）；无 goroutine、无 IO、事件不携带
// 墙钟（确定性回放属性的前提；计时观测归 tts.Router.Metrics 与 M2 真机）。
//
// 故障语义（CI-4 前 3 行的组件级落位，规格 §8.3 故障矩阵 / docs/gates/system.md）：
//
//	Responder err    → 兜底告知话术接话（诚实告知受限，绝不静默不响应）
//	云首包超时/失败  → Router 降级：静默占位 ≤SilenceCapMs → 端侧全新补偿重合成
//	输出超长/死循环  → 句界硬截断+自然收尾（防线一）+ 播报字节上限（防线二）
//
// 一切降级 ∈ DegradeReason 预定义集；任意播报终止路径必经 finishSpeak 收口
// （绝不 hang）；打断 Cancel 幂等、已播 Seq 不回退（不重播半句）。
//
// 依赖纪律：import 白名单=标准库 + 三资产包（同 module）；零新增外部依赖。
package loop

import (
	"errors"
	"fmt"
	"io"
	"math"
	"time"
	"unicode/utf8"

	"github.com/Cloudbird-Software/AI_Toy/packages/go/kws"
	"github.com/Cloudbird-Software/AI_Toy/packages/go/tts"
	"github.com/Cloudbird-Software/AI_Toy/packages/go/turntaking"
)

// ---- 事件面 ----

// EventKind 管道事件类别（零值=EvNone）。全链路事件序列即 L1 冒烟的断言面。
type EventKind int8

const (
	EvNone       EventKind = iota
	EvWake                 // 唤醒命中（kws.EvWake；Idle 态触发 FSM OnWake 开麦）
	EvMicOpen              // 开麦（ActOpenMic：唤醒/用户语音/打断后重开）
	EvTurnEnd              // 话轮终点（ActTurnEnd；SpeakRequest 已发往 Responder）
	EvMicClose             // 关麦（ActCloseMic，紧跟 EvTurnEnd）
	EvSpeakStart           // 开播（Router.Synthesize 成功；OnSpeakStart→Speaking）
	EvAudioOut             // 音频块出（Seq/Bytes；PumpSpeak 逐块交付）
	EvSpeakDone            // 播报收口（流尽/上限/中途故障；OnSpeakDone→Idle）
	EvInterrupt            // 用户打断（ActStopTTS+Router.Cancel；BI-3.2 同步单步）
	EvDegrade              // 降级行为（Reason ∈ DegradeReason 预定义集；CI-4 组件级）
)

// String 便于测试与日志可读。
func (k EventKind) String() string {
	switch k {
	case EvNone:
		return "None"
	case EvWake:
		return "Wake"
	case EvMicOpen:
		return "MicOpen"
	case EvTurnEnd:
		return "TurnEnd"
	case EvMicClose:
		return "MicClose"
	case EvSpeakStart:
		return "SpeakStart"
	case EvAudioOut:
		return "AudioOut"
	case EvSpeakDone:
		return "SpeakDone"
	case EvInterrupt:
		return "Interrupt"
	case EvDegrade:
		return "Degrade"
	}
	return fmt.Sprintf("EventKind(%d)", int8(k))
}

// DegradeReason 降级行为预定义集（CI-4 故障注入矩阵前 3 行 + m1-spec §4.4
// 降级行为表）——链路一切降级 ∈ 本枚举（穷举即「行为 ∈ 预定义集」断言面）。
type DegradeReason int8

const (
	DegradeNone             DegradeReason = iota
	DegradeResponderDown                  // CH-01 云 LLM 断连/5xx/限流：兜底告知话术接话（诚实告知受限）
	DegradeTTSFallback                    // CH-02 云首包超时/失败：静默占位 ≤SilenceCapMs → 端侧补偿重合成
	DegradeTTSNoAudio                     // CH-02 全通道失败/空文本/流中途 err：0 音频收口（不重播半句）
	DegradeSpeakOverrun                   // CH-03 输出超长：句界硬截断+自然收尾 / 播报字节上限生效
	DegradeSpeakIntercepted               // §4.4 PreSpeak 拒绝：读出 0 字节（T13-G0-01 行为面）
)

// String 便于测试与日志可读。
func (r DegradeReason) String() string {
	switch r {
	case DegradeNone:
		return "None"
	case DegradeResponderDown:
		return "ResponderDown"
	case DegradeTTSFallback:
		return "TTSFallback"
	case DegradeTTSNoAudio:
		return "TTSNoAudio"
	case DegradeSpeakOverrun:
		return "SpeakOverrun"
	case DegradeSpeakIntercepted:
		return "SpeakIntercepted"
	}
	return fmt.Sprintf("DegradeReason(%d)", int8(r))
}

// Event 单个管道事件。AtMs=逻辑时刻（驱动输入时间戳的镜像——无墙钟，确定性
// 回放属性的前提）。Bytes 语义随 Kind：EvAudioOut=块字节数；EvDegrade 的
// SpeakOverrun 截断面=截断后 rune 数（防线一）或已交付字节数（防线二）。
type Event struct {
	Kind   EventKind
	AtMs   int64
	Seq    int           // EvAudioOut：块序号（对齐 tts.Chunk.Seq）
	Bytes  int           // 见字段注释
	Reason DegradeReason // EvDegrade：降级原因（预定义集成员）
	Err    error         // EvDegrade：底层错误（诊断用，可 nil）
}

// ---- 回复生产面（M2=ASR+LLM 管道的 seam）----

// Turn 一次用户话轮上下文（Responder 输入；M2 扩展 ASR 文本/情绪/记忆引用）。
type Turn struct {
	ID    string // 话轮幂等键（与 tts.Request.TurnID 同源；打断/不重播半句的键）
	EndMs int64  // 话轮终点逻辑时刻（ActTurnEnd.AtMs）
}

// Responder 回复生产面（M1=脚本桩；M2=ASR+LLM+T15 路由+记忆管道）。
// 每话轮独立调用（断连恢复语义=下轮重试，对齐「每请求独立重试云」）；
// err≠nil=上游断连（CH-01 注入面）→ 管道降级为兜底告知话术接话；
// 空文本→tts.ErrEmptyText→DegradeTTSNoAudio 收口（不静默挂起）。
type Responder interface {
	Respond(t Turn) (string, error)
}

// ResponderFunc 函数式 Responder（脚本桩直用，无需定义类型）。
type ResponderFunc func(t Turn) (string, error)

// Respond 实现 Responder。
func (f ResponderFunc) Respond(t Turn) (string, error) { return f(t) }

// ---- 配置面 ----

// 组装层策略默认值（非门禁阈值——门禁阈值唯一来源 configs/gates/**）。
const (
	// DefaultFallbackText CH-01 兜底话术（诚实告知受限的 M1 组件级面）。
	DefaultFallbackText = "哎呀，我脑子有点转不动了，等一下再陪你聊好不好？"
	// DefaultMaxTextRunes 回复文本硬截断上限 rune（CH-03 防线一）。
	DefaultMaxTextRunes = 200
	// DefaultMaxSpeakBytes 单次播报音频字节上限（CH-03 防线二：1MiB ≈ 32.8s
	// @16kHz·16bit·mono——「播报时长上限生效」的 M1 无时钟代理）。
	DefaultMaxSpeakBytes = 1 << 20
	// DefaultDeadlineMs 首包预算镜像记录（云 300 门禁线；只记不判，同 tts 包）。
	DefaultDeadlineMs = 300
	// overrunClosing CH-03 自然收尾话术（截断后拼接，计入 rune 预算）。
	overrunClosing = "……今天就先说到这儿啦！"
)

// Config 管道配置：三资产子配置 + 组装层策略。Resp 与 TTS.PreSpeak 必接
// （fail-closed：生产禁裸奔，测试显式注入）。
type Config struct {
	KWS  kws.Config        // T4 检测器（Infer 注入面）
	FSM  turntaking.Config // T3 话轮 FSM
	TTS  tts.RouterConfig  // T13 路由（Cloud/Edge/Cache/PreSpeak 注入面）
	Resp Responder         // 回复生产面（nil→Wire error）

	Voice         string // 音色 ID（空=默认音色）
	Tier          int    // T14 档 0..3（0=L0 云端全能力…3=L3 仅缓存；越界由 Router 拒）
	DeadlineMs    int    // 首包预算镜像记录（≤0 取默认 300；只记不判）
	MaxTextRunes  int    // 回复文本硬截断上限 rune（≤0 取默认 200）
	MaxSpeakBytes int    // 单次播报音频字节上限（≤0 取默认 1MiB）
	FallbackText  string // Responder 断连兜底话术（空=DefaultFallbackText）
}

// Pipeline L1 演示闭环管道（单流串行——三资产同口径，不加锁）。
type Pipeline struct {
	cfg    Config
	det    *kws.Detector
	fsm    *turntaking.FSM
	router *tts.Router
	resp   Responder

	lastMs    int64           // 已见最大逻辑时刻（PumpSpeak 事件戳来源——无墙钟）
	turnSeq   int             // 话轮计数（TurnID 派生，确定性）
	stream    tts.AudioStream // 进行中播报流（nil=无；与 FSM Speaking 同进退）
	turnID    string          // 当前播报流归属话轮
	delivered int             // 当前播报已交付字节（上限基准）
	lat       latencyTracker  // 分段延迟采样（旁路观测，IR #95——不进事件流）
}

// Wire 组装三资产为闭环管道（m1-spec §1 驱动层）。错误仅此处返回：Resp 缺席
// （fail-closed）或三资产配置非法；此后任意调用序不 error 不 panic。
func Wire(cfg Config) (*Pipeline, error) {
	if cfg.Resp == nil {
		return nil, errors.New("loop: Responder 必接（M1 脚本桩；M2=ASR+LLM 管道）——fail-closed")
	}
	det, err := kws.NewDetector(cfg.KWS)
	if err != nil {
		return nil, fmt.Errorf("loop: kws 组装失败：%w", err)
	}
	fsm, err := turntaking.NewFSM(cfg.FSM)
	if err != nil {
		return nil, fmt.Errorf("loop: turntaking 组装失败：%w", err)
	}
	router, err := tts.NewRouter(cfg.TTS)
	if err != nil {
		return nil, fmt.Errorf("loop: tts 组装失败：%w", err)
	}
	if cfg.DeadlineMs <= 0 {
		cfg.DeadlineMs = DefaultDeadlineMs
	}
	if cfg.MaxTextRunes <= 0 {
		cfg.MaxTextRunes = DefaultMaxTextRunes
	}
	if cfg.MaxSpeakBytes <= 0 {
		cfg.MaxSpeakBytes = DefaultMaxSpeakBytes
	}
	if cfg.FallbackText == "" {
		cfg.FallbackText = DefaultFallbackText
	}
	return &Pipeline{
		cfg: cfg, det: det, fsm: fsm, router: router, resp: cfg.Resp,
		lastMs: math.MinInt64, lat: newLatencyTracker(),
	}, nil
}

// ---- 输入面 ----

// PushAudioFrame 推入音频帧：kws 检测（防抖/不应期在 kws 包）→ 唤醒即
// fsm.OnWake（Idle→Listening 开麦；其余态自转移，仅观测 EvWake）。
func (p *Pipeline) PushAudioFrame(f kws.Frame) []Event {
	p.seeMs(f.TS)
	if ev := p.det.Push(f); ev.Kind != kws.EvWake {
		return nil
	}
	out := []Event{{Kind: EvWake, AtMs: f.TS}}
	evs, _, _ := p.apply(p.fsm.OnWake(f.TS))
	return append(out, evs...)
}

// PushVAD 推入 VAD 事件（FSM 主转移入口）：话轮终点→speak 全链路（在动作
// 事件之后追加——对齐物理时序：说完→关麦→想→开口）；打断→Cancel+重开麦。
// 返回当步事件（可为 nil）。
func (p *Pipeline) PushVAD(ev turntaking.VADEvent) []Event {
	p.seeMs(ev.AtMs)
	p.latencyOnVAD(ev) // 分段延迟采样埋点（旁路，IR #95）
	out, turnEndAt, hasTurnEnd := p.apply(p.fsm.OnVAD(ev))
	if hasTurnEnd {
		out = append(out, p.speak(turnEndAt)...)
	}
	return out
}

// apply FSM 动作→管道事件映射：开麦/关麦直映射；打断（ActStopTTS）走
// stopSpeak；话轮终点（ActTurnEnd）记录待动作序列处理完后走 speak。
func (p *Pipeline) apply(acts []turntaking.Action) ([]Event, int64, bool) {
	var out []Event
	turnEndAt, hasTurnEnd := int64(0), false
	for _, a := range acts {
		switch a.Kind {
		case turntaking.ActOpenMic:
			out = append(out, Event{Kind: EvMicOpen, AtMs: a.AtMs})
		case turntaking.ActCloseMic:
			out = append(out, Event{Kind: EvMicClose, AtMs: a.AtMs})
		case turntaking.ActStopTTS:
			out = append(out, p.stopSpeak(a.AtMs)...)
		case turntaking.ActTurnEnd:
			out = append(out, Event{Kind: EvTurnEnd, AtMs: a.AtMs})
			p.lat.sampleTailSilence(a.AtMs) // 分段延迟采样埋点（旁路，IR #95）
			turnEndAt, hasTurnEnd = a.AtMs, true
		}
	}
	return out, turnEndAt, hasTurnEnd
}

// speak 话轮终点→出声全链路：Responder 产文（断连→兜底告知）→ 句界硬截断
// （超长→自然收尾）→ Router 合成（决策序见 tts 包）→ OnSpeakStart 开播；
// 合成失败→降级收口不开播（FSM 留 Idle，无挂起）。M1 同步调用；M2 由 runtime
// 管道异步化（Responder 接口不变）。
func (p *Pipeline) speak(atMs int64) []Event {
	p.turnSeq++
	turnID := fmt.Sprintf("turn-%d", p.turnSeq)
	var out []Event
	respEnter := time.Now()
	text, err := p.resp.Respond(Turn{ID: turnID, EndMs: atMs})
	p.lat.sampleResponder(time.Since(respEnter)) // 分段延迟采样埋点（旁路，IR #95）
	if err != nil {
		// CH-01：回复上游断连——诚实告知受限（兜底话术接话，绝不静默不响应）。
		out = append(out, Event{Kind: EvDegrade, AtMs: atMs, Reason: DegradeResponderDown, Err: err})
		text = p.cfg.FallbackText
	}
	if trunc, cut := truncateNatural(text, p.cfg.MaxTextRunes); cut {
		// CH-03 防线一：合成输入有界（句界硬截断+自然收尾）。
		out = append(out, Event{Kind: EvDegrade, AtMs: atMs, Reason: DegradeSpeakOverrun, Bytes: utf8.RuneCountInString(trunc)})
		text = trunc
	}
	p.lat.markSynth(atMs) // 分段延迟采样埋点：tts_first 起点（旁路，IR #95）
	stream, err := p.router.Synthesize(tts.Request{
		Text: text, Voice: p.cfg.Voice, TurnID: turnID,
		Tier: p.cfg.Tier, DeadlineMs: p.cfg.DeadlineMs,
	})
	if err != nil {
		reason := DegradeTTSNoAudio
		if errors.Is(err, tts.ErrIntercepted) {
			reason = DegradeSpeakIntercepted // 读出=0 字节（T13-G0-01 行为面）
		}
		out = append(out, Event{Kind: EvDegrade, AtMs: atMs, Reason: reason, Err: err})
		return out // 不开播：FSM 留 Idle
	}
	// CH-02 观测面：Router 元数据报告降级通道（静默占位≤SilenceCapMs→端侧补偿）。
	if p.channelOf(turnID) == "degraded" {
		out = append(out, Event{Kind: EvDegrade, AtMs: atMs, Reason: DegradeTTSFallback})
	}
	p.stream, p.turnID, p.delivered = stream, turnID, 0
	p.fsm.OnSpeakStart(atMs) // Idle→Speaking（放行转移，turntaking 路径①）
	out = append(out, Event{Kind: EvSpeakStart, AtMs: atMs})
	return out
}

// stopSpeak 打断执行面（ActStopTTS）：Router.Cancel（幂等）+EvInterrupt；流
// 引用即清——打断即收口，后续 PumpSpeak 零事件（BI-3.2 同步单步，逻辑延迟 0
// ≤ BargeInWindow ≤300ms 契约；链路实测延迟 M2 硬件计时）。
func (p *Pipeline) stopSpeak(atMs int64) []Event {
	if p.stream != nil {
		_ = p.router.Cancel(p.turnID)
		p.stream, p.turnID, p.delivered = nil, "", 0
	}
	return []Event{{Kind: EvInterrupt, AtMs: atMs}}
}

// PumpSpeak 交付下一音频块（调用方掌握节奏——块间可注入打断）。nil=无进行中
// 播报。流尽→EvSpeakDone 收口；字节超上限→防线二截断收口（终止流）；流错误
// →降级收口（终止态固化，不重播半句）；Cancel 终止→收口（打断面已发
// EvInterrupt，此处兜底防漏收口挂起）。
func (p *Pipeline) PumpSpeak() []Event {
	if p.stream == nil {
		return nil
	}
	c, err := p.stream.Next()
	switch {
	case err == nil:
		if p.delivered+len(c.Data) > p.cfg.MaxSpeakBytes {
			// CH-03 防线二：播报上限生效——该块起截断：终止流+收口回 Idle。
			_ = p.router.Cancel(p.turnID)
			out := []Event{{Kind: EvDegrade, AtMs: p.lastMs, Reason: DegradeSpeakOverrun, Bytes: p.delivered}}
			return append(out, p.finishSpeak()...)
		}
		if p.delivered == 0 {
			p.lat.sampleFirstChunk(p.lastMs) // 分段延迟采样埋点：首块 EvAudioOut（旁路，IR #95）
		}
		p.delivered += len(c.Data)
		return []Event{{Kind: EvAudioOut, AtMs: p.lastMs, Seq: c.Seq, Bytes: len(c.Data)}}
	case errors.Is(err, io.EOF):
		return p.finishSpeak()
	case errors.Is(err, tts.ErrCanceled):
		return p.finishSpeak()
	default:
		// 流中途 err：终止固化——已播 Seq 不回退、不重播半句（CI-4）。
		out := []Event{{Kind: EvDegrade, AtMs: p.lastMs, Reason: DegradeTTSNoAudio, Err: err}}
		return append(out, p.finishSpeak()...)
	}
}

// finishSpeak 播报收口：清流引用 + OnSpeakDone（Speaking→Idle）+ EvSpeakDone。
// 任意终止路径必经此处——漏收口=FSM 永久 Speaking（CI-4「绝不 hang」的实现面）。
func (p *Pipeline) finishSpeak() []Event {
	p.stream, p.turnID, p.delivered = nil, "", 0
	p.fsm.OnSpeakDone(p.lastMs)
	return []Event{{Kind: EvSpeakDone, AtMs: p.lastMs}}
}

// ---- 观测面 ----

// State 话轮 FSM 状态透传（组合观测面）。
func (p *Pipeline) State() turntaking.State { return p.fsm.State() }

// Speaking 是否有进行中播报（无挂起断言的观测面；与 FSM Speaking 同进退）。
func (p *Pipeline) Speaking() bool { return p.stream != nil }

// LastMs 已见最大逻辑时刻（驱动输入镜像；PumpSpeak 事件戳基准）。
func (p *Pipeline) LastMs() int64 { return p.lastMs }

// channelOf 话轮的路由通道（Router.Metrics 观测面；无记录=""）。M1 只读不判
// ——通道决策归 Router，此处仅把降级通道提升为管道事件。
func (p *Pipeline) channelOf(turnID string) string {
	var ch string
	for _, m := range p.router.Metrics() {
		if m.TurnID == turnID {
			ch = m.Channel
		}
	}
	return ch
}

// seeMs 镜像驱动输入时刻（单调不回退——事件戳基准，无墙钟）。
func (p *Pipeline) seeMs(at int64) {
	if at > p.lastMs {
		p.lastMs = at
	}
}

// truncateNatural 句界硬截断+自然收尾（CH-03 防线一）：≤maxRunes 原文直返
// （cut=false）；超出则在预算内取最长句界前缀（无句界取硬边界），拼接自然
// 收尾话术（计入 rune 预算，总长 ≤maxRunes）。
func truncateNatural(text string, maxRunes int) (string, bool) {
	if maxRunes <= 0 {
		return "", true // 病态上限：归零（上层 ErrEmptyText 降级收口，仍有界）
	}
	if utf8.RuneCountInString(text) <= maxRunes {
		return text, false
	}
	runes := []rune(text)
	cut := maxRunes - utf8.RuneCountInString(overrunClosing)
	if cut <= 0 {
		return string(runes[:maxRunes]), true // 上限过小：纯硬截断仍有界
	}
	end := cut
	for i := cut; i > 0; i-- {
		if isSentenceEnd(runes[i-1]) {
			end = i
			break
		}
	}
	return string(runes[:end]) + overrunClosing, true
}

// isSentenceEnd 句末标点（句界回退判定集：中英文句末+分句+换行）。
func isSentenceEnd(r rune) bool {
	switch r {
	case '。', '！', '？', '…', '!', '?', ';', '；', '\n':
		return true
	}
	return false
}
