"""budgets ledger 趋势台账测试（spec §3.6）——测试先行。

单文件 history 数组：{"history": [报告, ...]}（按时间升序）。
前 29 天稳定 ±10、第 30 天 700 → 该段 >2σ 劣化标红；稳定段不标。"""

from __future__ import annotations

import json
from pathlib import Path

from budgets.cli import main

SEG_BASES = {"tail_silence": 600, "asr_uplink": 150, "cloud_llm": 450, "tts_first": 280,
             "transport": 20}


def stable_p95(day: int, base: int) -> int:  # base±10，周期 3
    return base + ((day % 3) - 1) * 10


def entry(day: int, p95_by_id: dict) -> dict:
    return {"commit": f"c{day:04d}", "timestamp": f"2026-08-{day % 28 + 1:02d}T00:00:00Z",
            "segments": [{"id": i, "p50": p, "p95": p} for i, p in p95_by_id.items()]}


def write_history(tmp_path: Path, entries: list) -> Path:
    path = tmp_path / "latency-history.json"
    path.write_text(json.dumps({"history": entries}), encoding="utf-8")
    return path


def line_for(out: str, seg_id: str) -> str:
    return next(line for line in out.splitlines() if line.startswith(seg_id))


def stable_history(days: int) -> list:
    return [entry(day, {i: stable_p95(day, b) for i, b in SEG_BASES.items()})
            for day in range(days)]


# 1. 前 29 天稳定 ±10、第 30 天 700 → 该段标红，稳定段不标
def test_ledger_flags_degraded_segment(tmp_path, capsys):
    day30 = {i: stable_p95(29, b) for i, b in SEG_BASES.items()}
    day30["tail_silence"] = 700
    path = write_history(tmp_path, stable_history(29) + [entry(29, day30)])
    assert main(["ledger", "--history", str(path)]) == 20  # 标红 → 组合级 G1 红
    out = capsys.readouterr().out
    assert "红" in line_for(out, "tail_silence")
    for stable_id in ("asr_uplink", "cloud_llm", "tts_first", "transport"):
        assert "红" not in line_for(out, stable_id)


# 2. 稳定 30 天：无任何标红，各段仍入表
def test_ledger_stable_history_has_no_red(tmp_path, capsys):
    assert main(["ledger", "--history", str(write_history(tmp_path, stable_history(30)))]) == 0
    out = capsys.readouterr().out
    assert "红" not in out and "tail_silence" in out


# 3. 窗口只取最近 N 份：远古尖峰出窗不稀释基线
def test_ledger_uses_last_n_entries_only(tmp_path):
    entries = [entry(day, {"tail_silence": 5000}) for day in range(5)]  # 远古尖峰
    entries += [entry(day, {"tail_silence": stable_p95(day, 600)}) for day in range(5, 34)]
    entries.append(entry(34, {"tail_silence": 700}))  # 共 35 份，最新 700
    path = str(write_history(tmp_path, entries))
    assert main(["ledger", "--history", path]) == 20  # 默认 30 份：尖峰出窗 → 红
    assert main(["ledger", "--history", path, "--days", "40"]) == 0  # 全量：μ 被抬高 → 不红


# 4. 单份历史无基线，不标红
def test_ledger_single_entry_is_never_red(tmp_path):
    assert main(["ledger", "--history", str(write_history(tmp_path, [entry(0, SEG_BASES)]))]) == 0


# 5. 输入错误（缺文件 / 缺 history 键 / --days<1）→ exit 2
def test_ledger_bad_inputs_exit_2(tmp_path):
    assert main(["ledger", "--history", str(tmp_path / "nope.json")]) == 2
    bad = tmp_path / "bad.json"
    bad.write_text("[]", encoding="utf-8")  # 不是 {"history": [...]}
    assert main(["ledger", "--history", str(bad)]) == 2
    path = str(write_history(tmp_path, stable_history(3)))
    assert main(["ledger", "--history", path, "--days", "0"]) == 2
