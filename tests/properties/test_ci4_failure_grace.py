"""CI-4 失败优雅性：每个外部依赖失效→行为∈预定义降级集；无响应>10s不道歉=违反。
Stub: 故障注入矩阵 8 行（见 system.md），此处留骨架由 nightly 全量、PR 冒烟跑前 3 行。
"""
from __future__ import annotations
import pytest

# 故障注入矩阵：(注入名, 期望行为描述, 恢复描述, 门禁级)
FAILURE_MATRIX = [
    ("llm_outage",       "≤2 档内 3s 恢复；诚实告知受限", "≤30s 回 L0 无脏输出",       "G0"),
    ("tts_timeout",      "静默≤2s 端侧补偿；不重播半句", "下轮回云档",                   "G1"),
    ("output_runaway",   "硬截断+自然收尾",             "—",                           "G1"),
    ("memory_unwritable","降级无新记忆继续对话；缓存待写","恢复补写 0 丢失",              "G1"),
    ("voice_reject",     "CI-2 只读模式+明示不确定",     "识别成功即恢复",               "G0"),
    ("imu_storm",        "限流+聚合；无动作风暴",         "重启/人工",                   "G1"),
    ("clock_drift",      "时间类记忆停写；其余不影响",    "校时恢复",                    "G1"),
    ("pack_install_interrupt", "原子回滚上一完整版本",   "重试升级",                    "G0"),
]


@pytest.mark.parametrize("inject,expect,recover,level", FAILURE_MATRIX,
                         ids=[r[0] for r in FAILURE_MATRIX])
def test_ci4_failure_matrix_skeleton(inject: str, expect: str, recover: str, level: str) -> None:
    """骨架：每个故障注入占位。真实注入走 tests/chaos/ 对应文件。"""
    # 真实实现：注入对应组件故障，观测行为是否 ∈ 降级集，记录响应时延
    _ = (inject, expect, recover, level)
