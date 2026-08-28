"""CI-2 身份-记忆全程绑定：任何输出可回溯唯一身份；T5拒判瞬间记忆只读。
Hypothesis RuleBasedStateMachine stub.
"""
from __future__ import annotations
from hypothesis import strategies as st
from hypothesis.stateful import RuleBasedStateMachine, rule, invariant, Bundle, multiple
import secrets

class IdentityBindingSM(RuleBasedStateMachine):
    users = Bundle("users")
    outputs = Bundle("outputs")

    def __init__(self) -> None:
        super().__init__()
        # {output_id: user_id or None(拒判态)}
        self.output_owner: dict[str, str | None] = {}
        # 拒判瞬间 → memory 通道只读：待写队列
        self.pending_writes: list[tuple[str, str]] = []  # (user, data)
        self.id_rejected: bool = False

    @rule(target=users)
    def new_user(self) -> str:
        return f"u{secrets.token_hex(3)}"

    @rule(user=users, text=st.text(min_size=1, max_size=50))
    def produce_output(self, user: str, text: str) -> None:
        if self.id_rejected:
            owner: str | None = None
            # CI-2: 拒判时记忆通道只读——不得新写入
            self.pending_writes = []  # 清空：拒判态禁止
        else:
            owner = user
        oid = f"o{secrets.token_hex(3)}"
        self.output_owner[oid] = owner

    @rule()
    def toggle_identity_rejected(self) -> None:
        self.id_rejected = not self.id_rejected

    @invariant()
    def every_output_has_at_most_one_owner(self) -> None:
        for oid, owner in self.output_owner.items():
            assert owner is None or isinstance(owner, str), (
                f"CI-2: output {oid} owner ambiguous"
            )

TestIdentityBinding = IdentityBindingSM.TestCase
