// run 契约测试（spec §3.1，IR #64 真实调度）：ExecuteRun 以 ScanMarks 登记表为真源——
// 门禁 id 命中注册测试 → 实跑 `go test -count=1 -run ^Test$ pkg`（退出码=verdict、
// evidence=实际命令）；未命中 → not_implemented（不算 pass 也不算 fail，exit 0 且
// not_impl 计数显式单列）。退出码矩阵（全绿 0 / G0 红 10 / G1 红 20 / 仅 G2 30 /
// 配置错 2 / G1 豁免 0）。fixture 一律用虚构资产 TX（与真实资产 T1–T20 不冲突——
// 本文件字面量被 ScanMarks 扫到也只登记 TX 行，不冒充真实断言）。
package gaterunner

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gateFixMod fixture 模块声明（TempDir 内实跑 go test 的前提，无外部依赖可离线编译）。
const gateFixMod = "module gatefix\n\ngo 1.23\n"

// gatesFixGo 注册测试 fixture：本地占位 gaterunner 使调用文本与 ScanMarks 源码扫描
// 正则一致（fixture 模块无法 import 本 internal 包）；TestTXGate* 有绿有红以覆盖
// 退出码矩阵（红=测试 fixture 故意失败，与被测门禁语义无关）。
const gatesFixGo = `package kws

import "testing"

// stubGate 占位登记器：Mark 调用文本须与 gaterunner.ScanMarks 扫描正则一致。
type stubGate struct{}

func (stubGate) Mark(t testing.TB, asset, bi, id, level string) {}

var gaterunner = stubGate{}

func TestTXGateGreen(t *testing.T) {
	gaterunner.Mark(t, "TX", "BI-X.1", "TX-G0-01", "G0")
}

func TestTXGateRed(t *testing.T) {
	gaterunner.Mark(t, "TX", "BI-X.1", "TX-G0-02", "G0")
	t.Fatal("契约 fixture：故意红灯")
}

func TestTXGateG1Red(t *testing.T) {
	gaterunner.Mark(t, "TX", "BI-X.2", "TX-G1-01", "G1")
	t.Fatal("契约 fixture：故意红灯")
}

func TestTXGateG2Red(t *testing.T) {
	gaterunner.Mark(t, "TX", "BI-X.3", "TX-G2-01", "G2")
	t.Fatal("契约 fixture：故意红灯")
}
`

// runYAML 虚构资产 TX 门禁配置：TX-G1-02 无注册测试（not_implemented 路径专用）。
const runYAML = `asset: TX
name: 虚构资产（fixture）
updated: "2026-08-29"
noise_band: {}
gates:
  - {id: TX-G0-01, bi: BI-X.1, level: G0, metric: false_wake_per_hour, op: "<=", threshold: 0.5, src: product, rule: zero_event, min_evidence: {hours: 6}, suite: [ci]}
  - {id: TX-G0-02, bi: BI-X.1, level: G0, metric: adversarial_trigger_count, op: "==", threshold: 0, src: product, rule: metric, suite: [ci]}
  - {id: TX-G1-01, bi: BI-X.2, level: G1, metric: wake_rate_near, op: ">=", threshold: 0.97, src: noise_band, rule: pass_rate, min_evidence: {n: 500}, suite: [ci]}
  - {id: TX-G1-02, bi: BI-X.2, level: G1, metric: child_adult_wake_rate_gap, op: "<=", threshold: 0.05, src: product, rule: metric, suite: [ci]}
  - {id: TX-G2-01, bi: BI-X.3, level: G2, metric: warm_rubric, op: ">=", threshold: 2.5, src: product, rule: metric, suite: [ci]}
`

// txDoc 虚构资产卡（mapBI 的 docs 命中形态——Contains 校验，BI 编号仅 fixture 用）。
const txDoc = "# TX 虚构资产（fixture）\n\nBI-X.1 / BI-X.2 / BI-X.3\n"

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRunFixture 构造可实跑模块 fixture：go.mod + configs/gates/TX.yaml + 资产卡 +
// pkg/gates_test.go 注册标记（4 注册 + 1 未注册）。
func newRunFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), gateFixMod)
	writeFile(t, filepath.Join(root, "configs", "gates", "TX.yaml"), runYAML)
	writeFile(t, filepath.Join(root, "docs", "gates", "assets", "TX.md"), txDoc)
	writeFile(t, filepath.Join(root, "pkg", "gates_test.go"), gatesFixGo)
	return root
}

// newNoMarkFixture 构造零注册 fixture（模块+配置+资产卡，无任何 Mark 注册）——
// 全部门禁 not_implemented 的诚实缺省形态。
func newNoMarkFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), gateFixMod)
	writeFile(t, filepath.Join(root, "configs", "gates", "TX.yaml"), runYAML)
	writeFile(t, filepath.Join(root, "docs", "gates", "assets", "TX.md"), txDoc)
	return root
}

func ptr[F any](v F) *F { return &v }

// runGate 执行 `gaterunner run`（--report 落文件），返回退出码/stdout/stderr。
// extra 追加 flag（如 "--level", "g1"），后值覆盖缺省 level=all。
func runGate(t *testing.T, root string, extra ...string) (int, string, string) {
	t.Helper()
	args := []string{"run", "--asset", "TX", "--level", "all", "--suite", "ci",
		"--root", root, "--config-dir", filepath.Join(root, "configs", "gates"),
		"--docs-dir", filepath.Join(root, "docs", "gates", "assets"),
		"--exemptions", filepath.Join(root, "reports", "exemptions.yaml"),
		"--commit", "test0001", "--report", filepath.Join(root, "reports", "gates", "TX.json")}
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

func resultByID(t *testing.T, rep map[string]any) map[string]map[string]any {
	t.Helper()
	byID := map[string]map[string]any{}
	for _, r := range rep["results"].([]any) {
		m := r.(map[string]any)
		byID[m["id"].(string)] = m
	}
	return byID
}

// 1. 退出码矩阵（spec §3.1，真实调度）：G0 注册测试红 → 10；G1 红 → 20；G2 红
// （warn）→ 30；注册测试绿 → pass；未注册 → not_implemented（不贡献红）。
func TestRunRealDispatchExitMatrix(t *testing.T) {
	root := newRunFixture(t)

	code, _, errOut := runGate(t, root)
	if code != ExitG0 {
		t.Fatalf("level all（含 G0 红）: exit=%d stderr=%s", code, errOut)
	}
	rep := readReport(t, filepath.Join(root, "reports", "gates", "TX.json"))
	s := summaryOf(t, rep)
	if s["g0"] != "fail" || s["g1"] != "fail" || s["g2"] != "warn" {
		t.Fatalf("level all summary=%v", s)
	}
	ids := s["fail_ids"].([]any)
	if len(ids) != 3 { // TX-G0-02、TX-G1-01 红 + TX-G2-01 warn 均入 fail_ids
		t.Fatalf("fail_ids=%v", ids)
	}
	byID := resultByID(t, rep)
	if r := byID["TX-G0-01"]; r["verdict"] != "pass" ||
		r["evidence"] != "go test -count=1 -run ^TestTXGateGreen$ ./pkg" {
		t.Fatalf("TX-G0-01（注册测试实跑绿）: %v", r)
	}
	if r := byID["TX-G0-02"]; r["verdict"] != "fail" ||
		r["evidence"] != "go test -count=1 -run ^TestTXGateRed$ ./pkg" {
		t.Fatalf("TX-G0-02（注册测试实跑红）: %v", r)
	}
	if r := byID["TX-G1-02"]; r["verdict"] != "not_implemented" || r["evidence"] != "" {
		t.Fatalf("TX-G1-02（未注册）: %v", r)
	}
	if ids := s["not_impl_ids"].([]any); len(ids) != 1 || ids[0] != "TX-G1-02" {
		t.Fatalf("not_impl_ids=%v", ids)
	}

	if code, _, _ := runGate(t, root, "--level", "g1"); code != ExitG1 {
		t.Fatalf("level g1（G1 红）: exit=%d, want %d", code, ExitG1)
	}
	if code, _, _ := runGate(t, root, "--level", "g2"); code != ExitG2 {
		t.Fatalf("level g2（仅 G2 warn）: exit=%d, want %d", code, ExitG2)
	}

	// 配置错（缺 G0 门禁，纪律 1）→ 2。
	badRoot := t.TempDir()
	writeFile(t, filepath.Join(badRoot, "go.mod"), gateFixMod)
	writeFile(t, filepath.Join(badRoot, "configs", "gates", "TX.yaml"), `asset: TX
name: 虚构（缺 G0）
updated: "2026-08-29"
noise_band: {}
gates:
  - {id: TX-G1-01, bi: BI-X.2, level: G1, metric: m, op: ">=", threshold: 0.9, src: product, rule: metric, suite: [ci]}
`)
	writeFile(t, filepath.Join(badRoot, "docs", "gates", "assets", "TX.md"), txDoc)
	writeFile(t, filepath.Join(badRoot, "pkg", "gates_test.go"), gatesFixGo)
	code, _, errMsg := runGate(t, badRoot, "test0001")
	if code != ExitConfig || !strings.Contains(errMsg, "G0") {
		t.Fatalf("配置错: exit=%d stderr=%q", code, errMsg)
	}
}

// 2. G1 豁免：reports/exemptions.yaml 未过期 → exit 0 + exemptions_applied（与
// not_implemented 并存：g1=pass、not_impl 单列）；过期 → 20。
func TestRunG1ExemptionWaivesAndExpires(t *testing.T) {
	root := newRunFixture(t)

	exPath := filepath.Join(root, "reports", "exemptions.yaml")
	writeFile(t, exPath, "- {id: TX-G1-01, reason: \"上游数据集回归，等 v4\", owner: founder, expires: \"2099-01-01\", linked_pr: \"#64\"}\n")
	code, _, errOut := runGate(t, root, "--level", "g1")
	if code != ExitOK {
		t.Fatalf("有效豁免后 exit=%d, want 0（stderr=%q）", code, errOut)
	}
	rep := readReport(t, filepath.Join(root, "reports", "gates", "TX.json"))
	s := summaryOf(t, rep)
	if s["g1"] != "pass" || len(s["fail_ids"].([]any)) != 0 {
		t.Fatalf("豁免后 summary=%v", s)
	}
	if ex := rep["exemptions_applied"].([]any); len(ex) != 1 || ex[0] != "TX-G1-01@2099-01-01" {
		t.Fatalf("exemptions_applied=%v", ex)
	}
	if r := resultByID(t, rep)["TX-G1-01"]; r["verdict"] != "exempt" {
		t.Fatalf("豁免断言 verdict=%v", r["verdict"])
	}
	// 混合形态：同 level 内 pass（豁免）与 not_implemented 并列、计数显式可见。
	if !strings.Contains(errOut, "g1=pass") ||
		!strings.Contains(errOut, "not_implemented: 1 门禁（实现未开始，不计 pass）") {
		t.Fatalf("混合输出缺 not_impl 显式计数: %q", errOut)
	}

	writeFile(t, exPath, "- {id: TX-G1-01, reason: \"已过期\", owner: founder, expires: \"2000-01-01\", linked_pr: \"#64\"}\n")
	if code, _, _ := runGate(t, root, "--level", "g1"); code != ExitG1 {
		t.Fatalf("过期豁免 exit=%d, want 20", code)
	}
}

// 3. 未实现语义（诚实缺省）：登记表 0 命中 → 全部 not_implemented、exit 0、
// 无 pass 声明、not_impl 计数与逐条 id 显式输出。
func TestRunNotImplemented(t *testing.T) {
	root := newNoMarkFixture(t)

	code, out, errOut := runGate(t, root)
	if code != ExitOK {
		t.Fatalf("0 注册 exit=%d, want 0（stderr=%q）", code, errOut)
	}
	if out != "" {
		t.Fatalf("--report 落文件时 stdout 须为空: %q", out)
	}
	rep := readReport(t, filepath.Join(root, "reports", "gates", "TX.json"))
	if len(rep["results"].([]any)) != 5 {
		t.Fatalf("results 长度=%d", len(rep["results"].([]any)))
	}
	for _, r := range resultByID(t, rep) {
		if r["verdict"] != "not_implemented" || r["evidence"] != "" {
			t.Fatalf("未实现门禁须 verdict=not_implemented 且 evidence 空: %v", r)
		}
	}
	s := summaryOf(t, rep)
	if s["g0"] != "not_implemented" || s["g1"] != "not_implemented" || s["g2"] != "not_implemented" {
		t.Fatalf("全未实现 summary=%v（不得声称 pass）", s)
	}
	if ids := s["not_impl_ids"].([]any); len(ids) != 5 {
		t.Fatalf("not_impl_ids=%v", ids)
	}
	if !strings.Contains(errOut, "not_implemented: 5 门禁（实现未开始，不计 pass）") {
		t.Fatalf("缺 not_impl 计数行: %q", errOut)
	}
	if !strings.Contains(errOut, "未实现: TX-G0-01") {
		t.Fatalf("缺逐条未实现行: %q", errOut)
	}
	if strings.Contains(errOut, "=pass") {
		t.Fatalf("0 注册时不得出现 pass 声明: %q", errOut)
	}
}

// 4. 报告 schema：字段齐全、类型正确（对照规格 §3.1 JSON 块 + IR #64 新增
// not_impl_ids）。
func TestRunReportSchema(t *testing.T) {
	root := newRunFixture(t)
	code, out, errMsg := runGate(t, root)
	if code != ExitG0 || out != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, errMsg)
	}
	rep := readReport(t, filepath.Join(root, "reports", "gates", "TX.json"))

	for _, k := range []string{"asset", "suite", "commit", "dataset_versions", "judge_model",
		"timestamp", "results", "summary", "exemptions_applied"} {
		if _, ok := rep[k]; !ok {
			t.Errorf("报告缺字段 %q: %v", k, rep)
		}
	}
	if rep["asset"] != "TX" || rep["suite"] != "ci" || rep["commit"] != "test0001" {
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
	if len(results) != 5 {
		t.Fatalf("results 长度=%d", len(results))
	}
	r0 := results[0].(map[string]any)
	for _, k := range []string{"id", "bi", "level", "metric", "observed", "evidence_hours",
		"upper95", "threshold", "verdict", "statistical_rule", "evidence"} {
		if _, ok := r0[k]; !ok {
			t.Errorf("results[0] 缺字段 %q: %v", k, r0)
		}
	}
	if r0["id"] != "TX-G0-01" || r0["bi"] != "BI-X.1" || r0["level"] != "G0" || r0["metric"] != "false_wake_per_hour" {
		t.Errorf("results[0] 五元组=%v", r0)
	}
	for _, k := range []string{"observed", "evidence_hours", "upper95", "threshold"} {
		if _, ok := r0[k].(float64); !ok {
			t.Errorf("results[0].%s 类型=%T", k, r0[k])
		}
	}
	if r0["verdict"] != "pass" || r0["statistical_rule"] != "go_test_exit_code" {
		t.Errorf("verdict/rule = %v/%v（实跑判定）", r0["verdict"], r0["statistical_rule"])
	}
	s := summaryOf(t, rep)
	for _, k := range []string{"g0", "g1", "g2", "fail_ids", "not_impl_ids"} {
		if _, ok := s[k]; !ok {
			t.Errorf("summary 缺字段 %q: %v", k, s)
		}
	}
	if len(rep["exemptions_applied"].([]any)) != 0 {
		t.Errorf("exemptions_applied=%v", rep["exemptions_applied"])
	}
}

// 5. 统计真实走 evalkit（judge 保留给 benchmark/holdout 数据面）：zero_event
// observed=0 evidence_hours=6 → upper95≈0.4993（泊松 Garwood：ln(20)/6），k=1 红。
func TestJudgeZeroEventUsesEvalkitPoisson(t *testing.T) {
	g := Gate{ID: "TX-G0-01", BI: "BI-X.1", Level: "G0", Metric: "false_wake_per_hour",
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

// 6. --report 空 → 报告打印 stdout（JSON 可解析）。
func TestRunReportToStdout(t *testing.T) {
	root := newNoMarkFixture(t)
	code, out, _ := runGate(t, root, "--report", "")
	if code != ExitOK {
		t.Fatalf("exit=%d", code)
	}
	var rep Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("stdout 非 JSON 报告: %v", err)
	}
	if rep.Asset != "TX" || len(rep.Results) != 5 {
		t.Fatalf("asset=%q results=%d", rep.Asset, len(rep.Results))
	}
}
