#!/usr/bin/env python3
"""evaluate.py —— 训练后 synth 面预检（训练者的第一道反馈，不是门禁）。

对指定目录的 wav 逐条跑唤醒模型（openwakeword onnx 推理），报唤醒率与分数
分布，写 reports/training/t4-eval.json。真门禁（T4-G1-01 近讲/远场分层、
T4-G0-01 误唤醒泊松口径）仍由 Go 侧 gaterunner + synthgen 负批次执法——
本脚本只回答「这次训练值不值得提门禁」。

用法：
  training/.venv/bin/python training/t4/evaluate.py \
      --model training/t4/data/model/ai-toy-wakeword.onnx \
      --clips-dir training/t4/data/model/<验证集目录>   # train.py 生成的 test/val wav
退出码：0 通过（wake_rate ≥ --min-wake-rate）；20 未达；2 输入错误。
"""
import argparse
import datetime
import glob
import json
import os
import sys

import numpy as np

EXIT_OK, EXIT_INPUT, EXIT_GATE = 0, 2, 20


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", required=True, help="训练产出的 .onnx 模型路径")
    ap.add_argument("--clips-dir", required=True, help="验证集 wav 目录（递归收集）")
    ap.add_argument("--threshold", type=float, default=0.5, help="唤醒判定阈值（openwakeword 缺省 0.5）")
    ap.add_argument("--min-wake-rate", type=float, default=0.90,
                    help="本预检通过线（注意：T4-G1-01 近讲量产线是 0.97，由 Go 门禁执法）")
    ap.add_argument("--report", default="reports/training/t4-eval.json")
    args = ap.parse_args()

    if not os.path.isfile(args.model):
        print(f"error: 模型不存在: {args.model}", file=sys.stderr)
        return EXIT_INPUT
    wavs = sorted(glob.glob(os.path.join(args.clips_dir, "**", "*.wav"), recursive=True))
    if not wavs:
        print(f"error: 目录无 wav: {args.clips_dir}", file=sys.stderr)
        return EXIT_INPUT

    import scipy.io.wavfile
    from openwakeword import Model

    model = Model(wakeword_models=[args.model], inference_framework="onnx")
    scores, frame = [], 1280  # openwakeword 80ms 帧 @16kHz
    for wav in wavs:
        sr, data = scipy.io.wavfile.read(wav)
        if sr != 16000 or data.ndim != 1:
            continue  # openwakeword 管线统一 16k mono；异常样本跳过并计数
        if data.dtype != np.int16:
            data = (np.clip(data, -32768, 32767)).astype(np.int16)
        best = 0.0
        for i in range(0, max(len(data) - frame, 1), frame):
            chunk = data[i:i + frame]
            if len(chunk) < frame:
                chunk = np.pad(chunk, (0, frame - len(chunk)))
            best = max(best, max(model.predict(chunk).values()))
        scores.append(best)

    if not scores:
        print("error: 无有效 wav（须 16kHz mono）", file=sys.stderr)
        return EXIT_INPUT
    wake_rate = float(np.mean([s >= args.threshold for s in scores]))
    report = {
        "model": args.model,
        "clips_dir": args.clips_dir,
        "n": len(scores),
        "skipped": len(wavs) - len(scores),
        "threshold": args.threshold,
        "wake_rate": wake_rate,
        "mean_score": float(np.mean(scores)),
        "p95_score": float(np.percentile(scores, 95)),
        "min_wake_rate": args.min_wake_rate,
        "timestamp": datetime.datetime.now(datetime.timezone.utc).isoformat(),
    }
    os.makedirs(os.path.dirname(args.report), exist_ok=True)
    with open(args.report, "w", encoding="utf-8") as f:
        json.dump(report, f, ensure_ascii=False, indent=2)
    print(json.dumps(report, ensure_ascii=False, indent=2))
    if wake_rate < args.min_wake_rate:
        print(f"未达预检线 {args.min_wake_rate}（提高 n_samples/增强轮数后重训，见 config.yaml）", file=sys.stderr)
        return EXIT_GATE
    return EXIT_OK


if __name__ == "__main__":
    sys.exit(main())
