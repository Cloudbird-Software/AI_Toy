#!/usr/bin/env python3
"""T4 唤醒词 CPU 评估 —— 近/远场唤醒率、儿童/成人公平性、冻结批误唤醒、RTF。

打分走 openwakeword.Model 官方流式 wrapper（80ms 帧 + 特征缓冲），与训练侧
特征口径一致；检测器事件语义对齐 Go 门禁（连续 confirm 帧超阈 + 不应期），
帧粒度 80ms（Go 侧 30ms，event 数量级一致，TRAINLOG 记录口径差异）。

远场口径（协议替身，资产卡无远场正样本语料）：val 正样本 + 加性噪声
SNR 20/10/5dB 三档 × {pink 平稳噪声, babble 四人混叠语音}，评估脚本本地生成，
不使用冻结批任何音频。训练侧脚本：t4_kws_train_cpu.py（切分逻辑复用同一模块）。

冻结批 gen-tneg/gen-kwsadv 只在本脚本出现（eval-only 纪律），逐 80ms 帧扫描
存分数，阈值扫描离线完成。
"""

from __future__ import annotations

import argparse
import csv
import json
import logging
import multiprocessing as mp
import sys
import time
from pathlib import Path

import numpy as np

sys.path.insert(0, "/root/workspace/gpu-prep")
from t4_kws_train_cpu import FP_EVAL_DIR, collect_neg_files, load_positives  # noqa: E402

SR = 16000
CHUNK = 1280  # 80ms
FRAME_S = CHUNK / SR

log = logging.getLogger("t4_eval")


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(formatter_class=argparse.ArgumentDefaultsHelpFormatter)
    p.add_argument("--onnx", default="/root/workspace/gpu-prep/out/t4-kws-cpu/t4_wakeword.onnx")
    p.add_argument("--pos-data-dir", default="/root/workspace/synth/t4_kws_audio")
    p.add_argument("--report-dir", default="/root/workspace/gpu-prep/reports/t4-kws")
    p.add_argument("--work-dir", default="/root/workspace/gpu-prep/out/t4-kws-cpu/work")
    p.add_argument("--model-name", default="t4_wakeword")
    p.add_argument("--threshold", type=float, default=0.5, help="主操作阈值（repo 检测器配置口径）")
    p.add_argument("--confirm-frames", type=int, default=2, help="连续超阈值帧数（80ms/帧，≈Go 30ms×3）")
    p.add_argument("--refractory-frames", type=int, default=8, help="事件不应期帧数（640ms）")
    p.add_argument("--fp-workers", type=int, default=4)
    p.add_argument("--rtf-seconds", type=float, default=60.0, help="RTF 实测音频时长")
    p.add_argument("--seed", type=int, default=42)
    p.add_argument("--skip-fp-scan", action="store_true", help="复用已存分数（调阈值时用）")
    return p.parse_args()


# ---------------------------------------------------------------- streaming scorer
_MODEL = {}


def _init_worker(onnx_path, model_name):
    import openwakeword as oww

    _MODEL["m"] = oww.Model(wakeword_models=[onnx_path], inference_framework="onnx", ncpu=1)
    _MODEL["name"] = model_name


def _scan_file(path: str) -> np.ndarray:
    """整文件 80ms 帧扫描，返回每帧置信度 float32。"""
    import soundfile as sf

    m = _MODEL["m"]
    scores = []
    with sf.SoundFile(path) as h:
        carry = np.empty(0, dtype=np.int16)
        while True:
            x = h.read(CHUNK * 64, dtype="int16", always_2d=True)
            if x.shape[0] == 0:
                break
            pcm = np.concatenate([carry, x[:, 0].astype(np.int16)])
            n = (len(pcm) // CHUNK) * CHUNK
            carry = pcm[n:]
            for i in range(0, n, CHUNK):
                d = m.predict(pcm[i : i + CHUNK])
                scores.append(float(next(iter(d.values()))))
    return np.asarray(scores, dtype=np.float32)


def count_events(scores: np.ndarray, th: float, confirm: int, refractory: int) -> int:
    """检测器事件：连续 confirm 帧超阈 + 不应期内合并。"""
    over = scores >= th
    n, i, events = len(over), 0, 0
    while i < n:
        if over[i : i + confirm].all() and i + confirm <= n:
            events += 1
            i += refractory
        else:
            i += 1
    return events


def score_clip(pcm: np.ndarray, th: float, confirm: int, refractory: int) -> dict:
    """单 clip：最大置信度（clip 级）+ 检测器事件。"""
    import openwakeword as oww

    m = _MODEL["m"]
    scores = []
    for i in range(0, len(pcm) - CHUNK + 1, CHUNK):
        d = m.predict(pcm[i : i + CHUNK])
        scores.append(float(next(iter(d.values()))))
    scores = np.asarray(scores, dtype=np.float32)
    return {"max_score": float(scores.max()) if len(scores) else 0.0,
            "event": count_events(scores, th, confirm, refractory) > 0,
            "scores": scores}


def get_model(args):
    if "m" not in _MODEL:
        _init_worker(args.onnx, args.model_name)
    return _MODEL["m"]


# ---------------------------------------------------------------- noise (far-field proxy)
def make_pink(n: int, rng) -> np.ndarray:
    x = rng.standard_normal(n).astype(np.float32)
    X = np.fft.rfft(x)
    f = np.fft.rfftfreq(n, 1 / SR)
    X *= np.where(f > 0, 1 / np.sqrt(np.maximum(f, 1.0)), 1.0)
    y = np.fft.irfft(X, n).astype(np.float32)
    return y / (np.sqrt((y**2).mean()) + 1e-9)


def make_babble(n: int, files: list[str], rng) -> np.ndarray:
    import soundfile as sf

    y = np.zeros(n, dtype=np.float32)
    for f in rng.choice(files, size=min(4, len(files)), replace=False):
        x, _ = sf.read(f, dtype="float32")
        if len(x) <= n:
            start = rng.integers(0, max(1, n - len(x) + 1))
            y[start : start + len(x)] += x
        else:
            start = rng.integers(0, len(x) - n)
            y += x[start : start + n]
    y /= (np.sqrt((y**2).mean()) + 1e-9)
    return y


def mix_snr(speech: np.ndarray, noise: np.ndarray, snr_db: float) -> np.ndarray:
    s_rms = np.sqrt((speech.astype(np.float64) ** 2).mean())
    n_rms = np.sqrt((noise.astype(np.float64) ** 2).mean())
    k = s_rms / (n_rms * 10 ** (snr_db / 20) + 1e-12)
    out = speech.astype(np.float32) + noise * k
    return np.clip(out, -32767, 32767)


# ---------------------------------------------------------------- eval sections
def eval_wake(args, clips: list[dict], tag: str, rows: list, th) -> dict:
    """通用唤醒率评估：clips=[{wav, word, style, aug, ...}]，rows 收集明细。"""
    import soundfile as sf

    res: dict[str, list[int]] = {}
    for c in clips:
        x, _ = sf.read(c["wav"], dtype="int16")
        r = score_clip(x, th, args.confirm_frames, args.refractory_frames)
        rows.append({"tag": tag, **{k: c.get(k) for k in ("word", "style", "aug", "base")},
                     "wav": Path(c["wav"]).name, **{k: r[k] for k in ("max_score", "event")}})
        for g in (c.get("word"), c.get("aug"), c.get("style")):
            if g:
                res.setdefault(str(g), []).append(int(r["event"]))
    total = [int(r["event"]) for r in rows if r["tag"] == tag]
    return {"n": len(total), "wake_rate": sum(total) / max(1, len(total)),
            "by_key": {k: sum(v) / len(v) for k, v in res.items()}}


def eval_fp(args) -> dict:
    """冻结批扫描：gen-tneg 全量 + gen-kwsadv 全量，存每帧分数。"""
    out = {}
    for gen in ("gen-tneg", "gen-kwsadv"):
        seg_dir = Path(FP_EVAL_DIR) / gen
        score_dir = Path(args.work_dir) / "scores" / gen
        score_dir.mkdir(parents=True, exist_ok=True)
        segs = sorted(seg_dir.glob("seg_*.wav"))
        todo = [s for s in segs if not (score_dir / (s.stem + ".npy")).exists()]
        if todo and not args.skip_fp_scan:
            log.info("扫描 %s：%d 段（%d 已缓存）", gen, len(todo), len(segs) - len(todo))
            t0 = time.time()
            with mp.Pool(args.fp_workers, initializer=_init_worker,
                         initargs=(args.onnx, args.model_name)) as pool:
                for i, (seg, sc) in enumerate(zip(todo, pool.imap(_scan_file, [str(s) for s in todo]))):
                    np.save(score_dir / (Path(seg).stem + ".npy"), sc)
                    log.info("  %s %d/%d  %.1fs", seg.name, i + 1, len(todo), time.time() - t0)
        total_frames = 0
        for s in segs:
            total_frames += int(np.load(score_dir / (s.stem + ".npy")).shape[0])
        out[gen] = {"segments": len(segs), "frames": total_frames,
                    "hours": total_frames * FRAME_S / 3600}
    return out


def main() -> int:
    args = parse_args()
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    rng = np.random.default_rng(args.seed)
    report_dir = Path(args.report_dir)
    report_dir.mkdir(parents=True, exist_ok=True)
    th, cf, rf = args.threshold, args.confirm_frames, args.refractory_frames
    get_model(args)

    # ---------- 正样本切分（与训练侧一致） ----------
    pos_train, pos_val = load_positives(Path(args.pos_data_dir),
                                        type("A", (), {"val_ratio": 0.2})())
    wake_rows: list[dict] = []

    # ---------- 近讲（干净 val 正样本） ----------
    near = eval_wake(args, pos_val, "near", wake_rows, th)
    log.info("近讲唤醒率 %.4f（n=%d）", near["wake_rate"], near["n"])

    # ---------- 远场（SNR 档 × 噪声类型，协议替身） ----------
    neg_train_f, neg_val_f = collect_neg_files(
        ["/root/workspace/synth/t5_sv_audio/audio", "/root/workspace/synth/t13_audio/audio",
         "/root/workspace/synth/t7_audio/audio"],
        type("A", (), {"val_ratio": 0.2})())
    far = {}
    import soundfile as sf

    for snr in (20, 10, 5):
        for kind, maker in (("pink", lambda n: make_pink(n, rng)),
                            ("babble", lambda n: make_babble(n, neg_val_f, rng))):
            key = f"snr{snr}_{kind}"
            clips = []
            for c in pos_val:
                x, _ = sf.read(c["wav"], dtype="float32")
                noise = maker(len(x))
                xm = mix_snr(x, noise, snr)
                c2 = dict(c)
                c2["wav"] = str(Path(args.work_dir) / "far" / f"{Path(c['wav']).stem}_{key}.wav")
                Path(c2["wav"]).parent.mkdir(parents=True, exist_ok=True)
                sf.write(c2["wav"], xm, SR, subtype="PCM_16")  # float 直写，sf 负责 int16 缩放
                clips.append(c2)
            far[key] = eval_wake(args, clips, key, wake_rows, th)
            log.info("远场 %s 唤醒率 %.4f", key, far[key]["wake_rate"])

    # ---------- 公平性（儿童/成人，val 底片） ----------
    val_bases = {c["base"] for c in pos_val}
    import json as _json

    all_rows = [_json.loads(l) for l in open(Path(args.pos_data_dir) / "transcripts.jsonl", encoding="utf-8") if l.strip()]
    fair = {"child": [], "adult": []}
    for r in all_rows:
        stem = Path(r["audio_path"]).stem
        if stem.rsplit("_", 1)[0] in val_bases and "audio/fairness" in r["audio_path"]:
            group = "child" if "child" in r["aug"] else "adult"
            fair[group].append({**r, "base": stem.rsplit("_", 1)[0], "word": r["word"],
                                "style": r["style"], "aug": r["aug"],
                                "wav": str(Path(args.pos_data_dir) / r["audio_path"])})
    fair_res = {}
    for g, clips in fair.items():
        fair_res[g] = eval_wake(args, clips, f"fair_{g}", wake_rows, th)
    gap = (fair_res["adult"]["wake_rate"] - fair_res["child"]["wake_rate"]) if fair_res["adult"]["n"] and fair_res["child"]["n"] else None
    log.info("公平性 child %.4f (n=%d) adult %.4f (n=%d) gap=%.4f",
             fair_res["child"]["wake_rate"], fair_res["child"]["n"],
             fair_res["adult"]["wake_rate"], fair_res["adult"]["n"], gap or -1)

    # ---------- 冻结批误唤醒 ----------
    fp_info = eval_fp(args)
    thresholds = [round(t, 2) for t in np.arange(0.10, 0.91, 0.05)]
    fp_sweep = {}
    for gen in fp_info:
        seg_dir = Path(args.work_dir) / "scores" / gen
        arrs = [np.load(f) for f in sorted(seg_dir.glob("*.npy"))]
        allsc = np.concatenate(arrs) if arrs else np.empty(0, dtype=np.float32)
        # 段边界处分数不连续——事件计数逐段进行
        fp_sweep[gen] = {}
        for t in thresholds:
            ev = sum(count_events(a, t, cf, rf) for a in arrs)
            fp_sweep[gen][t] = ev
        fp_info[gen]["events@th"] = fp_sweep[gen].get(th)
        fp_info[gen]["per_hour@th"] = fp_info[gen]["events@th"] / fp_info[gen]["hours"]
        log.info("%s @th=%.2f: %d events / %.2fh = %.3f /h", gen, th,
                 fp_info[gen]["events@th"], fp_info[gen]["hours"], fp_info[gen]["per_hour@th"])

    # 逐 source 细分（blocks.jsonl 全局时间轴对齐事件帧）
    fp_by_source = {}
    for gen in ("gen-tneg", "gen-kwsadv"):
        blocks = sorted((json.loads(l) for l in open(Path(FP_EVAL_DIR) / gen / "blocks.jsonl")),
                        key=lambda b: b["start_ms"])
        starts = [b["start_ms"] for b in blocks]
        import bisect

        per = {}
        for f in sorted((Path(args.work_dir) / "scores" / gen).glob("*.npy")):
            sc = np.load(f)
            seg_start_ms = int(f.stem.split("_")[1]) * 600000  # seg_XXXX 每段 600s
            over = sc >= th
            i = 0
            while i < len(over):
                if over[i : i + cf].all() and i + cf <= len(over):
                    ts = seg_start_ms + i * FRAME_S * 1000
                    j = bisect.bisect_right(starts, ts) - 1
                    if j >= 0 and ts < blocks[j]["start_ms"] + blocks[j]["dur_ms"]:
                        src = blocks[j]["source"]
                        per[src] = per.get(src, 0) + 1
                    i += rf
                else:
                    i += 1
        fp_by_source[gen] = dict(sorted(per.items()))

    # ---------- RTF ----------
    rtf = {}
    seg0 = str(sorted(Path(FP_EVAL_DIR, "gen-tneg").glob("seg_*.wav"))[0])
    import soundfile as sf

    tflite_path = Path(args.onnx).with_suffix(".tflite")
    for fw, model_path in (("onnx", Path(args.onnx)), ("tflite", tflite_path)):
        if fw == "tflite" and not tflite_path.exists():
            log.info("TFLite 头模型不存在，跳过 tflite RTF")
            continue
        import openwakeword as oww

        m = oww.Model(wakeword_models=[str(model_path)], inference_framework=fw, ncpu=1)
        x, _ = sf.read(seg0, dtype="int16", frames=int(args.rtf_seconds * SR))
        t0 = time.time()
        nfr = 0
        for i in range(0, len(x) - CHUNK + 1, CHUNK):
            m.predict(x[i : i + CHUNK])
            nfr += 1
        dt = time.time() - t0
        rtf[fw] = {"audio_s": round(nfr * FRAME_S, 2), "proc_s": round(dt, 3),
                   "rtf": round(dt / (nfr * FRAME_S), 4)}

    # ---------- 落盘 ----------
    with open(report_dir / "wake_scores.csv", "w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=list(wake_rows[0].keys()))
        w.writeheader()
        w.writerows(wake_rows)
    with open(report_dir / "fp_sweep.csv", "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["generator", "threshold", "events", "hours", "per_hour"])
        for gen in fp_sweep:
            for t, ev in fp_sweep[gen].items():
                w.writerow([gen, t, ev, fp_info[gen]["hours"], ev / fp_info[gen]["hours"]])

    metrics = {
        "threshold": th, "confirm_frames": cf, "refractory_frames": rf,
        "near": near, "far": far, "fairness": {
            "child": fair_res["child"], "adult": fair_res["adult"], "gap_adult_minus_child": gap},
        "fp": {g: {k: v for k, v in fp_info[g].items()} for g in fp_info},
        "fp_by_source@th": fp_by_source,
        "rtf": rtf,
        "gates": {
            "T4-G1-01_near>=0.97": near["wake_rate"] >= 0.97,
            "T4-G1-01_far>=0.90(all_snr)": all(v["wake_rate"] >= 0.90 for v in far.values()) if far else False,
            "T4-G1-02_gap<=0.05": gap is not None and gap <= 0.05,
            "T4-G0-01_fp<=0.5_per_h@6h": fp_info["gen-tneg"]["per_hour@th"] <= 0.5,
            "T4-G0-02_adv==0@30min": fp_info["gen-kwsadv"]["events@th"] == 0,
            "T4-G1-03_rtf<=0.1": min(v["rtf"] for v in rtf.values()) <= 0.1,
        },
    }
    (report_dir / "metrics.json").write_text(json.dumps(metrics, indent=2, ensure_ascii=False))
    log.info("metrics.json 已写 %s", report_dir / "metrics.json")
    log.info("gates: %s", json.dumps(metrics["gates"], ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
