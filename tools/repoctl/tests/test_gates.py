import json
import subprocess
import sys
from datetime import UTC, datetime
from pathlib import Path

from repoctl.agentsmd import SECTIONS
from repoctl.cli import main

E_G0 = {"id": "T4-G0-01", "bi": "BI-4.2", "level": "G0"}
E_G1 = {"id": "T4-G1-01", "bi": "BI-4.1", "level": "G1"}
DOC = "# T4 唤醒词\n\n| 断言 | BI |\n|---|---|\n| T4-G0-01 | BI-4.2 |\n| T4-G1-01 | BI-4.1 |\n"
PKG = "# AGENTS.md — 唤醒词（T4）\n" + "".join(f"## {s}\n内容\n" for s in SECTIONS)


def write(root, rel, text):
    f = Path(root) / rel
    f.parent.mkdir(parents=True, exist_ok=True)
    f.write_text(text)


def setup_coverage(tmp_path, entries):
    write(tmp_path, "reports/gates/T4.json", json.dumps({"asset": "T4", "results": entries}))
    write(tmp_path, "docs/gates/assets/T4.md", DOC)
    return ["coverage", "--root", str(tmp_path)]


def setup_exemption(tmp_path, text):
    write(tmp_path, "reports/exemptions.yaml", text)
    return ["exemption", "audit", "--file", str(tmp_path / "reports/exemptions.yaml")]


def test_coverage_all_present(tmp_path):
    assert main(setup_coverage(tmp_path, [E_G0, E_G1])) == 0

def test_coverage_missing_bi(tmp_path):
    assert main(setup_coverage(tmp_path, [E_G0])) == 20  # BI-4.1 无断言
    assert main(setup_coverage(tmp_path, [dict(E_G0, level="G1"), E_G1])) == 20  # 无 G0 断言

def test_coverage_orphan_assertion(tmp_path):
    entries = [E_G0, E_G1, {"id": "T4-G1-09", "bi": "BI-4.9", "level": "G1"}]
    assert main(setup_coverage(tmp_path, entries)) == 20

def test_agents_md_all_present(tmp_path):
    write(tmp_path, "AGENTS.md", "# AGENTS.md — 根\n")
    write(tmp_path, "packages/py/kws/AGENTS.md", PKG)
    write(tmp_path, "packages/ts/cloud-orchestrator/AGENTS.md", PKG)
    assert main(["agents-md", "check", "--root", str(tmp_path)]) == 0

def test_agents_md_missing_package(tmp_path):
    write(tmp_path, "AGENTS.md", "# 根\n")
    write(tmp_path, "packages/py/kws/AGENTS.md", PKG)
    write(tmp_path, "packages/py/tts/.keep", "")
    assert main(["agents-md", "check", "--root", str(tmp_path)]) == 20

def test_agents_md_missing_section(tmp_path):
    write(tmp_path, "AGENTS.md", "# 根\n")
    write(tmp_path, "packages/py/kws/AGENTS.md", "# AGENTS.md\n## 本包边界\n一句话\n")
    assert main(["agents-md", "check", "--root", str(tmp_path)]) == 20

def test_forbidden_whitelist_ok(tmp_path):
    write(tmp_path, "tools/holdout/client.py", 'P = "datasets/holdout/real-t4"\n')
    write(tmp_path, "packages/py/eval-platform/registry.py", '"datasets/holdout"\n')
    write(tmp_path, "datasets/holdout/sealed-manifest.json", '{"objs": ["datasets/holdout/o1"]}\n')
    assert main(["forbidden-refs", "--root", str(tmp_path)]) == 0

def test_forbidden_package_ref(tmp_path):
    write(tmp_path, "packages/py/kws/train.py", 'open("datasets/holdout/train.wav")\n')
    assert main(["forbidden-refs", "--root", str(tmp_path)]) == 20

def test_forbidden_clean_repo(tmp_path):
    write(tmp_path, "packages/py/kws/src/a.py", "x = 1\n")
    write(tmp_path, "docs/gates/assets/T4.md", "# T4\n")
    assert main(["forbidden-refs", "--root", str(tmp_path)]) == 0

def test_exemption_no_expiry(tmp_path):
    assert main(setup_exemption(tmp_path, "- id: T4-G1-03\n  reason: r\n  expires: 2099-01-01\n")) == 0

def test_exemption_expired(tmp_path):
    assert main(setup_exemption(tmp_path, "- id: T4-G1-03\n  expires: 2001-01-01\n  reason: r\n")) == 20

def test_exemption_boundary_today_and_empty(tmp_path):
    today = f"- id: X\n  expiry: {datetime.now(UTC).date().isoformat()}\n  reason: r\n"
    assert main(setup_exemption(tmp_path, today)) == 0  # expiry 别名 + 当天未过期
    assert main(setup_exemption(tmp_path, "")) == 0  # 空台账

def test_module_entry_point():
    src = Path(__file__).resolve().parents[1] / "src"
    r = subprocess.run([sys.executable, "-m", "repoctl", "--help"], capture_output=True,
                       text=True, check=False, cwd=str(src))
    assert r.returncode == 0 and "repoctl" in r.stdout
