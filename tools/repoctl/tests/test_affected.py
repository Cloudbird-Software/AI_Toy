import json
import subprocess

import pytest
from hypothesis import given
from hypothesis import strategies as st
from repoctl.affected import assets_for
from repoctl.cli import main


def repo_with_changes(tmp_path, changes):
    def git(*a):
        subprocess.run(["git", "-C", str(tmp_path), *a], check=True, capture_output=True)

    def put(rel, text):
        f = tmp_path / rel
        f.parent.mkdir(parents=True, exist_ok=True)
        f.write_text(text)

    git("init", "-q")
    git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "--allow-empty", "-qm", "base")
    for rel, text in changes.items():
        put(rel, text)
    git("add", "-A")

def run_affected(tmp_path, capsys):
    code = main(["affected", "--base", "HEAD", "--root", str(tmp_path)])
    return code, json.loads(capsys.readouterr().out)

def test_kws_change_maps_t4(tmp_path, capsys):
    repo_with_changes(tmp_path, {"packages/py/kws/src/a.py": "y = 2\n"})
    assert run_affected(tmp_path, capsys) == (0, ["T4"])

def test_tools_change_maps_empty(tmp_path, capsys):
    repo_with_changes(tmp_path, {"tools/repoctl/x.py": "z = 3\n"})
    assert run_affected(tmp_path, capsys) == (0, [])

def test_union_of_paths(tmp_path, capsys):
    repo_with_changes(tmp_path, {"packages/py/kws/a.py": "1\n", "packages/py/speaker/b.py": "2\n",
                                  "docs/x.md": "3\n"})
    assert run_affected(tmp_path, capsys) == (0, ["T4", "T5"])

P = st.lists(st.from_regex(r"[a-z0-9/_.\-]{0,40}", fullmatch=True))

@pytest.mark.property
@given(P, P)
def test_map_deterministic_and_unional(a, b):
    assert assets_for(a) == assets_for(list(a))
    assert set(assets_for(a + b)) == set(assets_for(a)) | set(assets_for(b))
