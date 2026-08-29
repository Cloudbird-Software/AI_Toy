// Package tts 实现 T13 两级流式合成路由（docs/m1-spec.md §4 契约 C）：
//
//	PreSpeak（T9 拦截，fail-closed）→ PhraseCache（预注册短语命中=零合成延迟）→
//	按档选通道（L0/L1 云、L2 端、L3 仅缓存），云首包超时降级——静默占位
//	≤SilenceCapMs → Edge 全新补偿重合成（不拼半句、不重播半句）；每请求
//	独立尝试云（下轮回云档）。首包预算（DeadlineMs/FirstPacketMs）M1 只记不判，
//	供 configs/budgets 消费。
//
// 依赖纪律：import 白名单=标准库；ONNX/网络客户端一律接口化注入（M1 桩）。
package tts

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// Request 合成请求（契约签名照抄 spec §4）。
type Request struct {
	Text, Voice string // 待合成文本（空→ErrEmptyText）/音色 ID（角色声资产，空=默认）
	TurnID      string // 话轮幂等键（打断/不重播半句）
	Tier        int    // T14 档 0..3（越界→error）
	DeadlineMs  int    // 首包预算：云 300/端 150（T13-G1-01；M1 只记不判）
}

// Chunk 流式音频块。
type Chunk struct {
	Data  []byte
	Seq   int
	Final bool
}

// AudioStream 流式音频：Next io.EOF=流尽；err 后流终止（重试=重播半句风险，
// 禁止——实现保证终止态固化）；Cancel=打断执行面（幂等）。
type AudioStream interface {
	Next() (Chunk, error)
	Cancel() error
}

// Synthesizer 云/端/桩同接口的合成引擎。
type Synthesizer interface {
	Synthesize(req Request) (AudioStream, error)
}

// PhraseCache 预注册短语表：命中=零合成延迟出流；入缓存前提=已过 T9
// （Router 决策序保证每次取用仍先过 PreSpeak——「缓存短语同样过 T9」）。
type PhraseCache interface {
	Get(text, voice string) (AudioStream, bool)
	Put(text, voice string, s AudioStream)
}

// PreSpeakFunc T9 拦截钩子：err≠nil→拒绝合成（读出=0）。
type PreSpeakFunc func(text string) error

// TierCaps T14 档位通道镜像（镜像 RuntimeModel.TierCaps 语义，不 import tests/）。
type TierCaps interface {
	TTSChannel(tier int) (cloud, edge, cache bool)
}

// 错误集（哨兵，errors.Is 判定）。
var (
	ErrEmptyText   = errors.New("tts: empty text")
	ErrIntercepted = errors.New("tts: text intercepted by pre-speak safety hook")
	ErrNoChannel   = errors.New("tts: no tts channel available for tier")
	ErrTimeout     = errors.New("tts: first packet timeout")
	ErrCanceled    = errors.New("tts: stream canceled")
)

// 默认值（spec §4：FirstPacketTimeoutMs=300 云首包门禁线；SilenceCapMs=2000
// 故障矩阵「静默≤2s」）。
const (
	DefaultFirstPacketTimeoutMs = 300
	DefaultSilenceCapMs         = 2000

	tierMin = 0
	tierMax = 3
)

// RouterConfig 路由配置（契约签名照抄 spec §4）。
type RouterConfig struct {
	PreSpeak             PreSpeakFunc // T9 钩子；nil→NewRouter error（生产禁裸奔；测试显式注入）
	Cloud, Edge          Synthesizer  // 云端流式（L0/L1）/端侧引擎（L2+降级补偿，Edge 可 nil）
	Cache                PhraseCache  // 预合成短语（L3 档+各档加速）
	Caps                 TierCaps     // nil=默认镜像 configs/runtime/tiers.yaml
	FirstPacketTimeoutMs int          // 默认 300（云首包门禁线）
	SilenceCapMs         int          // 默认 2000（静默占位上限）
}

// defaultCaps 档位通道默认表，镜像 configs/runtime/tiers.yaml：
// L0/L1 tts=cloud_stream（edge 位=降级补偿通道）、L2 tts=piper、
// L3 tts=piper_cached（仅缓存）。
type defaultCaps struct{}

func (defaultCaps) TTSChannel(tier int) (cloud, edge, cache bool) {
	switch tier {
	case 0, 1:
		return true, true, true
	case 2:
		return false, true, true
	default: // 3
		return false, false, true
	}
}

// Metric 单次合成的路由决策与首包元数据（M1 只记不判——首包预算的观测面，
// 供 configs/budgets（tts_first P95=280ms 见 latency.yaml）消费；实测判定归
// M2 真机）。
type Metric struct {
	TurnID        string `json:"turn_id"`
	Tier          int    `json:"tier"`
	Voice         string `json:"voice"`
	Channel       string `json:"channel"`         // cache | cloud | edge | degraded
	FirstPacketMs int64  `json:"first_packet_ms"` // Synthesize 进入→首 Chunk 交付墙钟
	DeadlineMs    int    `json:"deadline_ms"`     // 请求首包预算（镜像记录）
	Partial       bool   `json:"partial"`         // 流中途失败（已播 Seq 不回退，不重播半句）
	LastSeq       int    `json:"last_seq"`        // 已交付最大 Seq（partial 快照）
}

// metricRec 内部可变记录（流交付/终止线程更新；Router.Metrics 快照读）。
type metricRec struct {
	mu       sync.Mutex
	m        Metric
	firstSet bool
}

func (r *metricRec) setChannel(ch string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m.Channel = ch
}

func (r *metricRec) setFirstPacket(ms int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.firstSet {
		r.firstSet = true
		r.m.FirstPacketMs = ms
	}
}

func (r *metricRec) finish(partial bool, lastSeq int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m.Partial, r.m.LastSeq = partial, lastSeq
}

func (r *metricRec) snapshot() Metric {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.m
}

// Router 两级流式合成路由（决策序见 Synthesize）。
type Router struct {
	cfg RouterConfig

	mu      sync.Mutex
	active  map[string]AudioStream // turnID → 活跃流（Cancel 打断面）
	metrics []*metricRec
}

// NewRouter 校验并组装路由：PreSpeak nil→error（fail-closed：生产禁裸奔，
// 测试显式注入放行/拦截桩）；超时参数 ≤0 取默认（300/2000）。
func NewRouter(cfg RouterConfig) (*Router, error) {
	if cfg.PreSpeak == nil {
		return nil, errors.New("tts: PreSpeak must be non-nil（fail-closed：T9 拦截钩子必接，测试显式注入）")
	}
	if cfg.Caps == nil {
		cfg.Caps = defaultCaps{}
	}
	if cfg.FirstPacketTimeoutMs <= 0 {
		cfg.FirstPacketTimeoutMs = DefaultFirstPacketTimeoutMs
	}
	if cfg.SilenceCapMs <= 0 {
		cfg.SilenceCapMs = DefaultSilenceCapMs
	}
	return &Router{cfg: cfg, active: map[string]AudioStream{}}, nil
}

// Synthesize 合成决策序（spec §4）：
// ① PreSpeak 拒→ErrIntercepted（读出=0，先于一切通道）
// ② Cache 命中→直返（零合成延迟）
// ③ 按档选通道：L0/L1→Cloud、L2→Edge、L3→仅 Cache（未命中→ErrNoChannel）
// ④ 云首包>FirstPacketTimeoutMs→降级行为表（静默占位≤SilenceCapMs→Edge
//
//	全新补偿重合成；Edge=nil→ErrTimeout；不重播半句；每请求独立重试云）。
func (r *Router) Synthesize(req Request) (AudioStream, error) {
	start := time.Now()
	if req.Tier < tierMin || req.Tier > tierMax {
		return nil, fmt.Errorf("tts: tier %d 越界（须 [%d,%d]）", req.Tier, tierMin, tierMax)
	}
	if req.Text == "" {
		return nil, ErrEmptyText
	}
	// ① T9 拦截（fail-closed：拒→0 字节音频，无论缓存/云/端）
	if err := r.cfg.PreSpeak(req.Text); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIntercepted, err)
	}
	rec := &metricRec{m: Metric{
		TurnID: req.TurnID, Tier: req.Tier, Voice: req.Voice, DeadlineMs: req.DeadlineMs,
	}}
	r.track(rec)

	cloud, edge, cacheOK := r.cfg.Caps.TTSChannel(req.Tier)

	// ② 缓存命中→直返（零合成延迟；入缓存前提=已过 T9——本次 PreSpeak 已过）
	if cacheOK && r.cfg.Cache != nil {
		if s, ok := r.cfg.Cache.Get(req.Text, req.Voice); ok {
			rec.setChannel("cache")
			st := newPassStream(r, req, s, nil, start, rec)
			r.register(req.TurnID, st)
			return st, nil
		}
	}

	// ③ 按档选通道
	if cloud {
		return r.synthCloud(req, start, rec)
	}
	if edge {
		if r.cfg.Edge == nil {
			return nil, ErrNoChannel
		}
		s, err := r.cfg.Edge.Synthesize(req)
		if err != nil {
			return nil, err
		}
		rec.setChannel("edge")
		st := newPassStream(r, req, s, nil, start, rec)
		r.register(req.TurnID, st)
		return st, nil
	}
	// L3 无缓存：档位无通道（上层走文字/动作补偿）
	return nil, ErrNoChannel
}

// Cancel 打断执行面（ActStopTTS 对接）：流立即终止（幂等）；同 TurnID 不续播。
func (r *Router) Cancel(turnID string) error {
	r.mu.Lock()
	s := r.active[turnID]
	delete(r.active, turnID)
	r.mu.Unlock()
	if s == nil {
		return nil // 幂等：未知/已终止话轮无操作
	}
	return s.Cancel()
}

// Metrics 返回路由决策与首包元数据快照（M1 只记不判，供 budgets 消费）。
func (r *Router) Metrics() []Metric {
	r.mu.Lock()
	recs := append([]*metricRec{}, r.metrics...)
	r.mu.Unlock()
	out := make([]Metric, 0, len(recs))
	for _, rec := range recs {
		out = append(out, rec.snapshot())
	}
	return out
}

func (r *Router) track(rec *metricRec) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.metrics = append(r.metrics, rec)
}

func (r *Router) register(turnID string, s AudioStream) {
	if turnID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active[turnID] = s
}

func (r *Router) unregister(turnID string, s AudioStream) {
	if turnID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.active[turnID]; ok && cur == s {
		delete(r.active, turnID)
	}
}

// synthCloud 云通道（L0/L1）：首包代理——FirstPacketTimeoutMs 内取云首 chunk；
// 超时或云失败（Synthesize err / 首拉 err，此时 0 字节已出、无半句风险）→
// 降级行为表。
func (r *Router) synthCloud(req Request, start time.Time, rec *metricRec) (AudioStream, error) {
	if r.cfg.Cloud == nil {
		return nil, ErrNoChannel
	}
	stream, err := r.cfg.Cloud.Synthesize(req)
	if err != nil {
		return r.degrade(req, start, rec)
	}
	ch := make(chan firstPacket, 1)
	go func() {
		c, err := stream.Next()
		ch <- firstPacket{c: c, err: err, at: time.Now()}
	}()
	timeout := time.Duration(r.cfg.FirstPacketTimeoutMs) * time.Millisecond
	select {
	case fp := <-ch:
		if fp.err != nil && !errors.Is(fp.err, io.EOF) {
			return r.degrade(req, start, rec)
		}
		rec.setChannel("cloud")
		st := newPassStream(r, req, stream, &fp, start, rec)
		r.register(req.TurnID, st)
		return st, nil
	case <-time.After(timeout):
		_ = stream.Cancel() // 云流终止（幂等；已播=0，无半句风险）
		return r.degrade(req, start, rec)
	}
}

// degrade 云首包失败/超时降级（spec §4 降级表）：静默占位 ≤SilenceCapMs →
// Edge 补偿重合成（全新完整话术，不拼半句）；Edge=nil→ErrTimeout（上层转
// 文字/动作补偿）。
func (r *Router) degrade(req Request, start time.Time, rec *metricRec) (AudioStream, error) {
	if r.cfg.Edge == nil {
		return nil, ErrTimeout
	}
	es, err := r.cfg.Edge.Synthesize(req)
	if err != nil {
		return nil, fmt.Errorf("%w: edge fallback: %v", ErrTimeout, err)
	}
	rec.setChannel("degraded")
	st := newDegradedStream(r, req, es, start, rec, time.Duration(r.cfg.SilenceCapMs)*time.Millisecond)
	r.register(req.TurnID, st)
	return st, nil
}
