"""agents-md check：根 + 全部 packages/* 的 AGENTS.md 存在性与必需小节（spec §3.8 / §7.2 模板）。"""
import sys
from pathlib import Path

SECTIONS = ("本包边界", "技术路径", "本地命令", "本地必绿再提 PR", "数据依赖", "本包禁令", "常见坑")

def missing_sections(text):
    heads = [ln.lstrip("#").strip() for ln in text.splitlines() if ln.lstrip().startswith("#")]
    return [s for s in SECTIONS if not any(s in h for h in heads)]

def run(args) -> int:
    root = Path(args.root)
    fails = [] if (root / "AGENTS.md").is_file() else ["根 AGENTS.md 缺失"]
    pkgs_dir = root / "packages"
    pkgs = sorted(p for p in pkgs_dir.glob("*/*") if p.is_dir()) if pkgs_dir.is_dir() else []
    for pkg in pkgs:
        f = pkg / "AGENTS.md"
        if not f.is_file():
            fails.append(f"{pkg.relative_to(root)}/AGENTS.md 缺失")
        elif miss := missing_sections(f.read_text()):
            fails.append(f"{pkg.relative_to(root)}/AGENTS.md 缺小节: {'、'.join(miss)}")
    for m in fails:
        print("agents-md FAIL:", m, file=sys.stderr)
    print(f"agents-md: 根 + {len(pkgs)} 个包")
    return 20 if fails else 0
