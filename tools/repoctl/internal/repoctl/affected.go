// affected —— affected（spec §3.8 / §7.3）：git diff 路径 → 受影响资产列表，
// stdout 输出 JSON 数组（ci.yml changes 消费）。映射：packages/<lang>/<pkg> → 资产；
// memory=T10+T11 合并报 T10；ts 两包无资产门（§7.3 表未覆盖）。
package repoctl

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// pkgAsset 包（<lang>/<pkg>）→ 资产映射（spec §7.3 表）。
var pkgAsset = map[string]string{
	"go/eval-platform": "T1", "go/data-flywheel": "T2", "go/turntaking": "T3",
	"go/kws": "T4", "go/speaker": "T5", "go/imu": "T6", "go/emotion": "T7",
	"go/persona": "T8", "go/safety": "T9", "go/memory": "T10", "go/motion-map": "T12",
	"go/tts": "T13", "go/runtime-fsm": "T14", "go/router": "T15", "go/packs": "T16",
	"go/content-pipeline": "T18", "go/user-sim": "T20",
	"native/edge-runtime": "T14", "native/firmware-imu": "T6",
}

// AssetsFor 路径 → 资产集合（按 T 后数字升序、去重）；非 packages/<lang>/<pkg> 不映射。
func AssetsFor(paths []string) []string {
	set := map[string]bool{}
	for _, p := range paths {
		parts := strings.Split(strings.TrimSpace(filepath.ToSlash(p)), "/")
		if len(parts) >= 3 && parts[0] == "packages" {
			if a, ok := pkgAsset[parts[1]+"/"+parts[2]]; ok {
				set[a] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for a := range set {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return assetNum(out[i]) < assetNum(out[j]) })
	return out
}

func assetNum(t string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(t, "T"))
	return n
}

// Affected 跑 `git -C root diff --name-only base` 并映射资产；git 失败 → 错误（exit 2）。
func Affected(root, base string) ([]string, error) {
	out, err := exec.Command("git", "-C", root, "diff", "--name-only", base).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("git diff 失败: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("git diff 失败: %w", err)
	}
	var paths []string
	for _, ln := range strings.Split(string(out), "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			paths = append(paths, ln)
		}
	}
	return AssetsFor(paths), nil
}

func cliAffected(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("affected", stderr)
	base := fs.String("base", "", "diff 基准 ref（如 origin/main）")
	root := fs.String("root", ".", "git 根")
	if fs.Parse(args) != nil {
		return ExitInput
	}
	if *base == "" {
		_, _ = fmt.Fprintln(stderr, "error: --base 必填（如 origin/main）")
		return ExitInput
	}
	assets, err := Affected(*root, *base)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitInput
	}
	data, err := json.Marshal(assets)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitInput
	}
	_, _ = fmt.Fprintln(stdout, string(data))
	return ExitOK
}
