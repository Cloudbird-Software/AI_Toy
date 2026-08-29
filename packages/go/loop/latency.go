// 分段延迟观测（IR #95 / docs/m2-spec.md §9 budgets 接线）：Pipeline 运行期
// 采集五段延迟样本 → LatencyReport() 导出 budgets 兼容 schema
// （reports/nightly/latency.json）→ `budgets check` 守恒消费
// （ΣP95−overlap ≤ total_p95_budget，基准 configs/budgets/latency.yaml）。
//
// 分段来源（每段口径写明——报告数字=逻辑时钟口径，事件 AtMs 之差；
// CI 宿主墙钟为补充观测面 LatencyWallReport，同 T7-G1-04「逻辑口径≈0 的
// 构造保证 + CI 墙钟实测」先例；M2 stub 语义由落盘方（latency-smoke）声明）：
//
//	tail_silence = EvVoiceEnd→ActTurnEnd 逻辑间隔——端点判定尾静音等待的真实
//	               逻辑值（FSM SilenceMs 门限逐轮镜像；加速回放下墙钟失真，
//	               逻辑口径承载链路语义）
//	asr_uplink   = 0：M2 桩——ASR 定稿/上行面未建独立面板（TurnEnd 即定稿、
//	               同步直调 Responder，逻辑推进 0，墙钟亦无面板可测）
//	cloud_llm    = Responder 面板耗时（M2=模板+persona Apply 桩）：逻辑口径 0
//	               （同步管道无逻辑推进——构造保证）；墙钟口径=面板真实实测
//	tts_first    = Synthesize 进入（=SpeakStart 逻辑同刻）→首块 EvAudioOut
//	               逻辑间隔（同步泵构造保证 0）；墙钟口径=Synthesize→首块交付
//	               实测（口径对齐 tts.Router.Metric.FirstPacketMs 观测面）
//	transport    = 首块 EvAudioOut→播放启动桩（≈0 恒 0：同步交付即启动）
//	overlap_ms   = 0（保守口径：旁路并行段不计入扣减，m2-spec §9）
//
// 旁路观测不进事件流：Event 不携带墙钟的禁律不动（loop/AGENTS.md），
// P1 重放确定性属性不受影响（观测数据不入 Events——time.Now 只进采样器，
// 同 tts.Router.Metrics 墙钟先例；单流串行驱动契约下不加锁）。
package loop

import (
	"math"
	"sort"
	"time"

	"github.com/Cloudbird-Software/AI_Toy/packages/go/turntaking"
)

// 五段 id——与 configs/budgets/latency.yaml segments[].id 逐一对齐
// （budgets check 的守恒输入面：报告缺一/多一段即 InputError exit 2）。
const (
	SegTailSilence = "tail_silence"
	SegASRUplink   = "asr_uplink"
	SegCloudLLM    = "cloud_llm"
	SegTTSFirst    = "tts_first"
	SegTransport   = "transport"
)

// latencySegmentOrder 报告段序（=latency.yaml 声明序——budgets 按配置序出负债表）。
var latencySegmentOrder = [...]string{SegTailSilence, SegASRUplink, SegCloudLLM, SegTTSFirst, SegTransport}

// SegmentStat 单段延迟统计（budgets.SegmentSample 兼容 schema：id/p50/p95）。
type SegmentStat struct {
	ID  string  `json:"id"`
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
}

// LatencyReportDoc reports/nightly/latency.json 文档（budgets.LatencyReport
// 兼容 schema：commit/timestamp/overlap_ms/segments）。Commit/Timestamp/Note
// 由落盘方填充（Pipeline 无 git/墙钟上下文——本面只产分段数据；Note=stub
// 语义声明面，m2-spec §9「stub 语义在报告 note 声明，数字如实记录」）。
type LatencyReportDoc struct {
	Commit    string        `json:"commit"`
	Timestamp string        `json:"timestamp"`
	OverlapMS float64       `json:"overlap_ms"`
	Segments  []SegmentStat `json:"segments"`
	Note      string        `json:"note,omitempty"` // stub 语义声明（落盘方填；budgets 解析忽略该键）
}

// latencyTracker 分段采样器（旁路观测——不进事件流；单流串行驱动契约下不加锁）。
type latencyTracker struct {
	voiceEndAt   int64     // tail_silence 逻辑起点：最近 EvVoiceEnd（0=未见）
	voiceEndWall time.Time // tail_silence 墙钟起点
	synthPending bool      // tts_first 起点有效标记（Synthesize 已进、首块未出）
	synthAt      int64     // tts_first 逻辑起点：本轮 Synthesize 进入（=SpeakStart 同刻）
	synthWall    time.Time // tts_first 墙钟起点

	logic map[string][]float64 // 逻辑口径样本（ms；key=段 id）
	wall  map[string][]float64 // CI 宿主墙钟口径样本（ms；补充观测面）
}

// newLatencyTracker 采样器零值装配（Wire 调用）。
func newLatencyTracker() latencyTracker {
	return latencyTracker{
		logic: make(map[string][]float64),
		wall:  make(map[string][]float64),
	}
}

// markVoiceEnd 记 tail_silence 起点（EvVoiceEnd 推入时）。
func (lt *latencyTracker) markVoiceEnd(at int64) {
	lt.voiceEndAt, lt.voiceEndWall = at, time.Now()
}

// sampleTailSilence 采 tail_silence 段（ActTurnEnd 产 EvTurnEnd 时）：逻辑差
// =EvVoiceEnd→ActTurnEnd；墙钟差为补充观测（加速回放下≈0，失真口径仅对照）。
func (lt *latencyTracker) sampleTailSilence(turnEndAt int64) {
	if lt.voiceEndAt > 0 && turnEndAt >= lt.voiceEndAt {
		lt.add(SegTailSilence, float64(turnEndAt-lt.voiceEndAt), msSince(lt.voiceEndWall))
	}
	lt.voiceEndAt = 0
}

// sampleResponder 采 Responder 面板段（Respond 调用返回后）：asr_uplink=M2 桩
// （0/0——无独立 ASR/上行面板）；cloud_llm 逻辑 0（同步管道无逻辑推进——构造
// 保证）+墙钟面板实测（M2=模板+persona Apply 桩，数字如实随宿主）。
func (lt *latencyTracker) sampleResponder(panelWall time.Duration) {
	lt.add(SegASRUplink, 0, 0)
	lt.add(SegCloudLLM, 0, panelWall.Seconds()*1000)
}

// markSynth 记 tts_first 起点（Router.Synthesize 进入=SpeakStart 逻辑同刻）。
func (lt *latencyTracker) markSynth(at int64) {
	lt.synthPending, lt.synthAt, lt.synthWall = true, at, time.Now()
}

// sampleFirstChunk 采 tts_first+transport 段（本播报窗首块 EvAudioOut 交付时）：
// tts_first 逻辑=SpeakStart→首块逻辑间隔（同步泵构造保证 0）+墙钟
// Synthesize→首块交付实测；transport=首块→播放启动桩（同步交付即启动，恒 0）。
func (lt *latencyTracker) sampleFirstChunk(firstChunkAt int64) {
	if lt.synthPending {
		lt.add(SegTTSFirst, float64(firstChunkAt-lt.synthAt), msSince(lt.synthWall))
	}
	lt.add(SegTransport, 0, 0)
	lt.synthPending = false
}

// add 追加单段双口径样本（ms）。
func (lt *latencyTracker) add(id string, logicMS, wallMS float64) {
	lt.logic[id] = append(lt.logic[id], logicMS)
	lt.wall[id] = append(lt.wall[id], wallMS)
}

// msSince 墙钟耗时（ms）。
func msSince(t time.Time) float64 { return time.Since(t).Seconds() * 1000 }

// LatencyReport 五段延迟报告（逻辑口径——报告面/守恒消费面）：段序与
// configs/budgets/latency.yaml 对齐；OverlapMS 恒 0（保守口径）。空样本段记 0
// （该段无完整观测轮——如实）；Commit/Timestamp 留空由落盘方填充。
func (p *Pipeline) LatencyReport() LatencyReportDoc { return p.latencyDoc(p.lat.logic) }

// LatencyWallReport 同 schema 的 CI 宿主墙钟口径（补充观测面——冒烟 note 与
// 趋势对照用；M2 stub 数字如实，不代表最终产品延迟）。
func (p *Pipeline) LatencyWallReport() LatencyReportDoc { return p.latencyDoc(p.lat.wall) }

// latencyDoc 按样本集产报告文档（段序=latencySegmentOrder）。
func (p *Pipeline) latencyDoc(samples map[string][]float64) LatencyReportDoc {
	doc := LatencyReportDoc{OverlapMS: 0}
	for _, id := range latencySegmentOrder {
		doc.Segments = append(doc.Segments, SegmentStat{
			ID:  id,
			P50: percentile(samples[id], 0.50),
			P95: percentile(samples[id], 0.95),
		})
	}
	return doc
}

// percentile 顺序统计量（空样本=0；升序取第 ceil(q·n) 位——对齐 loop 旅程
// 映射 p95 口径，budgets 只认 P95 参与守恒）。
func percentile(v []float64, q float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64{}, v...)
	sort.Float64s(s)
	idx := int(math.Ceil(q*float64(len(s)))) - 1
	if idx < 0 {
		idx = 0
	}
	return s[idx]
}

// latencyOnVAD VAD 输入面的采样埋点（PushVAD 入口调用——旁路，不改事件流）。
func (p *Pipeline) latencyOnVAD(ev turntaking.VADEvent) {
	if ev.Kind == turntaking.EvVoiceEnd {
		p.lat.markVoiceEnd(ev.AtMs)
	}
}
