// coverage 契约测试（spec §3.8，对齐 Python 版 PR #22）：全齐→0；缺 BI/缺 G0→20；
// 孤儿断言→20；坏 JSON→2；双空（无文档无登记表）→0。
package repoctl

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

const covDoc = "# T4 唤醒词\n\n| 断言 | BI |\n|---|---|\n| T4-G0-01 | BI-4.2 |\n| T4-G1-01 | BI-4.1 |\n"

// covSetup 落一份 gaterunner run 报告形态的登记表（{"asset": "T4", "results": [...]}）
// 与对应验收文档，返回 coverage 参数。
func covSetup(t *testing.T, entries ...map[string]string) []string {
	t.Helper()
	root := t.TempDir()
	items := make([]map[string]string, len(entries))
	copy(items, entries)
	data, err := json.Marshal(map[string]any{"asset": "T4", "results": items})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "reports/gates/T4.json"), string(data))
	writeFile(t, filepath.Join(root, "docs/gates/assets/T4.md"), covDoc)
	return []string{"coverage", "--root", root}
}

func covRow(id, bi, level string) map[string]string {
	return map[string]string{"id": id, "bi": bi, "level": level}
}

// 1. 全齐：doc 两个 BI 各有断言且含 G0 → 0。
func TestCoverageAllPresent(t *testing.T) {
	code, out, errOut := runRepoctl(t, covSetup(t,
		covRow("T4-G0-01", "BI-4.2", "G0"),
		covRow("T4-G1-01", "BI-4.1", "G1")))
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d（stderr: %s）", code, ExitOK, errOut)
	}
	if !strings.Contains(out, "1 资产 / 2 断言") || errOut != "" {
		t.Errorf("summary = %q, stderr = %q", out, errOut)
	}
}

// 2. 缺 BI（BI-4.1 无断言）→ 20；断言全 G1（无 G0）→ 20。
func TestCoverageMissingBIAndG0(t *testing.T) {
	code, _, errOut := runRepoctl(t, covSetup(t, covRow("T4-G0-01", "BI-4.2", "G0")))
	if code != ExitViolation || !strings.Contains(errOut, "T4: BI BI-4.1 无任何断言") {
		t.Fatalf("缺 BI: exit = %d, stderr = %q", code, errOut)
	}
	code, _, errOut = runRepoctl(t, covSetup(t,
		covRow("T4-G0-01", "BI-4.2", "G1"),
		covRow("T4-G1-01", "BI-4.1", "G1")))
	if code != ExitViolation || !strings.Contains(errOut, "T4: 缺 G0 断言") {
		t.Fatalf("缺 G0: exit = %d, stderr = %q", code, errOut)
	}
}

// 3. 孤儿断言（BI 未收录进文档）→ 20。
func TestCoverageOrphanAssertion(t *testing.T) {
	code, _, errOut := runRepoctl(t, covSetup(t,
		covRow("T4-G0-01", "BI-4.2", "G0"),
		covRow("T4-G1-01", "BI-4.1", "G1"),
		covRow("T4-G1-09", "BI-4.9", "G1")))
	if code != ExitViolation || !strings.Contains(errOut, "孤儿断言: T4/BI-4.9 (T4-G1-09)") {
		t.Fatalf("孤儿: exit = %d, stderr = %q", code, errOut)
	}
}

// 4. 边界：登记表 JSON 不可解析 → 2；无文档无登记表 → 0（无契约可查）。
func TestCoverageInputErrorAndVacuous(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "reports/gates/T4.json"), "{not json")
	if code, _, _ := runRepoctl(t, []string{"coverage", "--root", root}); code != ExitInput {
		t.Fatalf("坏 JSON: exit = %d, want %d", code, ExitInput)
	}
	empty := t.TempDir()
	code, out, _ := runRepoctl(t, []string{"coverage", "--root", empty})
	if code != ExitOK || !strings.Contains(out, "0 资产 / 0 断言") {
		t.Fatalf("双空: exit = %d, out = %q", code, out)
	}
}
