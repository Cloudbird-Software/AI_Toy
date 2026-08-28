"""8:2 切分（spec §3.7）：生成即切出 synth-holdout 并写 manifest。"""

import random

HOLDOUT_RATIO = 0.2


def split_holdout(sample_ids: list[str], seed: int) -> tuple[list[str], list[str]]:
    """确定性 8:2 切分：按 seed 洗牌，前 floor(n×0.2) 条为 holdout，其余为 train；同 seed 完全一致。"""
    shuffled = list(sample_ids)
    random.Random(seed).shuffle(shuffled)
    n = int(len(shuffled) * HOLDOUT_RATIO)
    return shuffled[n:], shuffled[:n]
