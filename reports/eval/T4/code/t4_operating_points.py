#!/usr/bin/env python3
"""交叉 wake_scores.csv × fp_sweep.csv，输出全阈段操作点表。
用法: t4_operating_points.py <report_dir> [report_dir2 ...]
wake 口径: clip 级 max_score >= th（与 TRAINLOG 一致）；另按 tag 分列（near/far_*/fair_*）。
"""
import csv, sys
from pathlib import Path

import numpy as np

GATES = {"near": 0.97, "far_min": 0.90, "fp_per_hour": 0.5, "adv_per_hour": 0.0}

def load(report_dir):
    rd = Path(report_dir)
    rows = list(csv.DictReader(open(rd / "wake_scores.csv")))
    fp = {}
    for r in csv.DictReader(open(rd / "fp_sweep.csv")):
        fp.setdefault(r["generator"], {})[float(r["threshold"])] = (int(r["events"]), float(r["hours"]))
    return rows, fp

def analyze(report_dir):
    rows, fp = load(report_dir)
    tags = sorted({r["tag"] for r in rows})
    by_tag = {t: [float(r["max_score"]) for r in rows if r["tag"] == t] for t in tags}
    ths = sorted(fp.get("gen-tneg", {}).keys())
    print(f"\n== {report_dir} ==")
    hdr = ["th", "near", "far_min", "fair_child", "fair_adult", "tneg/h", "kwsadv/h", "ALL-PASS"]
    print("\t".join(hdr))
    for th in ths:
        def rate(tag):
            sc = by_tag.get(tag)
            return sum(s >= th for s in sc) / len(sc) if sc else float("nan")
        near = rate("near")
        far_tags = [t for t in tags if t.startswith("snr")]
        far_min = min((rate(t) for t in far_tags), default=float("nan"))
        fc, fa = rate("fair_child"), rate("fair_adult")
        tn = fp["gen-tneg"][th][0] / fp["gen-tneg"][th][1]
        ka = fp["gen-kwsadv"][th][0] / fp["gen-kwsadv"][th][1]
        ok = (near >= GATES["near"] and far_min >= GATES["far_min"]
              and tn <= GATES["fp_per_hour"] and ka <= GATES["adv_per_hour"])
        print(f"{th:.2f}\t{near:.4f}\t{far_min:.4f}\t{fc:.4f}\t{fa:.4f}\t{tn:.1f}\t{ka:.1f}\t{'YES' if ok else ''}")

for d in sys.argv[1:]:
    analyze(d)
