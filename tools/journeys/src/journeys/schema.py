"""黄金旅程剧本解析与校验（spec §3.5/§8.1）。
schema：id, tier(core|variant), persona{age,patience}, steps[...],
inject{interrupts, safety_events}, assertions[]。"""

from dataclasses import dataclass
from pathlib import Path

import yaml

TIERS = ("core", "variant")
ASSERTION_METRICS = ("completion_rate", "latency_p95_ms", "safety_events", "memory_hit_rate")
ASSERTION_OPS = (">=", "<=", ">", "<", "==")
REQUIRED_FIELDS = ("id", "tier", "persona", "steps", "inject", "assertions")

class SchemaError(ValueError):
    """剧本不符合 journeys schema。"""

@dataclass(frozen=True)
class JourneyScript:
    id: str
    tier: str
    persona: dict
    steps: list
    inject: dict
    assertions: tuple  # ((metric, op, value), ...)
    source: str = ""

    @classmethod
    def from_dict(cls, data, source=""):
        where = f" [{source}]" if source else ""
        _check(isinstance(data, dict), f"script must be a mapping{where}")
        for field in REQUIRED_FIELDS:
            _check(field in data, f"missing required field: {field}{where}")
        _check(isinstance(data["id"], str) and data["id"].strip(),
               f"id must be a non-empty string{where}")
        _check(data["tier"] in TIERS, f"tier must be one of {TIERS}, got {data['tier']!r}{where}")
        persona = _mapping(data["persona"], "persona", ("age", "patience"), where)
        inject = _mapping(data["inject"], "inject", ("interrupts", "safety_events"), where)
        for key in ("interrupts", "safety_events"):
            _check(isinstance(inject[key], list), f"inject.{key} must be a list{where}")
        _check(isinstance(data["steps"], list) and data["steps"],
               f"steps must be a non-empty list{where}")
        raw = data["assertions"]
        _check(isinstance(raw, list) and raw, f"assertions must be a non-empty list{where}")
        return cls(data["id"], data["tier"], dict(persona), list(data["steps"]),
                   {k: list(v) for k, v in inject.items()},
                   tuple(_assertion(item, where) for item in raw), source)

def _check(ok, message):
    if not ok:
        raise SchemaError(message)

def _mapping(value, name, keys, where):
    _check(isinstance(value, dict), f"{name} must be a mapping{where}")
    for key in keys:
        _check(key in value, f"{name} missing required field: {key}{where}")
    return value

def _assertion(item, where):
    _check(isinstance(item, dict), f"assertion must be a mapping{where}")
    for key in ("metric", "op", "value"):
        _check(key in item, f"assertion missing required field: {key}{where}")
    _check(item["metric"] in ASSERTION_METRICS, f"metric must be {ASSERTION_METRICS}{where}")
    _check(item["op"] in ASSERTION_OPS, f"op must be {ASSERTION_OPS}{where}")
    return (item["metric"], item["op"], item["value"])

def load_scripts(scripts_dir):
    """读取目录下全部 *.yaml 剧本（按文件名排序，报告顺序确定）。"""
    directory = Path(scripts_dir)
    _check(directory.is_dir(), f"scripts dir not found: {directory}")
    paths = sorted(directory.glob("*.yaml"))
    _check(bool(paths), f"no journey scripts (*.yaml) in {directory}")
    scripts = []
    for path in paths:
        try:
            data = yaml.safe_load(path.read_text(encoding="utf-8"))
        except yaml.YAMLError as exc:
            raise SchemaError(f"invalid YAML: {path} ({exc})") from exc
        scripts.append(JourneyScript.from_dict(data, source=path.name))
    return scripts
