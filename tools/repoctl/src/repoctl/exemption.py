"""exemption audit：reports/exemptions.yaml 过期项检查（spec §3.8 / §9.3）。含纯标准库扁平 YAML 解析器。"""
import sys
from datetime import UTC, datetime
from pathlib import Path


def parse_yaml_list(text):
    """极简扁平 YAML 子集：顶层 `- key: value` 列表项，缩进 kv 归属当前项，行内引号剥除。"""
    items, cur = [], {}
    for raw in text.splitlines():
        s = raw.strip()
        if s.startswith("- "):
            cur = {}
            items.append(cur)
            s = s[2:].strip()
        if ":" in s and not s.startswith("#"):
            k, _, v = s.partition(":")
            cur[k.strip()] = v.strip().strip("'\"")
    return [i for i in items if i]

def run(args) -> int:
    path = Path(args.file)
    if not path.is_file():
        print(f"exemption audit FAIL: 豁免台账不存在: {path}", file=sys.stderr)
        return 2
    items, today = parse_yaml_list(path.read_text()), datetime.now(UTC).date()
    fails = []
    for it in items:
        exp = it.get("expires") or it.get("expiry") or ""
        try:
            if datetime.fromisoformat(exp).date() < today:
                fails.append(f"{it.get('id', '?')}: 已过期 {exp}")
        except ValueError:
            fails.append(f"{it.get('id', '?')}: expires 非法 {exp!r}")
    for m in fails:
        print("exemption audit FAIL:", m, file=sys.stderr)
    print(f"exemption audit: {len(items)} 项豁免, {len(fails)} 过期")
    return 20 if fails else 0
