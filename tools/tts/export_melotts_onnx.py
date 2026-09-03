#!/usr/bin/env python3
"""MeloTTS-Chinese → ONNX 导出 + PyTorch 对拍 + 端侧 RTF 实测（T13 W3，issue #132）。

模型：myshell-ai/MeloTTS-Chinese（MIT，LICENSE-LEDGER W3-2），官方音色 ZH(spk id 1)，
本仓库红线：不克隆真实儿童声音，端侧/云档均只用官方音色（语速经 length_scale 参数化，
音高为导出后 DSP 面待办——见 ADR-0008）。

子命令
    export   torch.onnx.export 单图（音素序列→波形），随机性显式化：
             - sdp reverse 内部 randn → 输入 sdp_noise（图内 ×noise_scale_w）
             - z_p = m_p + randn_like·exp(logs_p)·noise_scale → 输入 z_noise
               （形状数据相关=mel 长度：输入时间维按 ZHEAD×T 预留，图内切到 T_mel）
             - noise_scale / noise_scale_w / length_scale / sdp_ratio 为标量输入
    parity   同输入（前端张量 + 同一噪声张量）PyTorch vs onnxruntime 对拍；
             另做「补丁忠实性」检查：原版 infer 固定种子 vs 补丁版同噪声。
    rtf      onnxruntime CPU 端侧 RTF 实测（推理面），前端（g2p+BERT）单独计时。

语言面：ZH_MIX_EN 前端（chinese_mix，mBERT=bert-base-multilingual-uncased 进
ja_bert 槽、bert 槽置零——上游 api.py 对中文模型的实际路由）。japanese/english/
korean/french/spanish 清洗模块顶层加载各自 BERT，本脚本以 stub 显式拒绝
（只覆盖中文路径，不静默降级）。
"""

import argparse
import json
import math
import os
import sys
import time
import types

import numpy as np
import torch

MELLO_ROOT = os.environ.get(
    "MELOTTS_ROOT", "/root/workspace/datasets/models/melotts-zh"
)
REPO_DIR = os.path.join(MELLO_ROOT, "MeloTTS")
CONFIG_PATH = os.path.join(MELLO_ROOT, "weights", "config.json")
CKPT_PATH = os.path.join(MELLO_ROOT, "weights", "checkpoint.pth")
DEFAULT_ONNX = os.path.join(MELLO_ROOT, "onnx", "melotts-zh.onnx")

OPSET = 17
INPUT_NAMES = [
    "tokens", "tones", "lang_ids", "lengths", "sid",
    "bert", "ja_bert",
    "sdp_noise", "z_noise",
    "noise_scale", "noise_scale_w", "length_scale", "sdp_ratio",
]
OUTPUT_NAMES = ["audio"]


# ---------------------------------------------------------------- 环境准备

def setup_melo_import():
    """sys.path + 语言模块 stub。须先于 melo.utils 导入，两处顶层拉模型的链
    都要拦：
      a) cleaner.py 顶层 import japanese/english/korean/french/spanish（各自
         顶层 G2p()/tokenizer 加载）；
      b) text/__init__.get_bert 函数体 import 全部 *_bert 模块（对任何语言
         调用都会执行）——english_bert 等 *_bert 顶层 from_pretrained 自己的
         BERT。
    ZH_MIX_EN 路径只触达 chinese_bert（mBERT，已缓存）——其余 stub 为显式
    NotImplementedError：误用即报错，不静默降级。"""
    sys.path.insert(0, REPO_DIR)
    sys.path.insert(0, os.path.join(REPO_DIR, "melo"))
    import melo.text as mtext

    def distribute_phone(n_phone, n_word):
        # 上游 japanese.distribute_phone 原样（chinese.py 等引用的纯函数）
        per = [0] * n_word
        for _ in range(n_phone):
            i = per.index(min(per))
            per[i] += 1
        return per

    def refuse(*_a, **_k):
        raise NotImplementedError(
            "T13 W3 导出脚本只覆盖 ZH_MIX_EN 路径；其他语言面 stub 未接"
        )

    stubs = {
        "japanese": {"distribute_phone": distribute_phone, "g2p": refuse,
                     "text_normalize": refuse, "get_bert_feature": refuse},
        "english": {"g2p": refuse, "text_normalize": refuse,
                    "get_bert_feature": refuse},
        "korean": {"g2p": refuse, "text_normalize": refuse,
                   "get_bert_feature": refuse},
        "french": {"g2p": refuse, "text_normalize": refuse,
                   "get_bert_feature": refuse},
        "spanish": {"g2p": refuse, "text_normalize": refuse,
                    "get_bert_feature": refuse},
        "english_bert": {"get_bert_feature": refuse},
        "japanese_bert": {"get_bert_feature": refuse},
        "french_bert": {"get_bert_feature": refuse},
        "spanish_bert": {"get_bert_feature": refuse},
    }
    for name, attrs in stubs.items():
        mod = types.ModuleType(f"melo.text.{name}")
        for k, v in attrs.items():
            setattr(mod, k, v)
        sys.modules[f"melo.text.{name}"] = mod
        setattr(mtext, name, mod)


def load_net_g():
    """构造 SynthesizerTrn 并 strict 加载官方 checkpoint（对齐上游 melo/api.py）。"""
    from melo import utils as melo_utils
    from melo.models import SynthesizerTrn

    hps = melo_utils.get_hparams_from_file(CONFIG_PATH)
    net_g = SynthesizerTrn(
        len(hps.symbols),
        hps.data.filter_length // 2 + 1,
        hps.train.segment_size // hps.data.hop_length,
        n_speakers=hps.data.n_speakers,
        num_tones=hps.num_tones,
        num_languages=hps.num_languages,
        **hps.model,
    )
    ckpt = torch.load(CKPT_PATH, map_location="cpu")
    net_g.load_state_dict(ckpt["model"], strict=True)
    net_g.eval()
    return net_g, hps


def frontend(text, hps):
    """文本 → 推理张量（ZH_MIX_EN 路径；官方 api.py 对中文模型的实际路由）。"""
    from melo.utils import get_text_for_tts_infer

    symbol_to_id = {s: i for i, s in enumerate(hps.symbols)}
    bert, ja_bert, phones, tones, lang_ids = get_text_for_tts_infer(
        text, "ZH_MIX_EN", hps, "cpu", symbol_to_id
    )
    return {
        "bert": bert.unsqueeze(0),
        "ja_bert": ja_bert.unsqueeze(0),
        "tokens": phones.unsqueeze(0),
        "tones": tones.unsqueeze(0),
        "lang_ids": lang_ids.unsqueeze(0),
        "lengths": torch.LongTensor([phones.size(0)]),
        "sid": torch.LongTensor([1]),  # 官方音色 ZH（spk2id["ZH"]=1）
    }


# ---------------------------------------------------------------- 导出图

class MeloExport(torch.nn.Module):
    """SynthesizerTrn.infer 的去随机化重写（逐行对齐上游 models.py infer，
    唯二差异：sdp randn / randn_like → 显式噪声输入）。"""

    def __init__(self, net_g):
        super().__init__()
        self.net_g = net_g

    def _sdp_logw(self, x, x_mask, g, sdp_noise, noise_scale_w):
        sdp = self.net_g.sdp
        x = torch.detach(x)
        x = sdp.pre(x)
        if g is not None:
            g = torch.detach(g)
            x = x + sdp.cond(g)
        x = sdp.convs(x, x_mask)
        x = sdp.proj(x) * x_mask
        flows = list(reversed(sdp.flows))
        flows = flows[:-2] + [flows[-1]]  # remove a useless vflow（上游注释）
        z = sdp_noise * noise_scale_w  # 上游：randn(b,2,T) * noise_scale
        for flow in flows:
            z = flow(z, x_mask, g=x, reverse=True)
        z0, _z1 = torch.split(z, [1, 1], 1)
        return z0

    ZHEAD = 8  # z_noise 时间维预留倍数（T_mel ≤ 8T 上界；越界=病态输入， loudly fail）

    def forward(self, tokens, tones, lang_ids, lengths, sid, bert, ja_bert,
                sdp_noise, z_noise_full, noise_scale, noise_scale_w, length_scale,
                sdp_ratio):
        from melo import commons

        net = self.net_g
        g = net.emb_g(sid).unsqueeze(-1)  # [b, h, 1]
        x, m_p, logs_p, x_mask = net.enc_p(
            tokens, lengths, tones, lang_ids, bert, ja_bert, g=g
        )
        logw = self._sdp_logw(x, x_mask, g, sdp_noise, noise_scale_w) * sdp_ratio
        logw = logw + net.dp(x, x_mask, g=g) * (1 - sdp_ratio)
        w = torch.exp(logw) * x_mask * length_scale
        w_ceil = torch.ceil(w)
        y_lengths = torch.clamp_min(torch.sum(w_ceil, [1, 2]), 1).long()
        y_mask = torch.unsqueeze(
            commons.sequence_mask(y_lengths, None), 1
        ).to(x_mask.dtype)
        attn_mask = torch.unsqueeze(x_mask, 2) * torch.unsqueeze(y_mask, -1)
        attn = commons.generate_path(w_ceil, attn_mask)
        m_p = torch.matmul(attn.squeeze(1), m_p.transpose(1, 2)).transpose(1, 2)
        logs_p = torch.matmul(attn.squeeze(1), logs_p.transpose(1, 2)).transpose(1, 2)
        z_noise = z_noise_full[:, :, :y_lengths[0]]  # 数据相关形状：切片到 mel 长度
        z_p = m_p + z_noise * torch.exp(logs_p) * noise_scale
        z = net.flow(z_p, y_mask, g=g, reverse=True)
        o = net.dec(z * y_mask, g=g)
        return o


def build_inputs(hps, text):
    """真实前端张量 + 固定种子噪声（导出/对拍/RTF 共用的规整输入）。"""
    feed = frontend(text, hps)
    t = feed["tokens"].size(1)
    gen = torch.Generator().manual_seed(20260903)
    feed["sdp_noise"] = torch.randn(1, 2, t, generator=gen)
    feed["z_noise"] = torch.randn(1, 192, MeloExport.ZHEAD * t, generator=gen)
    return feed


def scalars(speed=1.0, noise_scale=0.6, noise_scale_w=0.8, sdp_ratio=0.2):
    """默认采样参数=上游 api.py tts_to_file 默认值；语速经 length_scale=1/speed。"""
    return {
        "noise_scale": torch.scalar_tensor(noise_scale, dtype=torch.float32),
        "noise_scale_w": torch.scalar_tensor(noise_scale_w, dtype=torch.float32),
        "length_scale": torch.scalar_tensor(1.0 / speed, dtype=torch.float32),
        "sdp_ratio": torch.scalar_tensor(sdp_ratio, dtype=torch.float32),
    }


def ordered_feed(feed, sc):
    return [
        feed["tokens"], feed["tones"], feed["lang_ids"], feed["lengths"],
        feed["sid"], feed["bert"], feed["ja_bert"],
        feed["sdp_noise"], feed["z_noise"],
        sc["noise_scale"], sc["noise_scale_w"], sc["length_scale"], sc["sdp_ratio"],
    ]


# ---------------------------------------------------------------- 子命令

def cmd_export(args):
    torch.set_num_threads(args.threads)
    setup_melo_import()
    net_g, hps = load_net_g()
    wrapper = MeloExport(net_g)
    wrapper.eval()

    with torch.no_grad():
        feed = build_inputs(hps, "你好呀，今天天气真好。")
        sc = scalars()
        outs = wrapper(*ordered_feed(feed, sc))
        assert outs.shape[0] == 1 and outs.shape[1] == 1, outs.shape
        print(f"[export] 试跑输出波形 samples={outs.shape[2]} "
              f"(峰值 {outs.abs().max():.4f})")

        os.makedirs(os.path.dirname(os.path.abspath(args.out)), exist_ok=True)
        torch.onnx.export(
            wrapper,
            tuple(ordered_feed(feed, sc)),
            args.out,
            input_names=INPUT_NAMES,
            output_names=OUTPUT_NAMES,
            dynamic_axes={  # 批维固定 1：z 切片按 B=1 语义；动态轴只有 T
                "tokens": {1: "t"},
                "tones": {1: "t"},
                "lang_ids": {1: "t"},
                "bert": {2: "t"},
                "ja_bert": {2: "t"},
                "sdp_noise": {2: "t"},
                "z_noise": {2: "t"},
                "audio": {2: "samples"},
            },
            opset_version=OPSET,
            do_constant_folding=True,
        )
    size = os.path.getsize(args.out)
    print(f"[export] {args.out} ({size/1e6:.1f} MB, opset {OPSET})")


def make_ort_session(onnx_path, threads):
    import onnxruntime as ort

    so = ort.SessionOptions()
    so.intra_op_num_threads = threads
    so.inter_op_num_threads = 1
    return ort.InferenceSession(onnx_path, so, providers=["CPUExecutionProvider"])


def feed_dict(feed, sc):
    d = {}
    for name, ten in zip(INPUT_NAMES, ordered_feed(feed, sc)):
        d[name] = ten.numpy()
    return d


def audio_metrics(a, b):
    a, b = np.asarray(a, dtype=np.float64).ravel(), np.asarray(b, dtype=np.float64).ravel()
    if a.shape != b.shape:
        return {"error": f"shape mismatch {a.shape} vs {b.shape}"}
    diff = a - b
    max_abs = float(np.abs(diff).max())
    mean_abs = float(np.abs(diff).mean())
    rmse = float(np.sqrt((diff ** 2).mean()))
    sig_rms = float(np.sqrt((a ** 2).mean()))
    snr = float(20 * math.log10(sig_rms / rmse)) if rmse > 0 else float("inf")
    if float(np.std(a)) > 0 and float(np.std(b)) > 0:
        r = float(np.corrcoef(a, b)[0, 1])
    else:
        r = float("nan")
    return {"samples": int(a.size), "max_abs": max_abs, "mean_abs": mean_abs,
            "rmse": rmse, "snr_db": snr, "pearson_r": r}


def cmd_parity(args):
    torch.set_num_threads(args.threads)
    setup_melo_import()
    net_g, hps = load_net_g()
    wrapper = MeloExport(net_g)
    wrapper.eval()

    sess = make_ort_session(args.onnx, args.threads)
    sentences = [
        "你好呀，我是小云。",
        "今天我们一起搭积木好不好？",
        "从前有一只小兔子，它住在森林里。",
        "一二三四五，上山打老虎。",
        "小猫咪，毛茸茸，看见老鼠喵喵叫。",
    ]  # 纯中文句面：英文 g2p 未覆盖（Go/Python 同构债务，见 ADR-0008），不入对拍
    report = {"onnx": args.onnx, "opset": OPSET,
              "torch": torch.__version__, "sampling_default": "ns=0.6 nsw=0.8 sdp=0.2 speed=1.0",
              "faithfulness": [], "pytorch_vs_onnx": []}

    for i, text in enumerate(sentences):
        feed = frontend(text, hps)
        t = feed["tokens"].size(1)
        seed = 1000 + i
        # 补丁忠实性（monkeypatch 法，与 RNG 流对齐解耦）：monkeypatch 原版 infer
        # 内部唯二随机点（sdp reverse 的 torch.randn、z_p 的 torch.randn_like），
        # 喂入指定噪声张量跑「原版」→ ref；同噪声喂「补丁版」→ patch。ref≈patch
        # ⇒ 补丁图 ≡ 原版 infer；再加 patched≈ONNX（下方对拍）⇒ 原版==ONNX 证据链。
        with torch.no_grad():
            torch.manual_seed(seed)  # Tm 由原版自由跑一遍得出（噪声无关，只取形状）
            outs = net_g.infer(
                feed["tokens"], feed["lengths"], feed["sid"], feed["tones"],
                feed["lang_ids"], feed["bert"], feed["ja_bert"],
                sdp_ratio=0.2, noise_scale=0.6, noise_scale_w=0.8, length_scale=1.0,
            )
            tm = int(outs[2].shape[-1])  # y_mask [1,1,Tm]
            gn = torch.Generator().manual_seed(seed)
            n1 = torch.randn(1, 2, t, generator=gn)
            n2 = torch.randn(1, 192, tm, generator=gn)

            real_randn, real_randn_like = torch.randn, torch.randn_like
            calls = {"randn": 0, "randn_like": 0}

            def cap_randn(*a, **k):
                out = n1 if (len(a) >= 3 and tuple(a[:3]) == (1, 2, t)) \
                    else real_randn(*a, **k)
                calls["randn"] += 1
                return out

            def cap_randn_like(x, **k):
                out = n2 if tuple(x.shape) == (1, 192, tm) else real_randn_like(x, **k)
                calls["randn_like"] += 1
                return out

            torch.randn, torch.randn_like = cap_randn, cap_randn_like
            try:
                out2 = net_g.infer(
                    feed["tokens"], feed["lengths"], feed["sid"], feed["tones"],
                    feed["lang_ids"], feed["bert"], feed["ja_bert"],
                    sdp_ratio=0.2, noise_scale=0.6, noise_scale_w=0.8, length_scale=1.0,
                )
            finally:
                torch.randn, torch.randn_like = real_randn, real_randn_like
            assert calls["randn"] >= 1 and calls["randn_like"] >= 1, calls
            ref = out2[0][0, 0].numpy()

            z_full = torch.zeros(1, 192, MeloExport.ZHEAD * t)
            z_full[:, :, :tm] = n2
            patch = wrapper(*ordered_feed({**feed, "sdp_noise": n1, "z_noise": z_full},
                                          scalars()))[0, 0].numpy()
        report["faithfulness"].append({"text": text, "mel_len": tm,
                                       **audio_metrics(ref, patch)})

        # 对拍主体：同输入（前端张量 + 同噪声）PyTorch vs onnxruntime。
        feed["sdp_noise"], feed["z_noise"] = n1, z_full
        with torch.no_grad():
            pt = wrapper(*ordered_feed(feed, scalars()))[0, 0].numpy()
        on = sess.run(["audio"], feed_dict(feed, scalars()))[0][0, 0]
        m = audio_metrics(pt, on)
        m["text"] = text
        m["torch_samples"] = int(pt.size)
        m["onnx_samples"] = int(on.size)
        report["pytorch_vs_onnx"].append(m)
        print(f"[parity {i}] {text!r}: max_abs={m['max_abs']:.2e} "
              f"snr={m['snr_db']:.1f}dB r={m['pearson_r']:.6f} | "
              f"faithful max_abs={report['faithfulness'][-1]['max_abs']:.2e}")

    # 确定性：同输入两次 ORT 字节一致。
    feed = frontend(sentences[0], hps)
    t = feed["tokens"].size(1)
    gn = torch.Generator().manual_seed(7)
    feed["sdp_noise"] = torch.randn(1, 2, t, generator=gn)
    feed["z_noise"] = torch.randn(1, 192, MeloExport.ZHEAD * t, generator=gn)
    h1 = sess.run(["audio"], feed_dict(feed, scalars()))[0].tobytes()
    h2 = sess.run(["audio"], feed_dict(feed, scalars()))[0].tobytes()
    report["ort_deterministic"] = bool(h1 == h2)

    worst = max(report["pytorch_vs_onnx"], key=lambda m: m["max_abs"])
    report["verdict"] = {
        "max_abs_worst": worst["max_abs"],
        "min_snr_db": min(m["snr_db"] for m in report["pytorch_vs_onnx"]),
        "faithfulness_max_abs": max(m["max_abs"] for m in report["faithfulness"]),
        "ort_deterministic": report["ort_deterministic"],
    }
    print("[parity] verdict:", json.dumps(report["verdict"]))
    if args.json:
        os.makedirs(os.path.dirname(os.path.abspath(args.json)), exist_ok=True)
        with open(args.json, "w") as f:
            json.dump(report, f, ensure_ascii=False, indent=2)
        print(f"[parity] → {args.json}")


def save_wav(path, audio, sr):
    import soundfile as sf

    a = np.asarray(audio, dtype=np.float32).ravel()
    peak = float(np.abs(a).max())
    if peak > 1.0:
        a = a / peak
    sf.write(path, a, sr, subtype="PCM_16")


def cmd_rtf(args):
    torch.set_num_threads(args.threads)
    setup_melo_import()
    _, hps = load_net_g()
    sess = make_ort_session(args.onnx, args.threads)

    sentences = [
        "你好呀，我是小云。", "你想听故事吗？", "我们来玩捉迷藏吧！",
        "今天天气真好，太阳晒得暖洋洋的。", "一、二、三，木头人！",
        "从前有一只小兔子，它最爱吃胡萝卜。", "晚安，做个好梦。",
        "小星星，亮晶晶，挂在天上放光明。", "你饿了吗？我这里有饼干。",
        "我们一起数数：一、二、三、四、五。",
        "为什么天上的云不会掉下来呢？", "明天记得带上小雨伞哦。",
        "你的积木搭得真高呀！", "我要给你唱一首歌。",
        "春天到了，花园里开满了花。", "小心台阶，慢慢走。",
        "嘿嘿，被你找到啦！", "这个秘密我只告诉你一个人。",
        "风一吹，树叶就沙沙地响。", "祝你生日快乐，天天开心！",
    ]

    sr = hps.data.sampling_rate
    os.makedirs(args.sample_dir, exist_ok=True)
    rows = []
    # 预热 1 句（会话/缓存/线程池）
    feed = frontend(sentences[0], hps)
    gn = torch.Generator().manual_seed(42)
    feed["sdp_noise"] = torch.randn(1, 2, feed["tokens"].size(1), generator=gn)
    feed["z_noise"] = torch.randn(1, 192, MeloExport.ZHEAD * feed["tokens"].size(1), generator=gn)
    sess.run(["audio"], feed_dict(feed, scalars()))

    for i, text in enumerate(sentences):
        t0 = time.perf_counter()
        f = frontend(text, hps)
        t1 = time.perf_counter()
        gn = torch.Generator().manual_seed(42 + i)
        f["sdp_noise"] = torch.randn(1, 2, f["tokens"].size(1), generator=gn)
        f["z_noise"] = torch.randn(1, 192, MeloExport.ZHEAD * f["tokens"].size(1), generator=gn)
        fd = feed_dict(f, scalars())
        t2 = time.perf_counter()
        out = sess.run(["audio"], fd)[0]
        t3 = time.perf_counter()
        n_samples = out.shape[-1]
        audio_s = n_samples / sr
        infer_ms = (t3 - t2) * 1000
        rows.append({
            "text": text, "phonemes": int(f["tokens"].size(1)),
            "audio_s": round(audio_s, 3), "frontend_ms": round((t1 - t0) * 1000, 1),
            "infer_ms": round(infer_ms, 1), "rtf": round(infer_ms / 1000 / audio_s, 4),
            "first_chunk_note": "非流式整段出——first_packet≈infer_ms（流式导出为待办）",
        })
        if i < args.save_samples:
            save_wav(os.path.join(args.sample_dir, f"melotts-zh-official-{i}.wav"),
                     out, sr)

    rtfs = sorted(r["rtf"] for r in rows)
    infers = sorted(r["infer_ms"] for r in rows)

    def pct(v, p):
        idx = min(len(v) - 1, int(math.ceil(p / 100 * len(v))) - 1)
        return v[max(0, idx)]

    report = {
        "onnx": args.onnx, "device": "CPU", "threads": args.threads,
        "sampling_rate": sr, "n": len(rows),
        "rtf_mean": round(sum(rtfs) / len(rtfs), 4),
        "rtf_p50": pct(rtfs, 50), "rtf_p95": pct(rtfs, 95), "rtf_max": rtfs[-1],
        "infer_ms_p50": pct(infers, 50), "infer_ms_p95": pct(infers, 95),
        "infer_ms_max": infers[-1],
        "frontend_ms_mean": round(sum(r["frontend_ms"] for r in rows) / len(rows), 1),
        "rows": rows,
    }
    print(json.dumps({k: v for k, v in report.items() if k != "rows"}, ensure_ascii=False))
    os.makedirs(os.path.dirname(os.path.abspath(args.json)), exist_ok=True)
    with open(args.json, "w") as f:
        json.dump(report, f, ensure_ascii=False, indent=2)
    print(f"[rtf] → {args.json}")


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    sub = ap.add_subparsers(dest="cmd", required=True)

    pe = sub.add_parser("export")
    pe.add_argument("--out", default=DEFAULT_ONNX)
    pe.add_argument("--threads", type=int, default=4)
    pe.set_defaults(fn=cmd_export)

    pp = sub.add_parser("parity")
    pp.add_argument("--onnx", default=DEFAULT_ONNX)
    pp.add_argument("--json", default="")
    pp.add_argument("--threads", type=int, default=4)
    pp.set_defaults(fn=cmd_parity)

    pr = sub.add_parser("rtf")
    pr.add_argument("--onnx", default=DEFAULT_ONNX)
    pr.add_argument("--json", required=True)
    pr.add_argument("--threads", type=int, default=4)
    pr.add_argument("--save-samples", type=int, default=3)
    pr.add_argument("--sample-dir", default="reports/eval/T13/samples")
    pr.set_defaults(fn=cmd_rtf)

    args = ap.parse_args()
    args.fn(args)


if __name__ == "__main__":
    main()
