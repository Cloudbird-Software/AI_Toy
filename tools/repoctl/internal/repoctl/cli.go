// cli —— repoctl 子命令入口（spec §3.8）。命令语义见各实现文件；cmd/repoctl 为薄壳。
package repoctl

import (
	"flag"
	"fmt"
	"io"
)

// CLI 退出码（CI 与 justfile 的依赖面，不得偏离）。
const (
	ExitOK        = 0  // 通过
	ExitViolation = 20 // 门禁红（coverage 缺口 / 违规引用 / 过期豁免 / sha256 不匹配）
	ExitInput     = 2  // 输入不可读或环境错误（坏 JSON、git 失败、非 file:// 源等）
)

const usage = "usage: repoctl <coverage|agents-md check|forbidden-refs|exemption audit|fetch-models|affected> [flags]"

// Run 执行 repoctl CLI（argv 不含程序名），返回进程退出码。
func Run(argv []string, stdout, stderr io.Writer) int {
	if len(argv) == 0 {
		fmt.Fprintln(stderr, usage)
		return ExitInput
	}
	cmd, rest := argv[0], argv[1:]
	switch cmd {
	case "coverage":
		return cliCoverage(rest, stdout, stderr)
	case "agents-md":
		return cliNested("agents-md", "check", rest, cliAgentsMD, stdout, stderr)
	case "forbidden-refs":
		return cliForbidden(rest, stdout, stderr)
	case "exemption":
		return cliNested("exemption", "audit", rest, cliExemption, stdout, stderr)
	case "fetch-models":
		return cliFetchModels(rest, stdout, stderr)
	case "affected":
		return cliAffected(rest, stdout, stderr)
	}
	fmt.Fprintf(stderr, "error: 未知子命令 %q\n%s\n", cmd, usage)
	return ExitInput
}

// cliNested 校验二级子命令（agents-md check / exemption audit）后转交实现。
func cliNested(parent, sub string, argv []string, fn func([]string, io.Writer, io.Writer) int, stdout, stderr io.Writer) int {
	if len(argv) == 0 || argv[0] != sub {
		fmt.Fprintf(stderr, "error: %s 须跟子命令 %q\n%s\n", parent, sub, usage)
		return ExitInput
	}
	return fn(argv[1:], stdout, stderr)
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}
