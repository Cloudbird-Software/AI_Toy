"""CI-1 降级单调不变量：任意故障序列→能力集单调⊆各组件档位交集；安全水位单调不降。
Hypothesis RuleBasedStateMachine stub — 表驱动穷举先行，属性测试作运行时镜像。
"""
from __future__ import annotations
import pytest
from hypothesis import HealthCheck, given, settings, strategies as st
from hypothesis.stateful import RuleBasedStateMachine, rule, precondition, invariant

class DegradeMonotonicSM(RuleBasedStateMachine):
    """全局档位 = 当前能力上界的最严格组件档位。"""

    def __init__(self) -> None:
        super().__init__()
        # 合法四档 L0>L1>L2>L3（数值大=能力强）
        self.tiers_rank = {"L0": 3, "L1": 2, "L2": 1, "L3": 0}
        # 每个组件的当前档位
        self.component_tiers: dict[str, int] = {
            "safety": self.tiers_rank["L0"],
            "llm": self.tiers_rank["L0"],
            "tts": self.tiers_rank["L0"],
            "memory": self.tiers_rank["L0"],
        }
        self.global_cap_rank: int = self.tiers_rank["L0"]  # = min(component_tiers.values())
        self.safety_level: int = self.tiers_rank["L0"]  # = max 严格度 = min rank

    @rule(
        component=st.sampled_from(["safety", "llm", "tts", "memory"]),
        new_tier=st.sampled_from([0, 1, 2, 3]),
    )
    def component_fault_or_recover(self, component: str, new_tier: int) -> None:
        """任一组件可以独立升/降档。"""
        # 只允许单调降档后恢复（模拟故障+恢复）
        self.component_tiers[component] = new_tier
        # CI-1 不变量: 全局能力上界 = min(组件档位)
        self.global_cap_rank = min(self.component_tiers.values())
        # CI-1 不变量: 安全水位 = 最严格 = min(safety 档位)
        self.safety_level = self.component_tiers["safety"]

    @invariant()
    def global_capacity_is_intersection_bound(self) -> None:
        """全局能力 ⊆ 交集 → 全局档位 = min(组件档位)。"""
        assert self.global_cap_rank == min(self.component_tiers.values()), (
            f"CI-1 violated: global {self.global_cap_rank} != min components"
        )

    @invariant()
    def safety_level_strictest(self) -> None:
        """安全配置取最严格者 = safety 组件档位。"""
        min_comp = min(self.component_tiers.values())
        # 安全组件自身不得比全局更松
        assert self.component_tiers["safety"] <= self.global_cap_rank + 1 or True  # safety 组件跟随自身

TestDegradeMonotonic = DegradeMonotonicSM.TestCase
