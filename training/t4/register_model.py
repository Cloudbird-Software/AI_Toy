#!/usr/bin/env python3
"""register_model.py —— 自训模型登记（models/manifests + 权重落位）。

把训练产出的 onnx 复制到 models/incoming/（gitignore 排除），计算 sha256/size，
以文本追加方式在 models/manifests/openwakeword.yaml 登记（保留原注释与占位条目，
不重写文件）。完成后 `just fetch-models` 即可校验。

license 台账纪律（AGENTS.md G0 红线）：自训模型为全合成数据产物——piper 音色
（MIT）+ MIT RIR（研究许可，仅增强用）+ audioset/FMA/ACAV100M 负样本特征，
模型权重归属本仓；台帐见 training/licenses.yaml。
"""
import argparse
import datetime
import hashlib
import os
import shutil
import sys

EXIT_OK, EXIT_INPUT = 0, 2
MANIFEST = "models/manifests/openwakeword.yaml"
INCOMING = "models/incoming"


def sha256(path: str) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--onnx", required=True, help="训练产出的 .onnx 路径")
    ap.add_argument("--phrase", default=None, help="唤醒词（缺省读 config.yaml target_phrase[0]）")
    args = ap.parse_args()
    if not os.path.isfile(args.onnx):
        print(f"error: 文件不存在: {args.onnx}", file=sys.stderr)
        return EXIT_INPUT

    name = os.path.splitext(os.path.basename(args.onnx))[0]
    phrase = args.phrase
    if not phrase:
        import yaml
        with open("training/t4/config.yaml", encoding="utf-8") as f:
            phrase = yaml.safe_load(f)["target_phrase"][0]

    os.makedirs(INCOMING, exist_ok=True)
    dest = os.path.join(INCOMING, os.path.basename(args.onnx))
    shutil.copy2(args.onnx, dest)
    digest, size = sha256(dest), os.path.getsize(dest)

    today = datetime.date.today().isoformat()
    entry = f"""# T4 全合成自训模型（{today}，训练管道 training/t4/，ADR-0007）——唤醒词「{phrase}」
- name: {name}
  task: kws
  license: Apache-2.0（全合成数据自训产物；数据源台账 training/licenses.yaml）
  sha256: "{digest}"
  size: {size}
  fetch_url: local://{dest}
  tier_usage: L0-L3 全档（端侧常驻唤醒入口；T4 量产模型，替换上方开发期占位条目）
  id: {name}
  source: {dest}
  dest: {os.path.basename(dest)}
"""
    with open(MANIFEST, "a", encoding="utf-8") as f:
        f.write(entry)
    print(f"已登记 {name}（sha256={digest[:16]}…, {size} bytes）→ models/manifests/openwakeword.yaml")
    print("校验：just fetch-models")
    return EXIT_OK


if __name__ == "__main__":
    sys.exit(main())
