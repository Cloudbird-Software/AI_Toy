"""CI-4 矩阵 07：时钟漂移/NTP 失效。
期望：时间类记忆停写；其余不受影响。恢复：校时后恢复。门禁：G1。
"""
from __future__ import annotations
import pytest


@pytest.mark.gate(asset="T10", bi="BI-10.4", id="T10-CI4-07", level="G1")
@pytest.mark.chaos("clock_drift")
def test_clock_drift_freeze_time_memory_skeleton() -> None:
    """骨架：真实实现需注入时钟漂移 → 断言时间类记忆停写。"""
    pass
