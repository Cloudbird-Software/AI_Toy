import hashlib

from repoctl.cli import main


def setup(tmp_path, sha=None, source=None, content=b"wake-weights\x00\x01"):
    src = tmp_path / "w.bin"
    src.write_bytes(content)
    mdir = tmp_path / "manifests"
    mdir.mkdir()
    (mdir / "kws.yaml").write_text(
        f"- id: kws-base\n  sha256: {sha or hashlib.sha256(content).hexdigest()}\n  source: {source or f'file://{src}'}\n")
    return ["fetch-models", "--manifest", str(mdir), "--cache", str(tmp_path / "cache")]


def test_sha_match(tmp_path):
    assert main(setup(tmp_path)) == 0
    assert (tmp_path / "cache/w.bin").read_bytes() == b"wake-weights\x00\x01"

def test_sha_mismatch(tmp_path):
    assert main(setup(tmp_path, sha="0" * 64)) == 20
    assert not (tmp_path / "cache/w.bin").exists()  # 坏权重不落缓存

def test_missing_source(tmp_path):
    assert main(setup(tmp_path, source=f"file://{tmp_path}/nope.bin")) == 2
