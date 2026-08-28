"""多样性指标（spec §3.7）：说话人/语速/主题分布熵、与真实参考集的 JS 距离、单源占比≤30%。"""

import math
from collections import Counter
from collections.abc import Iterable

DIVERSITY_FIELDS = ("speaker", "speed", "topic")
SINGLE_SOURCE_LIMIT = 0.30


def distribution(values: Iterable) -> dict[str, float]:
    """值序列 → 经验分布 {取值: 概率}；空序列 → 空分布。"""
    counts = Counter(values)
    total = sum(counts.values())
    return {key: count / total for key, count in counts.items()} if total else {}


def shannon_entropy(dist: dict[str, float]) -> float:
    """Shannon 熵（bit）：单点分布 0，k 类均匀分布达最大 log2(k)；只依赖概率多重集。"""
    return -sum(p * math.log2(p) for p in dist.values() if p > 0)


def js_distance(dist_a: dict[str, float], dist_b: dict[str, float]) -> float:
    """Jensen–Shannon 距离（bit，上界 1）：同分布 0，不相交分布 1；对称。"""
    def kl(p: dict[str, float], q: dict[str, float]) -> float:
        return sum(pk * math.log2(pk / q[key]) for key, pk in p.items()
                   if pk > 0 and q.get(key, 0.0) > 0)

    keys = set(dist_a) | set(dist_b)
    mid = {key: (dist_a.get(key, 0.0) + dist_b.get(key, 0.0)) / 2 for key in keys}
    return math.sqrt(max(0.0, 0.5 * kl(dist_a, mid) + 0.5 * kl(dist_b, mid)))


def single_source_share(upstream_models: Iterable) -> float:
    """单源占比：同一上游模型的最大占比；空输入 0。"""
    models = list(upstream_models)
    return max(Counter(models).values()) / len(models) if models else 0.0


def field_values(records: list[dict], field: str) -> list:
    """取多样性字段值：payload 优先，兼容扁平记录（真实参考集）；缺字段忽略。"""
    return [v for v in ((r.get("payload") or r).get(field) for r in records) if v is not None]


def evaluate(samples: list[dict], reference: list[dict] | None = None) -> dict:
    """多样性报告：三字段熵（含可选参考集 JS 距离）+ 单源占比 + 门槛判定（>30% 不通过）。"""
    report: dict = {"n": len(samples), "fields": {}}
    for field in DIVERSITY_FIELDS:
        dist = distribution(field_values(samples, field))
        entry = {"entropy_bits": shannon_entropy(dist), "categories": len(dist)}
        if reference is not None:
            ref_dist = distribution(field_values(reference, field))
            entry["js_distance_bits"] = js_distance(dist, ref_dist)
        report["fields"][field] = entry
    share = single_source_share(s["provenance"]["upstream_model"] for s in samples)
    report.update(single_source_share=share, ok=share <= SINGLE_SOURCE_LIMIT)
    return report
