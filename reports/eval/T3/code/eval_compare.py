#!/usr/bin/env python3
"""T3 对照评测：MC-VAP（噪声鲁棒）vs plain VAP（ch_kyoto，同 MIT 同架构）。
场景：真实成人语音（MaAI 示例 wav，eng/jpn）× SNR {clean,20,10,5}dB × 噪声 {pink,babble 代理}，
指标：VAD 追踪（与帧级能量门限伪真值的一致性）、p_now_user 分布、全静音 p_now_sys 上限、RTF。
英语原版 VAP 基线（Erik Ekstedt 原仓）权重未随本任务提供且训练数据许可（Switchboard/LDC）
不适配商用——对照口径说明见报告。"""
import sys
sys.path.insert(0, "/root/workspace/t3work")
import stubaudio  # noqa: F401

import json
import time

import numpy as np
import onnxruntime as ort
import soundfile as sf

NEW, PAD, WIN = 1600, 320, 1920
WAVDIR = "/root/workspace/datasets/models/maai-vap/MaAI/example/wav_sample"
MODELS = {
    "mc_ch_kyoto_10hz": "/root/workspace/t3work/vap_maai_ch_kyoto_mc_10hz.onnx",
    "ch_kyoto_10hz": "/root/workspace/t3work/vap_maai_ch_kyoto_10hz.onnx",
}


def session_for(path):
    so = ort.SessionOptions()
    so.intra_op_num_threads = 2
    so.inter_op_num_threads = 1
    return ort.InferenceSession(path, so, providers=["CPUExecutionProvider"])


def stream(sess, mono, frames_limit=300, noise=None, snr_db=None):
    names = [i.name for i in sess.get_inputs()]
    state = {n: np.zeros([d if isinstance(d, int) else 1 for d in sess.get_inputs()[i].shape],
                         dtype=np.float32) for i, n in enumerate(names)}
    sig = mono.copy()
    if noise is not None and snr_db is not None:
        reps = -(-len(sig) // len(noise))
        n = np.tile(noise, reps)[: len(sig)]
        ps = np.mean(sig ** 2) + 1e-12
        pn = np.mean(n ** 2) + 1e-12
        sig = sig + n * np.sqrt(ps / pn / (10 ** (snr_db / 10)))
    buf = np.zeros(PAD, dtype=np.float32)
    rows = []
    t0 = time.time()
    steps = 0
    for k in range(min(len(sig) // NEW, frames_limit)):
        w = np.concatenate([buf, sig[k * NEW:(k + 1) * NEW]])
        buf = w[-PAD:].copy()
        feed = {"wave1": w[None, None], "wave2": np.zeros((1, 1, WIN), dtype=np.float32)}
        for n in names[2:]:
            feed[n] = state[n]
        outs = sess.run(None, feed)
        for i, o in enumerate(sess.get_outputs()):
            state[o.name.removesuffix("_out")] = outs[i]
        rows.append((float(outs[2].reshape(-1)[0]), float(outs[0].reshape(-1)[0]),
                     float(outs[0].reshape(-1)[1])))
        steps += 1
    wall = time.time() - t0
    return np.array(rows), wall / steps


def pseudo_truth(sig, frame_hz=10.0, thresh=3.0):
    """帧级能量伪真值（评估口径，非标注真值）：帧 RMS > 全局中位 RMS×thresh → 语音。"""
    n = len(sig) // NEW
    rms = np.array([np.sqrt(np.mean(sig[i * NEW:(i + 1) * NEW] ** 2) + 1e-20) for i in range(n)])
    return rms > np.median(rms) * thresh


def main():
    rng = np.random.RandomState(42)
    results = {}
    # 全静音面
    for name, path in MODELS.items():
        sess = session_for(path)
        names = [i.name for i in sess.get_inputs()]
        state = {n: np.zeros([d if isinstance(d, int) else 1 for d in sess.get_inputs()[i].shape],
                             dtype=np.float32) for i, n in enumerate(names)}
        mx = 0.0
        for k in range(200):
            feed = {"wave1": np.zeros((1, 1, WIN), dtype=np.float32),
                    "wave2": np.zeros((1, 1, WIN), dtype=np.float32)}
            for n in names[2:]:
                feed[n] = state[n]
            outs = sess.run(None, feed)
            for i, o in enumerate(sess.get_outputs()):
                state[o.name.removesuffix("_out")] = outs[i]
            mx = max(mx, float(outs[0].reshape(-1)[1]))
        results.setdefault("silence_pnowsys_max", {})[name] = round(mx, 4)

    wavs = {"eng_divesh": f"{WAVDIR}/eng_divesh_16k.wav", "jpn_inoue": f"{WAVDIR}/jpn_inoue_16k.wav"}
    babble = None
    try:
        b, sr = sf.read(f"{WAVDIR}/eng_mix_16k.wav", dtype="float32", always_2d=True)
        assert sr == 16000
        babble = b.mean(axis=1)
    except Exception:
        pass

    for wav_name, wav_path in wavs.items():
        audio, sr = sf.read(wav_path, dtype="float32", always_2d=True)
        assert sr == 16000
        mono = audio.mean(axis=1)
        truth = pseudo_truth(mono)
        for name, path in MODELS.items():
            sess = session_for(path)
            for snr in ("clean", 20, 10, 5):
                noise = None
                if snr != "clean":
                    noise = rng.randn(min(len(mono), NEW * 400)).astype(np.float32)
                    if babble is not None and snr == 5:
                        noise = babble[: min(len(mono), NEW * 400)].astype(np.float32)
                rows, per_frame = stream(sess, mono, noise=noise, snr_db=None if snr == "clean" else snr)
                n = min(len(rows), len(truth))
                pred = rows[:n, 0] > 0.5
                agree = float(np.mean(pred[:n] == truth[:n]))
                pn_u = rows[:n, 1]
                key = f"{wav_name}_{snr}"
                results.setdefault(key, {})[name] = {
                    "vad_agree_vs_energy_truth": round(agree, 4),
                    "pnow_user_p50": round(float(np.percentile(pn_u, 50)), 4),
                    "pnow_user_p95": round(float(np.percentile(pn_u, 95)), 4),
                    "per_frame_s": round(per_frame, 4),
                }
    print(json.dumps(results, indent=1, ensure_ascii=False))
    with open("/root/workspace/t3work/comparison.json", "w") as f:
        json.dump(results, f, indent=1, ensure_ascii=False)
    print("written /root/workspace/t3work/comparison.json")


if __name__ == "__main__":
    main()
