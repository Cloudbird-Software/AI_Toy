#!/usr/bin/env python3
"""Golden vectors (synthetic, Go-reproducible) + behavior probe on real speech."""
import sys
sys.path.insert(0, "/root/workspace/t3work")
import stubaudio  # noqa: F401
sys.path.insert(0, "/root/workspace/datasets/models/maai-vap/MaAI/src")

import json
import math
import numpy as np
import onnxruntime as ort
import soundfile as sf

ONNX = "/root/workspace/t3work/vap_maai_ch_kyoto_mc_10hz.onnx"
OUT = "/root/workspace/AI_Toy/packages/go/turntaking/vap/testdata/golden.json"
WAVDIR = "/root/workspace/datasets/models/maai-vap/MaAI/example/wav_sample"

N_FRAMES = 40
NEW = 1600
PAD = 320
WIN = PAD + NEW


def golden_chunk(frame_idx, j):
    """Deterministic synthetic frame, byte-reproducible in Go (float64 math,
    single sin per sample). Speech bursts 6/10 frames, silence 4/10."""
    k = frame_idx % 10
    if k >= 6:  # silence frames
        return 0.0
    freq = 220.0 + 110.0 * (frame_idx % 7)
    t = j / 16000.0
    env = math.sin(math.pi * j / NEW)  # per-frame envelope
    return 0.08 * math.sin(2.0 * math.pi * freq * t) * env


def main():
    so = ort.SessionOptions()
    so.intra_op_num_threads = 2
    so.inter_op_num_threads = 1
    sess = ort.InferenceSession(ONNX, so, providers=["CPUExecutionProvider"])
    names = [i.name for i in sess.get_inputs()]
    n_out = len(sess.get_outputs())

    state = {n: np.zeros([d if d else 1 for d in sess.get_inputs()[i].shape], dtype=np.float32)
             for i, n in enumerate(names)}

    frames = []
    buf1 = np.zeros(PAD, dtype=np.float32)
    for k in range(N_FRAMES):
        c1 = np.array([golden_chunk(k, j) for j in range(NEW)], dtype=np.float32)
        w1 = np.concatenate([buf1, c1])
        buf1 = w1[-PAD:].copy()
        feed = {"wave1": w1[None, None], "wave2": np.zeros((1, 1, WIN), dtype=np.float32)}
        for n in names[2:]:
            feed[n] = state[n]
        outs = sess.run(None, feed)
        # 状态滚动：输出名 _out 后缀 → 输入名（cache_mask_out→cache_mask 等）
        for i, o in enumerate(sess.get_outputs()):
            state[o.name.removesuffix("_out")] = outs[i]
        frames.append({
            "p_now": [round(float(v), 7) for v in np.asarray(outs[0]).reshape(-1)],
            "vad": [round(float(v), 7) for v in np.asarray(outs[2]).reshape(-1)],
        })
    with open(OUT, "w") as f:
        json.dump({"frames": frames, "tol": 1e-3,
                   "note": "合成正弦帧（golden_chunk 公式 Go/Python 可复现），ORT fp32 参考"},
                  f, indent=1)
    print("golden written:", OUT, len(frames), "frames")
    print("sample p_now/vad (frames 0,1,7,13):")
    for i in (0, 1, 7, 13):
        print(f"  f{i}: p_now={frames[i]['p_now']} vad={frames[i]['vad']}")

    # ---- behavior probe on real speech (not committed; eval evidence) -------
    def probe(wav, label):
        audio, sr = sf.read(wav, dtype="float32", always_2d=True)
        assert sr == 16000
        mono = audio.mean(axis=1)
        st = {n: np.zeros([d if d else 1 for d in sess.get_inputs()[i].shape], dtype=np.float32)
              for i, n in enumerate(names)}
        buf = np.zeros(PAD, dtype=np.float32)
        rows = []
        for k in range(min(len(mono) // NEW, 300)):
            w = np.concatenate([buf, mono[k * NEW:(k + 1) * NEW]])
            buf = w[-PAD:].copy()
            feed = {"wave1": w[None, None], "wave2": np.zeros((1, 1, WIN), dtype=np.float32)}
            for n in names[2:]:
                feed[n] = st[n]
            outs = sess.run(None, feed)
            for i, o in enumerate(sess.get_outputs()):
                st[o.name] = outs[i]
            rows.append((float(outs[0].reshape(-1)[0]), float(outs[0].reshape(-1)[1]),
                         float(outs[2].reshape(-1)[0])))
        pn_u = [r[0] for r in rows]
        pn_s = [r[1] for r in rows]
        vad_u = [r[2] for r in rows]
        print(f"[{label}] frames={len(rows)} p_now_user mean={np.mean(pn_u):.3f} "
              f"p50={np.percentile(pn_u,50):.3f} p95={np.percentile(pn_u,95):.3f} | "
              f"p_now_sys p95={np.percentile(pn_s,95):.3f} | vad_user p50={np.percentile(vad_u,50):.3f} "
              f">0.5 frac={np.mean([v > 0.5 for v in vad_u]):.2f}")
        return rows

    probe(f"{WAVDIR}/eng_divesh_16k.wav", "eng_divesh(真实成人语音)")
    probe(f"{WAVDIR}/jpn_inoue_16k.wav", "jpn_inoue(真实成人语音)")
    # silence probe
    st = {n: np.zeros([d if d else 1 for d in sess.get_inputs()[i].shape], dtype=np.float32)
          for i, n in enumerate(names)}
    pn_s = []
    for k in range(200):
        feed = {"wave1": np.zeros((1, 1, WIN), dtype=np.float32),
                "wave2": np.zeros((1, 1, WIN), dtype=np.float32)}
        for n in names[2:]:
            feed[n] = st[n]
        outs = sess.run(None, feed)
        for i, o in enumerate(sess.get_outputs()):
            st[o.name] = outs[i]
        pn_s.append(float(outs[0].reshape(-1)[1]))
    print(f"[全静音] p_now_sys p95={np.percentile(pn_s, 95):.4f} max={max(pn_s):.4f}")


if __name__ == "__main__":
    main()
