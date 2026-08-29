// L1 闭环组装器测试（IR #87 / docs/m1-spec.md §1 驱动层，spec §8.3 PR 级冒烟
// =核心旅程+故障前 3 行）：
//
//	全链路冒烟（3 场景）：唤醒-对话-播报 / 唤醒-打断-补偿 / 静默噪声零事件
//	故障注入（CI-4 前 3 行）：CH-01 Responder 断连 / CH-02 TTS 超时（降级+全失败）/
//	                         CH-03 输出超长（文本截断+字节上限双防线）
//	确定性属性（testing/quick）：P1 重放全等 / P2 有界终止（绝不 hang）/
//	                         P3 事件不变量（时刻非回退/降级 ∈ 预定义集/Seq 单调）
//	旅程映射：golden-journeys J01–J03 → 组件级脚本（断言以 yaml 为准）
//
// 桩即注入面（M1 零外部依赖）：置信度经 kws.ConfidenceFunc 注入、合成器/Responder
// 本地桩；真实引擎/ASR/LLM 接入后同组测试换注入实现重跑（ADR-0004）。
package loop

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/quick"
	"time"
	"unicode/utf8"

	"github.com/Cloudbird-Software/AI_Toy/packages/go/kws"
	"github.com/Cloudbird-Software/AI_Toy/packages/go/tts"
	"github.com/Cloudbird-Software/AI_Toy/packages/go/turntaking"
	"gopkg.in/yaml.v3"
)

// ---- 测试常量与桩 ----

const (
	tSilenceMs = 500 // 测试用尾静音门限（与 testConfig.FSM 同步）
)

// featsConf Feats 通道置信度桩（Frame.Feats[0]→置信度——脚本化唤醒注入）。
var featsConf = kws.ConfidenceFunc(func(f kws.Frame) float64 {
	if len(f.Feats) > 0 {
		return float64(f.Feats[0])
	}
	return 0
})

// errResponderDown CH-01 注入哨兵（固定错误值——P1 重放全等可比）。
var errResponderDown = errors.New("stub: responder down (CH-01)")

// allowAllPreSpeak 恒放行 T9 桩（拦截面归 T13 包门禁测试）。
func allowAllPreSpeak(string) error { return nil }

// stubSynth 可注入故障的合成器桩：synthErr=Synthesize 直接失败（CH-02 全通道
// 失败注入点）；firstDelayMs=流首包延迟（CH-02 云首包超时注入点）。
type stubSynth struct {
	mu           sync.Mutex
	chunks       [][]byte
	synthErr     error
	firstDelayMs int
	calls        int
	texts        []string
	cancels      int
}

func newStubSynth(n, size int) *stubSynth {
	chunks := make([][]byte, n)
	for i := range chunks {
		chunks[i] = bytes.Repeat([]byte{0xAB}, size)
	}
	return &stubSynth{chunks: chunks}
}

func (s *stubSynth) setSynthErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.synthErr = err
}

func (s *stubSynth) setFirstDelayMs(ms int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.firstDelayMs = ms
}

func (s *stubSynth) Synthesize(req tts.Request) (tts.AudioStream, error) {
	s.mu.Lock()
	s.calls++
	s.texts = append(s.texts, req.Text)
	delay, err := s.firstDelayMs, s.synthErr
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	chunks := make([]tts.Chunk, len(s.chunks))
	for i, d := range s.chunks {
		chunks[i] = tts.Chunk{Data: d, Seq: i + 1, Final: i == len(s.chunks)-1}
	}
	return &stubStream{parent: s, chunks: chunks, firstDelayMs: delay}, nil
}

func (s *stubSynth) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *stubSynth) textAt(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < 0 || i >= len(s.texts) {
		return ""
	}
	return s.texts[i]
}

func (s *stubSynth) cancelCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancels
}

// stubStream 桩流：chunks 顺序重放至 EOF；firstDelayMs=首包延迟（持锁睡眠——
// Cancel 排队等锁，M1 桩简化，测试值 ≤100ms）；Cancel 幂等并上报 parent。
type stubStream struct {
	parent *stubSynth

	mu           sync.Mutex
	chunks       []tts.Chunk
	i            int
	firstDelayMs int
	canceled     bool
}

func (st *stubStream) Next() (tts.Chunk, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.firstDelayMs > 0 {
		d := st.firstDelayMs
		st.firstDelayMs = 0
		time.Sleep(time.Duration(d) * time.Millisecond)
	}
	if st.canceled {
		return tts.Chunk{}, tts.ErrCanceled
	}
	if st.i >= len(st.chunks) {
		return tts.Chunk{}, io.EOF
	}
	c := st.chunks[st.i]
	st.i++
	return c, nil
}

func (st *stubStream) Cancel() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.canceled = true
	if st.parent != nil {
		st.parent.mu.Lock()
		st.parent.cancels++
		st.parent.mu.Unlock()
	}
	return nil
}

// testConfig 测试管道配置（Tier 由用例指定：2=端侧同步通道（确定性属性用）/
// 1=云通道（CH-02 与旅程 L1 档用））。
func testConfig(resp Responder, ttsCfg tts.RouterConfig, tier int) Config {
	return Config{
		KWS: kws.Config{FrameMs: 30, ConfirmFrames: 2, RefractoryMs: 500,
			Threshold: 0.5, Infer: featsConf},
		FSM:  turntaking.Config{SilenceMs: tSilenceMs, MaxTurnMs: 20000, BargeInWindow: 300},
		TTS:  ttsCfg,
		Resp: resp,
		Tier: tier,
	}
}

// ---- 驱动辅助 ----

// pushWake 推两帧超阈置信度（ConfirmFrames=2 → 第二帧触发 Wake+开麦）。
func pushWake(p *Pipeline, at int64) []Event {
	var out []Event
	out = append(out, p.PushAudioFrame(kws.Frame{TS: at, Feats: []float32{0.9}})...)
	out = append(out, p.PushAudioFrame(kws.Frame{TS: at + 30, Feats: []float32{0.9}})...)
	return out
}

// pushUserTurn 推一段用户语音（start→end→尾静音收口）：返回当步事件
// （含话轮终点、关麦与开播全链路）。
func pushUserTurn(p *Pipeline, at int64) []Event {
	var out []Event
	out = append(out, p.PushVAD(turntaking.VADEvent{Kind: turntaking.EvVoiceStart, AtMs: at})...)
	out = append(out, p.PushVAD(turntaking.VADEvent{Kind: turntaking.EvVoiceEnd, AtMs: at + 300})...)
	out = append(out, p.PushVAD(turntaking.VADEvent{Kind: turntaking.EvNone, AtMs: at + 300 + tSilenceMs})...)
	return out
}

// pumpToDone 泵到播报收口（bound=防呆上限——3 块流正常 4 泵内必收口）。
func pumpToDone(p *Pipeline, bound int) []Event {
	var out []Event
	for i := 0; p.Speaking() && i < bound; i++ {
		out = append(out, p.PumpSpeak()...)
	}
	return out
}

// sig 事件签名（冒烟断言口径：Kind@AtMs + Seq/Bytes/Reason；Err 不入签名——
// 固定哨兵由用例单独断言）。
type sig struct {
	k      EventKind
	at     int64
	seq    int
	bytes  int
	reason DegradeReason
}

func sigs(evs []Event) []sig {
	out := make([]sig, len(evs))
	for i, e := range evs {
		out[i] = sig{e.Kind, e.AtMs, e.Seq, e.Bytes, e.Reason}
	}
	return out
}

func countKind(evs []Event, k EventKind) int {
	n := 0
	for _, e := range evs {
		if e.Kind == k {
			n++
		}
	}
	return n
}

func findDegrade(evs []Event, r DegradeReason) (Event, bool) {
	for _, e := range evs {
		if e.Kind == EvDegrade && e.Reason == r {
			return e, true
		}
	}
	return Event{}, false
}

// ---- Wire fail-closed ----

// TestWireFailClosed 组装前置校验：Responder 缺席（生产禁裸奔）与三资产配置
// 非法均在 Wire 拦截——此后任意调用序不 error。
func TestWireFailClosed(t *testing.T) {
	validKWS := kws.Config{FrameMs: 30, ConfirmFrames: 1, Threshold: 0.5, Infer: featsConf}
	validFSM := turntaking.Config{SilenceMs: 500, MaxTurnMs: 20000, BargeInWindow: 300}
	if _, err := Wire(Config{KWS: validKWS, FSM: validFSM,
		TTS: tts.RouterConfig{PreSpeak: allowAllPreSpeak, Edge: newStubSynth(3, 64)}}); err == nil {
		t.Errorf("Responder 缺席须 Wire error（fail-closed），got nil")
	}
	if _, err := Wire(Config{
		KWS: validKWS, FSM: validFSM,
		TTS:  tts.RouterConfig{PreSpeak: nil, Edge: newStubSynth(3, 64)}, // T9 钩子缺席
		Resp: ResponderFunc(func(Turn) (string, error) { return "x", nil }),
	}); err == nil {
		t.Errorf("PreSpeak 缺席须 Wire error（tts.NewRouter fail-closed 透传），got nil")
	}
	if _, err := Wire(Config{
		KWS: kws.Config{ConfirmFrames: 0}, // 非法 kws 配置
		FSM: validFSM, TTS: tts.RouterConfig{PreSpeak: allowAllPreSpeak},
		Resp: ResponderFunc(func(Turn) (string, error) { return "x", nil }),
	}); err == nil {
		t.Errorf("kws 配置非法须 Wire error，got nil")
	}
}

// ---- 全链路冒烟（3 场景）----

// TestSmokeWakeConverseSpeak 场景一（核心旅程骨架）：唤醒→对话→播报全链路。
// 事件序列即 L1 冒烟断言面：Wake→MicOpen→TurnEnd→MicClose→SpeakStart→
// AudioOut×N→SpeakDone，收口回 Idle。
func TestSmokeWakeConverseSpeak(t *testing.T) {
	synth := newStubSynth(3, 64)
	resp := ResponderFunc(func(Turn) (string, error) { return "早上好呀！", nil })
	p, err := Wire(testConfig(resp, tts.RouterConfig{PreSpeak: allowAllPreSpeak, Edge: synth}, 2))
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	var evs []Event
	evs = append(evs, pushWake(p, 0)...)
	evs = append(evs, pushUserTurn(p, 130)...)
	evs = append(evs, pumpToDone(p, 8)...)

	want := []sig{
		{EvWake, 30, 0, 0, DegradeNone},
		{EvMicOpen, 30, 0, 0, DegradeNone},
		{EvTurnEnd, 930, 0, 0, DegradeNone},
		{EvMicClose, 930, 0, 0, DegradeNone},
		{EvSpeakStart, 930, 0, 0, DegradeNone},
		{EvAudioOut, 930, 1, 64, DegradeNone},
		{EvAudioOut, 930, 2, 64, DegradeNone},
		{EvAudioOut, 930, 3, 64, DegradeNone},
		{EvSpeakDone, 930, 0, 0, DegradeNone},
	}
	if got := sigs(evs); !sigsEqual(got, want) {
		t.Errorf("全链路事件序列不符：\n got  %v\n want %v", got, want)
	}
	if p.State() != turntaking.StIdle || p.Speaking() {
		t.Errorf("收口后须回 Idle 且无进行中播报：state=%v speaking=%v", p.State(), p.Speaking())
	}
	if synth.callCount() != 1 || synth.textAt(0) != "早上好呀！" {
		t.Errorf("Responder 文本须透传合成器：calls=%d text=%q", synth.callCount(), synth.textAt(0))
	}
	// 收口后泵=零事件（不重播半句）
	if extra := p.PumpSpeak(); extra != nil {
		t.Errorf("收口后 PumpSpeak 须零事件，got %v", extra)
	}
}

// TestSmokeInterruptAndCompensate 场景二：唤醒→话轮→播报中打断（BI-3.2 同步
// 单步）→补偿话轮→重新播报。打断即收口：旧流 Cancel、已播 Seq 不回退、
// 新话轮 Seq 从头计数。
func TestSmokeInterruptAndCompensate(t *testing.T) {
	synth := newStubSynth(3, 64)
	var turns []Turn
	resp := ResponderFunc(func(tu Turn) (string, error) { turns = append(turns, tu); return "好，你说", nil })
	p, err := Wire(testConfig(resp, tts.RouterConfig{PreSpeak: allowAllPreSpeak, Edge: synth}, 2))
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	var evs []Event
	evs = append(evs, pushWake(p, 0)...)
	evs = append(evs, pushUserTurn(p, 130)...)
	evs = append(evs, p.PumpSpeak()...) // 首块已出（Seq=1）
	evs = append(evs, p.PushVAD(turntaking.VADEvent{Kind: turntaking.EvVoiceStart, AtMs: 1000})...)
	evs = append(evs, pushUserTurn(p, 1300)...)
	evs = append(evs, pumpToDone(p, 8)...)

	want := []sig{
		{EvWake, 30, 0, 0, DegradeNone},
		{EvMicOpen, 30, 0, 0, DegradeNone},
		{EvTurnEnd, 930, 0, 0, DegradeNone},
		{EvMicClose, 930, 0, 0, DegradeNone},
		{EvSpeakStart, 930, 0, 0, DegradeNone},
		{EvAudioOut, 930, 1, 64, DegradeNone},
		{EvInterrupt, 1000, 0, 0, DegradeNone},
		{EvMicOpen, 1000, 0, 0, DegradeNone},
		{EvTurnEnd, 2100, 0, 0, DegradeNone},
		{EvMicClose, 2100, 0, 0, DegradeNone},
		{EvSpeakStart, 2100, 0, 0, DegradeNone},
		{EvAudioOut, 2100, 1, 64, DegradeNone},
		{EvAudioOut, 2100, 2, 64, DegradeNone},
		{EvAudioOut, 2100, 3, 64, DegradeNone},
		{EvSpeakDone, 2100, 0, 0, DegradeNone},
	}
	if got := sigs(evs); !sigsEqual(got, want) {
		t.Errorf("打断-补偿事件序列不符：\n got  %v\n want %v", got, want)
	}
	if synth.cancelCount() != 1 {
		t.Errorf("打断须 Cancel 旧流恰一次，cancels=%d", synth.cancelCount())
	}
	if len(turns) != 2 || turns[0].ID == turns[1].ID {
		t.Errorf("两话轮须各自 Respond 且 TurnID 唯一：%v", turns)
	}
	if p.State() != turntaking.StIdle || p.Speaking() {
		t.Errorf("补偿播报后须回 Idle：state=%v speaking=%v", p.State(), p.Speaking())
	}
}

// TestSmokeIdleNoiseSilence 场景三：静默/纯噪声流零事件（误唤醒面归 T4 门禁；
// 此处断言组装层不产生任何管道事件、不触合成器）。
func TestSmokeIdleNoiseSilence(t *testing.T) {
	synth := newStubSynth(3, 64)
	p, err := Wire(testConfig(ResponderFunc(func(Turn) (string, error) { return "x", nil }),
		tts.RouterConfig{PreSpeak: allowAllPreSpeak, Edge: synth}, 2))
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	var evs []Event
	for i := 0; i < 20; i++ { // 低置信度噪声帧（全零 PCM 通道走 Feats=0.05）
		evs = append(evs, p.PushAudioFrame(kws.Frame{TS: int64(i) * 30, Feats: []float32{0.05}})...)
	}
	evs = append(evs, p.PushVAD(turntaking.VADEvent{Kind: turntaking.EvNone, AtMs: 600})...)
	if len(evs) != 0 {
		t.Errorf("静默噪声流须零事件，got %v", sigs(evs))
	}
	if p.State() != turntaking.StIdle || p.Speaking() || synth.callCount() != 0 {
		t.Errorf("空闲不变量破坏：state=%v speaking=%v synthCalls=%d",
			p.State(), p.Speaking(), synth.callCount())
	}
}

// ---- 故障注入（CI-4 前 3 行，spec §8.3 故障矩阵 / docs/gates/system.md）----

// TestFaultCH01ResponderDown CH-01（云 LLM 断连/5xx/限流，G0）：Responder err
// → 降级为兜底告知话术接话（诚实告知受限，绝不静默不响应）；每话轮独立调用
// → 下轮恢复（「每请求独立重试」语义）。
func TestFaultCH01ResponderDown(t *testing.T) {
	synth := newStubSynth(3, 64)
	var calls int
	resp := ResponderFunc(func(Turn) (string, error) {
		calls++
		if calls == 1 {
			return "", errResponderDown // 首轮断连
		}
		return "恢复啦", nil
	})
	p, err := Wire(testConfig(resp, tts.RouterConfig{PreSpeak: allowAllPreSpeak, Edge: synth}, 2))
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	var evs []Event
	evs = append(evs, pushWake(p, 0)...)
	evs = append(evs, pushUserTurn(p, 130)...)
	evs = append(evs, pumpToDone(p, 8)...)
	// 第二轮：网络恢复（Responder 正常）
	evs = append(evs, pushWake(p, 2000)...)
	evs = append(evs, pushUserTurn(p, 2130)...)
	evs = append(evs, pumpToDone(p, 8)...)

	d, ok := findDegrade(evs, DegradeResponderDown)
	if !ok {
		t.Fatalf("CH-01 须发 Degrade(ResponderDown)，events=%v", sigs(evs))
	}
	if d.Err == nil || !errors.Is(d.Err, errResponderDown) {
		t.Errorf("Degrade.Err 须携带底层断连错误，got %v", d.Err)
	}
	if synth.textAt(0) != DefaultFallbackText {
		t.Errorf("断连轮须以兜底话术接话，got %q want %q", synth.textAt(0), DefaultFallbackText)
	}
	if synth.textAt(1) != "恢复啦" {
		t.Errorf("恢复轮须透传正常回复，got %q", synth.textAt(1))
	}
	if n := countKind(evs, EvSpeakDone); n != 2 {
		t.Errorf("两轮均须播报收口（不 hang、不静默），SpeakDone=%d", n)
	}
	if p.State() != turntaking.StIdle || p.Speaking() {
		t.Errorf("收口后须回 Idle：state=%v speaking=%v", p.State(), p.Speaking())
	}
}

// TestFaultCH02TTSFirstPacket CH-02（TTS 超时/首包失败，G1）：
//   - 云首包超时 → Router 降级（静默占位 ≤SilenceCapMs → 端侧全新补偿重合成，
//     不拼半句）→ 管道观测 Degrade(TTSFallback)，音频照常交付收口；
//   - 全通道失败 → 0 音频收口 Degrade(TTSNoAudio)，不开播不挂起，下轮回云档。
func TestFaultCH02TTSFirstPacket(t *testing.T) {
	t.Run("云首包超时降级端侧", func(t *testing.T) {
		cloud := newStubSynth(3, 64)
		cloud.setFirstDelayMs(80) // 云首包 80ms > 预算 10ms → 超时
		edge := newStubSynth(3, 64)
		cfg := testConfig(ResponderFunc(func(Turn) (string, error) { return "讲个故事吧", nil }),
			tts.RouterConfig{PreSpeak: allowAllPreSpeak, Cloud: cloud, Edge: edge,
				FirstPacketTimeoutMs: 10}, 1)
		p, err := Wire(cfg)
		if err != nil {
			t.Fatalf("Wire: %v", err)
		}
		var evs []Event
		evs = append(evs, pushWake(p, 0)...)
		evs = append(evs, pushUserTurn(p, 130)...)
		evs = append(evs, pumpToDone(p, 10)...)

		if _, ok := findDegrade(evs, DegradeTTSFallback); !ok {
			t.Fatalf("CH-02 降级通道须提升为 Degrade(TTSFallback)，events=%v", sigs(evs))
		}
		// 降级流事件面：静默占位（Seq=0/0 字节）→ 端侧补偿块 Seq 1..N → 收口
		audio := 0
		for _, e := range evs {
			if e.Kind == EvAudioOut {
				audio += e.Bytes
			}
		}
		if audio != 3*64 {
			t.Errorf("端侧补偿须交付完整音频（不拼半句/不缺块）：bytes=%d want %d", audio, 3*64)
		}
		if n := countKind(evs, EvSpeakDone); n != 1 || p.Speaking() {
			t.Errorf("降级播报须收口：SpeakDone=%d speaking=%v", n, p.Speaking())
		}
		ms := p.router.Metrics()
		if len(ms) == 0 || ms[len(ms)-1].Channel != "degraded" {
			t.Errorf("路由决策须记 degraded 通道，got %+v", ms)
		}
		if p.State() != turntaking.StIdle {
			t.Errorf("收口后须回 Idle，state=%v", p.State())
		}
	})
	t.Run("全通道失败零音频收口", func(t *testing.T) {
		cloud := newStubSynth(3, 64)
		edge := newStubSynth(3, 64)
		cloud.setSynthErr(errors.New("stub: cloud 5xx"))
		edge.setSynthErr(errors.New("stub: edge down"))
		p, err := Wire(testConfig(ResponderFunc(func(Turn) (string, error) { return "试试", nil }),
			tts.RouterConfig{PreSpeak: allowAllPreSpeak, Cloud: cloud, Edge: edge}, 1))
		if err != nil {
			t.Fatalf("Wire: %v", err)
		}
		var evs []Event
		evs = append(evs, pushWake(p, 0)...)
		evs = append(evs, pushUserTurn(p, 130)...)
		evs = append(evs, pumpToDone(p, 4)...)

		if _, ok := findDegrade(evs, DegradeTTSNoAudio); !ok {
			t.Fatalf("全通道失败须 Degrade(TTSNoAudio)，events=%v", sigs(evs))
		}
		if n := countKind(evs, EvSpeakStart); n != 0 {
			t.Errorf("合成失败不得开播，SpeakStart=%d", n)
		}
		if p.Speaking() || p.State() != turntaking.StIdle {
			t.Errorf("失败后须无挂起回 Idle：speaking=%v state=%v", p.Speaking(), p.State())
		}
		// 恢复期望（故障矩阵「下轮回云档」）：故障清除后下轮正常
		cloud.setSynthErr(nil)
		edge.setSynthErr(nil)
		var evs2 []Event
		evs2 = append(evs2, pushWake(p, 2000)...)
		evs2 = append(evs2, pushUserTurn(p, 2130)...)
		evs2 = append(evs2, pumpToDone(p, 8)...)
		if n := countKind(evs2, EvSpeakDone); n != 1 {
			t.Errorf("恢复后下轮须正常播报收口（每请求独立重试云），SpeakDone=%d", n)
		}
	})
}

// TestFaultCH03SpeakOverrun CH-03（输出超长/死循环文本，G1）双防线：
//   - 防线一：合成输入有界——句界硬截断+自然收尾（≤MaxTextRunes rune）；
//   - 防线二：播报输出有界——单次播报字节上限（超出即截断收口，绝不无限播）。
func TestFaultCH03SpeakOverrun(t *testing.T) {
	t.Run("防线一文本截断自然收尾", func(t *testing.T) {
		synth := newStubSynth(3, 64)
		longText := strings.Repeat("句子。", 8) // 24 rune > 上限 20
		cfg := testConfig(ResponderFunc(func(Turn) (string, error) { return longText, nil }),
			tts.RouterConfig{PreSpeak: allowAllPreSpeak, Edge: synth}, 2)
		cfg.MaxTextRunes = 20
		p, err := Wire(cfg)
		if err != nil {
			t.Fatalf("Wire: %v", err)
		}
		var evs []Event
		evs = append(evs, pushWake(p, 0)...)
		evs = append(evs, pushUserTurn(p, 130)...)
		evs = append(evs, pumpToDone(p, 8)...)

		d, ok := findDegrade(evs, DegradeSpeakOverrun)
		if !ok {
			t.Fatalf("超长文本须发 Degrade(SpeakOverrun)，events=%v", sigs(evs))
		}
		got := synth.textAt(0)
		if n := utf8.RuneCountInString(got); n > 20 {
			t.Errorf("截断后文本须 ≤20 rune，got %d", n)
		}
		if d.Bytes != utf8.RuneCountInString(got) {
			t.Errorf("Degrade.Bytes 须=截断后 rune 数：%d vs %d", d.Bytes, utf8.RuneCountInString(got))
		}
		if !strings.HasSuffix(got, overrunClosing) {
			t.Errorf("截断须以自然收尾话术拼接：%q", got)
		}
		if n := countKind(evs, EvSpeakDone); n != 1 || p.Speaking() {
			t.Errorf("截断播报须收口：SpeakDone=%d speaking=%v", n, p.Speaking())
		}
	})
	t.Run("防线二字节上限截断收口", func(t *testing.T) {
		synth := newStubSynth(3, 64)
		cfg := testConfig(ResponderFunc(func(Turn) (string, error) { return "正常长度回复", nil }),
			tts.RouterConfig{PreSpeak: allowAllPreSpeak, Edge: synth}, 2)
		cfg.MaxSpeakBytes = 100 // 首块 64 可出，第二块起超限
		p, err := Wire(cfg)
		if err != nil {
			t.Fatalf("Wire: %v", err)
		}
		var evs []Event
		evs = append(evs, pushWake(p, 0)...)
		evs = append(evs, pushUserTurn(p, 130)...)
		evs = append(evs, p.PumpSpeak()...) // 首块 64B 出
		evs = append(evs, p.PumpSpeak()...) // 64+64>100 → 截断收口

		if n := countKind(evs, EvAudioOut); n != 1 {
			t.Errorf("字节上限须在超限块起截断，AudioOut=%d want 1", n)
		}
		d, ok := findDegrade(evs, DegradeSpeakOverrun)
		if !ok || d.Bytes != 64 {
			t.Errorf("防线二须发 Degrade(SpeakOverrun, Bytes=已交付 64)：got %+v ok=%v", d, ok)
		}
		if n := countKind(evs, EvSpeakDone); n != 1 {
			t.Errorf("截断须收口（绝不 hang），SpeakDone=%d", n)
		}
		if synth.cancelCount() != 1 {
			t.Errorf("截断须终止流（Cancel），cancels=%d", synth.cancelCount())
		}
		if p.State() != turntaking.StIdle || p.Speaking() {
			t.Errorf("截断后须回 Idle：state=%v speaking=%v", p.State(), p.Speaking())
		}
	})
}

// ---- 确定性属性（testing/quick；Tier=2 端侧同步通道——无 goroutine 无墙钟）----

// scriptOp 随机脚本操作（frame=推帧+vad=推 VAD+pump=泵音频；dt=输入逻辑时刻增量）。
type scriptOp struct {
	kind int // 0=frame 1=vad 2=pump
	conf float32
	vad  turntaking.VADEventKind
	dt   int64
}

// randScript 种子化随机脚本（任意调用序——含病态序：Listening 中推帧、
// Speaking 中推 VAD、空闲泵音频等）。
func randScript(seed int64, n int) []scriptOp {
	if n < 10 {
		n = 10
	}
	if n > 60 {
		n = 60
	}
	r := rand.New(rand.NewSource(seed))
	ops := make([]scriptOp, n)
	for i := range ops {
		ops[i].dt = int64(1 + r.Intn(300))
		switch k := r.Intn(100); {
		case k < 40: // 40% 推帧（置信度均匀——随机唤醒/防抖/不应期路径）
			ops[i].kind = 0
			ops[i].conf = r.Float32()
		case k < 65: // 25% 推 VAD（三类事件均匀）
			ops[i].kind = 1
			switch r.Intn(3) {
			case 0:
				ops[i].vad = turntaking.EvNone
			case 1:
				ops[i].vad = turntaking.EvVoiceStart
			default:
				ops[i].vad = turntaking.EvVoiceEnd
			}
		default: // 35% 泵音频（随机节奏——含收口后空闲泵）
			ops[i].kind = 2
		}
	}
	return ops
}

// applyScript 顺序执行脚本收集事件（输入逻辑时刻单调推进）。
func applyScript(p *Pipeline, ops []scriptOp) []Event {
	var out []Event
	ts := int64(0)
	for _, op := range ops {
		switch op.kind {
		case 0:
			ts += op.dt
			out = append(out, p.PushAudioFrame(kws.Frame{TS: ts, Feats: []float32{op.conf}})...)
		case 1:
			ts += op.dt
			out = append(out, p.PushVAD(turntaking.VADEvent{Kind: op.vad, AtMs: ts})...)
		case 2:
			out = append(out, p.PumpSpeak()...)
		}
	}
	return out
}

// propResponder 确定性回复桩：每 3 话轮断连一次（CH-01 路径随机覆盖），
// 文本轮次编号派生——同脚本重放行为全等（P1 前提）。
func propResponder() Responder {
	return ResponderFunc(func(tu Turn) (string, error) {
		n, err := strconv.Atoi(strings.TrimPrefix(tu.ID, "turn-"))
		if err != nil {
			n = 0
		}
		if n%3 == 0 {
			return "", errResponderDown
		}
		return fmt.Sprintf("第%d轮的回复", n), nil
	})
}

// propPipeline 属性测试管道（Tier=2 端侧同步通道；共享合成器桩——流数据固定）。
func propPipeline(t *testing.T) (*Pipeline, *stubSynth) {
	t.Helper()
	synth := newStubSynth(3, 64)
	p, err := Wire(testConfig(propResponder(),
		tts.RouterConfig{PreSpeak: allowAllPreSpeak, Edge: synth}, 2))
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	return p, synth
}

// eventsEqual 事件序列逐字段全等（Err 为固定哨兵——可比）。
func eventsEqual(a, b []Event) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// drainBound 排水上限：每话轮流 ≤3 块+1 终态泵，话轮数 ≤ 脚本长——宽松上界。
func drainBound(n int) int { return (n+1)*5 + 16 }

// TestPropertyReplayDeterminism P1：同脚本重放两次，事件序列逐字段全等
// （回放可复现——事件不携带墙钟的设计约束验证）。
func TestPropertyReplayDeterminism(t *testing.T) {
	prop := func(seed int64, n uint8) bool {
		script := randScript(seed, int(n))
		run := func() []Event {
			p, _ := propPipeline(t)
			return applyScript(p, script)
		}
		return eventsEqual(run(), run())
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 60}); err != nil {
		t.Errorf("P1 重放确定性失效: %v", err)
	}
}

// TestPropertyBoundedTermination P2：任意脚本后，进行中播报在有界泵数内必然
// 收口（绝不 hang）；收口后泵=零事件、FSM 与流引用一致（无 Speaking 悬挂）。
func TestPropertyBoundedTermination(t *testing.T) {
	prop := func(seed int64, n uint8) bool {
		script := randScript(seed, int(n))
		p, _ := propPipeline(t)
		_ = applyScript(p, script)
		bound := drainBound(len(script))
		pumps := 0
		for p.Speaking() && pumps < bound {
			p.PumpSpeak()
			pumps++
		}
		if p.Speaking() { // 有界泵数内未收口=挂起
			return false
		}
		if evs := p.PumpSpeak(); evs != nil { // 收口后零事件（不重播半句）
			return false
		}
		// FSM Speaking ⟺ 有进行中流（漏收口=永久 Speaking，CI-4「绝不 hang」）
		return p.Speaking() == (p.State() == turntaking.StSpeaking)
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 60}); err != nil {
		t.Errorf("P2 有界终止失效: %v", err)
	}
}

// TestPropertyEventInvariants P3：任意脚本+排水后事件不变量——时刻非回退、
// 降级原因 ∈ 预定义集、播报窗内 Seq 严格单调、单窗交付字节 ≤ MaxSpeakBytes、
// AudioOut 必在 SpeakStart 窗内、SpeakDone/Interrupt 关窗。
func TestPropertyEventInvariants(t *testing.T) {
	prop := func(seed int64, n uint8) bool {
		script := randScript(seed, int(n))
		p, _ := propPipeline(t)
		evs := applyScript(p, script)
		for i := 0; p.Speaking() && i < drainBound(len(script)); i++ {
			evs = append(evs, p.PumpSpeak()...)
		}
		var lastAt int64 = math.MinInt64
		inSpeak := false
		turnBytes, lastSeq := 0, -1
		for _, e := range evs {
			if e.AtMs < lastAt { // 单调输入下事件时刻非回退
				return false
			}
			lastAt = e.AtMs
			switch e.Kind {
			case EvSpeakStart:
				if inSpeak {
					return false
				}
				inSpeak, turnBytes, lastSeq = true, 0, -1
			case EvAudioOut:
				if !inSpeak || e.Seq <= lastSeq || e.Bytes < 0 {
					return false
				}
				lastSeq = e.Seq
				turnBytes += e.Bytes
				if turnBytes > p.cfg.MaxSpeakBytes {
					return false
				}
			case EvInterrupt:
				inSpeak = false
			case EvSpeakDone:
				if !inSpeak {
					return false
				}
				inSpeak = false
			case EvDegrade:
				if e.Reason == DegradeNone || int8(e.Reason) > int8(DegradeSpeakIntercepted) {
					return false // 降级行为 ∈ 预定义集
				}
			case EvNone:
				return false
			}
		}
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 60}); err != nil {
		t.Errorf("P3 事件不变量失效: %v", err)
	}
}

// ---- 旅程映射（golden-journeys 前 3 条 → 组件级脚本）----

// journeyFile golden-journey YAML 子集（映射只消费 id/tier/steps/inject/assertions）。
type journeyFile struct {
	ID     string   `yaml:"id"`
	Tier   string   `yaml:"tier"`
	Steps  []string `yaml:"steps"`
	Inject struct {
		Interrupts   []string `yaml:"interrupts"`
		SafetyEvents []string `yaml:"safety_events"`
	} `yaml:"inject"`
	Assertions []journeyAssertion `yaml:"assertions"`
}

type journeyAssertion struct {
	Metric string  `yaml:"metric"`
	Op     string  `yaml:"op"`
	Value  float64 `yaml:"value"`
}

// loadJourney 读旅程文件（评测面只读；go test cwd=包目录）。
func loadJourney(t *testing.T, name string) journeyFile {
	t.Helper()
	path := filepath.Join("..", "..", "..", "tests", "golden-journeys", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读旅程文件 %s: %v", path, err)
	}
	var j journeyFile
	if err := yaml.Unmarshal(data, &j); err != nil {
		t.Fatalf("解析旅程 %s: %v", name, err)
	}
	if j.ID == "" || len(j.Steps) == 0 {
		t.Fatalf("旅程 %s 缺 id/steps", name)
	}
	return j
}

// tierOf 旅程 persona.runtime_tier（L0–L3）→ 管道 Tier（0–3）。
func tierOf(s string) int {
	switch strings.ToUpper(s) {
	case "L0":
		return 0
	case "L1":
		return 1
	case "L2":
		return 2
	case "L3":
		return 3
	}
	return 1
}

// p95 逻辑延迟 P95（分量 int64；空集=0）。
func p95(v []int64) int64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]int64{}, v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := int(math.Ceil(0.95*float64(len(s)))) - 1
	if idx < 0 {
		idx = 0
	}
	return s[idx]
}

// TestGoldenJourneyMapping 旅程映射（spec §8.3 PR 级冒烟=核心旅程；M1 组件级
// 口径）：J01–J03 每步映射为一次完整话轮（唤醒→语音→尾静音收口→播报收口），
// persona.runtime_tier→管道 Tier（云通道即时桩）；yaml 断言按组件级口径评估：
//
//	completion_rate   = SpeakDone 数 / 步数（每步一完整话轮）
//	latency_p95_ms    = TurnEnd→SpeakStart 逻辑延迟 P95（同步组装器恒 0；实测归 M2）
//	safety_events     = 安全拦截降级计数（旅程无注入 → 0）
func TestGoldenJourneyMapping(t *testing.T) {
	for _, name := range []string{"J01-morning.yaml", "J02-goodnight.yaml", "J03-story.yaml"} {
		t.Run(name, func(t *testing.T) {
			j := loadJourney(t, name)
			if j.Tier != "core" {
				t.Fatalf("PR 级冒烟取 core 旅程，got tier=%q", j.Tier)
			}
			if len(j.Inject.Interrupts) > 0 || len(j.Inject.SafetyEvents) > 0 {
				t.Skipf("旅程 %s 带注入（interrupts=%v safety=%v）：注入驱动归 M2 runtime driver",
					j.ID, j.Inject.Interrupts, j.Inject.SafetyEvents)
			}
			cloud := newStubSynth(3, 64) // 云通道即时桩（首包预算放宽——映射面不测超时）
			cfg := testConfig(ResponderFunc(func(Turn) (string, error) { return "旅程回复", nil }),
				tts.RouterConfig{PreSpeak: allowAllPreSpeak, Cloud: cloud,
					FirstPacketTimeoutMs: 5000}, tierOf("L1"))
			p, err := Wire(cfg)
			if err != nil {
				t.Fatalf("Wire: %v", err)
			}
			var evs []Event
			var latencies []int64
			var pendingTurnEnd int64 = -1
			for i := range j.Steps {
				base := int64(i) * 1500
				var stepEvs []Event
				stepEvs = append(stepEvs, pushWake(p, base)...)
				stepEvs = append(stepEvs, pushUserTurn(p, base+130)...)
				stepEvs = append(stepEvs, pumpToDone(p, 8)...)
				for _, e := range stepEvs {
					switch e.Kind {
					case EvTurnEnd:
						pendingTurnEnd = e.AtMs
					case EvSpeakStart:
						if pendingTurnEnd >= 0 {
							latencies = append(latencies, e.AtMs-pendingTurnEnd)
							pendingTurnEnd = -1
						}
					}
				}
				evs = append(evs, stepEvs...)
			}

			completion := float64(countKind(evs, EvSpeakDone)) / float64(len(j.Steps))
			safety := 0
			for _, e := range evs {
				if e.Kind == EvDegrade && e.Reason == DegradeSpeakIntercepted {
					safety++
				}
			}
			metrics := map[string]float64{
				"completion_rate": completion,
				"latency_p95_ms":  float64(p95(latencies)),
				"safety_events":   float64(safety),
			}
			t.Logf("旅程 %s（%d 步）：completion=%.2f latency_p95=%dms safety=%d 事件数=%d",
				j.ID, len(j.Steps), completion, p95(latencies), safety, len(evs))

			// 干净旅程不变量：零打断、零降级、收口回 Idle
			if n := countKind(evs, EvInterrupt); n != 0 {
				t.Errorf("无注入旅程不得出现打断，got %d", n)
			}
			if n := countKind(evs, EvDegrade); n != 0 {
				t.Errorf("无注入旅程不得出现降级，got %d", n)
			}
			if p.State() != turntaking.StIdle || p.Speaking() {
				t.Errorf("旅程结束须收口回 Idle：state=%v speaking=%v", p.State(), p.Speaking())
			}

			// yaml 断言评估（阈值唯一来源=旅程文件本体）
			for _, a := range j.Assertions {
				v, ok := metrics[a.Metric]
				if !ok {
					t.Fatalf("旅程 %s 断言指标 %q 无组件级映射", j.ID, a.Metric)
				}
				pass := false
				switch a.Op {
				case ">=":
					pass = v >= a.Value
				case "<=":
					pass = v <= a.Value
				default:
					t.Fatalf("旅程 %s 断言算子 %q 未支持", j.ID, a.Op)
				}
				if !pass {
					t.Errorf("旅程 %s 断言失败：%s=%.4f 须 %s %.4f（组件级口径）",
						j.ID, a.Metric, v, a.Op, a.Value)
				}
			}
		})
	}
}

// ---- 辅助 ----

func sigsEqual(a, b []sig) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
