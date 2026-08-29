// 分段延迟观测测试（IR #95 / docs/m2-spec.md §9——测试先行：先定报告
// schema 与分段来源契约，再落 latency.go 实现）。
//
// 契约面（budgets 真消费的输入约束，tools/budgets check.go Evaluate 语义）：
//
//  1. LatencyReport() 五段 id 与 configs/budgets/latency.yaml 逐一对齐
//     （顺序与集合；缺一/多一段即 budgets InputError exit 2），JSON 字段名
//     与 budgets.LatencyReport schema 兼容（commit/timestamp/overlap_ms/
//     segments[{id,p50,p95}]——落盘方填 commit/timestamp）。
//  2. tail_silence = EvVoiceEnd→ActTurnEnd 逻辑间隔（真实逻辑值：端点判定
//     尾静音等待=FSM SilenceMs 门限逐轮镜像）。
//  3. 同步组装器段（asr_uplink/cloud_llm/tts_first/transport）逻辑口径=0
//     （构造保证——同步管道无逻辑推进，T7-G1-04 同款先例）；墙钟口径为
//     补充观测面（LatencyWallReport，样本逐轮对齐、值非负随宿主）。
//  4. 观测旁路不入侵事件流（Event 不携带墙钟的禁律不动——P1 重放确定性
//     属性保持：既有 M1 事件序列断言即零入侵回归）。
package loop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Cloudbird-Software/AI_Toy/packages/go/tts"
	"gopkg.in/yaml.v3"
)

// budgetsSegments 从 configs/budgets/latency.yaml 读声明段 id 序（预算基准
// 只读——段集合是 budgets check 的守恒输入面）。
func budgetsSegments(t *testing.T) []string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "configs", "budgets", "latency.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读延迟预算基准 %s: %v", path, err)
	}
	var cfg struct {
		Segments []struct {
			ID string `yaml:"id"`
		} `yaml:"segments"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("解析 %s: %v", path, err)
	}
	if len(cfg.Segments) == 0 {
		t.Fatalf("latency.yaml segments 为空——预算基准缺失")
	}
	ids := make([]string, len(cfg.Segments))
	for i, s := range cfg.Segments {
		ids[i] = s.ID
	}
	return ids
}

// smokePipeline 冒烟管道（Tier=2 端侧同步通道——确定性属性同款配置）跑
// n 轮「唤醒→对话→出声」全链路。
func smokePipeline(t *testing.T, n int) *Pipeline {
	t.Helper()
	p := wireSmoke(t)
	for i := 0; i < n; i++ {
		base := int64(i) * 2000
		pushWake(p, base)
		pushUserTurn(p, base+130)
		pumpToDone(p, 8)
	}
	return p
}

// wireSmoke 冒烟管道装配（端侧即时桩——loop_test.go M1 冒烟同款配置）。
func wireSmoke(t *testing.T) *Pipeline {
	t.Helper()
	synth := newStubSynth(3, 64)
	p, err := Wire(testConfig(ResponderFunc(func(Turn) (string, error) { return "早上好呀！", nil }),
		tts.RouterConfig{PreSpeak: allowAllPreSpeak, Edge: synth}, 2))
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	return p
}

// TestLatencyReportMatchesBudgetsSchema 契约 1：五段 id 与 latency.yaml 对齐
// （顺序+集合）、overlap_ms=0、JSON 字段名兼容 budgets schema。
func TestLatencyReportMatchesBudgetsSchema(t *testing.T) {
	p := smokePipeline(t, 3)
	rep := p.LatencyReport()

	want := budgetsSegments(t)
	if len(rep.Segments) != len(want) {
		t.Fatalf("报告段数 %d ≠ 预算基准 %d（%v）——budgets check 将 InputError", len(rep.Segments), len(want), want)
	}
	for i, s := range rep.Segments {
		if s.ID != want[i] {
			t.Errorf("段序[%d]=%q ≠ latency.yaml 声明 %q（段集合/顺序须逐一对齐）", i, s.ID, want[i])
		}
		if s.P50 < 0 || s.P95 < 0 {
			t.Errorf("段 %s 负值（p50=%v p95=%v）——budgets 要求 p95 非负", s.ID, s.P50, s.P95)
		}
	}
	if rep.OverlapMS != 0 {
		t.Errorf("overlap_ms=%v ≠ 0（保守口径：旁路并行段不计入扣减，m2-spec §9）", rep.OverlapMS)
	}

	// JSON 字段名兼容 budgets.LatencyReport（loadReport 严格按 tag 解析）。
	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, k := range []string{"commit", "timestamp", "overlap_ms", "segments"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("报告 JSON 缺键 %q——budgets.LatencyReport schema 不兼容", k)
		}
	}
	segs, _ := raw["segments"].([]any)
	if len(segs) != len(want) {
		t.Fatalf("segments 数组长度 %d ≠ %d", len(segs), len(want))
	}
	for i, s := range segs {
		m, _ := s.(map[string]any)
		for _, k := range []string{"id", "p50", "p95"} {
			if _, ok := m[k]; !ok {
				t.Errorf("segments[%d] 缺键 %q", i, k)
			}
		}
	}
}

// TestLatencyTailSilenceSamples 契约 2：tail_silence=EvVoiceEnd→ActTurnEnd
// 逻辑间隔——冒烟配置（SilenceMs=500）下逐轮样本恒 500（端点判定尾静音等待
// 的真实逻辑值，非墙钟测量）。
func TestLatencyTailSilenceSamples(t *testing.T) {
	p := smokePipeline(t, 3)
	samples := p.lat.logic[SegTailSilence]
	if len(samples) != 3 {
		t.Fatalf("tail_silence 样本数 %d ≠ 3（每完整话轮恰一个样本）", len(samples))
	}
	for i, v := range samples {
		if v != float64(tSilenceMs) {
			t.Errorf("tail_silence 样本[%d]=%v ≠ %d（EvVoiceEnd→ActTurnEnd 逻辑间隔=FSM 尾静音门限镜像）", i, v, tSilenceMs)
		}
	}
	rep := p.LatencyReport()
	stat := statOf(t, rep, SegTailSilence)
	if stat.P50 != float64(tSilenceMs) || stat.P95 != float64(tSilenceMs) {
		t.Errorf("tail_silence p50/p95=%v/%v ≠ %d/%d", stat.P50, stat.P95, tSilenceMs, tSilenceMs)
	}
}

// TestLatencySyncSegmentsLogicZero 契约 3：同步组装器段逻辑口径=0（构造保证
// ——Responder/Synthesize/泵同步执行无逻辑推进）；墙钟口径补充观测面逐轮
// 对齐（样本数=话轮数、值非负随宿主）。
func TestLatencySyncSegmentsLogicZero(t *testing.T) {
	const n = 3
	p := smokePipeline(t, n)
	rep := p.LatencyReport()
	for _, id := range []string{SegASRUplink, SegCloudLLM, SegTTSFirst, SegTransport} {
		stat := statOf(t, rep, id)
		if stat.P50 != 0 || stat.P95 != 0 {
			t.Errorf("段 %s 逻辑口径 p50/p95=%v/%v ≠ 0（同步管道无逻辑推进——构造保证；墙钟口径走 LatencyWallReport）", id, stat.P50, stat.P95)
		}
		if got := len(p.lat.logic[id]); got != n {
			t.Errorf("段 %s 逻辑样本数 %d ≠ %d（每完整话轮恰一个样本——空段=无观测轮，非本轮契约）", id, got, n)
		}
	}
	// 墙钟补充观测：样本数对齐（stub 真实执行耗时，数字随宿主只断非负）。
	wall := p.LatencyWallReport()
	for _, id := range []string{SegCloudLLM, SegTTSFirst} {
		if got := len(p.lat.wall[id]); got != n {
			t.Errorf("段 %s 墙钟样本数 %d ≠ %d（补充观测面逐轮对齐）", id, got, n)
		}
		stat := statOf(t, wall, id)
		if stat.P95 < 0 {
			t.Errorf("段 %s 墙钟 p95=%v < 0", id, stat.P95)
		}
	}
}

// TestLatencyObservationNonInvasive 契约 4：埋点后事件流与 M1 期望序列全等
// （旁路观测零入侵——Event 不携带墙钟、P1 重放确定性保持；M1 全量事件面
// 由 loop_test.go 既有断言回归）。
func TestLatencyObservationNonInvasive(t *testing.T) {
	p := wireSmoke(t)
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
		t.Errorf("埋点后事件序列漂移（旁路观测须零入侵）：\n got  %v\n want %v", got, want)
	}
	if p.LatencyReport().Segments == nil {
		t.Errorf("LatencyReport 须产非空 segments")
	}
}

// TestLatencyInterruptTurnPartialSamples 打断/无首块轮的部分样本面：tail_silence
// 已采、tts_first 仅在首块 EvAudioOut 交付后采——统计不 panic、样本数有界。
func TestLatencyInterruptTurnPartialSamples(t *testing.T) {
	p := wireSmoke(t)
	pushWake(p, 0)
	pushUserTurn(p, 130) // SpeakStart 已发、未泵——无首块
	pushWake(p, 2000)
	pushUserTurn(p, 2130)
	pumpToDone(p, 8) // 第二轮完整出声

	if got := len(p.lat.logic[SegTailSilence]); got != 2 {
		t.Errorf("tail_silence 样本数 %d ≠ 2（两轮话轮终点各一）", got)
	}
	if got := len(p.lat.logic[SegTTSFirst]); got != 1 {
		t.Errorf("tts_first 样本数 %d ≠ 1（仅首块 EvAudioOut 交付轮采——打断轮无首块不采）", got)
	}
	rep := p.LatencyReport() // 部分样本段统计须可产（不 panic）
	if len(rep.Segments) != len(latencySegmentOrder) {
		t.Errorf("报告段数 %d ≠ %d", len(rep.Segments), len(latencySegmentOrder))
	}
}

// statOf 取报告内单段统计。
func statOf(t *testing.T, rep LatencyReportDoc, id string) SegmentStat {
	t.Helper()
	for _, s := range rep.Segments {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("报告缺段 %s", id)
	return SegmentStat{}
}
