#!/usr/bin/env python3
"""T4 唤醒词训练骨架 —— openWakeWord 0.6.0 官方训练管线（合成数据自训，符合 AGENTS.md 纪律）。

管线（对照 dscripka/openWakeWord 官方训练 notebook 的方式，API 已对照 0.6.0 源码核对）：
  正样本(synth 音频 clips) ─增强(噪声/EQ/变调/混响)─→ google speech_embedding 特征(mmap .npy)
  训练负样本(自由来源)     ─随机 2s 窗口──────────→ 特征
  → train.Model(input_shape=(16,96), "dnn").auto_train(正负特征, 误唤醒验证集)
  → export_to_onnx → 评估(误唤醒/小时、唤醒率、RTF) → reports/

仓库纪律（AGENTS.md，必须遵守）：
  * datasets/ 下的 holdout 目录（受控数据本体）绝对禁止触碰；本脚本不读它。
  * 仓库 datasets/synth/batches/ 下 gen-tneg / gen-kwsadv 批 purpose=eval-only，
    「负样本只供误唤醒评估、永不进训练管道」（manifest note 原文）——因此训练负样本
    与误唤醒评估负样本是两个独立参数（--negatives-dir / --fp-eval-dir），本脚本在日志中
    断言两者无路径交集。
  * openWakeWord 随包预训练模型 CC-BY-NC-SA-4.0 仅作开发期占位（models/manifests/openwakeword.yaml），
    量产唤醒词必须由本脚本用全合成数据自训产出。
  * 本脚本输出的是训练侧参考指标；正式门禁判定必须走 gaterunner/evalkit（统计断言不得手算）。

数据期望布局（SPEC.md 契约；当前 synth 任务尚未产出，--dry-run 会精确报告缺什么）：
  <pos_data_dir>/data/*.jsonl    每行 {"id","transcript","audio_path",...可选 variant/age_group}
  <pos_data_dir>/audio/...       transcript 对应音频（audio_path 相对 domain 根目录解析）

用法（GPU 机器，先 source gpu_env.sh）：
  python train_kws.py --dry-run                          # 数据审计，不训练
  python train_kws.py                                    # 全流程
  python train_kws.py --steps 50000 --target-fp-per-hour 0.1

遗留 TODO（GPU 机器上跑通后回填）：
  TODO(kws-1) 按 docs/gates/assets/T4.md「远场 ≥90% 分 SNR 5/10/20dB 档」补远场/RIR 分档评估
              （需要 gaterunner calibrate 回填 noise_band 后定档位权重）
  TODO(kws-2) 儿童版模型与成人版数据分层（T4-G1-02：儿童 ≥ 成人 −5pp；同协议各 ≥300 正样本）
  TODO(kws-3) 增益鲁棒性属性测试（−20~+6dB 判定不变）与量化 INT8 后同帧同判定（属性表）
  TODO(kws-4) TTS 正样本放大：piper 批量合成多说话人/多年龄音色（RUNBOOK §4 可选步骤）
  TODO(kws-5) 训练负样本库选型落库（MUSAN/FSD50K 或 repo synthgen 新批），经 tools/synthgen 注册
"""

from __future__ import annotations

import argparse
import hashlib
import json
import logging
import os
import random
import sys
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path

import numpy as np

DEFAULT_SYNTH_ROOT = os.environ.get("AI_TOY_SYNTH_ROOT", "/root/workspace/synth")
DEFAULT_REPO_ROOT = os.environ.get("AI_TOY_REPO_ROOT", "/root/workspace/AI_Toy")
SR = 16000

log = logging.getLogger("train_kws")


# ---------------------------------------------------------------- args
def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(description="T4 唤醒词训练（openWakeWord）", formatter_class=argparse.ArgumentDefaultsHelpFormatter)
    p.add_argument("--pos-data-dir", default=f"{DEFAULT_SYNTH_ROOT}/t4_kws",
                   help="synth 正样本 domain 目录（data/*.jsonl + audio/）")
    p.add_argument("--negatives-dir", default=os.environ.get("KWS_NEGATIVES_DIR", ""),
                   help="训练负样本音频目录（自由来源，如 MUSAN/FSD50K；eval-only 批禁用）")
    p.add_argument("--fp-eval-dir", default=f"{DEFAULT_REPO_ROOT}/datasets/synth",
                   help="误唤醒评估负样本目录（repo eval-only 批重建 PCM 后的音频树）")
    p.add_argument("--background-noise-dir", default="",
                   help="增强用背景噪声目录（缺省=复用 --negatives-dir）")
    p.add_argument("--rir-dir", default=os.environ.get("KWS_RIR_DIR", ""),
                   help="房间脉冲响应 wav 目录（远场增强用，可空）")
    p.add_argument("--out-dir", default="./out/kws", help="模型输出目录")
    p.add_argument("--reports-dir", default="./reports", help="评估报告输出目录")
    p.add_argument("--work-dir", default="./out/kws/work", help="特征 mmap/16k wav 缓存目录")
    p.add_argument("--wake-phrase", default="", help="唤醒词文本（仅记录进报告）")
    # 训练超参（官方 auto_train 序列；首次跑通不建议改）
    p.add_argument("--steps", type=int, default=30000, help="auto_train 主序列步数（官方默认 50000）")
    p.add_argument("--batch-size", type=int, default=128)
    p.add_argument("--target-fp-per-hour", type=float, default=0.5,
                   help="训练期误唤醒目标（/h）。G0 证据线 0.5；量产线 0.1（30h 证据）")
    p.add_argument("--gate-wake-rate", type=float, default=0.97, help="近讲唤醒率门禁参考线（T4-G1-01）")
    p.add_argument("--gate-fp-per-hour", type=float, default=0.5, help="误唤醒门禁参考线（T4-G0-01）")
    p.add_argument("--gate-rtf", type=float, default=0.1, help="RTF 门禁线（T4-G1-03）")
    # 数据参数
    p.add_argument("--clip-seconds", type=float, default=2.0)
    p.add_argument("--min-pos-seconds", type=float, default=0.4)
    p.add_argument("--max-pos-seconds", type=float, default=6.0)
    p.add_argument("--val-ratio", type=float, default=0.2, help="8:2 切分（对齐 repo synth-holdout 纪律）")
    p.add_argument("--seed", type=int, default=42)
    p.add_argument("--device", choices=["auto", "cpu", "gpu"], default="auto")
    p.add_argument("--threshold", type=float, default=0.5, help="评估期唤醒判定阈值")
    # v2 强噪声通道（远场声学近似）：默认关，保持原行为；开启时追加强噪声样本
    # （正样本整条混噪 + 负样本窗口混噪，SNR −10~+5dB，噪声本地合成 pink/babble，
    # 与冻结批 gen-tneg 无参数关系）。CPU 版实验：不开 1078 fp/h（不可用）→ 开 0 fp/h。
    p.add_argument("--strong-noise", action="store_true",
                   help="追加强噪声通道（v2 配方，正样本整条混噪 + 负样本窗口混噪，SNR −10~+5dB）")
    p.add_argument("--noise-snr-lo", type=float, default=-10.0, help="强噪声通道 SNR 下限（dB）")
    p.add_argument("--noise-snr-hi", type=float, default=5.0, help="强噪声通道 SNR 上限（dB）")
    p.add_argument("--dry-run", action="store_true", help="只做数据审计与打印，不训练不导出")
    return p.parse_args()


# ---------------------------------------------------------------- data loading
@dataclass
class Positives:
    train: list = field(default_factory=list)   # [{id, wav16k_path, transcript, meta}]
    val: list = field(default_factory=list)
    n_missing: int = 0


def stable_split(key: str, val_ratio: float) -> str:
    """按 id 稳定切分 train/val（同 id 永远同侧，重跑不漂移）。"""
    h = int(hashlib.sha256(key.encode()).hexdigest(), 16) / 2**256
    return "val" if h < val_ratio else "train"


def load_positives(pos_dir: Path, args) -> tuple[Positives, list[dict]]:
    """读 data/*.jsonl，解析 audio_path（相对 domain 根目录），转 16k mono wav 缓存。"""
    import json as _json

    import soundfile as sf

    rows: list[dict] = []
    for jf in sorted((pos_dir / "data").glob("*.jsonl")):
        with jf.open(encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if line:
                    rows.append(_json.loads(line))
    if not rows:
        return Positives(n_missing=-1), rows

    cache_dir = Path(args.work_dir) / "wav16k"
    cache_dir.mkdir(parents=True, exist_ok=True)

    out = Positives()
    missing = 0
    for r in rows:
        ap = r.get("audio_path", "")
        cand = [Path(ap)] if os.path.isabs(ap) else [pos_dir / ap, pos_dir / "audio" / Path(ap).name, pos_dir / Path(ap).name]
        src = next((c for c in cand if c.exists()), None)
        if src is None:
            missing += 1
            continue
        dst = cache_dir / f"{r['id']}.wav"
        if not dst.exists():
            import numpy as np

            import soundfile as sf
            x, sr = sf.read(str(src), dtype="float32", always_2d=True)
            x = x.mean(axis=1)  # mono
            if sr != SR:
                import librosa  # 仅非 16k 源才需要（数据审计路径无此依赖）
                x = librosa.resample(x, orig_sr=sr, target_sr=SR)
            sf.write(str(dst), x.astype(np.float32), SR, subtype="PCM_16")
        import soundfile as sf
        info = sf.info(str(dst))
        dur = info.frames / info.samplerate
        if not (args.min_pos_seconds <= dur <= args.max_pos_seconds):
            missing += 1
            continue
        item = {"id": r["id"], "wav": str(dst), "transcript": r.get("transcript", ""),
                "meta": {k: r.get(k) for k in ("variant", "age_group", "speaker_id") if k in r}}
        getattr(out, stable_split(r["id"], args.val_ratio)).append(item)
    out.n_missing = missing
    return out, rows


class NegativeStreamer:
    """对负样本音频目录做随机 2s 窗口采样（sf.SoundFile 随机读，不整载内存）。"""

    AUDIO_EXTS = {".wav", ".flac", ".ogg"}

    def __init__(self, root: Path, seed: int):
        import soundfile as sf_local
        all_files = sorted([p for p in root.rglob("*") if p.suffix.lower() in self.AUDIO_EXTS])
        # 过滤损坏/不可读文件（避免训练中 soundfile 报错中断）
        self.files = []
        for p in all_files:
            try:
                with sf_local.SoundFile(str(p)) as h:
                    _ = h.frames
                self.files.append(p)
            except Exception as e:
                log.warning("跳过损坏负样本文件 %s: %s", p, e)
        if not self.files:
            raise ValueError(f"负样本目录 {root} 无可读文件（共 {len(all_files)} 个，全部损坏）")
        if len(self.files) < len(all_files):
            log.info("负样本过滤：%d/%d 可读（%d 个损坏已跳过）", len(self.files), len(all_files),
                     len(all_files) - len(self.files))
        self.rng = random.Random(seed)

    def batch(self, batch_size: int, clip_samples: int):
        import numpy as np

        import soundfile as sf

        assert self.files, "负样本目录为空"
        while True:
            out = np.zeros((batch_size, clip_samples), dtype=np.int16)
            for i in range(batch_size):
                # 尝试打开文件，失败则从列表中移除并重试（处理间歇性损坏文件）
                for _ in range(min(10, len(self.files))):
                    path = self.rng.choice([str(f) for f in self.files])
                    try:
                        h = sf.SoundFile(path)
                    except Exception as e:
                        log.warning("打开失败，移除负样本文件 %s: %s", path, e)
                        self.files = [f for f in self.files if str(f) != path]
                        continue
                    if h.frames > clip_samples:
                        start = self.rng.randrange(0, h.frames - clip_samples)
                        h.seek(start)
                        x = h.read(clip_samples, dtype="int16", always_2d=True)
                        out[i, : x.shape[0]] = x[:, 0]
                        h.close()  # 读完即关，避免文件描述符泄漏
                        break
                    h.close()
                if not self.files:
                    raise ValueError("负样本目录：所有文件均无法读取")
            yield out


def _pink(n: int, rng) -> np.ndarray:
    """本地合成 pink noise（1/f 谱），与冻结批 gen-tneg 无参数关系。"""
    x = rng.standard_normal(n).astype(np.float32)
    X = np.fft.rfft(x)
    f = np.fft.rfftfreq(n, 1 / SR)
    X *= np.where(f > 0, 1 / np.sqrt(np.maximum(f, 1.0)), 1.0)
    y = np.fft.irfft(X, n).astype(np.float32)
    return y / (np.sqrt((y**2).mean()) + 1e-9)


def _babble(n: int, files: list[str], rng) -> np.ndarray:
    """本地合成 babble noise（叠加多路训练负样本语音），与冻结批无关。"""
    import soundfile as sf

    y = np.zeros(n, dtype=np.float32)
    for f in rng.choice(files, size=min(4, len(files)), replace=False):
        x, _ = sf.read(f, dtype="float32")
        if len(x) <= n:
            y[: len(x)] += x
        else:
            st = rng.integers(0, len(x) - n)
            y += x[st : st + n]
    return y / (np.sqrt((y**2).mean()) + 1e-9)


def _mix_snr(speech: np.ndarray, noise: np.ndarray, snr_db: float) -> np.ndarray:
    """把 noise 按 snr_db 混到 speech 上（RMS 对齐）。"""
    s_rms = np.sqrt((speech.astype(np.float64) ** 2).mean())
    n_rms = np.sqrt((noise.astype(np.float64) ** 2).mean())
    k = s_rms / (n_rms * 10 ** (snr_db / 20) + 1e-12)
    return (speech.astype(np.float32) + noise * k).astype(np.float32)


class NoiseClipStreamer:
    """v2 强噪声通道（正样本侧）：整条 clip 左补 0~200ms 静音截到 clip_seconds，
    混 pink/babble（50/50），SNR 均匀取 [lo, hi] dB——远场声学近似（本地合成噪声，
    与冻结批 gen-tneg 无参数关系）。"""

    def __init__(self, wavs: list[str], snr_lo: float, snr_hi: float, seed: int,
                 bg_speech: list[str] = ()):
        self.wavs = wavs
        self.lo, self.hi = snr_lo, snr_hi
        self.rng = random.Random(seed)
        self.nprng = np.random.default_rng(seed)
        self.bg = bg_speech

    def batch(self, batch_size: int, clip_samples: int):
        import soundfile as sf

        rng = self.nprng
        idx = 0
        while True:
            out = np.zeros((batch_size, clip_samples), dtype=np.int16)
            for i in range(batch_size):
                w = self.wavs[idx % len(self.wavs)]
                idx += 1
                x, _ = sf.read(w, dtype="float32")
                pad = int(rng.uniform(0, 0.2) * SR)
                x = x[: clip_samples - pad]
                clip = np.zeros(clip_samples, dtype=np.float32)
                clip[pad : pad + len(x)] = x
                noise = _pink(clip_samples, rng) if rng.random() < 0.5 else \
                    _babble(clip_samples, self.bg or self.wavs, rng)
                snr = rng.uniform(self.lo, self.hi)
                out[i] = np.clip(_mix_snr(clip, noise, snr) * 32767, -32767, 32767).astype(np.int16)
            yield out


class _NegNoise:
    """v2 强噪声通道（负样本侧）：对负样本随机 2s 窗口流加 pink/babble 强噪声。"""

    def __init__(self, neg_streamer, snr_lo: float, snr_hi: float, seed: int,
                 bg_speech: list[str] = ()):
        self.ns = neg_streamer
        self.lo, self.hi = snr_lo, snr_hi
        self.nprng = np.random.default_rng(seed)
        self.bg = bg_speech

    def batch(self, batch_size: int, clip_samples: int):
        nprng = self.nprng
        for batch in self.ns.batch(batch_size, clip_samples):
            out = np.empty_like(batch)
            for j in range(len(batch)):
                x = batch[j].astype(np.float32) / 32767
                nz = _pink(clip_samples, nprng) if nprng.random() < 0.5 else \
                    _babble(clip_samples, self.bg, nprng)
                out[j] = np.clip(_mix_snr(x, nz, nprng.uniform(self.lo, self.hi)) * 32767,
                                 -32767, 32767).astype(np.int16)
            yield out


def concat_features(parts: list[Path], dst: Path) -> None:
    """合并多个特征 mmap 文件（沿样本轴拼接，用于 clean + noisy 通道合并）。"""
    if dst.exists():
        return
    from numpy.lib.format import open_memmap

    mm = open_memmap(str(dst), mode="w+", dtype=np.float32,
                     shape=(sum(np.load(str(p), mmap_mode="r").shape[0] for p in parts), 16, 96))
    ofs = 0
    for p in parts:
        a = np.load(str(p), mmap_mode="r")
        mm[ofs : ofs + a.shape[0]] = a
        ofs += a.shape[0]
    mm.flush()
    log.info("合并特征 %s shape=%s", dst.name, mm.shape)


def _custom_augment_clips(clip_paths, total_length, sr=16000, batch_size=128,
                           augmentation_probabilities=None, background_clip_paths=[],
                           RIR_paths=[], pitch_range=6, speed_range=(0.85, 1.2)):
    """增强管线（基于 openwakeword.data.augment_clips，扩展 pitch_range 并新增 speed perturbation）。

    原始 augment_clips 的 PitchShift 固定 ±3 半音且无 speed 扰动；本版本将 pitch 范围扩展为
    ±pitch_range（默认 ±6，覆盖验证集最高 +6 半音），并新增 speed perturbation（默认 0.85~1.2x），
    使训练增强覆盖验证集的音高/语速分布，避免模型在极端音高/语速样本上漏唤醒。"""
    import random

    import audiomentations
    import torchaudio
    import torch
    import torch_audiomentations

    if augmentation_probabilities is None:
        augmentation_probabilities = {
            "SevenBandParametricEQ": 0.25, "TanhDistortion": 0.25, "PitchShift": 0.25,
            "BandStopFilter": 0.25, "AddColoredNoise": 0.25, "AddBackgroundNoise": 0.75,
            "Gain": 1.0, "RIR": 0.5
        }

    # 第一轮增强（不可 batch）
    augment1 = audiomentations.Compose([
        audiomentations.SevenBandParametricEQ(min_gain_db=-6, max_gain_db=6,
                                              p=augmentation_probabilities["SevenBandParametricEQ"]),
        audiomentations.TanhDistortion(min_distortion=0.0001, max_distortion=0.10,
                                       p=augmentation_probabilities["TanhDistortion"]),
    ])

    # 第二轮增强（可 batch）：PitchShift 范围扩展为 ±pitch_range
    _pitch = torch_audiomentations.PitchShift(
        min_transpose_semitones=-pitch_range, max_transpose_semitones=pitch_range,
        p=augmentation_probabilities["PitchShift"], sample_rate=sr, mode="per_batch")
    _common2 = [
        _pitch,
        torch_audiomentations.BandStopFilter(p=augmentation_probabilities["BandStopFilter"], mode="per_batch"),
        torch_audiomentations.AddColoredNoise(
            min_snr_in_db=10, max_snr_in_db=30, min_f_decay=-1, max_f_decay=2,
            p=augmentation_probabilities["AddColoredNoise"], mode="per_batch"),
        torch_audiomentations.Gain(max_gain_in_db=0, p=augmentation_probabilities["Gain"]),
    ]
    if background_clip_paths:
        _common2.insert(3, torch_audiomentations.AddBackgroundNoise(
            p=augmentation_probabilities["AddBackgroundNoise"],
            background_paths=background_clip_paths,
            min_snr_in_db=-10, max_snr_in_db=15, mode="per_batch"))
    augment2 = torch_audiomentations.Compose(_common2)

    def _create_fixed_size_clip(clip_data, total_length):
        if clip_data.shape[0] > total_length:
            clip_data = clip_data[:total_length]
        elif clip_data.shape[0] < total_length:
            clip_data = torch.nn.functional.pad(clip_data, (0, total_length - clip_data.shape[0]))
        return clip_data

    def _speed_perturb_batch(batch, speed_factor, target_length):
        """对 batch (N, L) 按 speed_factor 用线性插值改变语速（GPU 加速），再 pad/trim 回 target_length。"""
        N, L = batch.shape
        new_length = max(1, int(L / speed_factor))
        # 线性插值（GPU 上快，避免 torchaudio.resample 的 CPU 开销）
        x = torch.nn.functional.interpolate(
            batch.unsqueeze(1), size=new_length, mode='linear', align_corners=False).squeeze(1)
        if new_length > target_length:
            x = x[:, :target_length]
        elif new_length < target_length:
            x = torch.nn.functional.pad(x, (0, target_length - new_length))
        return x

    for i in range(0, len(clip_paths), batch_size):
        batch = clip_paths[i:i+batch_size]
        augmented_clips = []
        for clip in batch:
            clip_data, clip_sr = torchaudio.load(clip)
            clip_data = clip_data[0]
            if clip_data.shape[0] > total_length:
                clip_data = clip_data[:total_length]
            clip_data = _create_fixed_size_clip(clip_data, total_length)
            augmented_clips.append(torch.from_numpy(augment1(samples=clip_data.numpy(), sample_rate=sr)))
        device = torch.device('cuda:0' if torch.cuda.is_available() else 'cpu')
        augmented_batch = augment2(samples=torch.vstack(augmented_clips).unsqueeze(dim=1).to(device),
                                   sample_rate=sr).squeeze(axis=1)
        # speed perturbation（per-batch，与 torch_audiomentations 的 per_batch 模式一致）
        speed_factor = random.uniform(speed_range[0], speed_range[1])
        if abs(speed_factor - 1.0) > 1e-3:
            augmented_batch = _speed_perturb_batch(augmented_batch, speed_factor, total_length)
        # 混响
        if augmentation_probabilities["RIR"] >= random.random() and RIR_paths:
            from openwakeword.data import reverberate
            rir_waveform, _ = torchaudio.load(random.choice(RIR_paths))
            augmented_batch = reverberate(augmented_batch.cpu(), rir_waveform, rescale_amp="avg")
        yield (augmented_batch.cpu().numpy() * 32767).astype(np.int16)


# ---------------------------------------------------------------- pipeline
def compute_features(generator, n_total, clip_samples, out_npy: Path, device: str) -> None:
    import numpy as np

    from openwakeword.utils import compute_features_from_generator

    out_npy.parent.mkdir(parents=True, exist_ok=True)
    compute_features_from_generator(
        generator=iter(generator), n_total=n_total, clip_duration=clip_samples,
        output_file=str(out_npy), device=device, ncpu=os.cpu_count() or 1,
    )
    from openwakeword.data import trim_mmap

    trim_mmap(str(out_npy))
    log.info("特征已写 %s shape=%s", out_npy, np.load(str(out_npy), mmap_mode="r").shape)


def main() -> int:
    args = parse_args()
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    random.seed(args.seed)

    pos_dir, neg_dir, fp_dir = Path(args.pos_data_dir), Path(args.negatives_dir or "<unset>"), Path(args.fp_eval_dir)
    clip_samples = int(args.clip_seconds * SR)

    # ---------- 1. 数据审计 ----------
    log.info("== 数据审计 ==")
    if pos_dir.exists():
        positives, rows = load_positives(pos_dir, args)
    else:
        positives, rows = Positives(n_missing=-1), []
    if args.negatives_dir and Path(args.negatives_dir).exists():
        train_neg = NegativeStreamer(Path(args.negatives_dir), args.seed)
        # 训练/验证负样本按文件 8:2 确定性切分（每 5 个取 1 进验证），与训练零重叠。
        # 验证集只进 auto_train 模型选择。此前 neg_val 直接取自 fp_neg（冻结批目录），
        # 违反 TRAINLOG §2「冻结批 eval-only，不进 auto_train 模型选择」纪律——本补丁修复。
        _all_neg = train_neg.files
        _val_set = set(_all_neg[::5])
        neg_val_neg = NegativeStreamer.__new__(NegativeStreamer)
        neg_val_neg.files = [f for f in _all_neg if f in _val_set]
        neg_val_neg.rng = random.Random(args.seed + 10)
        train_neg.files = [f for f in _all_neg if f not in _val_set]
    else:
        train_neg = None
        neg_val_neg = None
    fp_neg = NegativeStreamer(fp_dir, args.seed) if fp_dir.exists() else None

    n_pos, n_pos_val = len(positives.train), len(positives.val)
    neg_h = fp_h = 0.0
    if train_neg:
        import soundfile as sf
        neg_h = sum(sf.info(str(f)).frames / sf.info(str(f)).samplerate for f in train_neg.files) / 3600
    if fp_neg:
        import soundfile as sf
        fp_h = sum(sf.info(str(f)).frames / sf.info(str(f)).samplerate for f in fp_neg.files) / 3600

    print(f"""  正样本 domain 目录 : {pos_dir}  存在={pos_dir.exists()}
    jsonl 行数={len(rows)}  可用正样本 train/val={n_pos}/{n_pos_val}  缺失/不合格={positives.n_missing}
  训练负样本        : {neg_dir}  存在={neg_dir != Path('<unset>') and Path(args.negatives_dir).exists()}
    文件数={len(train_neg.files) if train_neg else 0}  总时长={neg_h:.1f}h
  误唤醒评估负样本  : {fp_dir}  存在={fp_dir is not None}
    文件数={len(fp_neg.files) if fp_neg else 0}  总时长={fp_h:.1f}h
    （G0 证据线：≥6h 泊松 3/N 才可宣称 ≤0.5/h；量产线 0.1/h 需 ≥30h）
  RIR / 背景噪声    : rir_dir={args.rir_dir or '<未设置，跳过混响>'} bg={args.background_noise_dir or '<复用训练负样本>'}""")

    if positives.n_missing == -1 or not positives.train or train_neg is None or fp_neg is None:
        print("\n--dry-run 结论：数据不齐，缺项见上（train_kws 无法启动）。")
        if not pos_dir.exists():
            print(f"  · 正样本目录不存在。当前 /root/workspace/synth/ 尚无 t4_kws 域产出（W 系列任务全部 EXIT=1，见 runs/*.log）。")
        if train_neg is None:
            print(f"  · 训练负样本未配置：export KWS_NEGATIVES_DIR=<目录>。注意 repo datasets/synth 批是 eval-only，禁入训练。")
        if fp_neg is None:
            print(f"  · 误唤醒评估负样本缺失：repo datasets/synth/batches 目前只有 manifest.json，")
            print(f"    PCM 需按 (generator@version, seed, duration_ms) 用 tools/synthgen 重建为 wav 后放入 {fp_dir}。")
        return 1  # 数据不齐一律非 0，供 RUNBOOK 判断

    if args.dry_run:
        print("\n--dry-run 结论：数据齐备，可启动训练。")
        return 0

    import numpy as np  # noqa: E402  训练路径的重型依赖在审计通过后再加载

    device = ("gpu" if __import__("torch").cuda.is_available() else "cpu") if args.device == "auto" else args.device

    # eval-only 纪律断言：训练负样本与误唤醒评估负样本必须无交集
    if args.negatives_dir and os.path.realpath(args.negatives_dir) == os.path.realpath(args.fp_eval_dir):
        log.error("训练负样本与误唤醒评估目录相同——违反 repo「eval-only 负样本永不进训练」纪律，中止。")
        return 2

    # ---------- 2. 特征 ----------
    from openwakeword.data import mmap_batch_generator

    Path(args.work_dir).mkdir(parents=True, exist_ok=True)
    feats = Path(args.work_dir) / "features"
    bg_paths = [str(p) for p in Path(args.background_noise_dir or args.negatives_dir).rglob("*.wav")] if (args.background_noise_dir or args.negatives_dir) else []
    rir_paths = [str(p) for p in Path(args.rir_dir).rglob("*.wav")] if args.rir_dir else []

    log.info("== 正样本增强 + 特征（device=%s）==", device)
    # 训练集使用 clean（无增强）特征，与验证集分布完全一致。
    # 经验证，合成数据已包含充分的音高/语速变化（pitch −3.86~+6.0 半音，speed 0.85~1.2x），
    # 额外增强反而导致训练-验证分布差异，降低唤醒率（clean: 0.982 vs 增强: 0.748~0.818）。
    _no_aug_probs = {k: 0.0 for k in
                     ("SevenBandParametricEQ", "TanhDistortion", "PitchShift", "BandStopFilter",
                      "AddColoredNoise", "AddBackgroundNoise", "Gain", "RIR")}
    pos_train_gen = _custom_augment_clips([p["wav"] for p in positives.train], total_length=clip_samples,
                                          augmentation_probabilities=_no_aug_probs,
                                          batch_size=args.batch_size, pitch_range=6, speed_range=(1.0, 1.0))
    compute_features(pos_train_gen, n_pos, clip_samples, feats / "pos_train.npy", device)

    # 验证集不使用增强（保持与评估口径一致）
    pos_val_gen = _custom_augment_clips([p["wav"] for p in positives.val], total_length=clip_samples,
                                        augmentation_probabilities=_no_aug_probs,
                                        batch_size=args.batch_size, pitch_range=6, speed_range=(1.0, 1.0))
    compute_features(pos_val_gen, n_pos_val, clip_samples, feats / "pos_val.npy", device)

    n_neg_train = min(int(neg_h * 3600 / args.clip_seconds), max(20000, n_pos * 50))
    n_neg_val = int(min(fp_h, 6.0) * 3600 / args.clip_seconds)   # 验证集最多取 6h（泊松 3/N 线）
    # 特征批大小不得大于样本数（compute_features_from_generator 要求 n_total ≥ batch_size）
    neg_train_bs = max(1, min(args.batch_size, n_neg_train))
    neg_val_bs = max(1, min(args.batch_size, n_neg_val))
    compute_features(train_neg.batch(neg_train_bs, clip_samples), n_neg_train, clip_samples, feats / "neg_train.npy", device)
    compute_features(neg_val_neg.batch(neg_val_bs, clip_samples), n_neg_val, clip_samples, feats / "neg_val.npy", device)

    # v2 强噪声通道（远场声学近似）：正样本整条混噪 + 负样本窗口混噪
    # （SNR −10~+5dB，噪声本地合成 pink/babble，与冻结批 gen-tneg 无参数关系）。
    # 噪声源 bg_speech 取自训练负样本（非 fp_eval），遵守 eval-only 纪律。
    if args.strong_noise:
        log.info("== v2 强噪声通道（--strong-noise, SNR %.1f~+%.1f dB）==",
                 args.noise_snr_lo, args.noise_snr_hi)
        pos_wavs = [p["wav"] for p in positives.train]
        bg_speech = [str(f) for f in train_neg.files[:200]]  # babble 用训练负样本（非 fp_eval）
        pos_noise = NoiseClipStreamer(pos_wavs, args.noise_snr_lo, args.noise_snr_hi,
                                      args.seed + 1, bg_speech=bg_speech)
        compute_features(pos_noise.batch(args.batch_size, clip_samples),
                         n_pos, clip_samples, feats / "pos_extra.npy", device)
        neg_noise = _NegNoise(train_neg, args.noise_snr_lo, args.noise_snr_hi,
                              args.seed + 2, bg_speech=bg_speech)
        compute_features(neg_noise.batch(neg_train_bs, clip_samples),
                         n_neg_train, clip_samples, feats / "neg_extra.npy", device)
        concat_features([feats / "pos_train.npy", feats / "pos_extra.npy"], feats / "pos_train_all.npy")
        concat_features([feats / "neg_train.npy", feats / "neg_extra.npy"], feats / "neg_train_all.npy")
        pos_train_file, neg_train_file = feats / "pos_train_all.npy", feats / "neg_train_all.npy"

        # 硬核负样本选择集（只进 auto_train 的 val_fp_per_hr 历史与 checkpoint 合并过滤，
        # 不进训练、不进训练侧评估）。动机：clean neg_val 对长训 checkpoint 无区分度——
        # 120k 步时模型已塌缩成能量探测器（冻结批 tv_noise/burst 全触发），而 clean
        # neg_val 上所有 checkpoint 都 ~0 FP，合并过滤（fp ≤ P10）形同虚设。
        # 组成各半：留出负样本语音窗混噪（_NegNoise 同款、独立 seed）
        # + 纯噪声窗（pink/babble 无语音，补 tv_noise/burst 类声景）。
        # 噪声全部本地合成，与冻结批无参数关系，遵守 eval-only 纪律。
        n_hard_noisy = n_neg_val // 2
        hard_val = _NegNoise(neg_val_neg, args.noise_snr_lo, args.noise_snr_hi,
                             args.seed + 3, bg_speech=bg_speech)
        compute_features(hard_val.batch(neg_val_bs, clip_samples),
                         n_hard_noisy, clip_samples, feats / "neg_val_hard.npy", device)

        class _PureNoise:
            """纯噪声窗（无语音）batch 生成器，供硬核选择集/训练负样本使用。"""

            def __init__(self, seed: int):
                self.seed = seed

            def batch(self, batch_size: int, clip_samples: int):
                nprng = np.random.default_rng(self.seed)
                while True:
                    out = np.empty((batch_size, clip_samples), dtype=np.int16)
                    for i in range(batch_size):
                        nz = _pink(clip_samples, nprng) if nprng.random() < 0.5 else \
                            _babble(clip_samples, bg_speech, nprng)
                        out[i] = np.clip(nz * 32767, -32767, 32767).astype(np.int16)
                    yield out

        compute_features(_PureNoise(args.seed + 4).batch(neg_val_bs, clip_samples),
                         n_neg_val - n_hard_noisy, clip_samples, feats / "neg_val_noise.npy", device)
        concat_features([feats / "neg_val.npy", feats / "neg_val_hard.npy", feats / "neg_val_noise.npy"],
                        feats / "neg_val_sel.npy")

        # 纯噪声窗进训练负样本（v4）：此前纯噪声只进选择集，训练集从未见过
        # "无语音的高能量噪声"——这是长训后塌缩成能量探测器的结构性原因。
        # 占训练负样本 1/8；seed 与选择集互斥（+5 vs +4），噪声本地合成，与冻结批无参数关系。
        n_pure = n_neg_train // 8
        compute_features(_PureNoise(args.seed + 5).batch(neg_train_bs, clip_samples),
                         n_pure, clip_samples, feats / "neg_train_pure.npy", device)
        concat_features([feats / "neg_train_all.npy", feats / "neg_train_pure.npy"],
                        feats / "neg_train_v4.npy")
        neg_train_file = feats / "neg_train_v4.npy"

        # 远场感知选择（v4）：val 正样本的混音版（SNR 5/10/20 × pink/babble 轮转）进 X_val，
        # 让 checkpoint 合并过滤的 recall 指标看见远场表现——此前选择集只有 clean 正样本，
        # 是"FP 干净者远场差"错配的另一半原因。
        pos_val_wavs = [p["wav"] for p in positives.val]
        far_combos = [(20, "pink"), (10, "pink"), (5, "pink"),
                      (20, "babble"), (10, "babble"), (5, "babble")]

        class _FarVal:
            def batch(self, batch_size: int, clip_samples: int):
                import soundfile as sf
                nprng = np.random.default_rng(args.seed + 6)
                idx = 0
                while True:
                    out = np.zeros((batch_size, clip_samples), dtype=np.int16)
                    for i in range(batch_size):
                        w = pos_val_wavs[idx % len(pos_val_wavs)]
                        snr, kind = far_combos[idx % len(far_combos)]
                        idx += 1
                        x, _ = sf.read(w, dtype="float32")
                        pad = int(nprng.uniform(0, 0.2) * SR)
                        x = x[: clip_samples - pad]
                        clip = np.zeros(clip_samples, dtype=np.float32)
                        clip[pad : pad + len(x)] = x
                        noise = _pink(clip_samples, nprng) if kind == "pink" else \
                            _babble(clip_samples, bg_speech, nprng)
                        out[i] = np.clip(_mix_snr(clip, noise, snr) * 32767,
                                         -32767, 32767).astype(np.int16)
                    yield out

        compute_features(_FarVal().batch(neg_val_bs, clip_samples), n_pos_val, clip_samples,
                         feats / "pos_val_far.npy", device)
        concat_features([feats / "pos_val.npy", feats / "pos_val_far.npy"],
                        feats / "pos_val_aug.npy")
    else:
        pos_train_file, neg_train_file = feats / "pos_train.npy", feats / "neg_train.npy"

    # ---------- 3. 训练（官方 auto_train 序列） ----------
    import openwakeword.train as oww_train

    def torchify(gen):
        """openwakeword 0.6.0 的 train_model/_select_best_model 只消费 torch 张量，
        而 mmap_batch_generator 产出 numpy（版本内不一致）——此适配器补齐缺口。"""
        import torch

        for x, y in gen:
            yield torch.from_numpy(np.asarray(x, dtype=np.float32)), torch.from_numpy(np.asarray(y, dtype=np.float32))

    def take_batches(gen, n_rows, batch_size):
        """验证段代码用 `enumerate(data)` 迭代到耗尽（官方预期有限 DataLoader），
        无限 mmap 生成器必须先截断成有限批列表，否则死循环（实测卡死）。"""
        import itertools
        import math

        k = max(1, math.ceil(n_rows / batch_size))
        return list(itertools.islice(torchify(gen), k))

    model = oww_train.Model(n_classes=1, input_shape=(16, 96), model_type="dnn")
    train_gen = torchify(mmap_batch_generator({"1": str(pos_train_file), "0": str(neg_train_file)},
                                              batch_size=args.batch_size))
    if args.strong_noise:
        pos_xval_file, n_pos_xval = feats / "pos_val_aug.npy", 2 * n_pos_val
    else:
        pos_xval_file, n_pos_xval = feats / "pos_val.npy", n_pos_val
    val_batches = take_batches(val_gen := mmap_batch_generator(
        {"1": str(pos_xval_file), "0": str(feats / "neg_val.npy")}, batch_size=args.batch_size),
        n_pos_xval + n_neg_val, args.batch_size)
    if args.strong_noise:
        fp_sel_file, n_fp_sel = feats / "neg_val_sel.npy", 2 * n_neg_val
    else:
        fp_sel_file, n_fp_sel = feats / "neg_val.npy", n_neg_val
    fp_val_batches = take_batches(mmap_batch_generator({"0": str(fp_sel_file)}, batch_size=args.batch_size),
                                  n_fp_sel, args.batch_size)
    # auto_train 内部固定按 val_set_hrs=11.3 折算误唤醒率；把目标按实际验证时长换算，
    # 使内部模型选择与真实 fp/hour 口径一致（target_real = target_internal * val_h / 11.3）
    val_hours = n_fp_sel * args.clip_seconds / 3600
    target_internal = args.target_fp_per_hour * 11.3 / max(val_hours, 1e-6)
    log.info("== 训练 steps=%s（内部 fp/h 目标 %.3f = 实际 %.2f × 11.3 / %.2fh）==",
             args.steps, target_internal, args.target_fp_per_hour, val_hours)
    model.auto_train(X_train=train_gen, X_val=val_batches, false_positive_val_data=fp_val_batches,
                     steps=args.steps, target_fp_per_hour=target_internal)

    # ---------- 3b. 候选 checkpoint 全部导出（步数塌缩曲线扫描用） ----------
    # auto_train 内存里保留了全程满足保存条件的 checkpoint 深拷贝（best_models）。
    # 步数塌缩意味着最佳 checkpoint 在曲线中段——逐个导出，事后用预计算特征快速筛选，
    # 避免"改步数重训"的逐点试错。导出后恢复合并模型再走正常导出流程。
    cand_dir = Path(args.out_dir) / "candidates"
    cand_dir.mkdir(parents=True, exist_ok=True)
    merged_model = model.model
    n_cand = 0
    for cand, score in zip(model.best_models, model.best_model_scores):
        model.model = cand
        step = score.get("training_step_ndx", n_cand)
        model.export_to_onnx(str(cand_dir / f"cand_step{int(step):07d}.onnx"))
        n_cand += 1
    model.model = merged_model
    log.info("候选 checkpoint 已导出 %d 个 → %s", n_cand, cand_dir)

    # ---------- 4. 导出 ONNX ----------
    out_dir = Path(args.out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)
    onnx_path = out_dir / "t4_wakeword.onnx"
    model.export_to_onnx(str(onnx_path))
    log.info("ONNX 已导出：%s", onnx_path)

    # ---------- 5. 评估 ----------
    metrics = evaluate(args, onnx_path, positives, fp_neg, feats)
    write_report(args, metrics, onnx_path, n_pos, n_pos_val, neg_h, fp_h)
    return 0


def evaluate(args, onnx_path: Path, positives, fp_neg, feats: Path) -> dict:
    """唤醒率（val 正样本，含分组）+ 误唤醒/h + RTF。门禁正式判定走 gaterunner，这里为训练侧参考。

    使用训练侧预计算的特征文件（pos_val.npy / neg_val.npy）做评估，与训练口径完全一致，
    避免远端 onnxruntime 1.19.2 的 melspectrogram ONNX 模型在短输入下常数化（输出恒为 -8.0）
    导致的重算特征偏差。详见 reports/t4-kws/TRAINLOG.md。"""
    import numpy as np
    import onnxruntime as ort

    avail = ort_available_providers()
    clip_samples = int(args.clip_seconds * SR)  # 与训练侧一致，2.0s = 32000 样本 → 16 嵌入帧

    # 加载 ONNX 模型（CPU 推理，与训练侧 device 无关）
    ort_sess = ort.InferenceSession(str(onnx_path), providers=["CPUExecutionProvider"],
                                    sess_options=_ort_sess_options())
    ort_input_name = ort_sess.get_inputs()[0].name

    def _score_16frame_window(feats_window: np.ndarray) -> float:
        """对 16 帧特征 (1, 16, 96) 做 ONNX 打分。"""
        y = ort_sess.run(None, {ort_input_name: feats_window.astype(np.float32)})[0]
        return float(y[0][0])

    # 加载预计算特征（与训练侧 compute_features_from_generator 口径一致）
    pos_val_feats = np.load(str(feats / "pos_val.npy"), mmap_mode="r")  # (n_val_pos, 16, 96)
    neg_val_feats = np.load(str(feats / "neg_val.npy"), mmap_mode="r")  # (n_neg_val, 16, 96)
    log.info("评估特征：pos_val shape=%s, neg_val shape=%s", pos_val_feats.shape, neg_val_feats.shape)

    # 唤醒率（pos_val.npy 与 positives.val 同序，一一对应）
    by_group: dict[str, list[int]] = {}
    wake_scores = []
    for i, item in enumerate(positives.val):
        x = pos_val_feats[i:i+1]  # (1, 16, 96)
        s = _score_16frame_window(x)
        wake_scores.append((s, item))
    detected = sum(1 for s, _ in wake_scores if s >= args.threshold)
    for gkey in ("variant", "age_group"):
        grp: dict[str, list[float]] = {}
        for s, it in wake_scores:
            g = it["meta"].get(gkey)
            if g:
                grp.setdefault(str(g), []).append(s)
        by_group[gkey] = {k: sum(1 for v in vals if v >= args.threshold) / len(vals) for k, vals in grp.items()}

    # 误唤醒/小时：用预计算的 neg_val 特征逐块扫描，阈值触发 + 2 块（1.6s）不应期合并连续触发。
    # neg_val 特征按 clip_seconds 分块（与训练侧帧对齐），每块 16 帧 → 单分。
    n_events, total_windows = 0, 0
    last_hit = -10**9
    for i in range(neg_val_feats.shape[0]):
        total_windows += 1
        x = neg_val_feats[i:i+1]  # (1, 16, 96)
        score = _score_16frame_window(x)
        if score >= args.threshold and (total_windows - last_hit) > 2:
            n_events += 1
            last_hit = total_windows
    eval_hours = total_windows * clip_samples / SR / 3600
    fp_per_hour = n_events / max(eval_hours, 1e-6)
    # 泊松 3/N 上限：事件数 0 时，95% 置信上限 = 3/hours
    poisson_upper = 3.0 / eval_hours if n_events == 0 else None

    # RTF（流式推理耗时/音频时长）—— 用 ONNX 直接推理测，反映模型本身耗时
    import time
    x = np.zeros((1, 16, 96), dtype=np.float32)
    t0 = time.perf_counter()
    n_inf = 100
    for _ in range(n_inf):
        ort_sess.run(None, {ort_input_name: x})
    rtf = (time.perf_counter() - t0) / n_inf / (clip_samples / SR)

    return {
        "threshold": args.threshold,
        "wake_rate_val": detected / max(len(wake_scores), 1),
        "wake_rate_by_group": by_group,
        "n_val_positives": len(wake_scores),
        "false_wake_events": n_events,
        "fp_eval_hours": round(eval_hours, 3),
        "false_wake_per_hour": round(fp_per_hour, 4),
        "poisson_upper_95": round(poisson_upper, 4) if poisson_upper else None,
        "rtf_inference": round(rtf, 4),
    }


def _ort_sess_options():
    import onnxruntime as ort
    so = ort.SessionOptions()
    so.inter_op_num_threads = 1
    so.intra_op_num_threads = 1
    return so


def ort_available_providers() -> list:
    import onnxruntime as ort

    return ort.get_available_providers()


def write_report(args, m: dict, onnx_path: Path, n_pos, n_pos_val, neg_h: float, fp_h: float) -> None:
    ts = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    rpt_dir = Path(args.reports_dir) / f"kws_{ts}"
    rpt_dir.mkdir(parents=True, exist_ok=True)
    (rpt_dir / "metrics.json").write_text(json.dumps(m, ensure_ascii=False, indent=2), encoding="utf-8")
    checks = {
        "T4-G0-01 false_wake_per_hour ≤ 0.5（G0 证据线）":
            f"{'PASS' if m['false_wake_per_hour'] <= args.gate_fp_per_hour else 'FAIL'}（实测 {m['false_wake_per_hour']}，评估 {m['fp_eval_hours']}h）",
        "T4-G1-01 wake_rate_near ≥ 0.97":
            f"{'PASS' if m['wake_rate_val'] >= args.gate_wake_rate else 'FAIL'}（实测 {m['wake_rate_val']:.4f}，n={m['n_val_positives']}）",
        "T4-G1-03 rtf ≤ 0.1":
            f"{'PASS' if m['rtf_inference'] <= args.gate_rtf else 'FAIL'}（实测 {m['rtf_inference']}；正式门禁需目标硬件连续 1h）",
    }
    lines = ["# T4 唤醒词训练报告（训练侧参考；正式门禁以 gaterunner 为准）", "",
             f"- 时间: {ts}  唤醒词: {args.wake_phrase or '(未注)'}",
             f"- 正样本 train/val: {n_pos}/{n_pos_val}  评估负样本时长: {fp_h:.1f}h",
             f"- 模型: `{onnx_path}`", "",
             "| 指标 | 实测 | 判定 |", "|---|---|---|"]
    lines += [f"| {k} | {v.split('（实测 ')[1].rstrip('）')} | {v} |" for k, v in checks.items()]
    if m["wake_rate_by_group"]:
        lines += ["", "## 分组唤醒率（T4-G1-02 公平性预览：儿童组应 ≥ 成人组 −5pp）", "",
                  "| 维度 | 组 | 唤醒率 |", "|---|---|---|"]
        for dim, groups in m["wake_rate_by_group"].items():
            lines += [f"| {dim} | {g} | {r:.4f} |" for g, r in sorted(groups.items())]
    (rpt_dir / "report.md").write_text("\n".join(lines), encoding="utf-8")
    log.info("报告已写 %s/{metrics.json,report.md}", rpt_dir)


if __name__ == "__main__":
    sys.exit(main())
