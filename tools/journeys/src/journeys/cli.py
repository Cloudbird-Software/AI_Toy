"""journeys CLI（spec §3.5）：`journeys run --set golden --seeds 3 --driver packages/py/user-sim`。
退出码：0 全部断言通过；1 任一旅程断言失败；2 配置/环境错误（schema 校验失败等）。"""

import argparse
import sys
from pathlib import Path

from . import report, runner, schema

SET_TIER_FILTERS = {"golden": None, "core10": "core"}
EXIT_OK, EXIT_FAIL, EXIT_CONFIG = 0, 1, 2

def build_parser():
    parser = argparse.ArgumentParser(prog="journeys", description="黄金旅程运行器（spec §3.5）")
    sub = parser.add_subparsers(dest="command", required=True)
    run = sub.add_parser("run", help="运行一组黄金旅程并产出 JSON 报告")
    run.add_argument("--set", default="golden", choices=sorted(SET_TIER_FILTERS), help="golden|core10")
    run.add_argument("--seeds", type=int, default=3, help="每剧本种子数（默认 3）")
    run.add_argument("--driver", default="packages/py/user-sim", help="user-sim driver 路径")
    run.add_argument("--scripts-dir", default="tests/golden-journeys", help="剧本目录")
    run.add_argument("--out", default=None, help="JSON 报告写入该文件（缺省打印到 stdout）")
    return parser

def main(argv=None):
    args = build_parser().parse_args(argv)
    if args.seeds < 1:
        print("error: --seeds must be >= 1", file=sys.stderr)
        return EXIT_CONFIG
    try:
        scripts = schema.load_scripts(Path(args.scripts_dir))
    except schema.SchemaError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_CONFIG
    tier_filter = SET_TIER_FILTERS[args.set]
    if tier_filter is not None:
        scripts = [s for s in scripts if s.tier == tier_filter]
        if not scripts:
            print(f"error: no tier={tier_filter} scripts for set {args.set!r}", file=sys.stderr)
            return EXIT_CONFIG
    rep = runner.run_journeys(scripts, seeds=args.seeds, set_name=args.set, driver=str(args.driver))
    report.emit(rep, args.out)
    return EXIT_OK if rep["summary"]["overall"] == "pass" else EXIT_FAIL
