#!/usr/bin/env python3
"""Go 前端（ChinesePhonemizer）与 Python 参考 g2p（chinese_mix v2）逐句对拍 +
Go token 序列经 ONNX 合成（端侧 Go 路径预演，JaBert 恒零）。

输入：dumpphonemes 工具的 JSON（text/symbols/tones/langs）。
输出：对拍 JSON（逐句电话音一致性 + 首个分歧点）+ Go 路径 wav 样例。

用法：
  go run ./tools/tts/dumpphonemes < sentences.txt > go.json
  python3 compare_go_frontend.py go.json --onnx <onnx> --json out.json --wav-dir dir
"""

import argparse
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from export_melotts_onnx import (  # noqa: E402
    MELLO_ROOT, REPO_DIR, setup_melo_import, load_net_g, MeloExport,
    make_ort_session, INPUT_NAMES, save_wav,
)

import numpy as np  # noqa: E402
import torch  # noqa: E402


def strip_pads(symbols, tones, langs):
    out_s, out_t, out_l = [], [], []
    for s, t, l in zip(symbols, tones, langs):
        if s == "_":
            continue
        out_s.append(s), out_t.append(int(t)), out_l.append(int(l))
    return out_s, out_t, out_l


def reference_g2p(text, hps):
    """参考：chinese_mix g2p v2（jieba+tone sandhi+pypinyin 全量）→ cleaned_text_to_sequence。"""
    from melo import utils as melo_utils
    from melo.text import cleaned_text_to_sequence
    from melo.text.cleaner import clean_text

    symbol_to_id = {s: i for i, s in enumerate(hps.symbols)}
    norm, phones, tones, word2ph = clean_text(text, "ZH_MIX_EN")
    phone_ids, tone_ids, lang_ids = cleaned_text_to_sequence(
        phones, tones, "ZH_MIX_EN", symbol_to_id)
    # g2p 自带 ["_"] 包夹，与 Go 侧同口径剥掉后再比
    ref_s = [p for p in phones if p != "_"]
    ref_t = [t for p, t in zip(phones, tone_ids) if p != "_"]
    return ref_s, [int(t) for t in ref_t]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("gojson")
    ap.add_argument("--onnx", default=os.path.join(MELLO_ROOT, "onnx", "melotts-zh.onnx"))
    ap.add_argument("--json", default="")
    ap.add_argument("--wav-dir", default="")
    ap.add_argument("--threads", type=int, default=4)
    args = ap.parse_args()

    import difflib

    setup_melo_import()
    _, hps = load_net_g()
    sess = make_ort_session(args.onnx, args.threads)
    sr = hps.data.sampling_rate

    entries = json.load(open(args.gojson))
    report = {"gojson": args.gojson, "onnx": args.onnx, "sentences": []}
    exact = 0
    phone_agree_sum, phone_agree_n = 0.0, 0
    for i, e in enumerate(entries):
        text = e["text"]
        ref_symbols, ref_tones = reference_g2p(text, hps)
        go_symbols, go_tones, _ = strip_pads(e["symbols"], e["tones"], e["langs"])

        sm = difflib.SequenceMatcher(a=go_symbols, b=ref_symbols)
        ratio = sm.ratio()
        first_div = None
        if go_symbols == ref_symbols and go_tones == ref_tones:
            exact += 1
        else:
            for j, (a, b) in enumerate(zip(go_symbols, ref_symbols)):
                if a != b or (j < len(go_tones) and j < len(ref_tones) and go_tones[j] != ref_tones[j]):
                    first_div = {"index": j, "go": a, "ref": b,
                                 "go_tone": go_tones[j] if j < len(go_tones) else None,
                                 "ref_tone": ref_tones[j] if j < len(ref_tones) else None}
                    break
        phone_agree_sum += ratio
        phone_agree_n += 1
        row = {
            "text": text,
            "go_phone_len": len(go_symbols), "ref_phone_len": len(ref_symbols),
            "phone_agreement": round(ratio, 4),
            "exact_match": go_symbols == ref_symbols and go_tones == ref_tones,
            "tone_match": go_tones == ref_tones,
            "first_divergence": first_div,
        }
        report["sentences"].append(row)
        print(f"[cmp {i}] {text!r} agree={ratio:.3f} exact={row['exact_match']} "
              f"div={first_div}")

        # Go token 序列 → ONNX 合成（Go 端侧路径预演：JaBert 恒零）
        if args.wav_dir:
            t = len(go_symbols) * 2 + 1
            feed = {
                "tokens": torch.LongTensor(intersperse_ids(e["symbols"], hps)).unsqueeze(0),
                "tones": torch.LongTensor(intersperse_tones(e)).unsqueeze(0),
                "lang_ids": torch.full((1, t), 3, dtype=torch.int64),
                "lengths": torch.LongTensor([t]),
                "sid": torch.LongTensor([1]),
                "bert": torch.zeros(1, 1024, t),
                "ja_bert": torch.zeros(1, 768, t),
                "sdp_noise": torch.randn(1, 2, t),
                "z_noise": torch.randn(1, 192, 8 * t),
                "noise_scale": torch.tensor(0.6), "noise_scale_w": torch.tensor(0.8),
                "length_scale": torch.tensor(1.0), "sdp_ratio": torch.tensor(0.2),
            }
            audio = sess.run(["audio"], {k: v.numpy() for k, v in feed.items()})[0][0, 0]
            save_wav(os.path.join(args.wav_dir, f"melotts-zh-gofrontend-{i}.wav"),
                     audio, sr)

    report["summary"] = {
        "n": len(entries), "exact_match": exact,
        "mean_phone_agreement": round(phone_agree_sum / max(1, phone_agree_n), 4),
    }
    print("[cmp] summary:", json.dumps(report["summary"]))
    if args.json:
        with open(args.json, "w", encoding="utf-8") as f:
            json.dump(report, f, ensure_ascii=False, indent=2)
        print(f"[cmp] → {args.json}")


def intersperse_ids(symbols, hps):
    sym2id = {s: i for i, s in enumerate(hps.symbols)}
    ids = [sym2id[s] for s in symbols if s != "_"]
    return [0] + sum([[i, 0] for i in ids], [])


def intersperse_tones(e):
    tones = [int(t) for s, t in zip(e["symbols"], e["tones"]) if s != "_"]
    return [0] + sum([[x, 0] for x in tones], [])


if __name__ == "__main__":
    main()
