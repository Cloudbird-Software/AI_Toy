// exemption audit 契约测试（spec §3.8 / §9.3）：无过期→0；过期→20；
// 边界：expiry 别名 + 当天未过期→0、空台账→0、台账缺失→2、非法日期→20。
package repoctl

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func exemptRun(t *testing.T, yamlText string) (int, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "reports", "exemptions.yaml")
	writeFile(t, path, yamlText)
	code, _, errOut := runRepoctl(t, []string{"exemption", "audit", "--file", path})
	return code, errOut
}

// 1. 未过期（expires 键 = gaterunner 台账 schema）→ 0。
func TestExemptionNoExpiry(t *testing.T) {
	code, errOut := exemptRun(t, "- id: T4-G1-03\n  reason: 等复测\n  expires: 2099-01-01\n")
	if code != ExitOK || errOut != "" {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
}

// 2. 过期 → 20，报文含 id 与过期日。
func TestExemptionExpired(t *testing.T) {
	code, errOut := exemptRun(t, "- id: T4-G1-03\n  expires: 2001-01-01\n  reason: r\n")
	if code != ExitViolation || !strings.Contains(errOut, "T4-G1-03: 已过期 2001-01-01") {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
}

// 3. 边界：expiry 别名 + 当天日期未过期 → 0；空台账 → 0。
func TestExemptionTodayAndEmpty(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")
	if code, errOut := exemptRun(t, "- id: X\n  expiry: "+today+"\n  reason: r\n"); code != ExitOK || errOut != "" {
		t.Fatalf("当天: exit = %d, stderr = %q", code, errOut)
	}
	if code, errOut := exemptRun(t, ""); code != ExitOK || errOut != "" {
		t.Fatalf("空台账: exit = %d, stderr = %q", code, errOut)
	}
}

// 4. 边界（负）：台账缺失 → 2；日期非法 → 20。
func TestExemptionMissingFileAndBadDate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.yaml")
	code, _, errOut := runRepoctl(t, []string{"exemption", "audit", "--file", path})
	if code != ExitInput || !strings.Contains(errOut, "不存在") {
		t.Fatalf("缺失: exit = %d, stderr = %q", code, errOut)
	}
	if code, errOut := exemptRun(t, "- id: Y\n  expires: not-a-date\n"); code != ExitViolation ||
		!strings.Contains(errOut, "expiry 非法") {
		t.Fatalf("非法日期: exit = %d, stderr = %q", code, errOut)
	}
}
