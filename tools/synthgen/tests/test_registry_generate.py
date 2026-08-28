"""注册器 + 溯源戳 + 8:2 切分测试（spec §3.7）——测试先行。"""

import json

import pytest
from hypothesis import given
from hypothesis import strategies as st
from synthgen import registry, split
from synthgen.cli import main

BATCH_DIR = "datasets/synth/batches"


def register_gen(monkeypatch, tmp_path):
    monkeypatch.chdir(tmp_path)
    assert main(["register", "--id", "gen-a", "--version", "1.0.0", "--seed-policy", "fixed",
                 "--outputs-manifest", "m.jsonl"]) == 0


def load_jsonl(path):
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]


# 1. 注册后可查询；重复 id+version 报错不落盘；同 id 新版本放行（缺省版本=最近注册）
def test_register_query_and_duplicate(tmp_path):
    path = tmp_path / "registry.jsonl"
    record = registry.register(path, "gen-a", "1.0.0", "fixed", "m.jsonl")
    assert set(record) == {"id", "version", "seed_policy", "outputs_manifest"}
    assert registry.get(registry.load(path), "gen-a") == record
    with pytest.raises(registry.DuplicateGeneratorError):
        registry.register(path, "gen-a", "1.0.0", "per-sample", "m2.jsonl")
    assert len(registry.load(path)) == 1  # 重复注册未追加
    registry.register(path, "gen-a", "1.1.0", "fixed", "m.jsonl")
    assert registry.get(registry.load(path), "gen-a")["version"] == "1.1.0"


# 1. CLI 层：重复注册 / 未注册生成 / 缺失批次 → 非零退出
def test_cli_input_errors_exit_nonzero(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    argv = ["register", "--id", "gen-a", "--version", "1", "--seed-policy", "fixed",
            "--outputs-manifest", "m.jsonl"]
    assert main(argv) == 0 and main(argv) != 0  # 重复注册
    assert main(["generate", "--id", "ghost", "--n", "5", "--seed", "1"]) != 0  # 未注册
    assert main(["dist-check", "--batch", "nope"]) == 2  # 批次缺失


# 2. 每条样本带溯源戳：{generator_id, generator_version, seed, upstream_model} 四字段
def test_every_sample_carries_provenance_stamp(tmp_path, monkeypatch):
    register_gen(monkeypatch, tmp_path)
    assert main(["generate", "--id", "gen-a", "--n", "25", "--seed", "7"]) == 0
    samples = load_jsonl(tmp_path / BATCH_DIR / "gen-a-1.0.0-seed7-n25" / "samples.jsonl")
    assert len(samples) == 25
    for s in samples:
        prov = s["provenance"]
        assert set(prov) == {"generator_id", "generator_version", "seed", "upstream_model"}
        assert (prov["generator_id"], prov["generator_version"], prov["seed"]) == ("gen-a", "1.0.0", 7)
        assert prov["upstream_model"]


# 3. N=100 → 80/20（train/holdout 不相交、并集=全量），manifest 记录切分；同 seed 逐字节复现
def test_generate_splits_80_20_manifest_and_reproducible(tmp_path, monkeypatch):
    register_gen(monkeypatch, tmp_path)
    args = ["generate", "--id", "gen-a", "--n", "100", "--seed", "42"]
    assert main(args) == 0
    d = tmp_path / BATCH_DIR / "gen-a-1.0.0-seed42-n100"
    train, holdout = load_jsonl(d / "synth-train.jsonl"), load_jsonl(d / "synth-holdout.jsonl")
    assert (len(train), len(holdout)) == (80, 20)
    tids, hids = {s["sample_id"] for s in train}, {s["sample_id"] for s in holdout}
    all_ids = {s["sample_id"] for s in load_jsonl(d / "samples.jsonl")}
    assert not tids & hids and tids | hids == all_ids
    manifest = json.loads((d / "manifest.json").read_text(encoding="utf-8"))
    assert (manifest["train_n"], manifest["holdout_n"]) == (80, 20) and manifest["seed"] == 42
    first = (d / "samples.jsonl").read_bytes()
    assert main(args) == 0 and (d / "samples.jsonl").read_bytes() == first


# 3. 属性：任意 seed 两次切分完全一致，且无重无漏、holdout=floor(0.2n)
@pytest.mark.property
@given(st.integers(0, 2**32 - 1))
def test_property_split_reproducible_any_seed(seed):
    ids = [f"s{i:04d}" for i in range(97)]
    first, second = split.split_holdout(ids, seed), split.split_holdout(ids, seed)
    assert first == second
    assert sorted(first[0] + first[1]) == sorted(ids)
    assert (len(first[0]), len(first[1])) == (78, 19)
