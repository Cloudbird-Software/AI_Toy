"""affected：diff 路径 → 受影响资产列表（ci.yml changes 消费，spec §3.8）。映射按 §7.3 包↔资产。"""
import json
import subprocess
import sys
from pathlib import Path

PKG_ASSET = {"py/eval-platform": "T1", "py/data-flywheel": "T2", "py/turntaking": "T3", "py/kws": "T4",
             "py/speaker": "T5", "py/imu": "T6", "py/emotion": "T7", "py/persona": "T8", "py/safety": "T9",
             "py/memory": "T10", "py/motion-map": "T12", "py/tts": "T13", "py/runtime-fsm": "T14",
             "py/router": "T15", "py/packs": "T16", "py/content-pipeline": "T18", "py/user-sim": "T20",
             "native/edge-runtime": "T14", "native/firmware-imu": "T6"}

def assets_for(paths):
    out = set()
    for p in paths:
        parts = Path(p).parts
        if len(parts) >= 3 and parts[0] == "packages" and (a := PKG_ASSET.get(f"{parts[1]}/{parts[2]}")):
            out.add(a)
    return sorted(out, key=lambda t: int(t[1:]))

def run(args) -> int:
    r = subprocess.run(["git", "-C", args.root, "diff", "--name-only", args.base],
                       capture_output=True, text=True, check=False)
    if r.returncode != 0:
        print(f"affected FAIL: git diff 失败: {r.stderr.strip()}", file=sys.stderr)
        return 2
    print(json.dumps(assets_for(ln.strip() for ln in r.stdout.splitlines() if ln.strip())))
    return 0
