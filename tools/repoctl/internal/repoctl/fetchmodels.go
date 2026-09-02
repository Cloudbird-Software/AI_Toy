// fetchmodels —— fetch-models（spec §3.8）：按 models/manifests 清单拉权重、校验
// sha256、落 models/cache（权重永不入 git）。本卡桩：仅 file:// 本地源，不触网络。
package repoctl

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ModelEntry 清单一行（id/sha256/source/dest；多余字段容忍）。
type ModelEntry struct {
	ID     string `yaml:"id"`
	SHA256 string `yaml:"sha256"`
	Source string `yaml:"source"`
	Dest   string `yaml:"dest"`
}

// FetchModels 逐条拉取：sha 不匹配 → shaFails（exit 20，坏权重不落缓存）；
// 清单字段非法/源缺失/dest 越界 → inputErrs（exit 2）。
func FetchModels(manifestDir, cacheDir string) (shaFails, inputErrs []string, total int, err error) {
	if st, serr := os.Stat(manifestDir); serr != nil || !st.IsDir() {
		return nil, nil, 0, fmt.Errorf("manifest 目录不存在: %s", manifestDir)
	}
	manifests, _ := filepath.Glob(filepath.Join(manifestDir, "*.yaml"))
	ymls, _ := filepath.Glob(filepath.Join(manifestDir, "*.yml"))
	manifests = append(manifests, ymls...)
	sort.Strings(manifests)
	var entries []ModelEntry
	for _, path := range manifests {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, 0, err
		}
		var list []ModelEntry
		if err := yaml.Unmarshal(data, &list); err != nil {
			return nil, nil, 0, fmt.Errorf("%s 不可解析: %w", path, err)
		}
		entries = append(entries, list...)
	}
	for _, m := range entries {
		total++
		sha := strings.ToLower(m.SHA256)
		srcPath, scheme := m.Source, ""
		if u, uerr := url.Parse(m.Source); uerr == nil {
			scheme = u.Scheme
			if scheme == "file" {
				srcPath = u.Path
			}
		} else {
			scheme = "invalid"
		}
		dest := m.Dest
		if dest == "" {
			dest = filepath.Base(srcPath)
		}
		switch {
		case m.ID == "" || m.Source == "" || !isHex64(sha) || (scheme != "" && scheme != "file"):
			inputErrs = append(inputErrs, fmt.Sprintf("%s: 清单字段非法或非 file:// 源（本卡桩不下载）",
				orDash(m.ID, m.Source)))
			continue
		}
		if st, serr := os.Stat(srcPath); serr != nil || st.IsDir() {
			inputErrs = append(inputErrs, fmt.Sprintf("%s: 源文件不存在: %s", m.ID, srcPath))
			continue
		}
		if badDest(dest) {
			inputErrs = append(inputErrs, fmt.Sprintf("%s: 非法 dest: %s", m.ID, dest))
			continue
		}
		got, herr := sha256File(srcPath)
		if herr != nil {
			return nil, nil, 0, herr
		}
		if got != sha {
			shaFails = append(shaFails, fmt.Sprintf("%s: sha256 不匹配 (期望 %s, 实得 %s)",
				m.ID, sha[:12], got[:12]))
			continue
		}
		if cerr := copyFile(srcPath, filepath.Join(cacheDir, filepath.FromSlash(dest))); cerr != nil {
			return nil, nil, 0, cerr
		}
	}
	return shaFails, inputErrs, total, nil
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// badDest：绝对路径或含 .. 段即越界（缓存只许落在 cache 之下）。
func badDest(dest string) bool {
	if filepath.IsAbs(dest) {
		return true
	}
	for _, part := range strings.Split(dest, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func orDash(a, b string) string {
	if a != "" {
		return a
	}
	if b != "" {
		return b
	}
	return "?"
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close() // Copy 错误为主因，关闭损失次生面（尽力）
		return err
	}
	return out.Close()
}

func cliFetchModels(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("fetch-models", stderr)
	manifest := fs.String("manifest", "models/manifests", "权重清单目录")
	cache := fs.String("cache", "models/cache", "本地缓存目录（不入 git）")
	if fs.Parse(args) != nil {
		return ExitInput
	}
	shaFails, inputErrs, total, err := FetchModels(*manifest, *cache)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitInput
	}
	for _, m := range append(inputErrs, shaFails...) {
		_, _ = fmt.Fprintln(stderr, "fetch-models FAIL: "+m)
	}
	_, _ = fmt.Fprintf(stdout, "fetch-models: %d/%d 权重就绪 -> %s\n",
		total-len(shaFails)-len(inputErrs), total, *cache)
	if len(shaFails) > 0 {
		return ExitViolation
	}
	if len(inputErrs) > 0 {
		return ExitInput
	}
	return ExitOK
}
