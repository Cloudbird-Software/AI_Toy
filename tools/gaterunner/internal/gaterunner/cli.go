// cli —— gaterunner 子命令入口（spec §3.1；cmd/gaterunner 为薄壳）。argv 不含程序名。
package gaterunner

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// Main 执行 gaterunner CLI，返回进程退出码。
func Main(argv []string, stdout, stderr io.Writer) int {
	if len(argv) == 0 {
		fmt.Fprintln(stderr, "usage: gaterunner <collect|verify-configs|calibrate|run> [flags]")
		return ExitConfig
	}
	switch argv[0] {
	case "collect":
		return cliCollect(argv[1:], stdout, stderr)
	case "verify-configs":
		return cliVerify(argv[1:], stdout, stderr)
	case "calibrate":
		return cliCalibrate(argv[1:], stdout, stderr)
	case "run":
		return cliRun(argv[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "error: 未知子命令 %q（可用：collect、verify-configs、calibrate、run）\n", argv[0])
	return ExitConfig
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func cliCollect(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("collect", stderr)
	root := fs.String("root", ".", "登记表扫描根（*_test.go 的 Mark 注册调用）")
	configDir := fs.String("config-dir", "configs/gates", "门禁阈值目录")
	if fs.Parse(args) != nil {
		return ExitConfig
	}
	rows, err := BuildRegistry(*root, *configDir)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitConfig
	}
	if err := EmitRegistry(rows, stdout); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitConfig
	}
	return ExitOK
}

func cliVerify(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("verify-configs", stderr)
	configDir := fs.String("config-dir", "configs/gates", "门禁阈值目录")
	docsDir := fs.String("docs-dir", "docs/gates/assets", "验收协议目录（BI 映射校验）")
	if fs.Parse(args) != nil {
		return ExitConfig
	}
	violations, total := VerifyConfigs(*configDir, *docsDir)
	for _, v := range violations {
		fmt.Fprintln(stderr, "error: "+v)
	}
	if len(violations) > 0 {
		return ExitConfig
	}
	fmt.Fprintf(stdout, "verify-configs: %d 门禁，0 违反\n", total)
	return ExitOK
}

func cliCalibrate(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("calibrate", stderr)
	asset := fs.String("asset", "", "资产 id（如 T4）")
	runs := fs.Int("runs", 10, "基线连跑次数（噪声带样本量）")
	configDir := fs.String("config-dir", "configs/gates", "门禁阈值目录")
	commit := fs.String("commit", "", "基线 commit（缺省取 git HEAD）")
	out := fs.String("out", "", "建议文件路径（缺省打印 stdout）")
	if fs.Parse(args) != nil {
		return ExitConfig
	}
	if *asset == "" || *runs < 1 {
		fmt.Fprintln(stderr, "error: --asset 必填且 --runs 须 ≥ 1")
		return ExitConfig
	}
	where, err := ExecuteCalibrate(*asset, *runs, *configDir, *commit, *out, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitConfig
	}
	fmt.Fprintf(stdout, "calibrate: 噪声带建议已写入 %s\n", where)
	return ExitOK
}

func cliRun(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("run", stderr)
	asset := fs.String("asset", "", "资产 id（如 T4）")
	level := fs.String("level", "all", "g0|g1|g2|all")
	suite := fs.String("suite", "ci", "ci|nightly|holdout")
	report := fs.String("report", "reports/gates/<asset>.json", "JSON 报告路径（缺省打印 stdout）")
	commit := fs.String("commit", "", "commit sha（缺省取 git HEAD）")
	root := fs.String("root", ".", "登记表扫描/git 根")
	configDir := fs.String("config-dir", "configs/gates", "门禁阈值目录")
	docsDir := fs.String("docs-dir", "docs/gates/assets", "验收协议目录（BI 映射校验）")
	exemptions := fs.String("exemptions", "reports/exemptions.yaml", "G1 豁免台账")
	if fs.Parse(args) != nil {
		return ExitConfig
	}
	if *asset == "" {
		fmt.Fprintln(stderr, "error: --asset 必填")
		return ExitConfig
	}
	if *level != "all" && !isValidLevelFlag(*level) || !isValidSuite(*suite) {
		fmt.Fprintf(stderr, "error: --level 须为 g0|g1|g2|all 且 --suite 须为 ci|nightly|holdout（got %q/%q）\n", *level, *suite)
		return ExitConfig
	}
	if *report == "reports/gates/<asset>.json" {
		*report = filepath.Join("reports", "gates", *asset+".json")
	}
	rep, exit, err := ExecuteRun(RunOpts{Asset: *asset, Level: *level, Suite: *suite,
		Commit: *commit, Root: *root, ConfigDir: *configDir, DocsDir: *docsDir,
		ExemptionsPath: *exemptions, ReportPath: *report})
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitConfig
	}
	if err := EmitReport(rep, *report, stdout); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitConfig
	}
	for _, id := range rep.Summary.FailIDs {
		fmt.Fprintf(stderr, "红: %s（%s）\n", id, levelOf(rep, id))
	}
	for _, ex := range rep.ExemptionsApplied {
		fmt.Fprintf(stderr, "豁免: %s\n", ex)
	}
	fmt.Fprintf(stderr, "gaterunner run: asset=%s suite=%s commit=%s g0=%s g1=%s g2=%s → exit %d\n",
		rep.Asset, rep.Suite, rep.Commit, rep.Summary.G0, rep.Summary.G1, rep.Summary.G2, exit)
	return exit
}

func isValidLevelFlag(level string) bool {
	switch level {
	case "g0", "g1", "g2":
		return true
	}
	return false
}

func isValidSuite(suite string) bool {
	return suite == "ci" || suite == "nightly" || suite == "holdout"
}

func levelOf(rep *Report, id string) string {
	for _, r := range rep.Results {
		if r.ID == id {
			return strings.ToLower(r.Level)
		}
	}
	return "?"
}
