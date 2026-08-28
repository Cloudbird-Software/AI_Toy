// collect 测试（spec §3.1/§6）：Mark 注册约定（运行期登记 + 源码扫描）、
// 登记表 = Mark 源码扫描 × configs/gates 合并、隐藏目录/vendor 跳过、
// configs/gates 缺失（W3 卡前）不构成错误。
package gaterunner

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// 1. Mark 运行期登记（进程内登记表，race-safe）。
func TestMarkRegistersInProcess(t *testing.T) {
	before := len(InProcessRegistry())
	Mark(t, "T9", "BI-9.1", "T9-G0-01", "G0")
	reg := InProcessRegistry()
	if len(reg) != before+1 {
		t.Fatalf("登记表长度=%d, want %d", len(reg), before+1)
	}
	e := reg[len(reg)-1]
	if e.ID != "T9-G0-01" || e.BI != "BI-9.1" || e.Asset != "T9" || e.Level != "G0" {
		t.Fatalf("登记行=%+v", e)
	}
}

// 2. Mark 非法参数 panic（对齐 evalkit 纪律：非法输入即失败）。
func TestMarkPanicsOnInvalidArgs(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("非法 level 须 panic")
		}
	}()
	Mark(t, "T4", "BI-4.2", "T4-G0-01", "G9")
}

// 3. collect：fixture 测试文件含 Mark 注册 → 登记表行正确（合并 configs/gates、
// 跳过 .git/vendor、Mark-only 行 metric 为 "-"）。
func TestCollectRegistryRows(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "configs", "gates", "T4.yaml"), runYAML)
	writeFile(t, filepath.Join(root, "pkg", "gates_test.go"), gatesTestGo)
	writeFile(t, filepath.Join(root, "sub", "more_test.go"), strings.Replace(gatesTestGo,
		"T4-G0-01", "T4-G0-03", 1))
	// 隐藏目录与 vendor 内的注册不得进入登记表。
	writeFile(t, filepath.Join(root, ".git", "hid_test.go"), gatesTestGo)
	writeFile(t, filepath.Join(root, "vendor", "v_test.go"), gatesTestGo)

	rows := collectRows(t, root, filepath.Join(root, "configs", "gates"))
	if len(rows) != 5 {
		t.Fatalf("登记行数=%d, want 5（3 配置门禁 + 2 个仅 Mark 注册）: %v", len(rows), rows)
	}
	want := map[string][]string{
		// id, bi, level, asset, metric, test, source, suite
		"T4-G0-01": {"T4-G0-01", "BI-4.2", "G0", "T4", "false_wake_per_hour", "TestG0FalseWake", "pkg/gates_test.go", "ci"},
		"T4-G0-02": {"T4-G0-02", "BI-4.2", "G0", "T4", "-", "TestG0AdvNegative", "pkg/gates_test.go", "-"},
		"T4-G0-03": {"T4-G0-03", "BI-4.2", "G0", "T4", "-", "TestG0FalseWake", "sub/more_test.go", "-"},
		"T4-G1-01": {"T4-G1-01", "BI-4.1", "G1", "T4", "wake_rate_near", "-", "-", "ci"},
		"T4-G2-01": {"T4-G2-01", "BI-4.3", "G2", "T4", "warm_rubric", "-", "-", "ci"},
	}
	for id, w := range want {
		if got := rows[id]; !equalRows(got, w) {
			t.Errorf("登记行 %s = %v, want %v", id, got, w)
		}
	}

	// configs/gates 缺失（W3 卡落盘前）→ 仅 Mark 行，exit 0。
	rows = collectRows(t, root, filepath.Join(root, "no-such-dir"))
	if len(rows) != 3 {
		t.Fatalf("无配置目录时登记行数=%d, want 3: %v", len(rows), rows)
	}
	if _, ok := rows["T4-G0-01"]; !ok {
		t.Fatalf("无配置目录时缺 Mark 行: %v", rows)
	}
}

// 4. 坏配置（不可解析）→ exit 2。
func TestCollectBadConfigExitsConfig(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pkg", "gates_test.go"), gatesTestGo)
	writeFile(t, filepath.Join(root, "configs", "gates", "T4.yaml"), "asset: T4\ngates: [{id: X}\n")
	var stdout, stderr bytes.Buffer
	code := Main([]string{"collect", "--root", root, "--config-dir", filepath.Join(root, "configs", "gates")}, &stdout, &stderr)
	if code != ExitConfig || !strings.Contains(stderr.String(), "不可解析") {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
}

func collectRows(t *testing.T, root, configDir string) map[string][]string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Main([]string{"collect", "--root", root, "--config-dir", configDir}, &stdout, &stderr)
	if code != ExitOK || stderr.Len() > 0 {
		t.Fatalf("collect exit=%d stderr=%q", code, stderr.String())
	}
	rows := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if line == "" {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) != 8 {
			t.Fatalf("登记行列数=%d: %q", len(cols), line)
		}
		rows[cols[0]] = cols
	}
	return rows
}

func equalRows(a, b []string) bool {
	return len(a) == len(b) && strings.Join(a, "\x00") == strings.Join(b, "\x00")
}
