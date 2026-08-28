// exemption —— exemption audit（spec §3.8 / §9.3）：reports/exemptions.yaml 过期项 → 20。
// 台账为对象列表；期限键兼容 expiry（任务书命名）与 expires（gaterunner LoadExemptions
// 的 schema，两工具共用同一台账文件），缺席/非法日期 = 台账纪律违规（20）。
package repoctl

import (
	"fmt"
	"io"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// ExemptionEntry 台账一行（id/期限/reason；owner/linked_pr 等字段容忍共存，不校验）。
type ExemptionEntry struct {
	ID      string `yaml:"id"`
	Expiry  string `yaml:"expiry"`
	Expires string `yaml:"expires"`
	Reason  string `yaml:"reason"`
}

func (e ExemptionEntry) deadline() string {
	if e.Expiry != "" {
		return e.Expiry
	}
	return e.Expires
}

// AuditExemptions 返回过期/非法项清单与台账条数。台账缺失 → 错误（exit 2）。
func AuditExemptions(path string) (fails []string, n int, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, fmt.Errorf("豁免台账不存在: %s", path)
		}
		return nil, 0, err
	}
	var entries []ExemptionEntry
	if err := yaml.Unmarshal(data, &entries); err != nil {
		return nil, 0, fmt.Errorf("豁免台账不可解析: %s: %w", path, err)
	}
	today := time.Now().UTC().Format("2006-01-02")
	for _, e := range entries {
		exp := e.deadline()
		if _, perr := time.Parse("2006-01-02", exp); perr != nil {
			fails = append(fails, fmt.Sprintf("%s: expiry 非法 %q", e.ID, exp))
			continue
		}
		if exp < today { // ISO 日期字典序即时间序；当天未过期（对齐 Python 契约）
			fails = append(fails, fmt.Sprintf("%s: 已过期 %s", e.ID, exp))
		}
	}
	return fails, len(entries), nil
}

func cliExemption(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("exemption audit", stderr)
	file := fs.String("file", "reports/exemptions.yaml", "豁免台账 YAML")
	if fs.Parse(args) != nil {
		return ExitInput
	}
	fails, n, err := AuditExemptions(*file)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitInput
	}
	for _, f := range fails {
		fmt.Fprintln(stderr, "exemption audit FAIL: "+f)
	}
	fmt.Fprintf(stdout, "exemption audit: %d 项豁免, %d 过期\n", n, len(fails))
	if len(fails) > 0 {
		return ExitViolation
	}
	return ExitOK
}
