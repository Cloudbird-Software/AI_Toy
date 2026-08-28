// coverage —— 登记表 × 验收文档 BI 三重核对（spec §3.8）：reports/gates/*.json
// （gaterunner 登记表落盘形态）× docs/gates/assets/*.md 的 BI 集合（正则 BI-\d+\.\d+）
// → 每 BI ≥1 断言、每资产 ≥1 G0、无孤儿断言；任一缺失 exit 20。
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
	ID    string `json:"id"`
	BI    string `json:"bi"`
	Level string `json:"level"`
	Asset string `json:"asset"`
}

var biRe = regexp.MustCompile(`BI-\d+\.\d+`)

// loadCovRegistry 读 gatesDir 下全部 *.json：顶层列表，或 {asset, results|assertions}
// 两种对象形态（后者即 gaterunner run 报告 schema）。目录缺席 = 空登记表（非错误）。
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

// CheckCoverage 三重核对，返回失败清单（排序去重）与（资产数、断言数）。
func CheckCoverage(root string) (fails []string, nAssets, nEntries int, err error) {
	docs, err := loadDocBIs(filepath.Join(root, "docs", "gates", "assets"))
	if err != nil {
		return nil, 0, 0, err
	}
	all, err := loadCovRegistry(filepath.Join(root, "reports", "gates"))
	if err != nil {
		return nil, 0, 0, err
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
	return dedup, len(docs), len(entries), nil
}

func cliCoverage(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("coverage", stderr)
	root := fs.String("root", ".", "仓库根（docs/ 与 reports/ 相对它）")
	if fs.Parse(args) != nil {
		return ExitInput
	}
	fails, nAssets, nEntries, err := CheckCoverage(*root)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitInput
	}
	for _, f := range fails {
		fmt.Fprintln(stderr, "coverage FAIL: "+f)
	}
	fmt.Fprintf(stdout, "coverage: %d 资产 / %d 断言\n", nAssets, nEntries)
	if len(fails) > 0 {
		return ExitViolation
	}
	return ExitOK
}
