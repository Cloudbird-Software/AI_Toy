"""holdoutctl —— 密封 holdout 客户端（spec §3.4）。纯标准库。
强制行为：作业只输出聚合指标；任何分片 n<5 的切片一律抑制输出（k-匿名）；
原始样本路径不出受控存储。"""

from __future__ import annotations

import argparse
import datetime as dt
import getpass
import hashlib
import hmac
import json
import os
import sys
from pathlib import Path
from typing import Mapping, Sequence

EXIT_OK = 0
EXIT_FAIL = 1
EXIT_NO_CREDENTIALS = 2

MIN_SLICE_N = 5
MANIFEST_RELPATH = Path("datasets/holdout/sealed-manifest.json")
AUDIT_RELPATH = Path("reports/holdout-audit.jsonl")
REQUIRED_HOLDOUT_ENV = (
    "HOLDOUT_ENVIRONMENT", "HOLDOUT_RUNNER_TOKEN", "HOLDOUT_STORAGE_URL", "HOLDOUT_SEAL_KEY",
)
_RAW_PATH_MARKERS = ("datasets/holdout/",)


def _utc_now_iso() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z")


def compute_seal(manifest: Mapping, key: str) -> str:
    """对去掉 signature 后的 canonical JSON 做 HMAC-SHA256。"""
    payload = {k: v for k, v in manifest.items() if k != "signature"}
    canonical = json.dumps(payload, sort_keys=True, separators=(",", ":")).encode()
    return hmac.new(key.encode(), canonical, hashlib.sha256).hexdigest()


def verify_seal(manifest_path: Path, key: str | None) -> tuple[bool, str, int]:
    """校验 sealed-manifest 签名与对象数。返回 (ok, message, object_count)。"""
    if not key:
        return False, "HOLDOUT_SEAL_KEY 未设置，无法校验签名", 0
    if not manifest_path.is_file():
        return False, f"sealed manifest 不存在: {manifest_path}", 0
    try:
        manifest = json.loads(manifest_path.read_text())
    except (OSError, json.JSONDecodeError) as exc:
        return False, f"sealed manifest 不可读: {exc}", 0
    if not isinstance(manifest, dict):
        return False, "manifest 结构非法", 0
    signature = manifest.get("signature")
    if not isinstance(signature, str) or len(signature) != 64:
        return False, "signature 缺失或格式非法", 0
    if not hmac.compare_digest(compute_seal(manifest, key), signature):
        return False, "签名不匹配", 0
    objects = manifest.get("objects")
    if not isinstance(objects, list) or not objects:
        return False, "objects 缺失或为空", 0
    if manifest.get("object_count") != len(objects):
        declared = manifest.get("object_count")
        return False, f"对象数不符: 声明 {declared} != 实际 {len(objects)}", len(objects)
    for obj in objects:
        path = obj.get("path") if isinstance(obj, dict) else None
        if not isinstance(path, str) or not path.startswith("datasets/holdout/"):
            return False, f"对象路径越出受控存储: {path!r}", len(objects)
    return True, f"seal verified: {len(objects)} objects", len(objects)


def apply_k_anonymity(slices: Mapping[str, float], k: int = MIN_SLICE_N) -> dict[str, float]:
    """任何分片 n<k 的切片一律抑制输出（k-匿名）。"""
    return {key: value for key, value in slices.items() if value >= k}


def aggregate_shards(shards: Sequence[Mapping[str, float]]) -> dict[str, float]:
    merged: dict[str, float] = {}
    for shard in shards:
        for key, value in shard.items():
            merged[key] = merged.get(key, 0) + value
    return merged


def redact_raw_paths(obj):
    """原始样本路径不出受控存储：序列化前剥除疑似路径字符串。"""
    if isinstance(obj, dict):
        return {key: redact_raw_paths(value) for key, value in obj.items()}
    if isinstance(obj, list):
        return [redact_raw_paths(value) for value in obj]
    if isinstance(obj, str) and any(marker in obj for marker in _RAW_PATH_MARKERS):
        return "[redacted]"
    return obj


def file_sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(65536), b""):
            digest.update(chunk)
    return digest.hexdigest()


def append_audit(audit_log: Path, *, suite: str, sha256: str,
                 actor: str | None = None, event: str = "audit") -> dict:
    """追加一行审计记录（谁/何时/哪个 suite/输出摘要哈希），不覆盖既有内容。"""
    row = {
        "timestamp": _utc_now_iso(),
        "actor": actor or os.environ.get("HOLDOUT_ACTOR") or getpass.getuser(),
        "event": event,
        "suite": suite,
        "sha256": sha256,
    }
    audit_log.parent.mkdir(parents=True, exist_ok=True)
    with audit_log.open("a", encoding="utf-8") as fh:
        fh.write(json.dumps(row, sort_keys=True) + "\n")
    return row


def cmd_verify_seal(args: argparse.Namespace) -> int:
    manifest = Path(args.manifest) if args.manifest else MANIFEST_RELPATH
    ok, message, _ = verify_seal(manifest, os.environ.get("HOLDOUT_SEAL_KEY"))
    if not ok:
        print(f"verify-seal FAILED: {message}", file=sys.stderr)
        return EXIT_FAIL
    print(f"OK: {message} ({manifest})")
    return EXIT_OK


def cmd_eval(args: argparse.Namespace) -> int:
    missing = [name for name in REQUIRED_HOLDOUT_ENV if not os.environ.get(name)]
    if missing or os.environ.get("HOLDOUT_ENVIRONMENT") != "holdout":
        detail = f"缺少凭据: {', '.join(missing)}" if missing else "HOLDOUT_ENVIRONMENT != holdout"
        print(f"eval 只能在 environment=holdout 的 runner 上运行（{detail}）", file=sys.stderr)
        return EXIT_NO_CREDENTIALS
    manifest = Path(args.manifest) if args.manifest else MANIFEST_RELPATH
    ok, message, _ = verify_seal(manifest, os.environ["HOLDOUT_SEAL_KEY"])
    if not ok:
        print(f"eval 中止，seal 校验失败: {message}", file=sys.stderr)
        return EXIT_FAIL
    try:
        shards = [json.loads(Path(p).read_text()) for p in args.shards]
    except (OSError, json.JSONDecodeError) as exc:
        print(f"eval 中止，分片不可读: {exc}", file=sys.stderr)
        return EXIT_FAIL
    merged = aggregate_shards(shards)
    kept = apply_k_anonymity(merged)
    out_dir = Path(args.out)
    out_dir.mkdir(parents=True, exist_ok=True)
    payload = redact_raw_paths({
        "suite": args.suite,
        "generated_at": _utc_now_iso(),
        "k_anonymity_min_n": MIN_SLICE_N,
        "metrics": kept,
        "suppressed_slices": len(merged) - len(kept),
        "source": "sealed-holdout",
    })
    out_file = out_dir / f"holdout-{args.suite}-metrics.json"
    out_file.write_text(json.dumps(payload, sort_keys=True, indent=2) + "\n")
    audit_log = Path(args.audit_log) if args.audit_log else AUDIT_RELPATH
    append_audit(audit_log, suite=args.suite, sha256=file_sha256(out_file), event="eval")
    print(f"eval {args.suite}: {len(kept)} 项聚合指标（抑制 {len(merged) - len(kept)} 个小分片）→ {out_file}")
    return EXIT_OK


def cmd_audit(args: argparse.Namespace) -> int:
    if args.artifact:
        sha256 = file_sha256(Path(args.artifact))
    else:
        seed = json.dumps({"suite": args.suite, "ts": _utc_now_iso()}, sort_keys=True).encode()
        sha256 = hashlib.sha256(seed).hexdigest()
    audit_log = Path(args.audit_log) if args.audit_log else AUDIT_RELPATH
    row = append_audit(audit_log, suite=args.suite, sha256=sha256, actor=args.actor)
    print(json.dumps(row, sort_keys=True))
    return EXIT_OK


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="holdoutctl", description="密封 holdout 客户端（spec §3.4）")
    sub = parser.add_subparsers(dest="command", required=True)

    p_seal = sub.add_parser("verify-seal", help="校验 sealed-manifest 签名与对象数")
    p_seal.add_argument("--manifest", default=None, help="默认 datasets/holdout/sealed-manifest.json")
    p_seal.set_defaults(func=cmd_verify_seal)

    p_eval = sub.add_parser("eval", help="在 environment=holdout 的 runner 上运行受控评测")
    p_eval.add_argument("--suite", required=True, help="例如 real-t4")
    p_eval.add_argument("--out", required=True, help="输出目录，例如 reports/nightly/")
    p_eval.add_argument("--shards", action="append", default=[],
                        help="分片计数 JSON（{slice: value}，可重复）；只输出聚合指标")
    p_eval.add_argument("--manifest", default=None)
    p_eval.add_argument("--audit-log", default=None)
    p_eval.set_defaults(func=cmd_eval)

    p_audit = sub.add_parser("audit", help="追加 reports/holdout-audit.jsonl")
    p_audit.add_argument("--suite", default="unspecified")
    p_audit.add_argument("--artifact", default=None, help="要记录摘要哈希的输出文件")
    p_audit.add_argument("--audit-log", default=None)
    p_audit.add_argument("--actor", default=None)
    p_audit.set_defaults(func=cmd_audit)
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    return args.func(args)
