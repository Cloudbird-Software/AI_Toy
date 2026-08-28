// 共享测试助手：写文件（自动建父目录）与内存内跑 CLI（不落进程）。
package repoctl

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runRepoctl 跑 CLI（argv 不含程序名），返回退出码与 stdout/stderr。
func runRepoctl(t *testing.T, argv []string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code = Run(argv, &out, &errBuf)
	return code, out.String(), errBuf.String()
}
