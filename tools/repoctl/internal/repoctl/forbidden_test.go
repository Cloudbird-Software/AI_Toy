// forbidden-refs 契约测试（spec §3.8）：白名单路径→0；packages/ 引用→20；
// 干净仓（含 .md/.gitignore 提及）→0；非 UTF-8 二进制跳过→0。
package repoctl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// holdoutRef 拆两段拼写：本测试源码不得出现被扫字面量（否则对真实仓自指误报）。
const holdoutRef = "datasets/hold" + "out"

// 1. 白名单（tools/holdout、tools/gaterunner、eval 侧包）与数据本体目录自身 → 0。
func TestForbiddenWhitelistOK(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "tools/holdout/client.go"), "var p = \""+holdoutRef+"/real-t4\"\n")
	writeFile(t, filepath.Join(root, "tools/gaterunner/run.go"), "// suite: "+holdoutRef+"\n")
	writeFile(t, filepath.Join(root, "packages/go/eval-platform/registry.go"), "\""+holdoutRef+"\"\n")
	writeFile(t, filepath.Join(root, filepath.FromSlash(holdoutRef+"/sealed-manifest.json")),
		`{"objs": ["`+holdoutRef+`/o1"]}`+"\n")
	code, out, errOut := runRepoctl(t, []string{"forbidden-refs", "--root", root})
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d（stderr: %s）", code, ExitOK, errOut)
	}
	if !strings.Contains(out, "0 处违规") || errOut != "" {
		t.Errorf("summary = %q, stderr = %q", out, errOut)
	}
}

// 2. packages/ 训练代码引用 → 20，报文含 文件:行号 与被引用路径。
func TestForbiddenPackageRef(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "packages/go/kws/train.go"),
		"package kws\n\nfunc train() { open(\""+holdoutRef+"/train.wav\") }\n")
	code, _, errOut := runRepoctl(t, []string{"forbidden-refs", "--root", root})
	if code != ExitViolation || !strings.Contains(errOut, "packages/go/kws/train.go:3 引用 "+holdoutRef) {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
}

// 3. 干净仓：代码无引用；.md 与 .gitignore 提及该路径（文档/卫生面，非代码引用）→ 0。
func TestForbiddenCleanRepo(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "packages/go/kws/src/a.go"), "package kws\n")
	writeFile(t, filepath.Join(root, "docs/gates/assets/T4.md"), "# T4\n"+holdoutRef+" 经 tools 侧。\n")
	writeFile(t, filepath.Join(root, ".gitignore"), holdoutRef+"/*\n")
	code, _, errOut := runRepoctl(t, []string{"forbidden-refs", "--root", root})
	if code != ExitOK || errOut != "" {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
}

// 4. 边界：含模式字节的非 UTF-8 二进制（量化权重形态）跳过 → 0。
func TestForbiddenSkipsBinary(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "packages/go/kws/model.bin")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append([]byte(holdoutRef), 0xff, 0xfe, 0x00), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runRepoctl(t, []string{"forbidden-refs", "--root", root})
	if code != ExitOK || errOut != "" {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
}
