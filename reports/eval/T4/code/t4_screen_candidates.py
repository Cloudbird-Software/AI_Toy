#!/usr/bin/env python3
"""T4 候选 checkpoint 快速筛选器。

原理：melspec+embedding 特征与头模型无关，一次性预计算成帧级 embedding .npy；
之后每个候选头模型（ONNX）扫全阈段只要秒级——纯批量矩阵乘。

用法：
  # 1) 一次性特征化（冻结批 + v3 val 正样本）
  t4_screen_candidates.py featurize
  # 2) 筛选候选（一个或多个 onnx）
  t4_screen_candidates.py screen cand1.onnx [cand2.onnx ...]

口径说明：与 t4_kws_eval_cpu.py 的差异——整段一次性算 melspec（流式是按 80ms 块喂，
边界处有随机初始化 buffer 的残留）， screening 用于排序，最终候选仍走 harness 全量确认。
"""
import sys
import re
import time
from pathlib import Path

import numpy as np

SR = 16000
CHUNK = 1280            # 80ms
FRAME_S = CHUNK / SR
FEAT_DIR = Path("/root/workspace/gpu-prep/out/t4-screen/feats")
FP_EVAL_DIR = Path("/root/workspace/gpu-prep/out/t4-kws-cpu/fp_eval")
POS_DIR = Path("/root/workspace/synth/t4_kws_audio_v3")
FEATURE_MODELS = Path("/root/workspace/gpu-prep/out/t4-kws-cpu/feature_models")
THRESHOLDS = [round(t, 2) for t in np.arange(0.05, 0.91, 0.05)]


def _sessions():
    import onnxruntime as ort
    so = ort.SessionOptions()
    so.inter_op_num_threads = 4
    so.intra_op_num_threads = 4
    mel = ort.InferenceSession(str(FEATURE_MODELS / "melspectrogram.onnx"),
                               sess_options=so, providers=["CPUExecutionProvider"])
    emb = ort.InferenceSession(str(FEATURE_MODELS / "embedding_model.onnx"),
                               sess_options=so, providers=["CPUExecutionProvider"])
    return mel, emb


def embed_audio(mel, emb, pcm_f32: np.ndarray) -> np.ndarray:
    """整段 PCM(float32, int16 取值) → 帧级 embedding (N, 96)。复刻 AudioFeatures:
    melspec → x/10+2 → 76 帧窗、步进 8 → embedding 模型。"""
    spec = mel.run(None, {"input": pcm_f32[None, :]})[0].squeeze()  # (T, 32)
    spec = spec / 10 + 2
    T = spec.shape[0]
    n_win = (T - 76) // 8 + 1
    if n_win <= 0:
        return np.empty((0, 96), dtype=np.float32)
    idx = np.arange(76)[None, :] + 8 * np.arange(n_win)[:, None]
    windows = spec[idx]                                     # (N, 76, 32)
    windows = windows[..., None].astype(np.float32)         # (N, 76, 32, 1)
    out = []
    for i in range(0, n_win, 512):
        e = emb.run(None, {"input_1": windows[i:i + 512]})[0]  # (B,1,1,96)
        out.append(e.reshape(-1, 96))
    return np.concatenate(out).astype(np.float32)


def featurize():
    import soundfile as sf
    mel, emb = _sessions()
    # 冻结批
    for gen in ("gen-tneg", "gen-kwsadv"):
        out_dir = FEAT_DIR / gen
        out_dir.mkdir(parents=True, exist_ok=True)
        segs = sorted((FP_EVAL_DIR / gen).glob("seg_*.wav"))
        for i, s in enumerate(segs):
            dst = out_dir / (s.stem + ".npy")
            if dst.exists():
                continue
            t0 = time.time()
            x, _ = sf.read(str(s), dtype="int16")
            e = embed_audio(mel, emb, x.astype(np.float32))
            np.save(dst, e)
            print(f"{gen}/{s.stem}: {e.shape} {time.time()-t0:.1f}s ({i+1}/{len(segs)})", flush=True)
    # v3 val 正样本（harness 同口径：t4_kws_train_cpu 底片级切分 val_ratio=0.2）
    sys.path.insert(0, "/root/workspace/gpu-prep")
    from t4_kws_train_cpu import load_positives
    pos_train, pos_val = load_positives(POS_DIR, type("A", (), {"val_ratio": 0.2})())
    out_dir = FEAT_DIR / "pos_val"
    out_dir.mkdir(parents=True, exist_ok=True)
    for i, c in enumerate(pos_val):
        dst = out_dir / (Path(c["wav"]).stem + ".npy")
        if dst.exists():
            continue
        x, _ = sf.read(c["wav"], dtype="int16")
        # 短于 2s 的 clip 右补零到 2s：否则 embedding 帧 <16 取不到头窗口，
        # 分数全 0 是特征化伪影而非模型漏检（流式评估靠 buffer 补）。v3 有 157 条此类。
        if len(x) < int(2.0 * SR):
            x = np.concatenate([x, np.zeros(int(2.0 * SR) - len(x), dtype=x.dtype)])
        e = embed_audio(mel, emb, x.astype(np.float32))
        np.save(dst, e)
        if (i + 1) % 100 == 0:
            print(f"pos_val {i+1}/{len(pos_val)}", flush=True)
    print("featurize done", flush=True)


def stream_embed_clip(af, x_int16: np.ndarray) -> np.ndarray:
    """用官方 AudioFeatures 流式路径逐 80ms 帧产出 embedding（复刻 harness 口径）：
    每 clip 独立 AudioFeatures 实例（reset 语义）。返回新增帧（不含初始化随机噪声帧）。"""
    af.reset()
    n0 = af.feature_buffer.shape[0]
    frames = []
    for i in range(0, len(x_int16) - CHUNK + 1, CHUNK):
        before = af.feature_buffer.shape[0]
        af(x_int16[i:i + CHUNK])
        after = af.feature_buffer.shape[0]
        if after > before:
            frames.extend(af.feature_buffer[before - after:] if after - before < 0
                          else af.feature_buffer[-(after - before):])
    return np.asarray(frames, dtype=np.float32) if frames else np.empty((0, 96), np.float32)


def featurize_clips_streaming():
    """clip 类集合（pos_val + 远场六档）改用官方流式特征化。
    整段一次性 melspec 在 2s 短 clip 上与流式逐块口径差 14pp（边界语义不同），
    冻结批长音频不受影响（已验证 <1%），故冻结批保留批量特征。"""
    import soundfile as sf
    from openwakeword.utils import AudioFeatures

    af = AudioFeatures(inference_framework="onnx", ncpu=1)
    jobs = []
    sys.path.insert(0, "/root/workspace/gpu-prep")
    from t4_kws_train_cpu import load_positives
    _, pos_val = load_positives(POS_DIR, type("A", (), {"val_ratio": 0.2})())
    jobs.append(("pos_val", [(Path(c["wav"]).stem, c["wav"]) for c in pos_val]))
    far_src = Path("/root/workspace/gpu-prep/out/t4-kws-cpu/work_s36118/far")
    far_by_key = {}
    for wav in sorted(far_src.glob("*.wav")):
        m = re.search(r'_(snr\d+_\w+)$', wav.stem)
        if m:
            far_by_key.setdefault(f"far_{m.group(1)}", []).append((wav.stem, str(wav)))
    jobs.extend(sorted(far_by_key.items()))

    for tag, items in jobs:
        out_dir = FEAT_DIR / tag
        out_dir.mkdir(parents=True, exist_ok=True)
        n_done = 0
        for stem, wav in items:
            dst = out_dir / (stem + ".npy")
            if dst.exists():
                continue
            x, _ = sf.read(wav, dtype="int16")
            np.save(dst, stream_embed_clip(af, x))
            n_done += 1
            if n_done % 200 == 0:
                print(f"{tag}: {n_done} 新完成", flush=True)
        print(f"{tag} done ({len(items)})", flush=True)


def head_scores(sess, emb_frames: np.ndarray, bs: int = 4096) -> np.ndarray:
    """帧 embedding (N,96) → 每帧得分（16 帧窗、步进 1，对齐流式 80ms/帧）。
    候选 ONNX 导出时未开动态 batch（固定 batch=1），逐窗推理。"""
    n = len(emb_frames)
    if n < 16:
        return np.empty(0, dtype=np.float32)
    from numpy.lib.stride_tricks import sliding_window_view
    w = sliding_window_view(emb_frames, (16, 96))[:, 0]     # (N-15, 16, 96)
    name = sess.get_inputs()[0].name
    out = np.empty(len(w), dtype=np.float32)
    for i in range(len(w)):
        out[i] = sess.run(None, {name: w[i:i + 1].astype(np.float32, copy=False)})[0][0][0]
    return out


def count_events(scores: np.ndarray, th: float, confirm: int = 2, refractory: int = 8) -> int:
    over = scores >= th
    n, i, events = len(over), 0, 0
    while i < n:
        if over[i : i + confirm].all() and i + confirm <= n:
            events += 1
            i += refractory
        else:
            i += 1
    return events


def screen(onnx_paths):
    import onnxruntime as ort
    tneg = sorted((FEAT_DIR / "gen-tneg").glob("*.npy"))
    kwsadv = sorted((FEAT_DIR / "gen-kwsadv").glob("*.npy"))
    pos = sorted((FEAT_DIR / "pos_val").glob("*.npy"))
    far_sets = {d.name: sorted(d.glob("*.npy")) for d in sorted(FEAT_DIR.glob("far_*"))}
    tneg_h = sum(len(np.load(f)) for f in tneg) * FRAME_S / 3600
    adv_h = sum(len(np.load(f)) for f in kwsadv) * FRAME_S / 3600
    print(f"数据: tneg {len(tneg)} 段 {tneg_h:.2f}h, kwsadv {len(kwsadv)} 段 {adv_h:.2f}h, pos {len(pos)}, far { {k: len(v) for k, v in far_sets.items()} }")

    for onnx in onnx_paths:
        sess = ort.InferenceSession(str(onnx), providers=["CPUExecutionProvider"])
        tneg_sc = [head_scores(sess, np.load(f)) for f in tneg]
        adv_sc = [head_scores(sess, np.load(f)) for f in kwsadv]
        pos_sc = [head_scores(sess, np.load(f)) for f in pos]
        far_sc = {k: [head_scores(sess, np.load(f)) for f in v] for k, v in far_sets.items()}
        print(f"\n== {Path(onnx).name} ==")
        print("th\tnear(ev)\tfar_min(ev)\ttneg/h\tkwsadv/h\tALL-PASS")
        for th in THRESHOLDS:
            near = np.mean([count_events(s, th) > 0 for s in pos_sc])
            far_min = min((np.mean([count_events(s, th) > 0 for s in scs]) for scs in far_sc.values()),
                          default=float("nan"))
            tn = sum(count_events(s, th) for s in tneg_sc) / tneg_h
            ka = sum(count_events(s, th) for s in adv_sc) / adv_h
            ok = near >= 0.97 and far_min >= 0.90 and tn <= 0.5 and ka <= 0.0
            print(f"{th:.2f}\t{near:.4f}\t{far_min:.4f}\t{tn:.1f}\t{ka:.1f}\t{'YES' if ok else ''}", flush=True)


if __name__ == "__main__":
    cmd = sys.argv[1]
    if cmd == "featurize":
        featurize()
    elif cmd == "featurize_streaming":
        featurize_clips_streaming()
    elif cmd == "screen":
        screen(sys.argv[2:])
    else:
        print(__doc__)
        sys.exit(1)
