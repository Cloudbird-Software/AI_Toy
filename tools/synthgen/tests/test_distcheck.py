"""dist-check 多样性测试（spec §3.7）：分布熵 / 参考集 JS 距离 / 单源占比 30% 门槛。"""

import json

import pytest
from hypothesis import given
from hypothesis import strategies as st
from synthgen import distcheck
from synthgen.cli import main


def sample(i, model="m-a", speaker="spk-1"):
    return {"sample_id": f"s{i:04d}",
            "provenance": {"generator_id": "g", "generator_version": "1", "seed": 0,
                           "upstream_model": model},
            "payload": {"speaker": speaker, "speed": "normal", "topic": "bedtime"}}


def batch_with_share(tmp_path, monkeypatch, dom, batch_id="batch-x"):
    """写 100 条批次：dom 条来自 m-dom，其余分散在 7 个模型。"""
    monkeypatch.chdir(tmp_path)
    d = tmp_path / "datasets/synth/batches" / batch_id
    d.mkdir(parents=True)
    samples = [sample(i, model="m-dom") for i in range(dom)]
    samples += [sample(dom + i, model=f"m-{i % 7}") for i in range(100 - dom)]
    (d / "samples.jsonl").write_text("".join(json.dumps(s) + "\n" for s in samples))


# 4. 均匀分布熵高（k 类均匀 = log2 k）、单点分布熵 0；JS 距离同分布 0 / 不相交 1 / 对称
def test_entropy_extremes_and_js_distance():
    assert distcheck.shannon_entropy(distcheck.distribution(["a", "b", "c", "d"])) == pytest.approx(2.0)
    assert distcheck.shannon_entropy(distcheck.distribution(["a"] * 10)) == 0.0
    a, b = distcheck.distribution(["x", "y"]), distcheck.distribution(["z", "w"])
    assert distcheck.js_distance(a, a) == pytest.approx(0.0)
    assert distcheck.js_distance(a, b) == pytest.approx(1.0)
    assert distcheck.js_distance(a, b) == pytest.approx(distcheck.js_distance(b, a))


# 4. 与真实参考集的 JS 距离：同分布 → 0；远分布 → >0.5
def test_evaluate_js_distance_to_reference():
    samples = [sample(i, speaker=f"spk-{i % 4}") for i in range(40)]
    same = [{"speaker": f"spk-{i % 4}", "speed": "normal", "topic": "bedtime"} for i in range(40)]
    far = [{"speaker": "spk-x", "speed": "slow", "topic": "play"}] * 40
    fields = distcheck.evaluate(samples, same)["fields"]
    assert fields["speaker"]["js_distance_bits"] == pytest.approx(0.0)
    assert distcheck.evaluate(samples, far)["fields"]["speaker"]["js_distance_bits"] > 0.5


# 4. 单源占比门槛：31% → 非零退出（输出占比）；30% → 通过
@pytest.mark.parametrize("dom,ok", [(31, False), (30, True)])
def test_dist_check_single_source_gate(tmp_path, monkeypatch, capsys, dom, ok):
    batch_with_share(tmp_path, monkeypatch, dom)
    rc = main(["dist-check", "--batch", "batch-x"])
    assert (rc == 0) is ok
    if not ok:
        assert "0.31" in capsys.readouterr().out


# 4. --reference 输出 js_ref
def test_cli_reference_output(tmp_path, monkeypatch, capsys):
    batch_with_share(tmp_path, monkeypatch, 10)
    ref = tmp_path / "real-ref.jsonl"
    ref.write_text(json.dumps({"speaker": "spk-1", "speed": "normal", "topic": "bedtime"}) + "\n")
    assert main(["dist-check", "--batch", "batch-x", "--reference", str(ref)]) == 0
    assert "js_ref=" in capsys.readouterr().out


# 5. 属性：分布熵对字段值置换不变（双射重命名 / 逆序重排）
@pytest.mark.property
@given(st.lists(st.sampled_from("012345"), min_size=1, max_size=60))
def test_property_entropy_invariant_under_value_permutation(values):
    relabel = {str(i): str((i + 3) % 6) for i in range(6)}
    base = distcheck.shannon_entropy(distcheck.distribution(values))
    renamed = distcheck.shannon_entropy(distcheck.distribution([relabel[v] for v in values]))
    reordered = distcheck.shannon_entropy(distcheck.distribution(list(reversed(values))))
    assert base == pytest.approx(renamed) == pytest.approx(reordered)
