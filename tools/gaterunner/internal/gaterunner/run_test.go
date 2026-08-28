// run 契约测试（spec §3.1）：退出码矩阵（全绿 0 / G0 红 10 / G1 红 20 / 仅 G2 30 /
// 配置错 2 / G1 豁免 0）、报告 schema 字段（对照规格 JSON 块）、统计判定真实走
// evalkit（zero_event observed=0 evidence_hours=6 → upper95≈0.499）。
// 红绿由 (commit,id) 种子决定（确定性桩），测试以 sha 搜索定位各退出码场景。
package gaterunner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const runYAML = `asset: T4
name: 唤醒词（测试）
updated: "2026-08-28"
noise_band: {}
gates:
  - {id: T4-G0-01, bi: BI-4.2, level: G0, metric: false_wake_per_hour, op: "<=", threshold: 0.5, src: product, rule: zero_event, min_evidence: {hours: 6}, suite: [ci]}
  - {id: T4-G1-01, bi: BI-4.1, level: G1, metric: wake_rate_near, op: ">=", threshold: 0.97, src: noise_band, rule: pass_rate, min_evidence: {n: 500}, suite: [ci]}
  - {id: T4-G2-01, bi: BI-4.3, level: G2, metric: warm_rubric, op: ">=", threshold: 2.5, src: product, rule: metric, suite: [ci]}
`

const gatesTestGo = `package kws_test

import (
	"testing"

	"github.com/Cloudbird-Software/AI_Toy/tools/gaterunner/internal/gaterunner"
)

func TestG0FalseWake(t *testing.T) {
	gaterunner.Mark(t, "T4", "BI-4.2", "T4-G0-01", "G0")
}

func TestG0AdvNegative(t *testing.T) {
	gaterunner.Mark(t, "T4", "BI-4.2", "T4-G0-02", "G0")
}
`

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRunFixture 构造单资产 fixture：configs/gates/T4.yaml + pkg/gates_test.go 注册标记。
func newRunFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "configs", "gates", "T4.yaml"), runYAML)
	writeFile(t, filepath.Join(root, "pkg", "gates_test.go"), gatesTestGo)
	return root
}

func ptr[F any](v F) *F { return &v }

// runGate 执行 `gaterunner run`（--report 落文件），返回退出码/stdout/stderr。
func runGate(t *testing.T, root, sha string, extra ...string) (int, string, string) {
	t.Helper()
	args := []string{"run", "--asset", "T4", "--level", "all", "--suite", "ci",
		"--root", root, "--config-dir", filepath.Join(root, "configs", "gates"),
		"--docs-dir", t.TempDir(),
		"--exemptions", filepath.Join(root, "reports", "exemptions.yaml"),
		"--commit", sha, "--report", filepath.Join(root, "reports", "gates", "T4.json")}
	var stdout, stderr bytes.Buffer
	code := Main(append(args, extra...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func readReport(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rep map[string]any
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatalf("报告不是 JSON: %v", err)
	}
	return rep
}

func summaryOf(t *testing.T, rep map[string]any) map[string]any {
	t.Helper()
	s, ok := rep["summary"].(map[string]any)
	if !ok {
		t.Fatalf("summary 类型错误: %T", rep["summary"])
	}
	return s
}

// findExit 在确定性 sha 序列中搜索指定退出码（g2Pass=true 时额外要求 g2 绿，
// 供豁免用例复用同 sha）。
func findExit(t *testing.T, root string, want int, g2Pass bool) (string, map[string]any) {
	t.Helper()
	for i := 1; i <= 400; i++ {
		sha := fmt.Sprintf("test%04d", i)
		code, _, _ := runGate(t, root, sha)
		if code != want {
			continue
		}
		rep := readReport(t, filepath.Join(root, "reports", "gates", "T4.json"))
		if !g2Pass || summaryOf(t, rep)["g2"] == "pass" {
			return sha, rep
		}
	}
	t.Fatalf("400 个 sha 内未找到 exit=%d（g2Pass=%v）", want, g2Pass)
	return "", nil
}

// 1. 退出码矩阵（spec §3.1）。
func TestRunExitCodeMatrix(t *testing.T) {
	root := newRunFixture(t)

	findExit(t, root, ExitOK, false)
	findExit(t, root, ExitG0, false)

	_, rep := findExit(t, root, ExitG1, true)
	if s := summaryOf(t, rep); s["g1"] != "fail" || s["g0"] != "pass" {
		t.Fatalf("G1 红场景 summary=%v", s)
	}

	_, rep = findExit(t, root, ExitG2, false)
	s := summaryOf(t, rep)
	if s["g2"] != "warn" || s["g0"] != "pass" || s["g1"] != "pass" {
		t.Fatalf("仅 G2 场景 summary=%v", s)
	}
	if ids := s["fail_ids"].([]any); len(ids) != 1 || ids[0] != "T4-G2-01" {
		t.Fatalf("fail_ids=%v", ids)
	}

	// 配置错（缺 G0 门禁，纪律 1）→ 2。
	badRoot := t.TempDir()
	writeFile(t, filepath.Join(badRoot, "configs", "gates", "T4.yaml"),
		strings.Replace(runYAML, "  - {id: T4-G0-01, bi: BI-4.2, level: G0, metric: false_wake_per_hour, op: \"<=\", threshold: 0.5, src: product, rule: zero_event, min_evidence: {hours: 6}, suite: [ci]}\n", "", 1))
	writeFile(t, filepath.Join(badRoot, "pkg", "gates_test.go"), gatesTestGo)
	code, _, errMsg := runGate(t, badRoot, "test0001")
	if code != ExitConfig || !strings.Contains(errMsg, "G0") {
		t.Fatalf("配置错: exit=%d stderr=%q", code, errMsg)
	}
}

// 2. G1 豁免：reports/exemptions.yaml 未过期 → exit 0 + exemptions_applied；过期 → 20。
func TestRunG1ExemptionWaivesAndExpires(t *testing.T) {
	root := newRunFixture(t)
	sha, _ := findExit(t, root, ExitG1, true) // G0 绿、G1 红、G2 绿

	exPath := filepath.Join(root, "reports", "exemptions.yaml")
	writeFile(t, exPath, "- {id: T4-G1-01, reason: \"上游数据集回归，等 v4\", owner: founder, expires: \"2099-01-01\", linked_pr: \"#31\"}\n")
	code, _, _ := runGate(t, root, sha)
	if code != ExitOK {
		t.Fatalf("有效豁免后 exit=%d, want 0", code)
	}
	rep := readReport(t, filepath.Join(root, "reports", "gates", "T4.json"))
	s := summaryOf(t, rep)
	if s["g1"] != "pass" || len(s["fail_ids"].([]any)) != 0 {
		t.Fatalf("豁免后 summary=%v", s)
	}
	if ex := rep["exemptions_applied"].([]any); len(ex) != 1 || ex[0] != "T4-G1-01@2099-01-01" {
		t.Fatalf("exemptions_applied=%v", ex)
	}
	for _, r := range rep["results"].([]any) {
		if r.(map[string]any)["id"] == "T4-G1-01" && r.(map[string]any)["verdict"] != "exempt" {
			t.Fatalf("豁免断言 verdict=%v", r)
		}
	}

	writeFile(t, exPath, "- {id: T4-G1-01, reason: \"已过期\", owner: founder, expires: \"2000-01-01\", linked_pr: \"#31\"}\n")
	if code, _, _ := runGate(t, root, sha); code != ExitG1 {
		t.Fatalf("过期豁免 exit=%d, want 20", code)
	}
}

// 3. 报告 schema：字段齐全、类型正确（对照规格 §3.1 JSON 块）。
func TestRunReportSchema(t *testing.T) {
	root := newRunFixture(t)
	sha, _ := findExit(t, root, ExitOK, false)

	stdoutPath := filepath.Join(root, "reports", "gates", "T4.json")
	code, out, errMsg := runGate(t, root, sha)
	if code != ExitOK || out != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, errMsg)
	}
	rep := readReport(t, stdoutPath)

	for _, k := range []string{"asset", "suite", "commit", "dataset_versions", "judge_model",
		"timestamp", "results", "summary", "exemptions_applied"} {
		if _, ok := rep[k]; !ok {
			t.Errorf("报告缺字段 %q: %v", k, rep)
		}
	}
	if rep["asset"] != "T4" || rep["suite"] != "ci" || rep["commit"] != sha {
		t.Errorf("asset/suite/commit = %v/%v/%v", rep["asset"], rep["suite"], rep["commit"])
	}
	if _, err := time.Parse(time.RFC3339, rep["timestamp"].(string)); err != nil {
		t.Errorf("timestamp 非 ISO8601: %v", rep["timestamp"])
	}
	if _, ok := rep["dataset_versions"].(map[string]any); !ok {
		t.Errorf("dataset_versions 类型=%T", rep["dataset_versions"])
	}
	if rep["judge_model"] != nil {
		t.Errorf("judge_model = %v, want null", rep["judge_model"])
	}

	results := rep["results"].([]any)
	if len(results) != 3 {
		t.Fatalf("results 长度=%d", len(results))
	}
	r0 := results[0].(map[string]any)
	for _, k := range []string{"id", "bi", "level", "metric", "observed", "evidence_hours",
		"upper95", "threshold", "verdict", "statistical_rule", "evidence"} {
		if _, ok := r0[k]; !ok {
			t.Errorf("results[0] 缺字段 %q: %v", k, r0)
		}
	}
	if r0["id"] != "T4-G0-01" || r0["bi"] != "BI-4.2" || r0["level"] != "G0" || r0["metric"] != "false_wake_per_hour" {
		t.Errorf("results[0] 五元组=%v", r0)
	}
	for _, k := range []string{"observed", "evidence_hours", "upper95", "threshold"} {
		if _, ok := r0[k].(float64); !ok {
			t.Errorf("results[0].%s 类型=%T", k, r0[k])
		}
	}
	if r0["evidence"] != "go test ./pkg -run ^TestG0FalseWake$ -count=1" {
		t.Errorf("evidence=%v（登记表命中时须给最小复现命令）", r0["evidence"])
	}
	if r0["verdict"] != "pass" || r0["statistical_rule"] != "poisson_zero_upper95" {
		t.Errorf("verdict/rule = %v/%v", r0["verdict"], r0["statistical_rule"])
	}
	s := summaryOf(t, rep)
	if s["g0"] != "pass" || s["g1"] != "pass" || s["g2"] != "pass" || len(s["fail_ids"].([]any)) != 0 {
		t.Errorf("summary=%v", s)
	}
	if len(rep["exemptions_applied"].([]any)) != 0 {
		t.Errorf("exemptions_applied=%v", rep["exemptions_applied"])
	}
}

// 4. 统计真实走 evalkit：zero_event observed=0 evidence_hours=6 → upper95≈0.4993
// （泊松 Garwood：ln(20)/6），k=1 则 0.77 > 0.5 判红。
func TestJudgeZeroEventUsesEvalkitPoisson(t *testing.T) {
	g := Gate{ID: "T4-G0-01", BI: "BI-4.2", Level: "G0", Metric: "false_wake_per_hour",
		Op: "<=", Threshold: 0.5, Src: "product", Rule: "zero_event",
		MinEvidence: &MinEvidence{Hours: ptr(6)}}

	r := judge(g, observation{k: 0})
	if r.Observed != 0 || r.EvidenceHours != 6 {
		t.Fatalf("observed=%v evidence_hours=%d", r.Observed, r.EvidenceHours)
	}
	if r.Upper95 < 0.49 || r.Upper95 > 0.501 {
		t.Fatalf("upper95=%v，须≈0.499（evalkit PoissonUpper95(0,6)）", r.Upper95)
	}
	if r.Verdict != "pass" {
		t.Fatalf("verdict=%q, want pass", r.Verdict)
	}

	if r := judge(g, observation{k: 1}); r.Verdict != "fail" || r.Upper95 <= 0.5 {
		t.Fatalf("k=1: verdict=%q upper95=%v，须 fail 且 95%%上限>0.5", r.Verdict, r.Upper95)
	}
}

// 5. 确定性：同 (commit,id) 种子的模拟观测逐位复现。
func TestSimulateDeterministic(t *testing.T) {
	g := Gate{ID: "T4-G1-01", Rule: "pass_rate", Op: ">=", Threshold: 0.97,
		MinEvidence: &MinEvidence{N: ptr(500)}}
	o1 := simulate(g, rand.New(rand.NewSource(seed("test0042", g.ID))))
	o2 := simulate(g, rand.New(rand.NewSource(seed("test0042", g.ID))))
	if o1 != o2 {
		t.Fatalf("同种子观测不同: %+v vs %+v", o1, o2)
	}
}

// 6. --report 空 → 报告打印 stdout（JSON 可解析）。
func TestRunReportToStdout(t *testing.T) {
	root := newRunFixture(t)
	code, out, _ := runGate(t, root, "test0001", "--report", "")
	if code != ExitOK {
		t.Fatalf("exit=%d", code)
	}
	var rep Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("stdout 非 JSON 报告: %v", err)
	}
	if rep.Asset != "T4" || len(rep.Results) != 3 {
		t.Fatalf("asset=%q results=%d", rep.Asset, len(rep.Results))
	}
}
