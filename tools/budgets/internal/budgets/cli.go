// cli —— budgets 子命令入口（spec §3.6）。格式见包 doc 注释。
package budgets

import (
	"flag"
	"fmt"
	"io"
)

// CLI 退出码（CI 与 justfile 的依赖面，不得偏离）。
const (
	ExitOK        = 0  // 通过
	ExitViolation = 20 // 守恒违反 / 存在 >2σ 劣化段
	ExitInput     = 2  // 输入不可读或不符合 schema
)

// Run 执行 budgets CLI（argv 不含程序名），返回进程退出码。
func Run(argv []string, stdout, stderr io.Writer) int {
	if len(argv) == 0 {
		fmt.Fprintln(stderr, "usage: budgets <check|ledger> [flags]")
		return ExitInput
	}
	switch argv[0] {
	case "check":
		return runCheck(argv[1:], stdout, stderr)
	case "ledger":
		return runLedger(argv[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "error: 未知子命令 %q（可用：check、ledger）\n", argv[0])
		return ExitInput
	}
}

func runCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	reportPath := fs.String("report", "reports/nightly/latency.json", "夜间延迟报告 JSON")
	configPath := fs.String("config", "configs/budgets/latency.yaml", "预算基准 YAML")
	if fs.Parse(args) != nil {
		return ExitInput
	}
	config, err := LoadConfig(*configPath)
	var result CheckResult
	if err == nil {
		var report LatencyReport
		if report, err = loadReport(*reportPath); err == nil {
			result, err = Evaluate(report, config)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitInput
	}
	fmt.Fprintln(stdout, FormatDebtTable(result, *reportPath))
	if result.OK {
		return ExitOK
	}
	return ExitViolation
}

func runLedger(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ledger", flag.ContinueOnError)
	fs.SetOutput(stderr)
	historyPath := fs.String("history", "reports/nightly/latency-history.json", `单文件 history JSON（{"history": [报告, ...]}）`)
	days := fs.Int("days", 30, "趋势窗口：取最近 N 份报告（默认 30，即近 30 天）")
	if fs.Parse(args) != nil {
		return ExitInput
	}
	if *days < 1 {
		fmt.Fprintf(stderr, "error: --days 须 ≥ 1，got %d\n", *days)
		return ExitInput
	}
	entries, err := LoadHistory(*historyPath)
	var rows []TrendRow
	if err == nil {
		window := entries
		if len(window) > *days {
			window = window[len(window)-*days:]
		}
		rows, err = ComputeTrends(window)
		if err == nil {
			fmt.Fprintln(stdout, FormatTrendTable(rows, *days, len(window)))
			for _, row := range rows {
				if row.Red {
					return ExitViolation
				}
			}
			return ExitOK
		}
	}
	fmt.Fprintf(stderr, "error: %v\n", err)
	return ExitInput
}
