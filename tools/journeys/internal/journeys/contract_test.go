// 契约测试：journeys run CLI（spec §3.5；测试设计承接 PR #19）。
package journeys

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const coreYAML = `id: J01-morning
tier: core
persona: {age: 7, patience: high}
steps: [say, wait, close]
inject: {interrupts: [], safety_events: []}
assertions:
  - {metric: completion_rate, op: '>=', value: 0.0}
  - {metric: safety_events, op: '<=', value: 100}
  - {metric: latency_p95_ms, op: '<=', value: 100000}
  - {metric: memory_hit_rate, op: '>=', value: 0.0}
`

const variantYAML = `id: J05-comfort-crisis
tier: variant
persona: {age: 4, patience: low}
steps: [say, inject_crisis, wait]
inject: {interrupts: [{at_step: 2, kind: user_interrupt}], safety_events: [{kind: crisis_metaphor}]}
assertions:
  - {metric: completion_rate, op: '>=', value: 0.0}
  - {metric: safety_events, op: '<=', value: 100}
`

// runCLI 把剧本写入临时目录并执行 journeys run；--driver 故意指向不存在的路径，
// 证明桩阶段不触碰 driver。
func runCLI(t *testing.T, scripts map[string]string, extra ...string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	for name, text := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	args := append([]string{"run", "--set", "golden", "--seeds", "3",
		"--driver", "no-such-driver", "--scripts-dir", dir}, extra...)
	var stdout, stderr bytes.Buffer
	code := Main(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestRunGoldenSetEmitsJSONWithFourMetrics(t *testing.T) {
	code, out, errMsg := runCLI(t, map[string]string{"J01.yaml": coreYAML, "J02.yaml": variantYAML})
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, errMsg)
	}
	for _, name := range metricNames {
		if !strings.Contains(out, `"`+name+`"`) {
			t.Errorf("report JSON missing metric %q", name)
		}
	}
	var rep Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("stdout is not JSON: %v", err)
	}
	if rep.Set != "golden" || rep.Seeds != 3 {
		t.Fatalf("set=%q seeds=%d", rep.Set, rep.Seeds)
	}
	tiersByID := map[string]string{}
	for _, j := range rep.Journeys {
		tiersByID[j.ID] = j.Tier
	}
	if len(tiersByID) != 2 || tiersByID["J01-morning"] != "core" || tiersByID["J05-comfort-crisis"] != "variant" {
		t.Fatalf("journeys=%v", tiersByID)
	}
	for _, j := range rep.Journeys {
		if len(j.Runs) != 3 {
			t.Fatalf("%s: %d runs, want 3", j.ID, len(j.Runs))
		}
		for want, r := range j.Runs {
			if r.Seed != want {
				t.Errorf("%s: runs[%d].Seed=%d", j.ID, want, r.Seed)
			}
		}
		for _, a := range j.Assertions {
			if !a.Pass {
				t.Errorf("%s: %s should pass (observed=%v)", j.ID, a.Metric, a.Observed)
			}
		}
		if j.Verdict != "pass" {
			t.Errorf("%s: verdict=%q", j.ID, j.Verdict)
		}
	}
	if rep.Summary.Overall != "pass" || rep.Summary.JourneysTotal != 2 || rep.Summary.Fail != 0 {
		t.Fatalf("summary=%+v", rep.Summary)
	}
}

func TestSchemaValidationFailuresExitConfig(t *testing.T) {
	cases := []struct{ name, yamlText, wantErr string }{
		{"missing id", strings.Replace(coreYAML, "id: J01-morning\n", "", 1), "id"},
		{"missing tier", strings.Replace(coreYAML, "tier: core\n", "", 1), "tier"},
		{"tier core2", strings.Replace(coreYAML, "tier: core", "tier: core2", 1), "core2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, _, errMsg := runCLI(t, map[string]string{"bad.yaml": c.yamlText})
			if code != ExitConfig || !strings.Contains(errMsg, c.wantErr) {
				t.Fatalf("exit=%d stderr=%q, want exit %d containing %q", code, errMsg, ExitConfig, c.wantErr)
			}
		})
	}
}

func TestImpossibleAssertionFailsVerdictAndExit(t *testing.T) {
	// completion_rate >= 1.01 不可能满足（均值上界 1.0）。
	// IR #72 后默认模拟态失败阶段化为 DEBT exit 0，故经 --strict 恢复旧 fail→1 语义。
	bad := strings.Replace(coreYAML, "value: 0.0}", "value: 1.01}", 1)
	code, out, _ := runCLI(t, map[string]string{"J01.yaml": bad}, "--strict")
	if code != ExitFail {
		t.Fatalf("exit=%d, want %d", code, ExitFail)
	}
	var rep Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	j := rep.Journeys[0]
	if j.Verdict != "fail" {
		t.Fatalf("verdict=%q", j.Verdict)
	}
	var failed []string
	for _, a := range j.Assertions {
		if !a.Pass {
			failed = append(failed, a.Metric)
		}
	}
	if len(failed) != 1 || failed[0] != "completion_rate" {
		t.Fatalf("failed assertions=%v", failed)
	}
	if rep.Summary.Overall != "fail" || len(rep.Summary.FailIDs) != 1 || rep.Summary.FailIDs[0] != j.ID {
		t.Fatalf("summary=%+v", rep.Summary)
	}
}

func TestOutFlagWritesReportToFile(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "report.json")
	code, out, _ := runCLI(t, map[string]string{"J01.yaml": coreYAML}, "--out", outFile)
	if code != ExitOK || strings.TrimSpace(out) != "" {
		t.Fatalf("exit=%d stdout=%q", code, out)
	}
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	var rep Report
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Set != "golden" {
		t.Fatalf("set=%q", rep.Set)
	}
}
