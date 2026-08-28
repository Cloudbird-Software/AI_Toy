// affected 契约测试（spec §3.8 / §7.3）：改 packages/go/kws/→[T4]；改 tools/→[]；
// 多路径并集（数值序）；固定种子属性（确定性+并集，对齐 Python hypothesis 契约）；
// 坏 base → 2。
package repoctl

import (
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

// gitRepoWithChanges 建临时 git 仓：空 base commit + staged 变更（diff HEAD 可见）。
// 隔离全局/系统 git 配置，避免签名等用户配置干扰。
func gitRepoWithChanges(t *testing.T, changes map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 不可用")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "--allow-empty", "-qm", "base")
	for rel, text := range changes {
		writeFile(t, filepath.Join(dir, rel), text)
	}
	git("add", "-A")
	return dir
}

// 1. kws 变更 → ["T4"]。
func TestAffectedKwsMapsT4(t *testing.T) {
	dir := gitRepoWithChanges(t, map[string]string{"packages/go/kws/internal/a.go": "package kws\n"})
	code, out, errOut := runRepoctl(t, []string{"affected", "--base", "HEAD", "--root", dir})
	if code != ExitOK || errOut != "" {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
	if out != "[\"T4\"]\n" {
		t.Fatalf("out = %q, want [\"T4\"]", out)
	}
}

// 2. tools/ 变更 → []（工具目录不映射资产）。
func TestAffectedToolsMapsEmpty(t *testing.T) {
	dir := gitRepoWithChanges(t, map[string]string{"tools/repoctl/x.go": "package repoctl\n"})
	code, out, _ := runRepoctl(t, []string{"affected", "--base", "HEAD", "--root", dir})
	if code != ExitOK || out != "[]\n" {
		t.Fatalf("exit = %d, out = %q", code, out)
	}
}

// 3. 多路径并集：kws(T4)+speaker(T5)+memory(T10)+docs（不映射）→ ["T4","T5","T10"]。
func TestAffectedUnion(t *testing.T) {
	dir := gitRepoWithChanges(t, map[string]string{
		"packages/go/speaker/b.go":   "package speaker\n",
		"docs/gates/assets/T4.md":    "# T4\n",
		"packages/go/memory/a.go":    "package memory\n",
		"packages/go/kws/internal/x": "y\n",
	})
	code, out, _ := runRepoctl(t, []string{"affected", "--base", "HEAD", "--root", dir})
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	if out != "[\"T4\",\"T5\",\"T10\"]\n" {
		t.Fatalf("out = %q, want [\"T4\",\"T5\",\"T10\"]（数值序并集）", out)
	}
}

// 4. 属性（固定种子 50 轮）：映射确定；A∪B = assets(A)∪assets(B)；输出数值升序。
func TestAssetsForDeterministicAndUnional(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	names := []string{"go/kws", "go/speaker", "go/memory", "go/eval-platform",
		"native/edge-runtime", "ts/founder-console", "go/nope"}
	randPaths := func(n int) []string {
		var ps []string
		for i := 0; i < n; i++ {
			ps = append(ps, "packages/"+names[rng.Intn(len(names))]+"/f"+strconv.Itoa(rng.Intn(3))+".go")
		}
		return ps
	}
	for round := 0; round < 50; round++ {
		a, b := randPaths(rng.Intn(6)), randPaths(rng.Intn(6))
		ga, gb := AssetsFor(a), AssetsFor(b)
		if again := AssetsFor(append([]string{}, a...)); !reflect.DeepEqual(ga, again) {
			t.Fatalf("round %d: 非确定: %v vs %v", round, ga, again)
		}
		gab := AssetsFor(append(append([]string{}, a...), b...))
		if !sameSet(gab, ga, gb) {
			t.Fatalf("round %d: 并集不守恒: %v ∪ %v ≠ %v", round, ga, gb, gab)
		}
		for i := 1; i < len(gab); i++ {
			if assetNum(gab[i-1]) >= assetNum(gab[i]) {
				t.Fatalf("round %d: 非数值升序: %v", round, gab)
			}
		}
	}
}

func sameSet(union, a, b []string) bool {
	want := map[string]bool{}
	for _, s := range append(append([]string{}, a...), b...) {
		want[s] = true
	}
	got := map[string]bool{}
	for _, s := range union {
		got[s] = true
	}
	return reflect.DeepEqual(got, want)
}

// 5. 边界（负）：坏 base → 2；缺 --base → 2。
func TestAffectedBadBaseAndMissingFlag(t *testing.T) {
	dir := gitRepoWithChanges(t, nil)
	if code, _, errOut := runRepoctl(t, []string{"affected", "--base", "no-such-ref", "--root", dir}); code != ExitInput {
		t.Fatalf("坏 base: exit = %d, stderr = %q", code, errOut)
	}
	if code, _, _ := runRepoctl(t, []string{"affected", "--root", dir}); code != ExitInput {
		t.Fatalf("缺 --base: exit = %d, want %d", code, ExitInput)
	}
}
