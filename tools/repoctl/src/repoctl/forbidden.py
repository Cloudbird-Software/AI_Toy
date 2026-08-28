"""forbidden-refs：全仓 grep datasets/holdout 引用，仅 tools/holdout 与 eval 侧白名单放行（spec §3.8）。"""
import sys
from pathlib import Path

PATTERN = "datasets/holdout"
WHITELIST = ("tools/holdout/", "tools/gaterunner/", "packages/py/eval-platform/")  # eval 侧白名单
SKIP = (".git/", "datasets/holdout/")  # holdout 数据本体自身不参与扫描

def run(args) -> int:
    root = Path(args.root).resolve()
    fails = []
    for f in sorted(root.rglob("*")):
        rel = f.relative_to(root).as_posix()
        if not f.is_file() or rel.startswith(SKIP) or f.stat().st_size > 1_000_000 or rel.startswith(WHITELIST):
            continue
        try:
            text = f.read_text()
        except (UnicodeDecodeError, ValueError):
            continue
        if PATTERN in text:
            ln = next(i for i, l in enumerate(text.splitlines(), 1) if PATTERN in l)
            fails.append(f"{rel}:{ln} 引用 {PATTERN}")
    for m in fails:
        print("forbidden-refs FAIL:", m, file=sys.stderr)
    print(f"forbidden-refs: {len(fails)} 处违规")
    return 20 if fails else 0
