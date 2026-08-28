"""CI-4 矩阵 05：声纹拒判。
期望：CI-2 只读模式+明示「不确定是谁」。恢复：下次识别成功即恢复。门禁：G0。
"""
from __future__ import annotations
import pytest


@pytest.mark.gate(asset="T5", bi="BI-5.2", id="T5-CI4-05", level="G0")
@pytest.mark.chaos("voice_reject")
def test_voice_reject_ci2_readonly_skeleton() -> None:
    """骨架：真实实现需注入声纹拒判 → 断言 CI-2 只读激活+提示不确定。"""
    pass
