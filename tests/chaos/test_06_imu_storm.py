"""CI-4 矩阵 06：IMU 事件风暴。
期望：限流+事件聚合；无动作风暴。恢复：重启/人工确认。门禁：G1。
"""
from __future__ import annotations
import pytest


@pytest.mark.gate(asset="T6", bi="BI-6.1", id="T6-CI4-06", level="G1")
@pytest.mark.chaos("imu_storm")
def test_imu_storm_rate_limit_skeleton() -> None:
    """骨架：真实实现需注入高频 IMU → 断言限流聚合，无动作风暴。"""
    pass
