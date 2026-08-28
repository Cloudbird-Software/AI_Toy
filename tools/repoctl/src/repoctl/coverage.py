"""coverage：gaterunner 登记表 × docs/gates/assets/*.md 的 BI 集合三重检查（spec §3.8）。"""
import json
import re
import sys
from pathlib import Path


def load_registry(gates_dir):
    entries = []
    for f in sorted(gates_dir.glob("*.json")):
        data = json.loads(f.read_text())
        items = data if isinstance(data, list) else data.get("results") or data.get("assertions") or []
        fa = data.get("asset") if isinstance(data, dict) else None
        entries += [dict(e, asset=e.get("asset") or fa) for e in items]
    return entries

def run(args) -> int:
    root = Path(args.root)
    docs = {f.stem: set(re.findall(r"BI-\d+\.\d+", f.read_text()))
            for f in sorted((root / "docs/gates/assets").glob("*.md"))}
    entries = [e for e in load_registry(root / "reports/gates") if e.get("asset")]
    fails = []
    for asset, bis in docs.items():
        own = [e for e in entries if e["asset"] == asset]
        fails += [f"{asset}: BI {bi} 无任何断言" for bi in sorted(bis - {e.get("bi") for e in own})]
        if not any(str(e.get("level", "")).lower() == "g0" for e in own):
            fails.append(f"{asset}: 缺 G0 断言")
    fails += [f"孤儿断言: {e['asset']}/{e.get('bi')} ({e.get('id')})" for e in entries
              if e.get("bi") not in docs.get(e["asset"], set())]
    for m in sorted(set(fails)):
        print("coverage FAIL:", m, file=sys.stderr)
    print(f"coverage: {len(docs)} 资产 / {len(entries)} 断言")
    return 20 if fails else 0
