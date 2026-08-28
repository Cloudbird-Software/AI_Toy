// fetch-models 契约测试（spec §3.8，桩：file:// 源）：sha256 匹配→0 且落缓存；
// 不匹配→20 且坏权重不落缓存；源缺失→2；字段非法/dest 越界→2。
package repoctl

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var fetchContent = []byte("wake-weights\x00\x01")

// fetchSetup 写一份单权重清单（sha/source 可覆盖），返回 CLI 参数与缓存目录。
func fetchSetup(t *testing.T, sha, source, dest string) ([]string, string) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "w.bin")
	if err := os.WriteFile(src, fetchContent, 0o644); err != nil {
		t.Fatal(err)
	}
	if sha == "" {
		sum := sha256.Sum256(fetchContent)
		sha = hex.EncodeToString(sum[:])
	}
	if source == "" {
		source = "file://" + src
	}
	entry := fmt.Sprintf("- id: kws-base\n  sha256: %s\n  source: %s\n", sha, source)
	if dest != "" {
		entry += "  dest: " + dest + "\n"
	}
	writeFile(t, filepath.Join(dir, "manifests/kws.yaml"), entry)
	cache := filepath.Join(dir, "cache")
	return []string{"fetch-models", "--manifest", filepath.Join(dir, "manifests"), "--cache", cache}, cache
}

// 1. sha256 匹配 → 0，权重按源文件名落缓存且字节相等。
func TestFetchModelsShaMatch(t *testing.T) {
	args, cache := fetchSetup(t, "", "", "")
	code, out, errOut := runRepoctl(t, args)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d（stderr: %s）", code, ExitOK, errOut)
	}
	if !strings.Contains(out, "1/1 权重就绪") {
		t.Errorf("summary = %q", out)
	}
	got, err := os.ReadFile(filepath.Join(cache, "w.bin"))
	if err != nil || string(got) != string(fetchContent) {
		t.Fatalf("缓存权重缺失或不符: %v", err)
	}
}

// 2. sha256 不匹配 → 20，坏权重不落缓存。
func TestFetchModelsShaMismatch(t *testing.T) {
	args, cache := fetchSetup(t, strings.Repeat("0", 64), "", "")
	code, _, errOut := runRepoctl(t, args)
	if code != ExitViolation || !strings.Contains(errOut, "sha256 不匹配") {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(cache, "w.bin")); !os.IsNotExist(err) {
		t.Fatal("坏权重不得落缓存")
	}
}

// 3. 源文件不存在 → 2。
func TestFetchModelsMissingSource(t *testing.T) {
	args, _ := fetchSetup(t, "", "file:///nonexistent/w.bin", "")
	if code, _, errOut := runRepoctl(t, args); code != ExitInput || !strings.Contains(errOut, "源文件不存在") {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
}

// 4. 边界（负）：https 源（桩不下载）、sha 长度错、dest 带 .. → 均 2。
func TestFetchModelsInvalidEntries(t *testing.T) {
	tests := []struct {
		name         string
		sha, src, ds string
		want         string
	}{
		{"非 file 源", "", "https://example.com/w.bin", "", "非 file:// 源"},
		{"sha 长度错", "abc", "", "", "清单字段非法"},
		{"dest 越界", "", "", "../evil.bin", "非法 dest"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args, _ := fetchSetup(t, tc.sha, tc.src, tc.ds)
			code, _, errOut := runRepoctl(t, args)
			if code != ExitInput || !strings.Contains(errOut, tc.want) {
				t.Fatalf("exit = %d, stderr = %q", code, errOut)
			}
		})
	}
}
