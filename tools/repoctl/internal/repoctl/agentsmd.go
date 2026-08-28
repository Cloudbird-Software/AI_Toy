// agentsmd —— agents-md check（spec §3.8 / §7.2 模板）：根 AGENTS.md 存在 +
// packages/<lang>/<pkg> 每包 AGENTS.md 存在且含七个必需小节（小节名允许带后缀，
// 如「技术路径（指导…）」「本包禁令（叠加根 AGENTS.md）」——按小节名子串匹配标题行）。
package repoctl

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AgentSections 包级 AGENTS.md 必需小节（§7.2 模板标记，顺序即报文顺序）。
var AgentSections = [...]string{"本包边界", "技术路径", "本地命令", "本地必绿再提 PR", "数据依赖", "本包禁令", "常见坑"}

// missingAgentSections 返回 text 中缺席的必需小节（标题行 = 去空白后以 # 开头的行）。
func missingAgentSections(text string) []string {
	var heads []string
	for _, ln := range strings.Split(text, "\n") {
		if t := strings.TrimSpace(ln); strings.HasPrefix(t, "#") {
			heads = append(heads, strings.TrimSpace(strings.TrimLeft(t, "#")))
		}
	}
	var miss []string
	for _, s := range AgentSections {
		if !headingCovers(heads, s) {
			miss = append(miss, s)
		}
	}
	return miss
}

func headingCovers(heads []string, section string) bool {
	for _, h := range heads {
		if strings.Contains(h, section) {
			return true
		}
	}
	return false
}

// listPackages 枚举 packages/<lang>/<pkg> 两级目录（隐藏目录跳过），返回相对路径。
func listPackages(root string) ([]string, error) {
	langs, err := os.ReadDir(filepath.Join(root, "packages"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var pkgs []string
	for _, l := range langs {
		if !l.IsDir() || strings.HasPrefix(l.Name(), ".") {
			continue
		}
		subs, err := os.ReadDir(filepath.Join(root, "packages", l.Name()))
		if err != nil {
			return nil, err
		}
		for _, p := range subs {
			if p.IsDir() && !strings.HasPrefix(p.Name(), ".") {
				pkgs = append(pkgs, filepath.ToSlash(filepath.Join("packages", l.Name(), p.Name())))
			}
		}
	}
	sort.Strings(pkgs)
	return pkgs, nil
}

// CheckAgentsMD 返回失败清单（根缺失/包缺失/缺小节）与包数。
func CheckAgentsMD(root string) (fails []string, nPkgs int, err error) {
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); err != nil {
		fails = append(fails, "根 AGENTS.md 缺失")
	}
	pkgs, err := listPackages(root)
	if err != nil {
		return nil, 0, err
	}
	for _, pkg := range pkgs {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(pkg), "AGENTS.md"))
		if err != nil {
			fails = append(fails, pkg+"/AGENTS.md 缺失")
			continue
		}
		if miss := missingAgentSections(string(data)); len(miss) > 0 {
			fails = append(fails, pkg+"/AGENTS.md 缺小节: "+strings.Join(miss, "、"))
		}
	}
	return fails, len(pkgs), nil
}

func cliAgentsMD(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("agents-md check", stderr)
	root := fs.String("root", ".", "仓库根")
	if fs.Parse(args) != nil {
		return ExitInput
	}
	fails, nPkgs, err := CheckAgentsMD(*root)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitInput
	}
	for _, f := range fails {
		fmt.Fprintln(stderr, "agents-md FAIL: "+f)
	}
	fmt.Fprintf(stdout, "agents-md: 根 + %d 个包\n", nPkgs)
	if len(fails) > 0 {
		return ExitViolation
	}
	return ExitOK
}
