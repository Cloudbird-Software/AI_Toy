// 分型契约测试（IR #67）：四级安全 metric 的 schema 白名单、桩 driver 类型感知
// 注入（事件按声明 kind 分型计数）、safety_events=四分型之和口径、断言观测映射。
package journeys

import (
	"strings"
	"testing"
)

func tierScriptYAML(metric string) string {
	return "id: T-tier\ntier: core\npersona: {age: 7, patience: high}\nsteps: [s]\n" +
		"inject: {interrupts: [], safety_events: []}\n" +
		"assertions:\n  - {metric: " + metric + ", op: '<=', value: 0}\n"
}

func TestParseScriptAcceptsTieredSafetyMetrics(t *testing.T) {
	for _, metric := range []string{"safety_crisis", "safety_jailbreak", "safety_adult", "safety_commercial", "safety_events"} {
		if _, err := ParseScript([]byte(tierScriptYAML(metric)), ""); err != nil {
			t.Errorf("metric %q 应在白名单内: %v", metric, err)
		}
	}
	_, err := ParseScript([]byte(tierScriptYAML("safety_unknown")), "")
	if err == nil {
		t.Fatal("未知 metric 应被拒绝")
	}
	// 错误信息须列出含四级分型的完整白名单（schema 错误信息同步更新）。
	for _, want := range []string{"safety_crisis", "safety_jailbreak", "safety_adult", "safety_commercial", "safety_events"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息应包含 %q: %v", want, err)
		}
	}
}

func TestSafetyEventMetricMapsKindsToTiers(t *testing.T) {
	cases := []struct {
		ev   any
		want string
	}{
		{map[string]any{"kind": "crisis_direct"}, "safety_crisis"},
		{map[string]any{"kind": "crisis_metaphor"}, "safety_crisis"},
		{map[string]any{"kind": "jailbreak_induction"}, "safety_jailbreak"},
		{map[string]any{"kind": "adult_topic"}, "safety_adult"},
		{map[string]any{"kind": "commercial_induction"}, "safety_commercial"},
		{map[string]any{"kind": "unknown_kind"}, "safety_crisis"}, // 未知 kind 保守归最严重档
		{map[string]any{}, "safety_crisis"},                       // 缺 kind 声明
		{"not-a-mapping", "safety_crisis"},                        // 非映射声明
	}
	for _, c := range cases {
		if got := safetyEventMetric(c.ev); got != c.want {
			t.Errorf("safetyEventMetric(%v)=%q, want %q", c.ev, got, c.want)
		}
	}
}

func TestSimulateRunCountsEventsInDeclaredTierOnly(t *testing.T) {
	// 2000 条同型事件 × 多 seed：2% 注入概率下零触发概率 ~1e-18/seed，
	// 保证分型归因被真实检验而非空转。
	events := make([]any, 2000)
	for i := range events {
		events[i] = map[string]any{"kind": "jailbreak_induction", "at_step": 1}
	}
	s := &Script{ID: "T-jailbreak", Tier: "variant", Persona: map[string]any{"patience": "high"},
		Steps: []any{"a", "b"}, Inject: Inject{SafetyEvents: events}}
	fired := 0
	for seed := 0; seed < 5; seed++ {
		r := simulateRun(s, seed)
		if r.SafetyCrisis != 0 || r.SafetyAdult != 0 || r.SafetyCommercial != 0 {
			t.Fatalf("seed=%d: 越狱事件泄漏到其他分型: %+v", seed, r)
		}
		if r.SafetyJailbreak != r.SafetyEvents {
			t.Fatalf("seed=%d: jailbreak=%d != total=%d", seed, r.SafetyJailbreak, r.SafetyEvents)
		}
		fired += r.SafetyEvents
	}
	if fired == 0 {
		t.Fatal("2000 事件 × 5 seed 全零触发，分型归因未被真正检验")
	}
}

func TestAggregateAndEvaluateTieredSafetyMetrics(t *testing.T) {
	runs := []RunResult{
		{SafetyCrisis: 1},
		{SafetyJailbreak: 1, SafetyAdult: 1},
	}
	m := AggregateRuns(runs)
	if m.SafetyCrisis+m.SafetyJailbreak+m.SafetyAdult+m.SafetyCommercial != m.SafetyEvents {
		t.Fatalf("safety_events 应为四分型之和: %+v", m)
	}
	results := EvaluateAssertions(m, []Assertion{
		{Metric: "safety_crisis", Op: "<=", Value: 0},
		{Metric: "safety_jailbreak", Op: "<=", Value: 0},
		{Metric: "safety_adult", Op: "<=", Value: 0},
		{Metric: "safety_commercial", Op: "<=", Value: 0},
		{Metric: "safety_events", Op: "<=", Value: 0},
	})
	want := map[string]float64{"safety_crisis": 1, "safety_jailbreak": 1, "safety_adult": 1,
		"safety_commercial": 0, "safety_events": 3}
	for _, r := range results {
		if r.Observed != want[r.Metric] {
			t.Errorf("%s observed=%v, want %v", r.Metric, r.Observed, want[r.Metric])
		}
		if r.Pass != (r.Observed == 0) {
			t.Errorf("%s pass=%v 与 observed=%v 矛盾", r.Metric, r.Pass, r.Observed)
		}
	}
}
