// collect —— 断言登记表（spec §3.1/§6）：扫描 *_test.go 中的 gaterunner.Mark 注册
// 调用 × configs/gates 合并；输出 TSV（列序：id, bi, level, asset, metric, test,
// source, suite；缺席列以 "-" 占位）。行数即断言数（`collect | wc -l` ≥ 70 条）。
package gaterunner

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
)

// RegEntry 登记表一行（Mark 源码扫描 × configs/gates 合并结果）。
type RegEntry struct {
	ID, BI, Asset, Level string
	Metric, Test, Source string
	Suite                string
}

var (
	regMu sync.Mutex
	// inProcess 为运行期 Mark 的进程内登记表（供未来进程内收集/审计，collect 用源码扫描）。
	inProcess []RegEntry
)

// Mark 在门禁测试内登记断言（spec §6 开发循环第 3 步）：
// gaterunner.Mark(t, "T4", "BI-4.2", "T4-G0-01", "G0")——Go 无原生 marker，
// collect 以源码扫描收集本调用；非法参数 panic（对齐 evalkit 纪律）。
func Mark(t testing.TB, asset, bi, id, level string) {
	t.Helper()
	if asset == "" || bi == "" || id == "" || !validLevel(level) {
		panic(fmt.Sprintf("gaterunner.Mark: 须为 (t, asset, bi, id, level∈{G0,G1,G2}) 非空，got (%q,%q,%q,%q)", asset, bi, id, level))
	}
	regMu.Lock()
	defer regMu.Unlock()
	inProcess = append(inProcess, RegEntry{ID: id, BI: bi, Asset: asset, Level: level})
}

// InProcessRegistry 返回运行期 Mark 登记快照。
func InProcessRegistry() []RegEntry {
	regMu.Lock()
	defer regMu.Unlock()
	return append([]RegEntry{}, inProcess...)
}

func validLevel(level string) bool { return level == "G0" || level == "G1" || level == "G2" }

var (
	testFuncRe = regexp.MustCompile(`^func\s+(?:\([^)]*\)\s+)?(Test\w+)\s*\(`)
	// markRe 匹配单行注册调用（约定：Mark 参数分行写时须收回单行）：
	// gaterunner.Mark(t, "T4", "BI-4.2", "T4-G0-01", "G0")
	markRe = regexp.MustCompile(`gaterunner\.Mark\(\s*\w+\s*,\s*"([^"]*)"\s*,\s*"([^"]*)"\s*,\s*"([^"]*)"\s*,\s*"([^"]*)"\s*\)`)
)

// ScanMarks 扫描 root 下全部 *_test.go 的 Mark 注册调用（跳过 .开头目录、vendor、
// node_modules），返回带测试函数与源文件位置的登记行。
func ScanMarks(root string) ([]RegEntry, error) {
	var entries []RegEntry
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, "_test.go") {
			return nil
		}
		ms, err := scanMarkFile(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, ms...)
		return nil
	})
	if err != nil {
		return nil, inputErrorf("扫描 %s 失败: %v", root, err)
	}
	return entries, nil
}

func scanMarkFile(root, path string) ([]RegEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, err
	}
	var entries []RegEntry
	curTest := ""
	for _, line := range strings.Split(string(data), "\n") {
		if m := testFuncRe.FindStringSubmatch(line); m != nil {
			curTest = m[1]
			continue
		}
		if m := markRe.FindStringSubmatch(line); m != nil {
			entries = append(entries, RegEntry{Asset: m[1], BI: m[2], ID: m[3],
				Level: m[4], Test: curTest, Source: filepath.ToSlash(rel)})
		}
	}
	return entries, nil
}

// BuildRegistry 合并 Mark 源码扫描与 configs/gates（按 id 并集；配置侧补 metric/suite，
// Mark 侧补 test/source），按 id 排序。配置不可解析 → 配置错误。
func BuildRegistry(root, configDir string) ([]RegEntry, error) {
	byID := map[string]*RegEntry{}
	marks, err := ScanMarks(root)
	if err != nil {
		return nil, err
	}
	for _, m := range marks {
		if _, dup := byID[m.ID]; dup {
			continue // 同 id 重复注册：保留先扫描到的（词序首个），保持登记表按 id 去重。
		}
		e := m
		byID[m.ID] = &e
	}
	for _, path := range ListConfigs(configDir) {
		c, err := LoadAssetConfig(path)
		if err != nil {
			return nil, err
		}
		for _, g := range c.Gates {
			e, ok := byID[g.ID]
			if !ok {
				e = &RegEntry{ID: g.ID}
				byID[g.ID] = e
			}
			e.BI, e.Asset, e.Level = g.BI, c.Asset, g.Level
			e.Metric, e.Suite = g.Metric, strings.Join(g.Suite, ",")
		}
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	rows := make([]RegEntry, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, *byID[id])
	}
	return rows, nil
}

// EmitRegistry 以 TSV 打印登记表。
func EmitRegistry(rows []RegEntry, w io.Writer) error {
	for _, r := range rows {
		cols := []string{r.ID, r.BI, r.Level, r.Asset, r.Metric, r.Test, r.Source, r.Suite}
		for i, c := range cols {
			if c == "" {
				cols[i] = "-"
			}
		}
		if _, err := fmt.Fprintln(w, strings.Join(cols, "\t")); err != nil {
			return err
		}
	}
	return nil
}
