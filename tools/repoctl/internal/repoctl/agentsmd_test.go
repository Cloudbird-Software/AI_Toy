// agents-md check 契约测试（spec §3.8 / §7.2）：齐→0；缺包→20；缺小节→20；
// 根缺失→20；子命令拼写错→2。
package repoctl

import (
	"path/filepath"
	"strings"
	"testing"
)

// pkgAgentsMD 含全部七个必需小节（带后缀形态，验证子串匹配）。
const pkgAgentsMD = "# AGENTS.md — 唤醒词（T4）\n" +
	"## 本包边界\n音频流进 → 唤醒事件出。\n" +
	"## 技术路径（指导，任选+可偏离，PR 记录）\nA｜B｜C\n" +
	"## 本地命令\njust gate T4 all\n" +
	"## 本地必绿再提 PR\nT4-G0-01｜T4-G1-01\n" +
	"## 数据依赖\nsynth manifest；真实童声经 tools/holdout\n" +
	"## 本包禁令（叠加根 AGENTS.md）\n- 禁合并报告\n" +
	"## 常见坑\n儿童基频高。\n"

// 1. 根 + go/ts 两语言包全齐 → 0。
func TestAgentsMDAllPresent(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "# AGENTS.md — 根\n")
	writeFile(t, filepath.Join(root, "packages/go/kws/AGENTS.md"), pkgAgentsMD)
	writeFile(t, filepath.Join(root, "packages/ts/cloud-orchestrator/AGENTS.md"), pkgAgentsMD)
	code, out, errOut := runRepoctl(t, []string{"agents-md", "check", "--root", root})
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d（stderr: %s）", code, ExitOK, errOut)
	}
	if !strings.Contains(out, "根 + 2 个包") || errOut != "" {
		t.Errorf("summary = %q, stderr = %q", out, errOut)
	}
}

// 2. 包目录在而 AGENTS.md 缺失 → 20。
func TestAgentsMDMissingPackage(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "# 根\n")
	writeFile(t, filepath.Join(root, "packages/go/kws/AGENTS.md"), pkgAgentsMD)
	writeFile(t, filepath.Join(root, "packages/go/tts/.keep"), "")
	code, _, errOut := runRepoctl(t, []string{"agents-md", "check", "--root", root})
	if code != ExitViolation || !strings.Contains(errOut, "packages/go/tts/AGENTS.md 缺失") {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
}

// 3. 只有一个小节 → 20，报文列全缺席小节名。
func TestAgentsMDMissingSection(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "# 根\n")
	writeFile(t, filepath.Join(root, "packages/go/kws/AGENTS.md"), "# AGENTS.md\n## 本包边界\n一句话\n")
	code, _, errOut := runRepoctl(t, []string{"agents-md", "check", "--root", root})
	if code != ExitViolation || !strings.Contains(errOut, "缺小节: 技术路径") || !strings.Contains(errOut, "常见坑") {
		t.Fatalf("exit = %d, stderr = %q", code, errOut)
	}
}

// 4. 边界：根 AGENTS.md 缺失 → 20；二级子命令拼错 → 2。
func TestAgentsMDRootMissingAndBadSub(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "packages/go/kws/AGENTS.md"), pkgAgentsMD)
	code, _, errOut := runRepoctl(t, []string{"agents-md", "check", "--root", root})
	if code != ExitViolation || !strings.Contains(errOut, "根 AGENTS.md 缺失") {
		t.Fatalf("根缺失: exit = %d, stderr = %q", code, errOut)
	}
	if code, _, _ = runRepoctl(t, []string{"agents-md", "chk", "--root", root}); code != ExitInput {
		t.Fatalf("拼错子命令: exit = %d, want %d", code, ExitInput)
	}
}
