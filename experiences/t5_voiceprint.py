#!/usr/bin/env python3
"""T5 声纹 SV 本机体验 —— enroll（若干 wav）→ verify（同人/陌生人）打印相似度与判定。

ONNX 模型输入为 80 维 log-mel fbank（T,80），用 librosa 复现 speechbrain 风格 fbank：
25ms 窗 / 10ms 步 / 80 mel / fmax=8000 / power=2 → power_to_db。
embedding 经 L2 归一化后做余弦相似度；同人显著高于陌生人即 PASS。
"""
from __future__ import annotations

import argparse
import sys
from pathlib import Path

import numpy as np
import soundfile as sr

SR = 16000


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(description="T5 声纹 SV 体验", formatter_class=argparse.ArgumentDefaultsHelpFormatter)
    p.add_argument("--models-dir", default="/root/workspace/gpu-prep/out", help="模型根目录")
    p.add_argument("--threshold", type=float, default=0.50, help="同人判定阈值（余弦相似度）")
    p.add_argument("--enroll", nargs="*", help="注册 wav 列表（缺省用 assets/t5_mother_enroll.wav）")
    p.add_argument("--verify", nargs="*", help="验证 wav 列表（缺省用 assets/ 演示集）")
    return p.parse_args()


def load_model(models_dir: str):
    import onnxruntime as ort
    onnx_path = Path(models_dir) / "sv" / "t5_ecapa.onnx"
    if not onnx_path.exists():
        sys.exit(f"[FAIL] 声纹模型不存在: {onnx_path}\n       请从 COS 拉取或指定 --models-dir。见 AGENT.md。")
    return ort.InferenceSession(str(onnx_path), providers=["CPUExecutionProvider"])


def fbank(wav_path: str) -> np.ndarray:
    """80 维 log-mel fbank，形状 (T, 80)。"""
    import librosa
    x, sr_ = sr.read(wav_path, dtype="float32", always_2d=True)
    x = x[:, 0]
    if sr_ != SR:
        x = librosa.resample(x, orig_sr=sr_, target_sr=SR)
    mel = librosa.feature.melspectrogram(y=x, sr=SR, n_fft=400, hop_length=160,
                                         win_length=400, n_mels=80, fmax=8000, power=2.0)
    log_mel = librosa.power_to_db(mel, ref=np.max)  # (80, T)
    return log_mel.T.astype(np.float32)              # (T, 80)


def embed(session, wav_path: str) -> np.ndarray:
    """提取 L2 归一化的 192 维声纹 embedding。"""
    feats = fbank(wav_path)[None]  # (1, T, 80)
    out = session.run(None, {"fbank": feats, "lengths": np.array([1.0], np.float32)})[0]
    emb = out[0, 0]
    return emb / (np.linalg.norm(emb) + 1e-9)


def cosine(a: np.ndarray, b: np.ndarray) -> float:
    return float(np.dot(a, b))


def main() -> int:
    args = parse_args()
    session = load_model(args.models_dir)

    # 缺省演示集
    assets = Path("assets")
    enroll_wavs = args.enroll or [str(assets / "t5_mother_enroll.wav")]
    if not args.verify:
        verify_wavs = sorted(str(p) for p in assets.glob("t5_*.wav") if p.name not in {Path(enroll_wavs[0]).name})
    else:
        verify_wavs = args.verify

    print(f"# 阈值={args.threshold}  余弦相似度（L2 归一化 embedding）")
    print()

    # ---------- enroll：多条取均值 centroid ----------
    print(f"【注册】{len(enroll_wavs)} 条 → centroid")
    enroll_embs = [embed(session, w) for w in enroll_wavs]
    centroid = np.mean(enroll_embs, axis=0)
    centroid = centroid / (np.linalg.norm(centroid) + 1e-9)
    for w, e in zip(enroll_wavs, enroll_embs):
        print(f"  · {Path(w).name:<24s} 与centroid余弦={cosine(e, centroid):.4f}")
    print()

    # ---------- verify ----------
    print("【验证】")
    pos_ok, neg_ok, pos_n, neg_n = 0, 0, 0, 0
    print(f"# {'wav':<24s} {'余弦':>8s} {'判定':>8s} {'期望':>6s}")
    print(f"# {'-'*24} {'-'*8} {'-'*8} {'-'*6}")
    for w in verify_wavs:
        e = embed(session, w)
        sim = cosine(centroid, e)
        name = Path(w).name
        # 期望标签：mother=同人，father/stranger=异人
        if "mother" in name and "verify" in name:
            expected, pos_n = "同人", pos_n + 1
            verdict = "PASS" if sim >= args.threshold else "FAIL"
            if sim >= args.threshold:
                pos_ok += 1
            tag = " ✓" if verdict == "PASS" else " ✗"
        else:
            expected, neg_n = "异人", neg_n + 1
            verdict = "PASS" if sim < args.threshold else "FAIL"
            if sim < args.threshold:
                neg_ok += 1
            tag = " ✓" if verdict == "PASS" else " ✗"
        print(f"  {name:<24s} {sim:8.4f} {verdict:>8s} {expected:>6s}{tag}")
    print()

    pos_rate = pos_ok / pos_n if pos_n else 1.0
    neg_rate = neg_ok / neg_n if neg_n else 1.0
    print(f"  同人通过率: {pos_ok}/{pos_n} = {pos_rate:.0%}")
    print(f"  异人拒绝率: {neg_ok}/{neg_n} = {neg_rate:.0%}")
    ok = pos_rate >= 0.95 and neg_rate >= 0.95
    print(f"  PASS={'是' if ok else '否'}")
    return 0 if ok else 2


if __name__ == "__main__":
    raise SystemExit(main())
