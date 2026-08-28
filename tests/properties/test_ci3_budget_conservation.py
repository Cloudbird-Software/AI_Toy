"""CI-3 延迟预算守恒：Σ分段实测 P95 − 并行重叠收益 ≤ 1500ms。
Stub: 结构先行，实测数值由 budgets 工具写入 reports/nightly/latency.json。
"""
from __future__ import annotations
import json
import pathlib
import pytest

TOTAL_P95_BUDGET_MS = 1500
REPORT_PATH = pathlib.Path("reports/nightly/latency.json")


def test_ci3_budget_conservation_when_report_exists() -> None:
    """CI-3: Σ分段 P95 − 并行重叠 ≤ 总预算。
    当 nightly 报告不存在时标记 skip。
    """
    if not REPORT_PATH.exists():
        pytest.skip(f"{REPORT_PATH} 未生成，由 nightly 或 budgets check 填充")
    data = json.loads(REPORT_PATH.read_text())
    segments = data.get("segments", [])
    sum_p95 = sum(s["observed_p95_ms"] for s in segments) if segments else 0
    overlap = data.get("parallel_overlap_ms", 0)
    assert sum_p95 - overlap <= TOTAL_P95_BUDGET_MS, (
        f"CI-3 G1 红: ΣP95({sum_p95})−并行({overlap})={sum_p95-overlap} > 预算{TOTAL_P95_BUDGET_MS}"
    )
