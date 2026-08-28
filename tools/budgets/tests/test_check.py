"""budgets check 守恒校验测试（spec §3.6）——测试先行。

差分：ΣP95 − 并行重叠 ≤ total_p95_budget(1500) → exit 0，否则 exit 20；
超预算时输出「延迟负债表」（各段实际值 vs 预算值、差值、超标段）。"""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from budgets.check import load_config
from budgets.cli import main
from hypothesis import HealthCheck, given, settings
from hypothesis import strategies as st

SEG_IDS = ("tail_silence", "asr_uplink", "cloud_llm", "tts_first", "transport")
SEG_P95 = (600, 150, 450, 280, 20)  # Σ=1500，与 configs/budgets/latency.yaml 一致
REPO_CONFIG = Path(__file__).resolve().parents[3] / "configs" / "budgets" / "latency.yaml"

CONFIG_TEXT = """\
# 测试用预算（与 configs/budgets/latency.yaml 同构）
total_p95_budget: 1500
segments:
  - { id: tail_silence, p50: 450, p95: 600 }
  - { id: asr_uplink,   p50: 100, p95: 150 }
  - { id: cloud_llm,    p50: 300, p95: 450 }
  - { id: tts_first,    p50: 200, p95: 280 }
  - { id: transport,    p50: 20,  p95: 20 }
rules:
  - 劣化 >2σ 且无划拨说明 → 组合级 G1 红
"""


def write_config(tmp_path: Path) -> Path:
    path = tmp_path / "latency.yaml"
    path.write_text(CONFIG_TEXT, encoding="utf-8")
    return path


def write_report(tmp_path: Path, p95s, overlap=None, name="latency.json", ids=SEG_IDS) -> Path:
    report = {"commit": "test0001", "timestamp": "2026-08-28T00:00:00Z",
              "segments": [{"id": i, "p50": p, "p95": p} for i, p in zip(ids, p95s)]}
    if overlap is not None:
        report["overlap_ms"] = overlap
    path = tmp_path / name
    path.write_text(json.dumps(report), encoding="utf-8")
    return path


# 1. 差分：Σ=1500 → 0；Σ=1501 → 20；并行重叠抵扣 Σ=1550−50=1500 → 0、−49 → 20
def test_check_sum_at_budget_passes(tmp_path):
    report = write_report(tmp_path, SEG_P95)
    assert main(["check", "--report", str(report), "--config", str(write_config(tmp_path))]) == 0


def test_check_sum_over_budget_fails(tmp_path):
    report = write_report(tmp_path, (601, 150, 450, 280, 20))  # Σ=1501
    assert main(["check", "--report", str(report), "--config", str(write_config(tmp_path))]) == 20


def test_check_parallel_overlap_offsets_sum(tmp_path):
    config = str(write_config(tmp_path))
    p95s = (650, 150, 450, 280, 20)  # Σ=1550
    ok = write_report(tmp_path, p95s, overlap=50, name="ok.json")
    bad = write_report(tmp_path, p95s, overlap=49, name="bad.json")
    assert main(["check", "--report", str(ok), "--config", config]) == 0
    assert main(["check", "--report", str(bad), "--config", config]) == 20


# 2. 负债表内容：超预算时含标题、各段 id、预算/实际/差值、超标段
def test_check_over_budget_prints_debt_table(tmp_path, capsys):
    report = write_report(tmp_path, (700, 200, 500, 280, 20))  # Σ=1700
    assert main(["check", "--report", str(report), "--config", str(write_config(tmp_path))]) == 20
    out = capsys.readouterr().out
    assert "延迟负债表" in out and "差值" in out and "超标段" in out and "1700" in out
    for seg_id in SEG_IDS:
        assert seg_id in out
    assert "600" in out and "700" in out  # 预算值与实际值同表


def test_check_within_budget_has_no_overdue_segments(tmp_path, capsys):
    report = write_report(tmp_path, SEG_P95)
    assert main(["check", "--report", str(report), "--config", str(write_config(tmp_path))]) == 0
    assert "超标段" not in capsys.readouterr().out


# 3. 真实仓内 latency.yaml 可被极简解析器读取
def test_load_real_repo_latency_yaml():
    config = load_config(REPO_CONFIG)
    assert config.total_p95_budget == 1500
    assert [s.id for s in config.segments] == list(SEG_IDS)
    assert [s.p95 for s in config.segments] == list(SEG_P95)


# 4. 输入错误（缺文件 / 坏 JSON / 段与配置不一致）→ exit 2
def test_check_bad_inputs_exit_2(tmp_path):
    config = str(write_config(tmp_path))
    assert main(["check", "--report", str(tmp_path / "nope.json"), "--config", config]) == 2
    bad = tmp_path / "bad.json"
    bad.write_text("{not json", encoding="utf-8")
    assert main(["check", "--report", str(bad), "--config", config]) == 2
    missing = write_report(tmp_path, SEG_P95[:4], ids=SEG_IDS[:4], name="miss.json")
    assert main(["check", "--report", str(missing), "--config", config]) == 2


# 5. 属性：任意非负 p95 组合 + 非负并行重叠，退出码 ∈ {0,20} 且与守恒判定一致
@pytest.mark.property
@settings(max_examples=100, deadline=None,
          suppress_health_check=[HealthCheck.function_scoped_fixture])  # 复用 tmp 目录，每样例整体重写
@given(p95s=st.lists(st.integers(0, 3000), min_size=5, max_size=5), overlap=st.integers(0, 500))
def test_property_exit_code_matches_conservation(tmp_path, p95s, overlap):
    config = write_config(tmp_path)
    report = write_report(tmp_path, p95s, overlap=overlap)
    rc = main(["check", "--report", str(report), "--config", str(config)])
    assert rc in (0, 20)
    assert rc == (0 if sum(p95s) - overlap <= 1500 else 20)
