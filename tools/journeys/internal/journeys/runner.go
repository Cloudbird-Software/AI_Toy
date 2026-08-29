package journeys

import (
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
	"slices"
	"time"
)

// Metrics 旅程级聚合指标（spec §3.5 四指标 + IR #67 四级安全分型；
// SafetyEvents 为四分型之和的总和口径，兼容旧报告）。
type Metrics struct {
	CompletionRate   float64 `json:"completion_rate"`
	LatencyP95MS     float64 `json:"latency_p95_ms"`
	SafetyEvents     int     `json:"safety_events"`
	SafetyCrisis     int     `json:"safety_crisis"`
	SafetyJailbreak  int     `json:"safety_jailbreak"`
	SafetyAdult      int     `json:"safety_adult"`
	SafetyCommercial int     `json:"safety_commercial"`
	MemoryHitRate    float64 `json:"memory_hit_rate"`
}

// RunResult 单 seed 运行观测（安全事件按四级分型计数，SafetyEvents=四分型之和）。
type RunResult struct {
	Seed             int     `json:"seed"`
	CompletionRate   float64 `json:"completion_rate"`
	LatencyMS        float64 `json:"latency_ms"`
	SafetyEvents     int     `json:"safety_events"`
	SafetyCrisis     int     `json:"safety_crisis"`
	SafetyJailbreak  int     `json:"safety_jailbreak"`
	SafetyAdult      int     `json:"safety_adult"`
	SafetyCommercial int     `json:"safety_commercial"`
	MemoryHit        bool    `json:"memory_hit"`
}

// safetyTierKinds 注入事件 kind → 四级分型 metric（IR #67）；
// crisis_direct/crisis_metaphor 同归危机档（危机安抚失败）。
var safetyTierKinds = map[string]string{
	"crisis_direct":        "safety_crisis",
	"crisis_metaphor":      "safety_crisis",
	"jailbreak_induction":  "safety_jailbreak",
	"adult_topic":          "safety_adult",
	"commercial_induction": "safety_commercial",
}

// tierCounts 四级安全分型计数。
type tierCounts struct{ crisis, jailbreak, adult, commercial int }

// total 返回四分型之和（= safety_events 总和口径）。
func (t tierCounts) total() int {
	return t.crisis + t.jailbreak + t.adult + t.commercial
}

// add 按分型 metric 名计数。
func (t *tierCounts) add(metric string) {
	switch metric {
	case "safety_jailbreak":
		t.jailbreak++
	case "safety_adult":
		t.adult++
	case "safety_commercial":
		t.commercial++
	default: // safety_crisis
		t.crisis++
	}
}

// safetyEventMetric 从注入事件声明提取 kind 并映射到分型 metric；
// 缺失/未知 kind 保守计入最严重的危机档（宁可多计不可漏计）。
func safetyEventMetric(ev any) string {
	if m, ok := ev.(map[string]any); ok {
		if k, ok := m["kind"].(string); ok {
			if tier, ok := safetyTierKinds[k]; ok {
				return tier
			}
		}
	}
	return "safety_crisis"
}

// AssertionResult 断言逐条评估。
type AssertionResult struct {
	Metric   string  `json:"metric"`
	Op       string  `json:"op"`
	Value    float64 `json:"value"`
	Observed float64 `json:"observed"`
	Pass     bool    `json:"pass"`
}

type JourneyReport struct {
	ID         string            `json:"id"`
	Tier       string            `json:"tier"`
	Source     string            `json:"source"`
	Runs       []RunResult       `json:"runs"`
	Metrics    Metrics           `json:"metrics"`
	Assertions []AssertionResult `json:"assertions"`
	Verdict    string            `json:"verdict"`
}

type Summary struct {
	JourneysTotal int      `json:"journeys_total"`
	Pass          int      `json:"pass"`
	Fail          int      `json:"fail"`
	FailIDs       []string `json:"fail_ids"`
	Overall       string   `json:"overall"`
}

type Report struct {
	Set        string          `json:"set"`
	Seeds      int             `json:"seeds"`
	Driver     string          `json:"driver"`
	DriverMode string          `json:"driver_mode"`
	Timestamp  string          `json:"timestamp"`
	Journeys   []JourneyReport `json:"journeys"`
	Summary    Summary         `json:"summary"`
}

// seedSource 把字符串种子 "seed:journey_id" 经 fnv64a 哈希成 int64 随机源
// （Go 的 rand.NewSource 只接受 int64，故用 fnv 哈希字符串种子）。
func seedSource(scriptID string, seed int) rand.Source {
	h := fnv.New64a()
	fmt.Fprintf(h, "%d:%s", seed, scriptID)
	return rand.NewSource(int64(h.Sum64()))
}

// simulateRun 确定性桩 driver（真 driver=packages/go/user-sim 协议由后续卡接入，
// 届时替换本函数调用点即可）。
func simulateRun(s *Script, seed int) RunResult {
	rng := rand.New(seedSource(s.ID, seed))
	pFail := 0.05
	if s.Tier != "core" {
		pFail = 0.08
	}
	if s.Persona["patience"] == "low" {
		pFail += 0.03
	}
	completed := len(s.Steps)
	if rng.Float64() < pFail {
		completed = rng.Intn(len(s.Steps))
	}
	latency := 400 + rng.Float64()*1000
	// 类型感知注入：每条声明事件按其 kind 分入对应分型；随机数消耗顺序与
	// 2% 注入概率不变，保证分型改造前后红绿一致（红绿只由种子决定）。
	var tiers tierCounts
	for _, ev := range s.Inject.SafetyEvents {
		if rng.Float64() < 0.02 {
			tiers.add(safetyEventMetric(ev))
		}
	}
	return RunResult{Seed: seed, CompletionRate: round(float64(completed)/float64(len(s.Steps)), 4),
		LatencyMS: round(latency, 1), SafetyEvents: tiers.total(),
		SafetyCrisis: tiers.crisis, SafetyJailbreak: tiers.jailbreak,
		SafetyAdult: tiers.adult, SafetyCommercial: tiers.commercial, MemoryHit: rng.Float64() < 0.95}
}

func round(x float64, digits int) float64 {
	p := math.Pow(10, float64(digits))
	return math.Round(x*p) / p
}

// AggregateRuns 聚合单旅程多 seed 运行：完成率/记忆命中率取均值，延迟取 P95，
// 安全事件按四级分型求和（SafetyEvents=四分型之和的总和口径）。
func AggregateRuns(runs []RunResult) Metrics {
	latencies := make([]float64, len(runs))
	var completion, hits float64
	var tiers tierCounts
	for i, r := range runs {
		latencies[i] = r.LatencyMS
		completion += r.CompletionRate
		tiers.crisis += r.SafetyCrisis
		tiers.jailbreak += r.SafetyJailbreak
		tiers.adult += r.SafetyAdult
		tiers.commercial += r.SafetyCommercial
		if r.MemoryHit {
			hits++
		}
	}
	slices.Sort(latencies)
	p95 := latencies[max(1, int(math.Ceil(0.95*float64(len(latencies)))))-1]
	return Metrics{CompletionRate: round(completion/float64(len(runs)), 4), LatencyP95MS: round(p95, 1),
		SafetyEvents: tiers.total(), SafetyCrisis: tiers.crisis, SafetyJailbreak: tiers.jailbreak,
		SafetyAdult: tiers.adult, SafetyCommercial: tiers.commercial,
		MemoryHitRate: round(hits/float64(len(runs)), 4)}
}

// EvaluateAssertions 逐条评估断言，记录观测值与 pass。
func EvaluateAssertions(m Metrics, assertions []Assertion) []AssertionResult {
	observed := map[string]float64{"completion_rate": m.CompletionRate, "latency_p95_ms": m.LatencyP95MS,
		"safety_events": float64(m.SafetyEvents), "safety_crisis": float64(m.SafetyCrisis),
		"safety_jailbreak": float64(m.SafetyJailbreak), "safety_adult": float64(m.SafetyAdult),
		"safety_commercial": float64(m.SafetyCommercial), "memory_hit_rate": m.MemoryHitRate}
	results := make([]AssertionResult, len(assertions))
	for i, a := range assertions {
		results[i] = AssertionResult{Metric: a.Metric, Op: a.Op, Value: a.Value,
			Observed: observed[a.Metric], Pass: compare(a.Op, observed[a.Metric], a.Value)}
	}
	return results
}

func compare(op string, a, b float64) bool {
	switch op {
	case ">=":
		return a >= b
	case "<=":
		return a <= b
	case ">":
		return a > b
	case "<":
		return a < b
	}
	return a == b // "=="
}

// Run 执行 scripts × seeds（每剧本 seed=0..seeds-1），聚合指标并评估断言。
func Run(scripts []*Script, seeds int, setName, driver string) (*Report, error) {
	if seeds < 1 {
		return nil, errors.New("seeds must be >= 1")
	}
	rep := &Report{Set: setName, Seeds: seeds, Driver: driver, DriverMode: "simulated",
		Timestamp: time.Now().UTC().Format(time.RFC3339), Journeys: make([]JourneyReport, 0, len(scripts))}
	for _, s := range scripts {
		runs := make([]RunResult, seeds)
		for seed := range seeds {
			runs[seed] = simulateRun(s, seed)
		}
		m := AggregateRuns(runs)
		results := EvaluateAssertions(m, s.Assertions)
		verdict := "pass"
		for _, r := range results {
			if !r.Pass {
				verdict = "fail"
			}
		}
		rep.Journeys = append(rep.Journeys,
			JourneyReport{ID: s.ID, Tier: s.Tier, Source: s.Source, Runs: runs, Metrics: m,
				Assertions: results, Verdict: verdict})
	}
	rep.Summary = summarize(rep.Journeys)
	return rep, nil
}

func summarize(journeys []JourneyReport) Summary {
	failIDs := []string{}
	for _, j := range journeys {
		if j.Verdict == "fail" {
			failIDs = append(failIDs, j.ID)
		}
	}
	overall := "pass"
	if len(failIDs) > 0 {
		overall = "fail"
	}
	return Summary{JourneysTotal: len(journeys), Pass: len(journeys) - len(failIDs),
		Fail: len(failIDs), FailIDs: failIDs, Overall: overall}
}
