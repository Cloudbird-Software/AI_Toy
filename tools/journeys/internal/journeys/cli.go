package journeys

import (
	"flag"
	"fmt"
	"io"
)

// CLI 退出码契约（spec §3.5）：0 全绿 / 1 断言失败 / 2 配置错误。
const (
	ExitOK     = 0
	ExitFail   = 1
	ExitConfig = 2
)

// Main 是 journeys CLI 入口（cmd/journeys 为薄壳），返回进程退出码。
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "run" {
		fmt.Fprintln(stderr, "usage: journeys run --set golden --seeds 3 --driver packages/go/user-sim [--scripts-dir DIR] [--out FILE]")
		return ExitConfig
	}
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	set := fs.String("set", "golden", "golden|core10")
	seeds := fs.Int("seeds", 3, "每剧本种子数")
	driver := fs.String("driver", "packages/go/user-sim", "user-sim driver 路径")
	scriptsDir := fs.String("scripts-dir", "tests/golden-journeys", "剧本目录")
	out := fs.String("out", "", "JSON 报告写入该文件（缺省打印 stdout）")
	fail := func(format string, args ...any) int {
		fmt.Fprintf(stderr, "error: "+format+"\n", args...)
		return ExitConfig
	}
	if err := fs.Parse(args[1:]); err != nil {
		return ExitConfig
	}
	if fs.NArg() > 0 {
		return fail("unexpected arguments: %v", fs.Args())
	}
	tier, ok := tierFilter(*set)
	if !ok {
		return fail("--set must be golden or core10, got %q", *set)
	}
	if *seeds < 1 {
		return fail("--seeds must be >= 1")
	}
	scripts, err := LoadScripts(*scriptsDir)
	if err != nil {
		return fail("%v", err)
	}
	if tier != "" {
		if scripts, ok = filterTier(scripts, tier); !ok {
			return fail("no tier=%s scripts for set %q", tier, *set)
		}
	}
	rep, err := Run(scripts, *seeds, *set, *driver)
	if err != nil {
		return fail("%v", err)
	}
	if err := Emit(rep, *out, stdout); err != nil {
		return fail("%v", err)
	}
	if rep.Summary.Overall == "pass" {
		return ExitOK
	}
	return ExitFail
}

// tierFilter：golden=全部剧本；core10=仅 tier=core。
func tierFilter(setName string) (string, bool) {
	switch setName {
	case "golden":
		return "", true
	case "core10":
		return "core", true
	}
	return "", false
}

func filterTier(scripts []*Script, tier string) ([]*Script, bool) {
	filtered := make([]*Script, 0, len(scripts))
	for _, s := range scripts {
		if s.Tier == tier {
			filtered = append(filtered, s)
		}
	}
	return filtered, len(filtered) > 0
}
