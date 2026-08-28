// check 守恒校验测试（spec §3.6）——契约先行：差分（ΣP95−overlap ≤ 1500 → 0，
// 否则 20）、负债表内容、真实 latency.yaml 解析回归、固定种子属性轮。
package budgets

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 与 configs/budgets/latency.yaml 一致的段与预算（Σ=1500）。
var (
	segIDs   = []string{"tail_silence", "asr_uplink", "cloud_llm", "tts_first", "transport"}
	segP95   = []float64{600, 150, 450, 280, 20}
	repoYAML = filepath.Join("..", "..", "..", "..", "configs", "budgets", "latency.yaml")
)

const testConfigYAML = `# 测试用预算（与 configs/budgets/latency.yaml 同构）
total_p95_budget: 1500
segments:
  - { id: tail_silence, asset: T3, p50: 450, p95: 600, note: 端点判定·尾静音等待 }
  - { id: asr_uplink,   asset: T3, p50: 100, p95: 150 }
  - { id: cloud_llm,    asset: T15, p50: 300, p95: 450 }
  - { id: tts_first,    asset: T13, p50: 200, p95: 280 }
  - { id: transport,    asset: T14, p50: 20,  p95: 20 }
rules:
  - 劣化 >2σ 且无划拨说明 → 组合级 G1 红
`

func ptr[F any](v F) *F { return &v }

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// reportJSON 构造夜间延迟报告；ids 取 segIDs 前 len(p95s) 个。
func reportJSON(p95s []float64, overlap *float64) string {
	segs := make([]map[string]any, len(p95s))
	for i, v := range p95s {
		segs[i] = map[string]any{"id": segIDs[i], "p50": v, "p95": v}
	}
	report := map[string]any{"commit": "test0001", "timestamp": "2026-08-28T00:00:00Z", "segments": segs}
	if overlap != nil {
		report["overlap_ms"] = *overlap
	}
	data, err := json.Marshal(report)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// checkRun 对给定实测值跑 `budgets check`，返回退出码与 stdout。
func checkRun(t *testing.T, p95s []float64, overlap *float64) (int, string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code := Run([]string{
		"check",
		"--report", writeFile(t, "latency.json", reportJSON(p95s, overlap)),
		"--config", writeFile(t, "latency.yaml", testConfigYAML),
	}, &out, &errBuf)
	if errBuf.Len() > 0 {
		t.Fatalf("unexpected stderr: %s", errBuf.String())
	}
	return code, out.String()
}

// 1. 差分：Σ=1500 → 0；Σ=1501 → 20；Σ=1550−overlap50=1500 → 0；−49 → 20。
func TestCheckConservationDifferential(t *testing.T) {
	tests := []struct {
		name    string
		p95s    []float64
		overlap *float64
		want    int
	}{
		{"sum equals budget", segP95, nil, ExitOK},
		{"sum over by 1", []float64{601, 150, 450, 280, 20}, nil, ExitViolation},
		{"overlap offsets exactly", []float64{650, 150, 450, 280, 20}, ptr(50.0), ExitOK},
		{"overlap one short", []float64{650, 150, 450, 280, 20}, ptr(49.0), ExitViolation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if code, _ := checkRun(t, tc.p95s, tc.overlap); code != tc.want {
				t.Fatalf("exit = %d, want %d (p95s=%v overlap=%v)", code, tc.want, tc.p95s, tc.overlap)
			}
		})
	}
}

// 2. 负债表：超预算时含各段 id、预算/实际/差值与超标段清单；守恒通过无超标段。
func TestCheckDebtTable(t *testing.T) {
	code, out := checkRun(t, []float64{700, 200, 500, 280, 20}, nil) // Σ=1700
	if code != ExitViolation {
		t.Fatalf("exit = %d, want %d", code, ExitViolation)
	}
	for _, want := range []string{"延迟负债表", "差值", "超标段", "1700", "+100", "600", "守恒校验违反"} {
		if !strings.Contains(out, want) {
			t.Errorf("debt table missing %q in output:\n%s", want, out)
		}
	}
	for _, id := range segIDs {
		if !strings.Contains(out, id) {
			t.Errorf("debt table missing segment id %q", id)
		}
	}

	code, out = checkRun(t, segP95, nil) // Σ=1500，各段恰在预算内
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d", code, ExitOK)
	}
	if strings.Contains(out, "超标段") {
		t.Errorf("within-budget run must not list over segments:\n%s", out)
	}
	if !strings.Contains(out, "守恒校验通过") {
		t.Errorf("within-budget run missing pass verdict:\n%s", out)
	}
}

// 3. 属性：任意非负 p95×5 + 非负 overlap，exit ∈ {0,20} 且与守恒判定一致（固定种子 50 轮）。
func TestCheckExitMatchesConservation(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for round := 0; round < 50; round++ {
		p95s := make([]float64, 5)
		sum := 0.0
		for i := range p95s {
			p95s[i] = float64(rng.Intn(3001)) // 0..3000
			sum += p95s[i]
		}
		overlap := float64(rng.Intn(501)) // 0..500
		want := ExitOK
		if sum-overlap > 1500 {
			want = ExitViolation
		}
		if code, _ := checkRun(t, p95s, ptr(overlap)); code != want {
			t.Fatalf("round %d: p95s=%v overlap=%v Σ−overlap=%v: exit %d, want %d",
				round, p95s, overlap, sum-overlap, code, want)
		}
	}
}

// 4. 真实仓内 latency.yaml 解析回归（yaml.v3，含内联 mapping 与 CJK note）。
func TestLoadRealRepoLatencyYAML(t *testing.T) {
	config, err := LoadConfig(repoYAML)
	if err != nil {
		t.Fatalf("LoadConfig(%s): %v", repoYAML, err)
	}
	if config.TotalP95Budget != 1500 {
		t.Errorf("total_p95_budget = %v, want 1500", config.TotalP95Budget)
	}
	if len(config.Segments) != len(segIDs) {
		t.Fatalf("got %d segments, want %d", len(config.Segments), len(segIDs))
	}
	for i, seg := range config.Segments {
		if seg.ID != segIDs[i] || seg.P95 != segP95[i] {
			t.Errorf("segment[%d] = {%s %v}, want {%s %v}", i, seg.ID, seg.P95, segIDs[i], segP95[i])
		}
	}
	if config.Segments[0].Asset != "T3" || config.Segments[0].Note == "" {
		t.Errorf("segment[0] asset/note not parsed: %+v", config.Segments[0])
	}
}

// 5. 输入错误（缺文件 / 坏 JSON / 段与配置不一致 / --days<1 类）→ exit 2。
func TestCheckInputErrors(t *testing.T) {
	dir := t.TempDir()
	cfg := writeFile(t, "latency.yaml", testConfigYAML)
	ok := writeFile(t, "ok.json", reportJSON(segP95, nil))
	partial := reportJSON(segP95[:4], nil) // 缺 transport
	tests := []struct {
		name string
		args []string
	}{
		{"报告不存在", []string{"check", "--report", filepath.Join(dir, "nope.json"), "--config", cfg}},
		{"坏 JSON", []string{"check", "--report", writeFile(t, "bad.json", "{not json"), "--config", cfg}},
		{"段与配置不一致", []string{"check", "--report", writeFile(t, "miss.json", partial), "--config", cfg}},
		{"配置不存在", []string{"check", "--report", ok, "--config", filepath.Join(dir, "nope.yaml")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, errBuf bytes.Buffer
			if code := Run(tc.args, &out, &errBuf); code != ExitInput {
				t.Fatalf("exit = %d, want %d", code, ExitInput)
			} else if errBuf.Len() == 0 {
				t.Fatal("expected diagnostic on stderr")
			}
		})
	}
}
