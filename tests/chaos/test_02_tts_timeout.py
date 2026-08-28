"""CI-4 矩阵 02：TTS 超时/首包失败。
期望：静默 ≤2s 端侧补偿；不重播半句。恢复：下轮回云档。门禁：G1。
"""
from __future__ import annotations
import pytest


@pytest.mark.gate(asset="T13", bi="BI-13.2", id="T13-CI4-02", level="G1")
@pytest.mark.chaos("tts_timeout")
def test_tts_timeout_compensation_skeleton() -> None:
    """骨架：真实实现需 mock TTS 首包超时 → 断言静默补偿≤2s，不重播半句。"""
    pass
