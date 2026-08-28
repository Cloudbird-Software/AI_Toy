"""fetch-models：按 models/manifests 清单拉权重、校验 sha256、落本地缓存（本卡桩：仅 file:// 本地源）。"""
import hashlib
import shutil
import sys
from pathlib import Path
from urllib.parse import urlparse

from repoctl.exemption import parse_yaml_list


def sha256_file(path):
    with path.open("rb") as fh:
        return hashlib.file_digest(fh, "sha256").hexdigest()

def run(args) -> int:
    mdir, cache = Path(args.manifest), Path(args.cache)
    if not mdir.is_dir():
        print(f"fetch-models FAIL: manifest 目录不存在: {mdir}", file=sys.stderr)
        return 2
    models = [m for f in sorted(mdir.glob("*.y*ml")) for m in parse_yaml_list(f.read_text())]
    errs, fails = [], []
    for m in models:
        mid, src, sha = m.get("id", ""), m.get("source", ""), (m.get("sha256") or "").lower()
        u = urlparse(src)
        spath = Path(u.path if u.scheme == "file" else src)
        dest = m.get("dest") or spath.name
        if not mid or not src or len(sha) != 64 or u.scheme not in ("", "file"):
            errs.append(f"{mid or src}: 清单字段非法或非 file:// 源（本卡桩不下载）")
        elif not spath.is_file():
            errs.append(f"{mid}: 源文件不存在: {spath}")
        elif Path(dest).is_absolute() or ".." in Path(dest).parts:
            errs.append(f"{mid}: 非法 dest: {dest}")
        elif (got := sha256_file(spath)) != sha:
            fails.append(f"{mid}: sha256 不匹配 (期望 {sha[:12]}, 实得 {got[:12]})")
        else:
            target = cache / dest
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(spath, target)
    for m in errs + fails:
        print("fetch-models FAIL:", m, file=sys.stderr)
    print(f"fetch-models: {len(models) - len(fails) - len(errs)}/{len(models)} 权重就绪 -> {cache}")
    return 20 if fails else (2 if errs else 0)
