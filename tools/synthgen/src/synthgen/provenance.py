"""溯源戳（spec §3.7）：每条合成样本强制携带生成器 id+版本+种子+上游模型四字段。"""


def stamp(generator_id: str, generator_version: str, seed: int, upstream_model: str) -> dict:
    """构造溯源戳（恰好四个强制字段）。"""
    return {"generator_id": generator_id, "generator_version": generator_version,
            "seed": seed, "upstream_model": upstream_model}
