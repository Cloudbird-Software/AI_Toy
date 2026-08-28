"""CI-4 矩阵 04：记忆存储不可写。
期望：降级无新记忆继续对话；缓存待写不阻塞。恢复：补写 0 丢失。门禁：G1。
"""
from __future__ import annotations
import pytest


@pytest.mark.gate(asset="T10", bi="BI-10.4", id="T10-CI4-04", level="G1")
@pytest.mark.chaos("memory_unwritable")
def test_memory_unwritable_degrade_skeleton() -> None:
    """骨架：真实实现需 mock memory 写失败 → 断言对话不阻塞，补写 0 丢失。"""
    pass
