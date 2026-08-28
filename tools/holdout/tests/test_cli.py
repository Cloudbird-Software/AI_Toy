"""holdoutctl CLI 契约测试（spec §3.4）——测试先行。"""

from __future__ import annotations

import hashlib, hmac, json, os, sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))
from holdoutctl.cli import MIN_SLICE_N, apply_k_anonymity, main  # noqa: E402

TEST_SEAL_KEY = "test-only-seal-key"


def seal_manifest(manifest: dict, key: str = TEST_SEAL_KEY) -> dict:
    """测试端独立实现 HMAC 签名，避免与实现端自洽循环。"""
    payload = {k: v for k, v in manifest.items() if k != "signature"}
    canonical = json.dumps(payload, sort_keys=True, separators=(",", ":")).encode()
    manifest["signature"] = hmac.new(key.encode(), canonical, hashlib.sha256).hexdigest()
    return manifest


def make_manifest(object_count: int = 3, bad_count: bool = False) -> dict:
    objects = [{"path": f"datasets/holdout/shard-{i:03d}.jsonl",
                "sha256": hashlib.sha256(f"obj{i}".encode()).hexdigest(), "bytes": 100 + i}
               for i in range(object_count)]
    return {"version": 1, "suite": "real-t4", "created_at": "2026-08-28T00:00:00Z",
            "objects": objects, "object_count": object_count + 1 if bad_count else object_count}


def write_manifest(manifest: dict, tmp_path: Path) -> Path:
    path = tmp_path / "sealed-manifest.json"
    path.write_text(json.dumps(manifest, indent=2))
    return path


def clear_holdout_env(monkeypatch: pytest.MonkeyPatch) -> None:
    for var in [v for v in os.environ if v.startswith("HOLDOUT_")]:
        monkeypatch.delenv(var, raising=False)


def prepare_eval_run(tmp_path: Path, monkeypatch: pytest.MonkeyPatch, extra: list[str] | None = None):
    """凭据 + cwd + 合法 seal 就绪，返回 (out_dir, argv)。"""
    clear_holdout_env(monkeypatch)
    for key, value in {"HOLDOUT_ENVIRONMENT": "holdout", "HOLDOUT_RUNNER_TOKEN": "t",
                       "HOLDOUT_STORAGE_URL": "blob://holdout",
                       "HOLDOUT_SEAL_KEY": TEST_SEAL_KEY}.items():
        monkeypatch.setenv(key, value)
    monkeypatch.chdir(tmp_path)
    holdout_dir = tmp_path / "datasets" / "holdout"
    holdout_dir.mkdir(parents=True)
    (holdout_dir / "sealed-manifest.json").write_text(
        json.dumps(seal_manifest(make_manifest()), indent=2))
    out_dir = tmp_path / "reports" / "nightly"
    return out_dir, ["eval", "--suite", "real-t4", "--out", str(out_dir)] + (extra or [])


# 1. eval 凭据门禁：无 HOLDOUT_* 凭据 / 非 holdout 环境 → exit 2
@pytest.mark.parametrize("env", [
    {},
    {"HOLDOUT_ENVIRONMENT": "dev", "HOLDOUT_RUNNER_TOKEN": "t",
     "HOLDOUT_STORAGE_URL": "blob://x", "HOLDOUT_SEAL_KEY": "k"},
])
def test_eval_without_holdout_credentials_exits_2(tmp_path, monkeypatch, env):
    clear_holdout_env(monkeypatch)
    for key, value in env.items():
        monkeypatch.setenv(key, value)
    assert main(["eval", "--suite", "real-t4", "--out", str(tmp_path)]) == 2


def test_eval_with_credentials_but_missing_seal_fails_not_2(tmp_path, monkeypatch):
    _, argv = prepare_eval_run(tmp_path, monkeypatch)
    Path(tmp_path / "datasets" / "holdout" / "sealed-manifest.json").unlink()
    rc = main(argv)
    assert rc != 0 and rc != 2


# 2. verify-seal：合法 manifest → 0；篡改/缺失/计数不符 → 非零
def test_verify_seal_ok(tmp_path, monkeypatch):
    monkeypatch.setenv("HOLDOUT_SEAL_KEY", TEST_SEAL_KEY)
    assert main(["verify-seal", "--manifest",
                 str(write_manifest(seal_manifest(make_manifest()), tmp_path))]) == 0


def test_verify_seal_tampered_object(tmp_path, monkeypatch):
    monkeypatch.setenv("HOLDOUT_SEAL_KEY", TEST_SEAL_KEY)
    manifest = seal_manifest(make_manifest())
    manifest["objects"][0]["sha256"] = "0" * 64  # 篡改一条，不动 signature
    assert main(["verify-seal", "--manifest", str(write_manifest(manifest, tmp_path))]) != 0


def test_verify_seal_missing_manifest(tmp_path, monkeypatch):
    monkeypatch.setenv("HOLDOUT_SEAL_KEY", TEST_SEAL_KEY)
    monkeypatch.chdir(tmp_path)
    assert main(["verify-seal"]) != 0  # 默认路径 datasets/holdout/sealed-manifest.json
    assert main(["verify-seal", "--manifest", str(tmp_path / "nope.json")]) != 0


def test_verify_seal_object_count_mismatch(tmp_path, monkeypatch):
    """签名对整个 payload 合法，但声明 object_count 与 objects 长度不符 → 拒绝。"""
    monkeypatch.setenv("HOLDOUT_SEAL_KEY", TEST_SEAL_KEY)
    path = write_manifest(seal_manifest(make_manifest(bad_count=True)), tmp_path)
    assert main(["verify-seal", "--manifest", str(path)]) != 0


def test_verify_seal_no_key(tmp_path, monkeypatch):
    clear_holdout_env(monkeypatch)
    assert main(["verify-seal", "--manifest",
                 str(write_manifest(seal_manifest(make_manifest()), tmp_path))]) != 0


# 3. audit：追加 jsonl 行含 timestamp/suite/sha256，重复调用追加不覆盖
def test_audit_appends_jsonl_and_keeps_history(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    artifact = tmp_path / "metrics.json"
    payloads = ['{"suite": "real-t4"}', '{"suite": "real-t4", "v": 2}']
    for payload in payloads:
        artifact.write_text(payload)
        assert main(["audit", "--suite", "real-t4", "--artifact", str(artifact)]) == 0
    log = tmp_path / "reports" / "holdout-audit.jsonl"
    rows = [json.loads(line) for line in log.read_text().splitlines() if line.strip()]
    assert len(rows) == 2  # 追加不覆盖，首行原样保留
    for row in rows:
        assert all(row[field] for field in ("timestamp", "actor", "suite", "sha256"))
    assert [r["sha256"] for r in rows] == [hashlib.sha256(p.encode()).hexdigest() for p in payloads]


# 4. k-匿名单测：n<5 抑制（含边界 4 抑/5 留）
def test_k_anonymity_suppresses_small_slices():
    assert MIN_SLICE_N == 5
    out = apply_k_anonymity({"a": 4, "b": 5, "slice": 3, "other": 10})
    assert "slice" not in out and "a" not in out
    assert out == {"b": 5, "other": 10}


# 5. k-匿名属性测试（hypothesis 可选）：任意分片集合中 n<5 的键都不在输出
def test_property_small_slices_never_in_output():
    pytest.importorskip("hypothesis")
    from hypothesis import given, strategies as st

    @given(st.dictionaries(st.text(min_size=1, max_size=8), st.integers(0, 50), max_size=25))
    def check(slices: dict[str, int]) -> None:
        out = apply_k_anonymity(slices)
        assert all(v >= MIN_SLICE_N for v in out.values())
        assert all(k not in out for k, v in slices.items() if v < MIN_SLICE_N)
        assert all(out[k] == v for k, v in slices.items() if v >= MIN_SLICE_N)

    check()


# eval 集成：只输出聚合指标 + 原始路径不出受控存储 + 写审计
def test_eval_outputs_only_aggregates_and_audits(tmp_path, monkeypatch):
    out_dir, argv = prepare_eval_run(tmp_path, monkeypatch)
    assert main(argv) == 0
    metrics_files = list(out_dir.glob("*.json"))
    assert len(metrics_files) == 1
    text = metrics_files[0].read_text()
    assert "datasets/holdout" not in text  # 原始样本路径不出受控存储
    assert json.loads(text)["suite"] == "real-t4"
    rows = [json.loads(line) for line
            in (tmp_path / "reports" / "holdout-audit.jsonl").read_text().splitlines() if line.strip()]
    assert rows[-1]["suite"] == "real-t4"
    assert rows[-1]["sha256"] == hashlib.sha256(metrics_files[0].read_bytes()).hexdigest()


def test_eval_suppresses_small_slices_in_output(tmp_path, monkeypatch):
    shards = tmp_path / "shards.json"
    shards.write_text(json.dumps({"slice": 3, "other": 10}))
    out_dir, argv = prepare_eval_run(tmp_path, monkeypatch, extra=["--shards", str(shards)])
    assert main(argv) == 0
    data = json.loads(next(out_dir.glob("*.json")).read_text())
    assert "slice" not in data["metrics"]
    assert data["metrics"]["other"] == 10
    assert data["suppressed_slices"] == 1
