// coverage —— 登记表 × 验收文档 BI 核对（spec §3.8，IR #64 阶段化执法，ADR-0002）：
// 登记表 = reports/gates/*.json 中 verdict ∈ {pass, fail, exempt, debt} 的条目
// （not_implemented/warn 不算已落地断言；debt=已注册测试接线、处部分实现态——
// IR #76）× docs/gates/assets/*.md 的 BI 集合（正则 BI-\d+\.\d+）→ 资产登记
// ≥1 条 → 强制全 BI 覆盖 + ≥1 G0 + 无孤儿断言（任一缺失 exit 20）；登记 0 条 →
// DEBT 行（stdout，不 FAIL，exit 0——实现未开始的资产先欠账、首条断言落地即
// 进入全执法）。
package repoctl

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// covEntry 登记表一行（gaterunner run 报告 Result 的核对子集；asset 条目级优先、文件级兜底）。
type covEntry struct {
	ID      string `json:"id"`
	BI      string `json:"bi"`
	Level   string `json:"level"`
	Asset   string `json:"asset"`
	Verdict string `json:"verdict"`
}

var biRe = regexp.MustCompile(`BI-\d+\.\d+`)

// countedVerdict 报告形态条目是否计入登记表：pass/fail/exempt/debt（与 verdict
// 缺席的旧式条目）算已落地；not_implemented/warn 不算（未实现/趋势警告非断言
// 事实）。debt（IR #76）：该 BI 已有真实测试接线（t.Skip 通道），处部分实现态。
func countedVerdict(v string) bool {
	return v == "" || v == "pass" || v == "fail" || v == "exempt" || v == "debt"
}

// loadCovRegistry 读 gatesDir 下全部 *.json：顶层列表，或 {asset, results|assertions}
// 两种对象形态（后者即 gaterunner run 报告 schema，按 verdict 过滤）。目录缺席 =
// 空登记表（非错误）。
func loadCovRegistry(gatesDir string) ([]covEntry, error) {
	files, _ := filepath.Glob(filepath.Join(gatesDir, "*.json"))
	sort.Strings(files)
	entries := []covEntry{}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var list []covEntry
		if err := json.Unmarshal(data, &list); err == nil {
			entries = append(entries, list...)
			continue
		}
		var doc struct {
			Asset      string     `json:"asset"`
			Results    []covEntry `json:"results"`
			Assertions []covEntry `json:"assertions"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("%s 不可解析: %w", path, err)
		}
		for _, e := range append(doc.Results, doc.Assertions...) {
			if !countedVerdict(e.Verdict) {
				continue
			}
			if e.Asset == "" {
				e.Asset = doc.Asset
			}
			entries = append(entries, e)
		}
	}
	return entries, nil
}

// loadDocBIs 读 docsDir 下全部 *.md（文件名去后缀 = 资产 id）→ BI 集合（已排序）。
func loadDocBIs(docsDir string) (map[string][]string, error) {
	files, _ := filepath.Glob(filepath.Join(docsDir, "*.md"))
	sort.Strings(files)
	docs := map[string][]string{}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		set := map[string]bool{}
		for _, m := range biRe.FindAllString(string(data), -1) {
			set[m] = true
		}
		bis := make([]string, 0, len(set))
		for bi := range set {
			bis = append(bis, bi)
		}
		sort.Strings(bis)
		docs[strings.TrimSuffix(filepath.Base(path), ".md")] = bis
	}
	return docs, nil
}

// CheckCoverage 核对（阶段化执法，ADR-0002）：有登记的资产强制全 BI 覆盖 + ≥1 G0 +
// 无孤儿断言（失败清单排序去重）；0 登记资产进 DEBT 清单（不 FAIL）。返回
// （失败清单、DEBT 清单、资产数、断言数）。
func CheckCoverage(root string) (fails, debts []string, nAssets, nEntries int, err error) {
	docs, err := loadDocBIs(filepath.Join(root, "docs", "gates", "assets"))
	if err != nil {
		return nil, nil, 0, 0, err
	}
	all, err := loadCovRegistry(filepath.Join(root, "reports", "gates"))
	if err != nil {
		return nil, nil, 0, 0, err
	}
	entries := []covEntry{}
	for _, e := range all { // 无 asset 的登记行无从核对，跳过（对齐 Python 契约）
		if e.Asset != "" {
			entries = append(entries, e)
		}
	}
	assets := make([]string, 0, len(docs))
	for a := range docs {
		assets = append(assets, a)
	}
	sort.Strings(assets)
	for _, asset := range assets {
		var own []covEntry
		hasG0 := false
		for _, e := range entries {
			if e.Asset != asset {
				continue
			}
			own = append(own, e)
			if strings.EqualFold(e.Level, "G0") {
				hasG0 = true
			}
		}
		if len(own) == 0 { // 阶段化执法：0 断言 = DEBT（实现未开始，不 FAIL）
			debts = append(debts, asset+": 实现未开始（0 断言）")
			continue
		}
		for _, bi := range docs[asset] {
			if !slices.ContainsFunc(own, func(e covEntry) bool { return e.BI == bi }) {
				fails = append(fails, fmt.Sprintf("%s: BI %s 无任何断言", asset, bi))
			}
		}
		if !hasG0 {
			fails = append(fails, asset+": 缺 G0 断言")
		}
	}
	for _, e := range entries {
		if !slices.Contains(docs[e.Asset], e.BI) { // 资产无文档或 BI 未收录 → 孤儿
			fails = append(fails, fmt.Sprintf("孤儿断言: %s/%s (%s)", e.Asset, e.BI, e.ID))
		}
	}
	sort.Strings(fails)
	dedup := fails[:0]
	for i, f := range fails {
		if i == 0 || f != fails[i-1] {
			dedup = append(dedup, f)
		}
	}
	return dedup, debts, len(docs), len(entries), nil
}

func cliCoverage(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("coverage", stderr)
	root := fs.String("root", ".", "仓库根（docs/ 与 reports/ 相对它）")
	if fs.Parse(args) != nil {
		return ExitInput
	}
	fails, debts, nAssets, nEntries, err := CheckCoverage(*root)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitInput
	}
	for _, d := range debts {
		_, _ = fmt.Fprintln(stdout, "coverage DEBT: "+d)
	}
	for _, f := range fails {
		_, _ = fmt.Fprintln(stderr, "coverage FAIL: "+f)
	}
	summary := fmt.Sprintf("coverage: %d 资产 / %d 断言", nAssets, nEntries)
	if len(debts) > 0 { // 全齐时不出现 DEBT 字样；有欠账才显式计数
		summary += fmt.Sprintf("（%d DEBT）", len(debts))
	}
	_, _ = fmt.Fprintln(stdout, summary)
	if len(fails) > 0 {
		return ExitViolation
	}
	return ExitOK
}
