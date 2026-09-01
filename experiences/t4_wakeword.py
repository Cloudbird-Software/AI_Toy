#!/usr/bin/env python3
"""T4 唤醒词 KWS 本机体验 —— 批量过 wav 打印分数/判定，可选 --mic 实时模式。

流式口径对齐训练侧：openwakeword.Model.predict 80ms 帧 + 内部特征缓冲；
短于 2s 的音频右补零到 2s（批量 melspec 在短 clip 上差 14pp，是错误口径）。
操作阈值 0.20（近讲唤醒 0.9936、冻结批 6h 0 误唤醒的实测口径）。
"""
from __future__ import annotations

import argparse
import glob
import sys
from pathlib import Path

import numpy as np
import soundfile as sf

SR = 16000
CHUNK = 1280          # 80ms
PAD_SECONDS = 2.0
OPER_THRESHOLD = 0.20  # 操作阈值（repo 检测器配置口径）


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(description="T4 唤醒词 KWS 体验", formatter_class=argparse.ArgumentDefaultsHelpFormatter)
    p.add_argument("--models-dir", default="/root/workspace/gpu-prep/out", help="模型根目录")
    p.add_argument("--threshold", type=float, default=OPER_THRESHOLD, help="判定阈值")
    p.add_argument("--wav", nargs="*", help="指定 wav 文件（缺省用 assets/ 演示集）")
    p.add_argument("--mic", action="store_true", help="实时麦克风模式（无 mic 时友好退出）")
    return p.parse_args()


def load_model(models_dir: str):
    import openwakeword as oww
    onnx_path = Path(models_dir) / "kws" / "t4_wakeword.onnx"
    if not onnx_path.exists():
        sys.exit(f"[FAIL] 唤醒词模型不存在: {onnx_path}\n       请从 COS 拉取或指定 --models-dir。见 AGENT.md。")
    return oww.Model(wakeword_models=[str(onnx_path)], inference_framework="onnx", ncpu=1)


def score_clip(model, wav_path: str, pad_samples: int) -> dict:
    """单 clip 流式打分：80ms 帧扫描，返回 max_score 与是否唤醒。"""
    x, _ = sf.read(wav_path, dtype="int16", always_2d=True)
    pcm = x[:, 0].astype(np.int16)
    orig_len = len(pcm)
    # 短于 2s 右补零（与训练侧 featurize_streaming 口径一致）
    if len(pcm) < pad_samples:
        pcm = np.concatenate([pcm, np.zeros(pad_samples - len(pcm), dtype=np.int16)])
    model.reset()  # 流式模型有内部状态，文件间必须 reset
    scores = []
    for i in range(0, len(pcm) - CHUNK + 1, CHUNK):
        d = model.predict(pcm[i : i + CHUNK])
        scores.append(float(next(iter(d.values()))))
    scores = np.asarray(scores, dtype=np.float32)
    return {"max_score": float(scores.max()) if len(scores) else 0.0,
            "n_frames": len(scores), "dur_s": round(orig_len / SR, 2)}


def run_wav_mode(args, model) -> int:
    pad_samples = int(PAD_SECONDS * SR)
    wavs = args.wav
    if not wavs:
        # 缺省：assets/ 下 t4_*.wav（避免 T5 声纹素材被误处理）
        wavs = sorted(str(p) for p in Path("assets").glob("t4_*.wav"))
    if not wavs:
        sys.exit("[FAIL] 未指定 --wav 且 assets/ 下无 wav。")

    wake_total, wake_ok, fail_total, fail_ok = 0, 0, 0, 0
    print(f"# 阈值={args.threshold}  流式 80ms 帧 + 2s 右补零")
    print(f"# {'wav':<28s} {'时长':>5s} {'帧':>4s} {'max_score':>10s} {'判定':>6s}")
    print(f"# {'-'*28} {'-'*5} {'-'*4} {'-'*10} {'-'*6}")
    for w in wavs:
        r = score_clip(model, w, pad_samples)
        name = Path(w).name
        is_pos = name.startswith("t4_pos")
        is_neg = name.startswith("t4_neg")
        wake = r["max_score"] >= args.threshold
        verdict = "WAKE" if wake else "quiet"
        tag = ""
        if is_pos:
            wake_total += 1
            if wake:
                wake_ok += 1
                tag = " ✓"
            else:
                tag = " ✗ MISS"
        elif is_neg:
            fail_total += 1
            if not wake:
                fail_ok += 1
                tag = " ✓"
            else:
                tag = " ✗ FALSE-WAKE"
        print(f"  {name:<28s} {r['dur_s']:5.2f}s {r['n_frames']:4d} {r['max_score']:10.4f} {verdict:>6s}{tag}")

    print()
    if wake_total:
        print(f"  正例唤醒率: {wake_ok}/{wake_total} = {wake_ok/wake_total:.1%}  (门禁 ≥95%)")
    if fail_total:
        print(f"  负例零唤醒: {fail_ok}/{fail_total} = {fail_ok/fail_total:.1%}  (目标 100%)")
    ok = (wake_total == 0 or wake_ok / wake_total >= 0.95) and (fail_total == 0 or fail_ok == fail_total)
    print(f"  PASS={'是' if ok else '否'}")
    return 0 if ok else 2


def run_mic_mode(args, model) -> int:
    """实时麦克风模式。无 mic 时友好退出。"""
    try:
        import sounddevice as sd
    except ImportError:
        print("[INFO] 实时模式需要 sounddevice：pip install sounddevice")
        print("[INFO] 当前机器可能无麦克风；跳过实时模式，请用 wav 模式体验。")
        return 0

    import queue
    pad_samples = int(PAD_SECONDS * SR)
    q_: queue.Queue = queue.Queue()
    print(f"# 实时麦克风模式（阈值={args.threshold}，Ctrl+C 退出）")
    try:
        dev = sd.query_devices(kind="input")
        print(f"  输入设备: {dev['name']}")
    except Exception:
        print("[WARN] 未检测到输入设备；仍尝试打开默认输入流。")

    def cb(indata, frames, t, status):
        if status:
            print(f"  [stream status {status}]", file=sys.stderr)
        q_.put(indata[:, 0].astype(np.float32).copy())

    model.reset()
    try:
        with sd.InputStream(samplerate=SR, channels=1, blocksize=CHUNK, dtype="float32", callback=cb):
            print("  监听中 ...")
            while True:
                frame = q_.get()
                pcm = np.clip(frame * 32767, -32767, 32767).astype(np.int16)
                d = model.predict(pcm)
                score = float(next(iter(d.values())))
                bar = "#" * int(score * 40)
                mark = " <<<WAKE" if score >= args.threshold else ""
                print(f"\r  {score:.4f} |{bar:<40s}|{mark}", end="", flush=True)
    except KeyboardInterrupt:
        print("\n  退出。")
    except Exception as e:
        print(f"\n[WARN] 麦克风打开失败：{e}")
        print("  当前机器可能无麦克风；跳过实时模式，请用 wav 模式体验。")
    return 0


def main() -> int:
    args = parse_args()
    model = load_model(args.models_dir)
    if args.mic:
        return run_mic_mode(args, model)
    return run_wav_mode(args, model)


if __name__ == "__main__":
    raise SystemExit(main())
