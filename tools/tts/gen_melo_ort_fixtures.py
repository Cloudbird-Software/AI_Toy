#!/usr/bin/env python3
"""Go 侧 MeloSession（ORT）对拍 fixtures 生成（M2，issue #133）。

对拍口径（确定性）：会话级——Python 前端张量 + torch 固定种子噪声张量喂导出图，
存盘为原始二进制；Go 测试（packages/go/tts/meloort）加载同一组张量跑 yalue
绑定，与 Python ORT 参考波形逐样本比对。前端（g2p）差异不进本对拍（另有
reports/eval/T13/go-frontend-parity.json）；噪声源差异不进本对拍（Go P1 噪声
是 splitmix64 派生，会话契约只管「同输入同输出」）。

用法：python3 gen_melo_ort_fixtures.py --out <testdata 目录>
产物：NN-tokens.i64 / NN-tones.i64 / NN-langs.i64 / NN-sdp.f32 / NN-z.f32 /
     NN-ref.f32（小端原始二进制）+ meta.json
"""

import argparse
import json
import os
import sys

import numpy as np

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import export_melotts_onnx as base  # noqa: E402  复用环境准备/前端/ORT 会话机制


# 与 cmd_parity 同一句面（前 3 句：短/中/长），seed 约定 1000+i。
SENTENCES = [
    "你好呀，我是小云。",
    "今天我们一起搭积木好不好？",
    "从前有一只小兔子，它住在森林里。",
]


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--out", required=True)
    ap.add_argument("--threads", type=int, default=2,
                    help="参考 ORT intra_op 线程数（与 Go 侧 RTF 口径一致）")
    args = ap.parse_args()

    base.setup_melo_import()
    import torch

    _, hps = base.load_net_g()
    sess = base.make_ort_session(base.DEFAULT_ONNX, args.threads)
    os.makedirs(args.out, exist_ok=True)

    meta = {
        "onnx": base.DEFAULT_ONNX, "opset": base.OPSET,
        "torch": torch.__version__,
        "ort_python": __import__("onnxruntime").__version__,
        "ort_python_threads": args.threads,
        "sampling_rate": hps.data.sampling_rate,
        "sampling_default": "ns=0.6 nsw=0.8 sdp=0.2 speed=1.0",
        "zhead": base.MeloExport.ZHEAD,
        "noise_seed_rule": "torch.Generator().manual_seed(1000+i)，先 sdp 后 z 连续抽取",
        "sentences": [],
    }
    for i, text in enumerate(SENTENCES):
        feed = base.frontend(text, hps)
        t = feed["tokens"].size(1)
        gn = torch.Generator().manual_seed(1000 + i)
        sdp = torch.randn(1, 2, t, generator=gn)
        z = torch.randn(1, 192, base.MeloExport.ZHEAD * t, generator=gn)
        feed["sdp_noise"], feed["z_noise"] = sdp, z
        ref = sess.run(["audio"], base.feed_dict(feed, base.scalars()))[0][0, 0]

        stem = os.path.join(args.out, f"{i:02d}")
        for name, arr in [("tokens", feed["tokens"]), ("tones", feed["tones"]),
                          ("langs", feed["lang_ids"])]:
            arr.numpy().astype("<i8").tofile(f"{stem}-{name}.i64")
        # ja_bert=mBERT 韵律特征（会话输入的一部分，必须随 fixtures 走——喂零则
        # mel 长度与波形全变）；bert 槽按 ZH_MIX_EN 契约恒零，Go 侧按契约置零。
        feed["ja_bert"].numpy().astype("<f4").tofile(f"{stem}-jabert.f32")
        feed["sdp_noise"].numpy().astype("<f4").tofile(f"{stem}-sdp.f32")
        feed["z_noise"].numpy().astype("<f4").tofile(f"{stem}-z.f32")
        ref.astype("<f4").tofile(f"{stem}-ref.f32")
        meta["sentences"].append({"i": i, "text": text, "t": int(t),
                                  "samples": int(ref.size),
                                  "ja_bert_norm": float(feed["ja_bert"].norm())})
        print(f"[fixture {i}] {text!r} t={t} samples={ref.size} "
              f"ja_bert_norm={float(feed['ja_bert'].norm()):.2f}")

    with open(os.path.join(args.out, "meta.json"), "w") as f:
        json.dump(meta, f, ensure_ascii=False, indent=2)
    print(f"[fixtures] → {args.out}（{len(SENTENCES)} 句）")


if __name__ == "__main__":
    main()
