"""生成器注册表（spec §3.7）：jsonl 每行恰含 {id, version, seed_policy, outputs_manifest}；(id, version) 唯一。"""

import json
from pathlib import Path


class DuplicateGeneratorError(Exception):
    """(id, version) 已注册。"""


def load(path: Path) -> list[dict]:
    """读注册表；文件不存在视作空表。"""
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()
            if line.strip()] if path.exists() else []


def get(records: list[dict], generator_id: str, version: str | None = None) -> dict:
    """按 id（可带 version）查注册记录；未注册抛 KeyError；缺省版本取最近注册。"""
    matched = [r for r in records if r["id"] == generator_id
               and (version is None or r["version"] == version)]
    if not matched:
        raise KeyError(f"生成器未注册: {generator_id}" + (f"@{version}" if version else ""))
    return matched[-1]


def register(path: Path, generator_id: str, version: str, seed_policy: str,
             outputs_manifest: str) -> dict:
    """追加注册一个生成器；(id, version) 重复时抛 DuplicateGeneratorError 且不落盘。"""
    if any(r["id"] == generator_id and r["version"] == version for r in load(path)):
        raise DuplicateGeneratorError(f"生成器已注册: {generator_id}@{version}")
    record = {"id": generator_id, "version": version, "seed_policy": seed_policy,
              "outputs_manifest": outputs_manifest}
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("a", encoding="utf-8") as fh:
        fh.write(json.dumps(record, ensure_ascii=False) + "\n")
    return record
