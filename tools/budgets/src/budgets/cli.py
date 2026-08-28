"""budgets CLI（spec §3.6）：延迟预算台账。

  budgets check --report reports/nightly/latency.json [--config configs/budgets/latency.yaml]
  budgets ledger [--history reports/nightly/latency-history.json] [--days 30]

退出码：0 通过；20 守恒违反 / 存在 >2σ 劣化段；2 输入不可读或不符合 schema。
报告与 history 的 JSON 格式见 budgets.check / budgets.ledger 模块 docstring。"""

from __future__ import annotations

import argparse
import sys
from collections.abc import Sequence
from pathlib import Path

from . import check, ledger

EXIT_OK = 0
EXIT_VIOLATION = 20
EXIT_CONFIG = 2


def cmd_check(args: argparse.Namespace) -> int:
    try:
        config = check.load_config(Path(args.config))
        result = check.evaluate(check.load_report(Path(args.report)), config)
    except check.BudgetInputError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_CONFIG
    print(check.format_debt_table(result, report_path=str(args.report)))
    return EXIT_OK if result.ok else EXIT_VIOLATION


def cmd_ledger(args: argparse.Namespace) -> int:
    if args.days < 1:
        print(f"error: --days 须 ≥ 1，got {args.days}", file=sys.stderr)
        return EXIT_CONFIG
    try:
        entries = ledger.load_history(Path(args.history))
        rows = ledger.compute_trends(entries[-args.days:])
    except ledger.HistoryError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_CONFIG
    print(ledger.format_trend_table(rows, args.days, len(entries[-args.days:])))
    return EXIT_VIOLATION if any(row.red for row in rows) else EXIT_OK


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="budgets", description="延迟预算台账（spec §3.6）")
    sub = parser.add_subparsers(dest="command", required=True)

    p_check = sub.add_parser("check", help="守恒校验 ΣP95−并行重叠 ≤ 总预算，输出延迟负债表")
    p_check.add_argument("--report", default="reports/nightly/latency.json", help="夜间延迟报告 JSON")
    p_check.add_argument("--config", default="configs/budgets/latency.yaml", help="预算基准 YAML")
    p_check.set_defaults(func=cmd_check)

    p_ledger = sub.add_parser("ledger", help="各段近 N 天趋势，>2σ 劣化标红")
    p_ledger.add_argument("--history", default="reports/nightly/latency-history.json",
                          help='单文件 history 数组 JSON（{"history": [报告, ...]}）')
    p_ledger.add_argument("--days", type=int, default=30,
                          help="趋势窗口：取最近 N 份报告（默认 30，即近 30 天）")
    p_ledger.set_defaults(func=cmd_ledger)
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
