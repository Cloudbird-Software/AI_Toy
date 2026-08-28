// ledger 趋势台账测试（spec §3.6）——契约先行：29 天 600±10 + 第 30 天 700
// → 该段 >2σ 劣化标红（exit 20）；稳定段不标；窗口只取最近 N 份。
package budgets

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stableP95：base±10，周期 3（模拟夜间噪声带）。
func stableP95(day int, base float64) float64 {
	return base + float64((day%3)-1)*10
}

// entry 构造第 day 天的历史报告；p95ByID 为该日各段实测（按 segIDs 序输出）。
func entry(day int, p95ByID map[string]float64) LatencyReport {
	segs := make([]SegmentSample, 0, len(p95ByID))
	for _, id := range segIDs {
		if v, ok := p95ByID[id]; ok {
			segs = append(segs, SegmentSample{ID: id, P50: v, P95: v})
		}
	}
	return LatencyReport{
		Commit:    fmt.Sprintf("c%04d", day),
		Timestamp: fmt.Sprintf("2026-08-%02dT00:00:00Z", day%28+1),
		Segments:  segs,
	}
}

// stableHistory 生成 n 天全段稳定 ±10 的历史。
func stableHistory(n int) []LatencyReport {
	entries := make([]LatencyReport, 0, n)
	for day := 0; day < n; day++ {
		p95s := make(map[string]float64, len(segIDs))
		for i, id := range segIDs {
			p95s[id] = stableP95(day, segP95[i])
		}
		entries = append(entries, entry(day, p95s))
	}
	return entries
}

// ledgerRun 写 history 文件并跑 `budgets ledger`；days 为空用默认 30。
func ledgerRun(t *testing.T, entries []LatencyReport, days string) (int, string) {
	t.Helper()
	data, err := json.Marshal(HistoryFile{History: &entries})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "latency-history.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"ledger", "--history", path}
	if days != "" {
		args = append(args, "--days", days)
	}
	var out, errBuf bytes.Buffer
	code := Run(args, &out, &errBuf)
	if errBuf.Len() > 0 {
		t.Fatalf("unexpected stderr: %s", errBuf.String())
	}
	return code, out.String()
}

func lineFor(out, segID string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, segID) {
			return line
		}
	}
	return ""
}

// 1. 前 29 天稳定 ±10、第 30 天 700 → 该段标红（exit 20），稳定段不标。
func TestLedgerFlagsDegradedSegment(t *testing.T) {
	day30 := make(map[string]float64, len(segIDs))
	for i, id := range segIDs {
		day30[id] = stableP95(29, segP95[i])
	}
	day30["tail_silence"] = 700
	code, out := ledgerRun(t, append(stableHistory(29), entry(29, day30)), "")
	if code != ExitViolation {
		t.Fatalf("exit = %d, want %d（标红 → 组合级 G1 红）", code, ExitViolation)
	}
	if line := lineFor(out, "tail_silence"); !strings.Contains(line, "红") {
		t.Errorf("tail_silence 行应标红:\n%s", line)
	}
	for _, stable := range segIDs[1:] {
		if line := lineFor(out, stable); strings.Contains(line, "红") {
			t.Errorf("稳定段 %s 不应标红:\n%s", stable, line)
		}
	}
}

// 2. 稳定 30 天：无任何标红（exit 0），各段仍入表；单份历史无基线同样不红。
func TestLedgerStableHistoryHasNoRed(t *testing.T) {
	tests := []struct {
		name    string
		entries []LatencyReport
	}{
		{"稳定 30 天", stableHistory(30)},
		{"单份历史无基线", []LatencyReport{entry(0, map[string]float64{"tail_silence": 600, "transport": 20})}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, out := ledgerRun(t, tc.entries, "")
			if code != ExitOK {
				t.Fatalf("exit = %d, want %d", code, ExitOK)
			}
			if strings.Contains(out, "红") {
				t.Errorf("稳定历史不应标红:\n%s", out)
			}
			if lineFor(out, "tail_silence") == "" {
				t.Errorf("各段应入表:\n%s", out)
			}
		})
	}
}

// 3. 窗口只取最近 N 份：远古尖峰出窗不稀释基线（默认 30 → 红；全量 40 → 不红）。
func TestLedgerWindowTakesLastN(t *testing.T) {
	var entries []LatencyReport
	for day := 0; day < 5; day++ { // 远古尖峰
		entries = append(entries, entry(day, map[string]float64{"tail_silence": 5000}))
	}
	for day := 5; day < 34; day++ {
		entries = append(entries, entry(day, map[string]float64{"tail_silence": stableP95(day, 600)}))
	}
	entries = append(entries, entry(34, map[string]float64{"tail_silence": 700})) // 共 35 份
	if code, _ := ledgerRun(t, entries, ""); code != ExitViolation {
		t.Fatalf("默认 30 份窗口：尖峰出窗 → 红，exit = %d, want %d", code, ExitViolation)
	}
	if code, _ := ledgerRun(t, entries, "40"); code != ExitOK {
		t.Fatalf("全量 35 份：μ 被尖峰抬高 → 不红，exit = %d, want %d", code, ExitOK)
	}
}

// 4. 输入错误（缺文件 / 缺 history 键 / --days<1）→ exit 2。
func TestLedgerInputErrors(t *testing.T) {
	dir := t.TempDir()
	stable3 := stableHistory(3)
	tests := []struct {
		name    string
		entries []LatencyReport
		raw     string
		days    string
	}{
		{"history 不存在", nil, "", ""},
		{"缺 history 键", nil, "[]", ""},
		{"days 为 0", stable3, "", "0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, "nope.json")
			if tc.raw != "" {
				path = writeFile(t, "bad.json", tc.raw)
			} else if tc.entries != nil {
				data, err := json.Marshal(HistoryFile{History: &tc.entries})
				if err != nil {
					t.Fatal(err)
				}
				path = writeFile(t, "history.json", string(data))
			}
			args := []string{"ledger", "--history", path}
			if tc.days != "" {
				args = append(args, "--days", tc.days)
			}
			var out, errBuf bytes.Buffer
			if code := Run(args, &out, &errBuf); code != ExitInput {
				t.Fatalf("exit = %d, want %d", code, ExitInput)
			} else if errBuf.Len() == 0 {
				t.Fatal("expected diagnostic on stderr")
			}
		})
	}
}
