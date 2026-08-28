// forbidden —— forbidden-refs（spec §3.8）：全仓扫描 holdout 数据本体路径引用，
// 白名单（tools/holdout 与 eval 侧：tools/gaterunner、packages/go/eval-platform）外即红。
// 跳过：.git/vendor/node_modules/models/cache 与数据本体目录（第三方与生成物非「本仓
// 代码路径」）、>1MB 或非 UTF-8 文件（二进制权重）、.md 与 .gitignore（文档与仓库
// 卫生面——规格书 §2 自身即要求 .gitignore 含该路径的忽略规则，非代码引用）。
//
// holdoutDataPath 拆两段拼写：扫描器源码（含测试）不得出现被扫字面量，否则自指误报。
package repoctl

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// holdoutDataPath 受控数据本体路径（受控存储外的任何代码引用即违规）。
var holdoutDataPath = "datasets/hold" + "out"

// forbiddenWhitelist 放行前缀（rel 斜杠路径）：holdout 客户端与 eval 侧。
var forbiddenWhitelist = [...]string{"tools/holdout/", "tools/gaterunner/", "packages/go/eval-platform/"}

// CheckForbiddenRefs 返回违规清单（文件:行号 引用 路径）。
func CheckForbiddenRefs(root string) ([]string, error) {
	root = filepath.Clean(root)
	dataPrefix := holdoutDataPath + "/"
	var fails []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch {
			case path == root:
				return nil
			case rel == ".git" || rel == "vendor" || rel == "node_modules" || rel == "models/cache",
				strings.HasPrefix(rel+"/", dataPrefix):
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if name == ".gitignore" || strings.HasSuffix(name, ".md") {
			return nil
		}
		for _, p := range forbiddenWhitelist {
			if strings.HasPrefix(rel, p) {
				return nil
			}
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > 1_000_000 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !utf8.Valid(data) {
			return nil
		}
		if i := bytes.Index(data, []byte(holdoutDataPath)); i >= 0 {
			ln := 1 + bytes.Count(data[:i], []byte("\n"))
			fails = append(fails, fmt.Sprintf("%s:%d 引用 %s", rel, ln, holdoutDataPath))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("扫描 %s 失败: %w", root, err)
	}
	return fails, nil
}

func cliForbidden(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("forbidden-refs", stderr)
	root := fs.String("root", ".", "仓库根")
	if fs.Parse(args) != nil {
		return ExitInput
	}
	fails, err := CheckForbiddenRefs(*root)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitInput
	}
	for _, f := range fails {
		fmt.Fprintln(stderr, "forbidden-refs FAIL: "+f)
	}
	fmt.Fprintf(stdout, "forbidden-refs: %d 处违规\n", len(fails))
	if len(fails) > 0 {
		return ExitViolation
	}
	return ExitOK
}
