"""CI-4 矩阵 08：升级中断/场景包半装。
期望：原子回滚上一完整版本（T16-G0）。恢复：重试升级。门禁：G0。
"""
from __future__ import annotations
import pytest


@pytest.mark.gate(asset="T16", bi="BI-16.1", id="T16-CI4-08", level="G0")
@pytest.mark.chaos("pack_install_interrupt")
def test_pack_install_interrupt_atomic_rollback_skeleton() -> None:
    """骨架：真实实现需中断 pack install → 断言原子回滚上一完整版本。"""
    pass
