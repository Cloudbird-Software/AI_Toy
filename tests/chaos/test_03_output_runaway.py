"""CI-4 矩阵 03：输出超长/死循环文本。
期望：硬截断+自然收尾。门禁：G1。
"""
from __future__ import annotations
import pytest


@pytest.mark.gate(asset="T14", bi="BI-14.2", id="T14-CI4-03", level="G1")
@pytest.mark.chaos("output_runaway")
def test_output_runaway_hard_truncation_skeleton() -> None:
    """骨架：真实实现需注入超长生成 → 断言硬截断触发+收尾自然。"""
    pass
