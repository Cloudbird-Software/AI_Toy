"""契约测试：`journeys run` CLI（spec §3.5）。"""

import json

import pytest

from journeys.cli import main

METRICS = ("completion_rate", "latency_p95_ms", "safety_events", "memory_hit_rate")
CORE_YAML = (
    "id: J01-morning\n"
    "tier: core\n"
    "persona: {age: 7, patience: high}\n"
    "steps: [say, wait, close]\n"
    "inject: {interrupts: [], safety_events: []}\n"
    "assertions:\n"
    "  - {metric: completion_rate, op: '>=', value: 0.0}\n"
    "  - {metric: safety_events, op: '<=', value: 100}\n"
    "  - {metric: latency_p95_ms, op: '<=', value: 100000}\n"
    "  - {metric: memory_hit_rate, op: '>=', value: 0.0}\n"
)
VARIANT_YAML = (
    "id: J05-comfort-crisis\n"
    "tier: variant\n"
    "persona: {age: 4, patience: low}\n"
    "steps: [say, inject_crisis, wait]\n"
    "inject: {interrupts: [{at_step: 2, kind: user_interrupt}], safety_events: [{kind: crisis_metaphor}]}\n"
    "assertions:\n"
    "  - {metric: completion_rate, op: '>=', value: 0.0}\n"
    "  - {metric: safety_events, op: '<=', value: 100}\n"
)

def _run(tmp_path, capsys, texts, *extra):
    root = tmp_path / "scripts"
    root.mkdir()
    for name, text in texts.items():
        (root / name).write_text(text, encoding="utf-8")
    argv = ["run", "--set", "golden", "--seeds", "3", "--driver", "no-such-driver",
            "--scripts-dir", str(root), *extra]
    rc = main(argv)
    captured = capsys.readouterr()
    return rc, captured.out, captured.err

def test_run_golden_set_emits_json_with_four_metrics(tmp_path, capsys):
    rc, out, err = _run(tmp_path, capsys, {"J01.yaml": CORE_YAML, "J02.yaml": VARIANT_YAML})
    assert rc == 0, err
    report = json.loads(out)
    assert report["set"] == "golden" and report["seeds"] == 3
    tiers = {j["id"]: j["tier"] for j in report["journeys"]}
    assert tiers == {"J01-morning": "core", "J05-comfort-crisis": "variant"}
    assert all(
        all(m in j["metrics"] for m in METRICS)
        and [r["seed"] for r in j["runs"]] == [0, 1, 2]
        and all(a["pass"] for a in j["assertions"])
        and j["verdict"] == "pass"
        for j in report["journeys"]
    )
    assert report["summary"]["overall"] == "pass"

@pytest.mark.parametrize("field", ["id", "tier"])
def test_schema_missing_field_exits_nonzero_with_message(tmp_path, capsys, field):
    value = "J01-morning" if field == "id" else "core"
    texts = {"bad.yaml": CORE_YAML.replace(f"{field}: {value}\n", "")}
    rc, _, err = _run(tmp_path, capsys, texts)
    assert rc == 2 and field in err

def test_schema_invalid_tier_value_fails_validation(tmp_path, capsys):
    texts = {"bad.yaml": CORE_YAML.replace("tier: core", "tier: core2")}
    rc, _, err = _run(tmp_path, capsys, texts)
    assert rc == 2 and "tier" in err and "core2" in err

def test_assertion_failure_yields_fail_verdict_and_exit_code(tmp_path, capsys):
    texts = {"J01.yaml": CORE_YAML.replace("value: 0.0}", "value: 1.01}", 1)}
    rc, out, _ = _run(tmp_path, capsys, texts)
    assert rc == 1
    report = json.loads(out)
    journey = report["journeys"][0]
    assert journey["verdict"] == "fail"
    assert [a for a in journey["assertions"] if not a["pass"]][0]["metric"] == "completion_rate"
    assert report["summary"]["overall"] == "fail"
    assert report["summary"]["fail_ids"] == [journey["id"]]

def test_out_flag_writes_report_to_file(tmp_path, capsys):
    out_file = tmp_path / "report.json"
    rc, out, _ = _run(tmp_path, capsys, {"J01.yaml": CORE_YAML}, "--out", str(out_file))
    assert rc == 0 and out.strip() == ""
    assert json.loads(out_file.read_text(encoding="utf-8"))["set"] == "golden"
