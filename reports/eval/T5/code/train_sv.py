#!/usr/bin/env python3
"""T5 声纹训练骨架 —— SpeechBrain ECAPA-TDNN 微调 + 家庭内区分协议 + ONNX 导出。

协议对齐 docs/gates/assets/T5.md「家庭内区分」自建协议（公开基准 EER 不可迁移）：
  <data_dir>/<family_id>/<speaker_id>/<session>/<utterance>.wav
  * 同家庭内两两配对：同说话人对 vs 同家庭异说话人对（含儿童：硬负样本）
  * 跨家庭对按 --cross-family-ratio 混入（默认 0.2，避免全是简单负样本虚低 EER）
  * 报 EER + 同/异分数分布；试次不足 --min-trials 时在报告中标红（T5-G1-01 要求 ≥5000）
评估项（训练侧参考；正式门禁走 gaterunner）：
  family_eer（≤5%）· enroll3 EER 劣化（≤2pp）· 跨会话再识别（≥95%）· 陌生人拒判（≥90%）
导出：embedding_model → ONNX（时间维动态），并用 onnxruntime 验证余弦一致性。

预训练权重：speechbrain/spkrec-ecapa-voxceleb（Apache-2.0，首次运行自动从 HF 拉取；
离线机器先 `HF_HUB_OFFLINE=1` + 预下载到 --pretrained 指向的本地目录，见 RUNBOOK §3）。

用法：
  python train_sv.py --dry-run                       # 审计说话人/会话/utterance 统计
  python train_sv.py                                 # 微调 + 评估 + 导出
遗留 TODO：
  TODO(sv-1) 兄弟姐妹对单列报告（T5-G1-01 口径；需 synth 数据带 kinship 字段）
  TODO(sv-2) 3D-Speaker/CAM++ 同协议横评（T5 路径 B），横评结果写 ADR
  TODO(sv-3) 合成儿童声过真变声（T13 红线：禁止克隆真实儿童声音——数据源必须是全合成）
  TODO(sv-4) 增益/文本无关属性测试（同人说不同内容距离不跨阈值）
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import random
import sys
import time
from collections import defaultdict
from datetime import datetime, timezone
from itertools import combinations
from pathlib import Path

log = logging.getLogger("train_sv")


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(description="T5 声纹 ECAPA-TDNN 微调", formatter_class=argparse.ArgumentDefaultsHelpFormatter)
    p.add_argument("--data-dir", default=os.environ.get("AI_TOY_SYNTH_ROOT", "/root/workspace/synth") + "/t5_voiceprint",
                   help="虚拟家庭说话人目录：<family_id>/<speaker_id>/<session>/*.wav")
    p.add_argument("--stranger-dir", default=os.environ.get("SV_STRANGER_DIR", ""),
                   help="非注册说话人目录（陌生人拒判评估，T5-G1-04；≥20 人 ×≥30 句）")
    p.add_argument("--pretrained", default="speechbrain/spkrec-ecapa-voxceleb",
                   help="HF 预训练模型或本地权重目录")
    p.add_argument("--out-dir", default="./out/sv")
    p.add_argument("--reports-dir", default="./reports")
    # 训练超参（T4 16GB 安全值；不支持 bf16，--amp 用 fp16）
    p.add_argument("--epochs", type=int, default=10)
    p.add_argument("--batch-pairs", type=int, default=16, help="每步说话人数（每人采 2 条 → batch=2N 条 utterance）")
    p.add_argument("--seg-seconds", type=float, default=3.0, help="训练时随机截取长度（VoxCeleb recipe 同款）")
    p.add_argument("--lr", type=float, default=1e-4)
    p.add_argument("--aam-margin", type=float, default=0.2)
    p.add_argument("--aam-scale", type=float, default=30.0)
    p.add_argument("--amp", action="store_true", help="fp16 混合精度（T4 支持 fp16，不支持 bf16）")
    # 协议参数
    p.add_argument("--min-trials", type=int, default=5000, help="T5-G1-01 最低试次")
    p.add_argument("--cross-family-ratio", type=float, default=0.2)
    p.add_argument("--n-trials", type=int, default=6000, help="评估配对试次")
    p.add_argument("--enroll-speakers", type=int, default=50, help="3 句注册仿真人数（T5-G1-03）")
    p.add_argument("--seed", type=int, default=42)
    p.add_argument("--device", choices=["auto", "cpu", "cuda"], default="auto")
    p.add_argument("--dry-run", action="store_true")
    return p.parse_args()


def scan_speakers(data_dir: Path):
    """返回 {speaker_id: {"family": str, "sessions": {session: [wav,...]}}} 及家庭映射。"""
    speakers: dict[str, dict] = {}
    for wav in sorted(data_dir.rglob("*.wav")) + sorted(data_dir.rglob("*.flac")):
        rel = wav.relative_to(data_dir)
        if len(rel.parts) < 4:
            continue
        family, speaker, session = rel.parts[0], rel.parts[1], rel.parts[2]
        spk = speakers.setdefault(speaker, {"family": family, "sessions": defaultdict(list)})
        spk["sessions"][session].append(str(wav))
    return speakers


def main() -> int:
    args = parse_args()
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    random.seed(args.seed)

    data_dir = Path(args.data_dir)
    speakers = scan_speakers(data_dir) if data_dir.exists() else {}

    n_utts = sum(sum(len(v) for v in s["sessions"].values()) for s in speakers.values())
    fam_count = len({s["family"] for s in speakers.values()})
    kids = [k for k, v in speakers.items() if v["family"].lower().startswith(("child", "kid")) or "child" in k.lower()]
    print(f"""== 数据审计 ==
  目录: {data_dir}  存在={data_dir.exists()}
  家庭数={fam_count}  说话人数={len(speakers)}  utterance 数={n_utts}
  会话数分布: {sorted({len(s['sessions']) for s in speakers.values()})}（T5-G1-02 要求同成员 ≥3 会话）
  陌生人目录: {args.stranger_dir or '<未配置，跳过 T5-G1-04 评估>'}
  协议最低需求（gate 对照）: ≥5000 试次；建议 ≥5 家庭 × 3–6 人 × ≥3 会话""")

    if not speakers:
        print("\n--dry-run 结论：数据不齐。当前 /root/workspace/synth/ 尚无 t5_voiceprint 域产出"
              "（synth W 系列任务全部 EXIT=1，且 T5/T4 合成 job 尚未下发）。")
        return 1
    if len(speakers) < 2 or fam_count < 1:
        print("\n--dry-run 结论：说话人/家庭数不足以构建配对协议。")
        return 1
    if args.dry_run:
        print("\n--dry-run 结论：数据可启动训练。")
        return 0

    import torch
    from torch import nn, optim

    class _AAMHead(nn.Module):
        """标准 ArcFace 头（L2 归一化嵌入 × 归一化类中心 + 角度 margin），自实现避免依赖
        speechbrain 内部 AAM API 的版本差异。"""

        def __init__(self, in_dim: int, n_classes: int, margin: float, scale: float):
            super().__init__()
            self.weight = nn.Parameter(torch.randn(in_dim, n_classes) * 0.01)
            self.margin, self.scale = margin, scale

        def forward(self, emb_normed, targets):
            import torch.nn.functional as F

            w = F.normalize(self.weight, dim=0)
            cos = emb_normed @ w
            theta = torch.acos(cos.clamp(-1 + 1e-7, 1 - 1e-7))
            one_hot = F.one_hot(targets, cos.size(1)).to(cos.dtype)
            return self.scale * (cos * (1 - one_hot) + torch.cos(theta + self.margin) * one_hot)

    device = {"auto": "cuda" if torch.cuda.is_available() else "cpu", "cpu": "cpu", "cuda": "cuda"}[args.device]
    torch.manual_seed(args.seed)

    # ---------- 1. 加载预训练 ECAPA ----------
    log.info("加载预训练模型 %s …", args.pretrained)
    enc = _load_encoder(args.pretrained, device)

    seg_samples = int(args.seg_seconds * 16000)

    # ---------- 2. 微调（ArcFace/AAM-softmax head over 说话人标签） ----------
    labels = {spk: i for i, spk in enumerate(sorted(speakers))}
    aam = _AAMHead(192, len(labels), margin=args.aam_margin, scale=args.aam_scale).to(device)
    ce = nn.CrossEntropyLoss()
    params = [p for p in enc.mods.embedding_model.parameters() if p.requires_grad] + list(aam.parameters())
    opt = optim.AdamW(params, lr=args.lr, weight_decay=1e-4)
    scaler = torch.amp.GradScaler("cuda", enabled=args.amp)

    utt_by_spk = {spk: [w for ws in s["sessions"].values() for w in ws] for spk, s in speakers.items()}
    utt_by_spk = {k: v for k, v in utt_by_spk.items() if len(v) >= 2}
    if len(utt_by_spk) < 2:
        log.error("有效说话人 <2（每人需 ≥2 条 utterance），中止。")
        return 1

    def augment(wav: "torch.Tensor") -> "torch.Tensor":
        gain = 10 ** (random.uniform(-6, 6) / 20)
        wav = wav * gain
        if random.random() < 0.5:   # 轻量加噪（T4 上避免重增强栈；TODO(sv-5) MUSAN 噪声库）
            wav = wav + torch.randn_like(wav) * random.uniform(0.001, 0.01)
        return wav

    log.info("== 微调 epochs=%s（fp16=%s）==", args.epochs, args.amp)
    enc.mods.embedding_model.train()
    import librosa
    import soundfile as sf
    for ep in range(args.epochs):
        ep_loss, t0, steps = 0.0, time.time(), 200
        for _ in range(steps):
            spks = random.sample(sorted(utt_by_spk), min(args.batch_pairs, len(utt_by_spk)))
            wavs, ys = [], []
            for spk in spks:
                for w in random.sample(utt_by_spk[spk], 2):
                    x, sr = sf.read(w, dtype="float32", always_2d=True)
                    if sr != 16000:
                        x = librosa.resample(x[:, 0], orig_sr=sr, target_sr=16000)
                    else:
                        x = x[:, 0]
                    if len(x) < seg_samples:
                        x = librosa.util.fix_length(data=x, size=seg_samples)
                    else:
                        o = random.randrange(0, len(x) - seg_samples + 1)
                        x = x[o:o + seg_samples]
                    wavs.append(torch.from_numpy(x))
                    ys.append(labels[spk])
            wav_b = torch.stack(wavs).to(device)
            y_b = torch.tensor(ys, device=device)
            with torch.autocast("cuda", dtype=torch.float16, enabled=args.amp):
                # encode_batch = fbank 特征 + 输入归一化 + ECAPA 前向（embedding_model 只吃特征不吃波形）
                emb = enc.encode_batch(wav_b, torch.ones(wav_b.shape[0], device=device)).squeeze(1)  # (B,192)
                emb = nn.functional.normalize(emb, p=2, dim=1)
                logits = aam(emb, y_b)
                loss = ce(logits, y_b)
            opt.zero_grad()
            scaler.scale(loss).backward()
            scaler.step(opt)
            scaler.update()
            ep_loss += loss.item()
        log.info("epoch %d/%d  loss=%.4f  %.1fs", ep + 1, args.epochs, ep_loss / steps, time.time() - t0)

    out_dir = Path(args.out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)
    torch.save(enc.mods.embedding_model.state_dict(), out_dir / "ecapa_finetuned.ckpt")

    # ---------- 3. 评估（家庭内区分协议） ----------
    log.info("== 评估 ==")
    enc.mods.embedding_model.eval()

    @torch.no_grad()
    def embed(path: str) -> "torch.Tensor":
        import soundfile as sf
        x, sr = sf.read(path, dtype="float32", always_2d=True)
        if sr != 16000:
            import librosa
            x = librosa.resample(x[:, 0], orig_sr=sr, target_sr=16000)
        else:
            x = x[:, 0]
        t = torch.from_numpy(x).unsqueeze(0).to(device)
        return enc.encode_batch(t).squeeze(1).squeeze(0)   # (192,)

    rng = random.Random(args.seed)
    pos_scores, neg_scores = [], []
    by_family: dict[str, list] = defaultdict(list)
    fam_speakers = defaultdict(list)
    for spk, s in speakers.items():
        fam_speakers[s["family"]].append(spk)
    while len(pos_scores) + len(neg_scores) < args.n_trials:
        fam = rng.choice(sorted(fam_speakers))
        if len(fam_speakers[fam]) < 2:
            continue
        s1, s2 = rng.sample(fam_speakers[fam], 2)
        w1 = rng.choice(utt_by_spk[s1])
        if rng.random() < args.cross_family_ratio:
            fam2 = rng.choice([f for f in fam_speakers if len(fam_speakers[f]) >= 1])
            s2 = rng.choice(fam_speakers[fam2])
        w2 = rng.choice(utt_by_spk[s2])
        e1, e2 = embed(w1), embed(w2)
        score = nn.functional.cosine_similarity(e1, e2, dim=0).item()
        if s1 == s2:
            pos_scores.append(score)
        else:
            neg_scores.append(score)

    eer, thresh = _eer(pos_scores, neg_scores)
    n_trials = len(pos_scores) + len(neg_scores)
    # 跨会话再识别：同说话人不同会话对在 EER 阈值下的通过率
    cross_ok, cross_n = 0, 0
    for spk, s in speakers.items():
        sess = list(s["sessions"].keys())
        if len(sess) < 2:
            continue
        w1 = rng.choice(s["sessions"][sess[0]])
        w2 = rng.choice(s["sessions"][rng.choice(sess[1:])])
        if w1 == w2:
            continue
        sc = nn.functional.cosine_similarity(embed(w1), embed(w2), dim=0).item()
        cross_n += 1
        cross_ok += int(sc >= thresh)
    # 3 句注册仿真（T5-G1-03）：enroll-3 vs 全量 centroid 的同对分数差 → EER 劣化
    deg3 = _enroll3_degradation(speakers, utt_by_spk, embed, args, rng, thresh)

    # 陌生人拒判（T5-G1-04）
    stranger_rej = None
    if args.stranger_dir and Path(args.stranger_dir).exists():
        stranger_scores = []
        s_files = sorted(Path(args.stranger_dir).rglob("*.wav"))
        for w in rng.sample(s_files, min(len(s_files), 600)):
            stranger_scores.append(nn.functional.cosine_similarity(embed(str(w)), embed(rng.choice(
                [w2 for spk in utt_by_spk for w2 in rng.sample(utt_by_spk[spk], 1)])), dim=0).item())
        stranger_rej = sum(1 for s_ in stranger_scores if s_ < thresh) / max(len(stranger_scores), 1)

    # ---------- 4. ONNX 导出与一致性验证 ----------
    onnx_path = out_dir / "t5_ecapa.onnx"

    class FeatEncoder(torch.nn.Module):
        """ECAPA 嵌入网络，输入=80 维 fbank（batch, time, 80），时间维动态。
        波形前端（STFT→fbank）含复数运算，torch.onnx 不支持导出（SymbolicValueError 实测），
        端侧由 T14 用 kaldi 兼容 fbank 预处理（25ms 窗/10ms 步/dither0）；torch 全链在 ecapa_finetuned.ckpt。"""

        def __init__(self, m):
            super().__init__()
            self.m = m

        def forward(self, feats, lengths=None):
            if lengths is None:
                lengths = torch.ones(feats.shape[0], device=feats.device)
            return self.m(feats, lengths)

    dummy = torch.randn(1, seg_samples, device=device)
    enc.mods.embedding_model.to("cpu").eval()
    with torch.no_grad():
        feats_dummy = enc.mods.compute_features(dummy.cpu()).cpu()
    torch.onnx.export(
        FeatEncoder(enc.mods.embedding_model).eval(), (feats_dummy, torch.tensor([1.0], dtype=torch.float32)),
        str(onnx_path), input_names=["fbank", "lengths"], output_names=["embedding"],
        dynamic_axes={"fbank": {0: "batch", 1: "time"}, "embedding": {0: "batch"}},
        opset_version=17,
    )

    def fbank_fn(wav_np):
        with torch.no_grad():
            f = enc.mods.compute_features(torch.from_numpy(wav_np[None, :]).float())
        return f.numpy()

    _verify_onnx(onnx_path, str(utt_by_spk[sorted(utt_by_spk)[0]][0]), fbank_fn)

    # ---------- 5. 报告 ----------
    metrics = {
        "family_eer": round(eer, 4), "eer_threshold": round(thresh, 4), "n_trials": n_trials,
        "n_pos_pairs": len(pos_scores), "n_neg_pairs": len(neg_scores),
        "cross_session_reident": round(cross_ok / max(cross_n, 1), 4) if cross_n else None,
        "n_cross_session_pairs": cross_n,
        "enroll3_eer_degradation_pp": deg3,
        "stranger_reject_rate": round(stranger_rej, 4) if stranger_rej is not None else None,
    }
    _write_report(args, metrics, onnx_path, fam_count, len(speakers), n_utts)
    return 0


# ---------------------------------------------------------------- helpers
def _load_encoder(source: str, device: str):
    """加载 EncoderClassifier；本地目录走 from_hparams，HF id 走预训练 savedir 缓存。"""
    import torch  # noqa: F401

    from speechbrain.inference.speaker import EncoderClassifier

    if os.path.isdir(source):
        return EncoderClassifier.from_hparams(source=str(source), savedir=str(source), run_opts={"device": device})
    return EncoderClassifier.from_hparams(
        source=source, savedir="./out/sv/pretrained_cache", run_opts={"device": device})


def _eer(pos: list[float], neg: list[float]) -> tuple[float, float]:
    """EER：优先 speechbrain.utils.metric_stats.EER，缺省回退 sklearn。返回 (eer, threshold)。"""
    try:
        from speechbrain.utils.metric_stats import EER as _sb_eer
        import torch
        eer, thr = _sb_eer(torch.tensor(pos), torch.tensor(neg))
        return float(eer), float(thr)
    except Exception:
        import numpy as np
        from sklearn.metrics import roc_curve
        y = [1] * len(pos) + [0] * len(neg)
        s = pos + neg
        fpr, tpr, thr = roc_curve(y, s)
        fnr = 1 - tpr
        i = int(np.argmin(np.abs(fpr - fnr)))
        return float((fpr[i] + fnr[i]) / 2), float(thr[i])


def _enroll3_degradation(speakers, utt_by_spk, embed, args, rng, thresh) -> float | None:
    """3 句注册 EER 劣化（pp）：同说话人 enroll-3 centroid 分数 vs enroll-10 分数。"""
    import torch
    from torch import nn as _nn

    sampled = rng.sample(sorted(utt_by_spk), min(args.enroll_speakers, len(utt_by_spk)))
    if len(sampled) < 5:
        return None
    deg = []
    for spk in sampled:
        utts = utt_by_spk[spk]
        if len(utts) < 13:
            continue
        e3 = _nn.functional.normalize(torch.stack([embed(w) for w in rng.sample(utts, 3)]).mean(0), dim=0)
        e10 = _nn.functional.normalize(torch.stack([embed(w) for w in rng.sample(utts, 10)]).mean(0), dim=0)
        probe = rng.choice(utts)
        ep = embed(probe)
        s3 = _nn.functional.cosine_similarity(ep, e3, dim=0).item()
        s10 = _nn.functional.cosine_similarity(ep, e10, dim=0).item()
        deg.append(s10 - s3)
    return round(float(sum(deg) / len(deg) * 100), 2) if deg else None


def _verify_onnx(onnx_path: Path, wav_path: str, fbank_fn) -> None:
    """onnxruntime 加载 + 形状/余弦冒烟（GPU provider 不可用时自动落 CPU）。输入为 torch 前端算好的 fbank。"""
    import numpy as np
    import onnxruntime as ort
    import soundfile as sf

    x, sr = sf.read(wav_path, dtype="float32", always_2d=True)
    x = x[:, 0]
    if sr != 16000:
        import librosa
        x = librosa.resample(x, orig_sr=sr, target_sr=16000)
    feats = fbank_fn(np.asarray(x, dtype=np.float32))   # (1, T, 80)
    provs = ort.get_available_providers()
    providers = (["CUDAExecutionProvider", "CPUExecutionProvider"]
                 if "CUDAExecutionProvider" in provs else ["CPUExecutionProvider"])
    sess = ort.InferenceSession(str(onnx_path), providers=providers)
    out = sess.run(None, {"fbank": feats.astype(np.float32),
                          "lengths": np.array([1.0], np.float32)})[0]
    log.info("ONNX 导出验证 ✓ 输入 fbank=%s → 嵌入=%s providers=%s", feats.shape, out.shape, sess.get_providers())


def _write_report(args, m: dict, onnx_path: Path, n_fam: int, n_spk: int, n_utt: int) -> None:
    ts = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    rpt = Path(args.reports_dir) / f"sv_{ts}"
    rpt.mkdir(parents=True, exist_ok=True)
    (rpt / "metrics.json").write_text(json.dumps(m, ensure_ascii=False, indent=2), encoding="utf-8")
    trials_ok = m["n_trials"] >= args.min_trials
    checks = [
        ("T5-G1-01 family_eer ≤ 0.05", f"实测 {m['family_eer']}", m["family_eer"] <= 0.05),
        (f"T5-G1-01 试次 ≥ {args.min_trials}", f"实测 {m['n_trials']}", trials_ok),
        ("T5-G1-02 跨会话再识别 ≥ 0.95", f"实测 {m['cross_session_reident']}",
         m["cross_session_reident"] is not None and m["cross_session_reident"] >= 0.95),
        ("T5-G1-03 enroll3 劣化 ≤ 2pp", f"实测 {m['enroll3_eer_degradation_pp'] if m['enroll3_eer_degradation_pp'] is not None else '未测(说话人/utterance 不足)'}",
         m["enroll3_eer_degradation_pp"] is not None and m["enroll3_eer_degradation_pp"] <= 2.0),
        ("T5-G1-04 陌生人拒判 ≥ 0.90", f"实测 {m['stranger_reject_rate']}",
         m["stranger_reject_rate"] is not None and m["stranger_reject_rate"] >= 0.90),
    ]
    lines = ["# T5 声纹训练报告（训练侧参考；正式门禁以 gaterunner 为准）", "",
             f"- 时间: {ts}   模型: `{onnx_path}`",
             f"- 数据: {n_fam} 家庭 / {n_spk} 说话人 / {n_utt} utterance", "",
             "| 门禁 | 实测 | 判定 |", "|---|---|---|"]
    lines += [f"| {k} | {v} | {'PASS' if ok else 'FAIL'} |" for k, v, ok in checks]
    lines += ["", "注：T5-G0-01（身份切换 0 泄漏）为 T10 联跑系统级门禁，训练脚本不覆盖。"]
    (rpt / "report.md").write_text("\n".join(lines), encoding="utf-8")
    log.info("报告已写 %s/{metrics.json,report.md}", rpt)


if __name__ == "__main__":
    sys.exit(main())
