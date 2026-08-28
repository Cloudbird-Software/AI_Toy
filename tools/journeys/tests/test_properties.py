"""属性测试（hypothesis）：同 seed 同剧本输出完全相同；seeds 数与报告条目一致。"""

import random

import pytest
from hypothesis import given, settings
from hypothesis import strategies as st

from journeys import runner, schema

ASSERTIONS = [
    {"metric": "completion_rate", "op": ">=", "value": 0.0},
    {"metric": "safety_events", "op": "<=", "value": 100},
    {"metric": "latency_p95_ms", "op": "<=", "value": 100000},
    {"metric": "memory_hit_rate", "op": ">=", "value": 0.0},
]

def _script(salt):
    rng = random.Random(salt)
    return schema.JourneyScript.from_dict({
        "id": f"J{rng.randint(1, 999)}-{salt}",
        "tier": rng.choice(["core", "variant"]),
        "persona": {"age": rng.randint(3, 12), "patience": rng.choice(["low", "high"])},
        "steps": [f"s{i}" for i in range(rng.randint(1, 6))],
        "inject": {
            "interrupts": rng.choice([[], [{"at_step": 1, "kind": "user_interrupt"}]]),
            "safety_events": rng.choice([[], [{"kind": "crisis_metaphor"}]]),
        },
        "assertions": ASSERTIONS,
    })

@pytest.mark.property
@settings(max_examples=50, deadline=None)
@given(seeds=st.integers(1, 5), salt=st.integers(0, 10_000))
def test_same_seeds_same_scripts_produce_identical_output(seeds, salt):
    scripts = [_script(salt), _script(salt + 1)]
    first = runner.run_journeys(scripts, seeds=seeds)
    second = runner.run_journeys(list(reversed(scripts)), seeds=seeds)
    first.pop("timestamp")
    second.pop("timestamp")
    assert {j["id"]: j for j in first["journeys"]} == {j["id"]: j for j in second["journeys"]}
    assert first["summary"] == second["summary"]

@pytest.mark.property
@settings(max_examples=50, deadline=None)
@given(seeds=st.integers(1, 8), n_scripts=st.integers(1, 4))
def test_seed_count_matches_report_entries(seeds, n_scripts):
    report = runner.run_journeys([_script(1000 + i) for i in range(n_scripts)], seeds=seeds)
    assert len(report["journeys"]) == n_scripts and report["seeds"] == seeds
    assert all([r["seed"] for r in j["runs"]] == list(range(seeds)) for j in report["journeys"])
