"""旅程执行循环。桩阶段：真 driver（packages/py/user-sim）协议未接入，
以确定性模拟执行（random.Random(f"{seed}:{journey_id}")）；后续卡接真
driver 时替换 `_simulate_run` 调用点即可。"""

import math
import operator
import random
import statistics
from datetime import datetime, timezone

_OPS = {">=": operator.ge, "<=": operator.le, ">": operator.gt, "<": operator.lt, "==": operator.eq}

def _simulate_run(script, seed):
    rng = random.Random(f"{seed}:{script.id}")
    steps_total = len(script.steps)
    p_fail = (0.05 if script.tier == "core" else 0.08) + (
        0.03 if script.persona["patience"] == "low" else 0.0)
    completed = rng.randrange(0, steps_total) if rng.random() < p_fail else steps_total
    return {
        "seed": seed,
        "completion_rate": round(completed / steps_total, 4),
        "latency_ms": round(rng.uniform(400.0, 1400.0), 1),
        "safety_events": sum(1 for _ in script.inject["safety_events"] if rng.random() < 0.02),
        "memory_hit": rng.random() < 0.95,
    }

def aggregate_metrics(runs):
    ordered = sorted(r["latency_ms"] for r in runs)
    p95 = ordered[max(1, math.ceil(0.95 * len(ordered))) - 1]
    return {
        "completion_rate": round(statistics.fmean(r["completion_rate"] for r in runs), 4),
        "latency_p95_ms": round(p95, 1),
        "safety_events": sum(r["safety_events"] for r in runs),
        "memory_hit_rate": round(statistics.fmean(1.0 if r["memory_hit"] else 0.0 for r in runs), 4),
    }

def evaluate_assertions(metrics, assertions):
    return [{"metric": m, "op": op, "value": value, "observed": metrics[m],
             "pass": bool(_OPS[op](metrics[m], value))} for m, op, value in assertions]

def run_journeys(scripts, seeds, set_name="golden", driver="packages/py/user-sim"):
    if seeds < 1:
        raise ValueError("seeds must be >= 1")
    journeys = []
    for script in scripts:
        runs = [_simulate_run(script, seed) for seed in range(seeds)]
        metrics = aggregate_metrics(runs)
        results = evaluate_assertions(metrics, script.assertions)
        journeys.append({"id": script.id, "tier": script.tier, "source": script.source,
                         "runs": runs, "metrics": metrics, "assertions": results,
                         "verdict": "pass" if all(r["pass"] for r in results) else "fail"})
    fail_ids = [j["id"] for j in journeys if j["verdict"] == "fail"]
    return {"set": set_name, "seeds": seeds, "driver": driver, "driver_mode": "simulated",
            "timestamp": datetime.now(timezone.utc).isoformat(timespec="seconds"),
            "journeys": journeys,
            "summary": {"journeys_total": len(journeys), "pass": len(journeys) - len(fail_ids),
                        "fail": len(fail_ids), "fail_ids": fail_ids,
                        "overall": "fail" if fail_ids else "pass"}}
