package journeys

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

// CLI 退出码契约（spec §3.5）：0 全绿 / 1 断言失败 / 2 配置错误。
// IR #72 / ADR-0003 阶段化：driver_mode=simulated 且判 fail → exit 0 + DEBT 行
// （桩噪声债务，非产品信号）；--strict 或真实 driver（spec §8：real 失败=真
// 失败——ADR-0003 语义自然收敛）维持旧语义 fail→1。
const (
	ExitOK     = 0
	ExitFail   = 1
	ExitConfig = 2
)

// Main 是 journeys CLI 入口（cmd/journeys 为薄壳），返回进程退出码。
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "run" {
		fmt.Fprintln(stderr, "usage: journeys run --set golden|core10 [--seeds N] [--driver real|simulated] [--scripts-dir DIR] [--out FILE] [--strict]")
		return ExitConfig
	}
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	set := fs.String("set", "golden", "golden|core10")
	seeds := fs.Int("seeds", 3, "每剧本种子数")
	driver := fs.String("driver", DriverModeSimulated, "real=T20 user-sim 真管道回放 / simulated=确定性桩（IR #72 阶段化）")
	scriptsDir := fs.String("scripts-dir", "tests/golden-journeys", "剧本目录")
	out := fs.String("out", "", "JSON 报告写入该文件（缺省打印 stdout）")
	strict := fs.Bool("strict", false, "严格模式：模拟 driver 失败也 exit 1（缺省桩失败阶段化为 SIMULATION-DEBT 不阻断）")
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
	var notes []string
	if tier != "" {
		if scripts, ok = filterTier(scripts, tier); !ok {
			return fail("no tier=%s scripts for set %q", tier, *set)
		}
		// spec §8：M2 无记忆（memory_hit 恒 false），断言含 memory_hit_rate 的
		// 剧本不入 core10（收敛策略写进报告 note，不改剧本本体；M3 记忆落地后纳入）。
		scripts, notes = filterCore10(scripts)
	}
	rep, err := Run(scripts, *seeds, *set, *driver)
	if err != nil {
		return fail("%v", err)
	}
	rep.Notes = append(rep.Notes, notes...)
	if err := Emit(rep, *out, stdout); err != nil {
		return fail("%v", err)
	}
	if rep.Summary.Overall == "pass" {
		return ExitOK
	}
	// IR #72：模拟态失败 = 债务而非信号（ADR-0002 同一阶段化哲学），
	// 不阻断但必须醒目可见；--driver real（spec §8）失败为真失败，自然旧执法。
	if rep.SimulationDebt && !*strict {
		fmt.Fprintf(stdout, "SIMULATION-DEBT: %d 旅程失败（driver=simulated 桩噪声，不代表产品行为；--driver real 真管道执法，--strict 可恢复阻断）\n", rep.Summary.Fail)
		fmt.Fprintf(stdout, "SIMULATION-DEBT-IDS: %s\n", strings.Join(rep.Summary.FailIDs, ","))
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

// filterCore10 剔除断言含 memory_hit_rate 的剧本（spec §8：M2 无记忆恒 false，
// 入 core10 必假红——非产品信号），返回过滤后剧本与报告附注。
func filterCore10(scripts []*Script) ([]*Script, []string) {
	filtered := make([]*Script, 0, len(scripts))
	dropped := []string{}
	for _, s := range scripts {
		hasMemory := false
		for _, a := range s.Assertions {
			if a.Metric == "memory_hit_rate" {
				hasMemory = true
				break
			}
		}
		if hasMemory {
			dropped = append(dropped, s.ID)
			continue
		}
		filtered = append(filtered, s)
	}
	if len(dropped) == 0 {
		return filtered, nil
	}
	return filtered, []string{fmt.Sprintf(
		"core10 收敛（spec §8）：M2 无记忆（memory_hit 恒 false），断言含 memory_hit_rate 的剧本不入本集：%s；M3 记忆落地后纳入",
		strings.Join(dropped, ","))}
}
