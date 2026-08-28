// verify-configs 测试（spec §3.1 纪律 1–6）：六条纪律各一测（违反 fixture →
// exit 2 + 错误含规则名）+ schema 校验 + 合法配置通过。统计下限经 evalkit 实算
// （如 6h 零事件→0.4993 ≤ 0.5；n=500 ≥ ZeroFailN(0.97)=99）。
package gaterunner

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// verifyYAML 覆盖全部 5 种 rule 的合法 T4 配置（各纪律用变体破坏）。
const verifyYAML = `asset: T4
name: 唤醒词（测试）
updated: "2026-08-28"
noise_band: {}
gates:
  - {id: T4-G0-01, bi: BI-4.2, level: G0, metric: false_wake_per_hour, op: "<=", threshold: 0.5, src: product, rule: zero_event, min_evidence: {hours: 6}, suite: [ci, nightly]}
  - {id: T4-G1-01, bi: BI-4.1, level: G1, metric: wake_rate_near, op: ">=", threshold: 0.97, src: noise_band, rule: pass_rate, min_evidence: {n: 500}, suite: [ci, nightly]}
  - {id: T4-G1-02, bi: BI-4.1, level: G1, metric: wake_eer, op: "<=", threshold: 0.05, src: benchmark, rule: eer, min_evidence: {min_trials: 5000}, suite: [ci, nightly]}
  - {id: T4-G2-01, bi: BI-4.3, level: G2, metric: adv_asr, op: "<=", threshold: 0.1, src: benchmark, rule: asr, samples_per_attack: 5, report: [mean, best], suite: [ci]}
  - {id: T4-G2-02, bi: BI-4.3, level: G2, metric: rubric_warmth, op: ">=", threshold: 2.5, src: product, rule: metric, suite: [ci, nightly]}
`

const g0Line = "  - {id: T4-G0-01, bi: BI-4.2, level: G0, metric: false_wake_per_hour, op: \"<=\", threshold: 0.5, src: product, rule: zero_event, min_evidence: {hours: 6}, suite: [ci, nightly]}\n"

// 1. 六条纪律各一测（+schema）：违反 fixture → exit 2、错误信息含规则名、单条独立报错。
func TestVerifyConfigsDisciplines(t *testing.T) {
	cases := []struct {
		name           string
		yaml           string
		wantErr        string
		wantSingleViol bool // 变体只应触发一条错误（独立性）
	}{
		{"纪律1 缺 G0 门禁", strings.Replace(verifyYAML, g0Line, "", 1), "G0", true},
		{"纪律1 bi 未映射", strings.Replace(verifyYAML, "BI-4.2", "BI-9.2", 1), "BI-9.2", true},
		{"纪律2 泊松下限不足", strings.Replace(verifyYAML, "hours: 6", "hours: 3", 1), "zero_event", true},
		{"纪律2 min_evidence 未声明", strings.Replace(verifyYAML, "min_evidence: {hours: 6}", "min_evidence: {}", 1), "zero_event", true},
		{"纪律3 样本量不足", strings.Replace(verifyYAML, "n: 500", "n: 50", 1), "pass_rate", true},
		{"纪律4 min_trials 不足", strings.Replace(verifyYAML, "min_trials: 5000", "min_trials: 999", 1), "eer", true},
		{"纪律5 samples_per_attack", strings.Replace(verifyYAML, "samples_per_attack: 5", "samples_per_attack: 2", 1), "asr", true},
		{"纪律5 report 口径", strings.Replace(verifyYAML, "report: [mean, best]", "report: [mean]", 1), "asr", true},
		{"纪律6 src 三选一", strings.Replace(verifyYAML, "src: product", "src: guess", 1), "src", true},
		{"schema level 非法", strings.Replace(verifyYAML, "level: G1, metric: wake_eer", "level: G9, metric: wake_eer", 1), "level", false},
		{"schema 未知字段", strings.Replace(verifyYAML, "threshold: 0.05", "threshold: 0.05, thresholdx: 1", 1), "thresholdx", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "T4.yaml"), c.yaml)
			var stdout, stderr bytes.Buffer
			code := Main([]string{"verify-configs", "--config-dir", dir, "--docs-dir", t.TempDir()}, &stdout, &stderr)
			if code != ExitConfig {
				t.Fatalf("exit=%d, want %d（stdout=%q）", code, ExitConfig, stdout.String())
			}
			if !strings.Contains(stderr.String(), c.wantErr) {
				t.Fatalf("stderr 缺 %q:\n%s", c.wantErr, stderr.String())
			}
			if c.wantSingleViol && strings.Count(stderr.String(), "error: ") != 1 {
				t.Fatalf("须恰好 1 条独立错误，got:\n%s", stderr.String())
			}
		})
	}
}

// 2. 合法配置 → exit 0、零违反。
func TestVerifyConfigsValidPasses(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "T4.yaml"), verifyYAML)
	var stdout, stderr bytes.Buffer
	code := Main([]string{"verify-configs", "--config-dir", dir, "--docs-dir", t.TempDir()}, &stdout, &stderr)
	if code != ExitOK || stderr.Len() > 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "5 门禁，0 违反") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

// 3. id 全仓唯一（跨文件重复 → exit 2）。
func TestVerifyConfigsDuplicateID(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "T4.yaml"), verifyYAML)
	writeFile(t, filepath.Join(dir, "T5.yaml"), strings.Replace(
		strings.Replace(verifyYAML, "asset: T4", "asset: T5", 1), "BI-4.", "BI-5.", -1))
	var stdout, stderr bytes.Buffer
	code := Main([]string{"verify-configs", "--config-dir", dir, "--docs-dir", t.TempDir()}, &stdout, &stderr)
	if code != ExitConfig || !strings.Contains(stderr.String(), "重复") {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
}

// 4. bi 映射校验：资产卡存在时须含该 BI 编号（docs/gates/assets/<T>.md 落盘后语义）。
func TestVerifyConfigsChecksDocsWhenPresent(t *testing.T) {
	dir, docs := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(dir, "T4.yaml"), verifyYAML)
	writeFile(t, filepath.Join(docs, "T4.md"), "# T4 验收协议\nBI-4.1 / BI-4.3\n") // 缺 BI-4.2
	var stdout, stderr bytes.Buffer
	code := Main([]string{"verify-configs", "--config-dir", dir, "--docs-dir", docs}, &stdout, &stderr)
	if code != ExitConfig || !strings.Contains(stderr.String(), "BI-4.2") {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
}
