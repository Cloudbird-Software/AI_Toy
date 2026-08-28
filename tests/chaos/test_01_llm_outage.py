"""CI-4 故障注入矩阵 01：云 LLM 断连/5xx/限流。
期望：≤2 档内 3s 恢复对话；诚实告知受限（BI-14.2）。恢复：≤30s 回 L0 无脏输出。门禁级：G0。
"""
from __future__ import annotations
import pytest


@pytest.mark.gate(asset="T14", bi="BI-14.1", id="T14-CI4-01", level="G0")
@pytest.mark.chaos("llm_outage")
def test_llm_outage_degrade_and_recover_skeleton() -> None:
    """骨架：真实实现需 mock LLM 层 → 断言降级档位/提示语/恢复后无脏输出。"""
    pass
