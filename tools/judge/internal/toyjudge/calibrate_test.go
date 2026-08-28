// 契约测试（spec §3.3/§4.2）：calibrate per-criterion κ 走 evalkit.CohensKappa，
// 门禁阈值来自 model.yaml policy.kappa_gate.automation（非硬编码）；
// κ=1 → exit 0；已知分歧 κ≈0.4 → exit 20；输出含 per-criterion κ。
package toyjudge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFixture 写临时文件（自动建父目录）。
func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runCalibrateCLIWithModel 组装临时 fixture（rubric r1 + 指定 model.yaml + gold
// jsonl）执行 calibrate。
func runCalibrateCLIWithModel(t *testing.T, modelYAML, goldLines string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	rubrics := filepath.Join(dir, "rubrics")
	writeFixture(t, filepath.Join(rubrics, "r1.yaml"), testRubricYAML)
	model := filepath.Join(dir, "model.yaml")
	writeFixture(t, model, modelYAML)
	gold := filepath.Join(dir, "gold.jsonl")
	writeFixture(t, gold, goldLines)
	var stdout, stderr bytes.Buffer
	code := Main([]string{"calibrate", "--rubric", "r1", "--gold", gold,
		"--rubrics-dir", rubrics, "--model", model}, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// runCalibrateCLI 同上，用默认 §4.2 fixture（kappa_gate.automation=0.61）。
func runCalibrateCLI(t *testing.T, goldLines string) (int, string, string) {
	t.Helper()
	return runCalibrateCLIWithModel(t, testModelYAML, goldLines)
}

const perfectGold = `{"criterion": "tone", "human": 1, "judge": 1}
{"criterion": "tone", "human": 2, "judge": 2}
{"criterion": "tone", "human": 3, "judge": 3}
{"criterion": "safety", "human": 2, "judge": 2}
{"criterion": "safety", "human": 3, "judge": 3}
`

// knownDisagreementGold：20 行 tone（human 边际 6/7/7、judge 边际 5/9/6、一致
// 12 行）→ po=0.6、pe=0.3375、κ=21/53≈0.396；safety κ=1。
func knownDisagreementGold() string {
	rows := [][2]int{
		{1, 1}, {1, 1}, {1, 1}, {1, 2}, {1, 3}, {1, 2},
		{2, 2}, {2, 2}, {2, 2}, {2, 2}, {2, 2}, {2, 1}, {2, 3},
		{3, 3}, {3, 3}, {3, 3}, {3, 3}, {3, 1}, {3, 2}, {3, 2},
	}
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "{\"criterion\": \"tone\", \"human\": %d, \"judge\": %d}\n", r[0], r[1])
	}
	b.WriteString("{\"criterion\": \"safety\", \"human\": 2, \"judge\": 2}\n")
	b.WriteString("{\"criterion\": \"safety\", \"human\": 3, \"judge\": 3}\n")
	return b.String()
}

// κ=1 fixture → exit 0；stdout 为含 per-criterion κ 的 JSON 报告，judge 身份锁定其中。
func TestCalibratePerfectKappaExitsZero(t *testing.T) {
	code, out, errMsg := runCalibrateCLI(t, perfectGold)
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, errMsg)
	}
	var rep CalibrateReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("stdout 非完整 JSON: %v\n%s", err, out)
	}
	if !rep.Pass || rep.MinKappa != 1 || len(rep.Criteria) != 2 {
		t.Fatalf("report=%+v", rep)
	}
	byName := map[string]CriterionKappa{}
	for _, c := range rep.Criteria {
		byName[c.Criterion] = c
	}
	for _, name := range []string{"tone", "safety"} {
		if byName[name].Kappa != 1 || byName[name].N == 0 {
			t.Errorf("%s: %+v", name, byName[name])
		}
		if !strings.Contains(out, `"criterion": "`+name+`"`) { // 输出含 per-criterion κ
			t.Errorf("stdout 缺 per-criterion 条目 %s: %s", name, out)
		}
	}
	if rep.Judge.Model != "claude-sonnet-4-5" || rep.Judge.PromptSHA256 == "" {
		t.Errorf("judge 身份未锁定进报告: %+v", rep.Judge)
	}
	if rep.KappaGate != 0.61 {
		t.Errorf("kappa_gate=%v want 0.61（来自 fixture 配置）", rep.KappaGate)
	}
}

// 已知分歧 fixture（κ=21/53≈0.396<0.61）→ exit 20；safety κ=1。
func TestCalibrateKnownDisagreementExitsTwenty(t *testing.T) {
	code, out, _ := runCalibrateCLI(t, knownDisagreementGold())
	if code != ExitKappaGate {
		t.Fatalf("exit=%d want %d（κ≈0.4 < 0.61）", code, ExitKappaGate)
	}
	var rep CalibrateReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	want := 21.0 / 53.0 // 手算对照值，不复用被测实现
	if rep.MinKappa < want-1e-12 || rep.MinKappa > want+1e-12 {
		t.Errorf("min κ=%v want 21/53=%v", rep.MinKappa, want)
	}
	if rep.Pass {
		t.Error("κ<0.61 不应 Pass")
	}
	for _, c := range rep.Criteria {
		switch c.Criterion {
		case "tone":
			if c.Kappa < want-1e-12 || c.Kappa > want+1e-12 || c.N != 20 {
				t.Errorf("tone κ=%v n=%d want %v/20", c.Kappa, c.N, want)
			}
		case "safety":
			if c.Kappa != 1 || c.N != 2 {
				t.Errorf("safety κ=%v n=%d want 1/2", c.Kappa, c.N)
			}
		}
	}
}

// κ 门禁阈值来自 model.yaml policy.kappa_gate.automation（非硬编码）：
// 同一 κ≈0.396 金标，automation=0.2 → pass/exit 0；automation=0.9 → exit 20；
// 报告 KappaGate 字段记录实际使用的阈值。
func TestCalibrateKappaGateSourcedFromConfig(t *testing.T) {
	gold := knownDisagreementGold()
	t.Run("automation=0.2 放行", func(t *testing.T) {
		model := strings.Replace(testModelYAML, "automation: 0.61", "automation: 0.2", 1)
		code, out, _ := runCalibrateCLIWithModel(t, model, gold)
		if code != ExitOK {
			t.Fatalf("exit=%d want 0（κ≈0.396 ≥ 0.2）", code)
		}
		var rep CalibrateReport
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatal(err)
		}
		if rep.KappaGate != 0.2 || !rep.Pass {
			t.Errorf("kappa_gate=%v pass=%v want 0.2/true", rep.KappaGate, rep.Pass)
		}
	})
	t.Run("automation=0.9 拦截", func(t *testing.T) {
		model := strings.Replace(testModelYAML, "automation: 0.61", "automation: 0.9", 1)
		code, out, _ := runCalibrateCLIWithModel(t, model, gold)
		if code != ExitKappaGate {
			t.Fatalf("exit=%d want %d（κ≈0.396 < 0.9）", code, ExitKappaGate)
		}
		var rep CalibrateReport
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatal(err)
		}
		if rep.KappaGate != 0.9 || rep.Pass {
			t.Errorf("kappa_gate=%v pass=%v want 0.9/false", rep.KappaGate, rep.Pass)
		}
	})
}

// Calibrate 直接以传入阈值判定（非硬编码）：κ=1 金标在 0.8 阈值下 pass，
// 报告记录该阈值。
func TestCalibrateUsesProvidedGate(t *testing.T) {
	rubric, err := ParseRubric([]byte(testRubricYAML), "r1")
	if err != nil {
		t.Fatal(err)
	}
	judge := JudgeConfig{Provider: "anthropic", Model: "claude-sonnet-4-5", Temperature: 0}.Info(rubric)
	rows := []GoldRow{
		{Criterion: "tone", Human: 1, Judge: 1}, {Criterion: "tone", Human: 2, Judge: 2},
		{Criterion: "safety", Human: 3, Judge: 3}, {Criterion: "safety", Human: 1, Judge: 1},
	}
	rep := Calibrate(rubric, judge, rows, 0.8)
	if !rep.Pass || rep.KappaGate != 0.8 || rep.MinKappa != 1 {
		t.Errorf("report=%+v", rep)
	}
}

// gold 校验：未知 criterion / 越界评分 / 缺列 / 未覆盖 criterion → exit 2。
func TestCalibrateGoldValidation(t *testing.T) {
	cases := []struct{ name, gold string }{
		{"未知 criterion", "{\"criterion\": \"nope\", \"human\": 1, \"judge\": 1}\n" + perfectGold},
		{"越界评分", "{\"criterion\": \"tone\", \"human\": 4, \"judge\": 1}\n" + perfectGold},
		{"缺 judge 列", "{\"criterion\": \"tone\", \"human\": 1}\n" + perfectGold},
		{"未覆盖 criterion", "{\"criterion\": \"tone\", \"human\": 1, \"judge\": 1}\n"},
		{"空文件", "\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if code, _, _ := runCalibrateCLI(t, c.gold); code != ExitInput {
				t.Fatalf("exit=%d want %d", code, ExitInput)
			}
		})
	}
}
