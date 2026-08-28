"""synthgen CLI（spec §3.7）：批次目录（samples/train/holdout/manifest，8:2 切分）；退出码 0/2 输入错/20 单源>30%。"""

import argparse
import json
import random
import sys
from pathlib import Path

from . import distcheck, provenance, registry, split

EXIT_OK, EXIT_CONFIG, EXIT_VIOLATION = 0, 2, 20
REGISTRY_PATH, BATCHES_DIR = Path("datasets/synth/registry.jsonl"), Path("datasets/synth/batches")
# 桩词表（说话人/语速/主题）+ 上游模型池：6 模型均匀抽取 → 单源占比远低于 30%
SPEAKERS, SPEEDS = tuple(f"spk-{i:02d}" for i in range(1, 9)), ("slow", "normal", "fast")
TOPICS = ("bedtime", "play", "learning", "emotion", "safety")
DEFAULT_UPSTREAM = tuple(f"toy-llm-{c}" for c in "abcdef")


def _read_jsonl(path: Path) -> list[dict]:
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]


def _write_jsonl(path: Path, records: list[dict]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("".join(json.dumps(r, ensure_ascii=False) + "\n" for r in records), encoding="utf-8")


def cmd_register(args: argparse.Namespace) -> int:
    try:
        record = registry.register(REGISTRY_PATH, args.id, args.version,
                                   args.seed_policy, args.outputs_manifest)
    except registry.DuplicateGeneratorError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_CONFIG
    print(f"registered {record['id']}@{record['version']} -> {REGISTRY_PATH}")
    return EXIT_OK


def cmd_generate(args: argparse.Namespace) -> int:
    try:
        record = registry.get(registry.load(REGISTRY_PATH), args.id)
    except KeyError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_CONFIG
    rng = random.Random(args.seed)
    samples = [{"sample_id": f"{args.id}-{args.seed}-{i:06d}",
                "provenance": provenance.stamp(record["id"], record["version"], args.seed,
                                               rng.choice(DEFAULT_UPSTREAM)),
                "payload": {"speaker": rng.choice(SPEAKERS), "speed": rng.choice(SPEEDS),
                            "topic": rng.choice(TOPICS), "text": f"stub utterance {i}"}}
               for i in range(args.n)]
    by_id = {s["sample_id"]: s for s in samples}
    train_ids, holdout_ids = split.split_holdout(list(by_id), args.seed)
    batch_dir = BATCHES_DIR / f"{args.id}-{record['version']}-seed{args.seed}-n{args.n}"
    _write_jsonl(batch_dir / "samples.jsonl", samples)
    _write_jsonl(batch_dir / "synth-train.jsonl", [by_id[i] for i in train_ids])
    _write_jsonl(batch_dir / "synth-holdout.jsonl", [by_id[i] for i in holdout_ids])
    manifest = {"batch_id": batch_dir.name, "generator_id": record["id"],
                "generator_version": record["version"], "seed": args.seed, "n": len(samples),
                "train_n": len(train_ids), "holdout_n": len(holdout_ids)}
    (batch_dir / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
    print(f"batch {batch_dir.name}: n={len(samples)} train={len(train_ids)} "
          f"holdout={len(holdout_ids)} -> {batch_dir}")
    return EXIT_OK


def cmd_dist_check(args: argparse.Namespace) -> int:
    try:
        samples = _read_jsonl(BATCHES_DIR / args.batch / "samples.jsonl")
        if not samples:
            raise ValueError(f"空批次: {args.batch}")
        reference = _read_jsonl(Path(args.reference)) if args.reference else None
        report = distcheck.evaluate(samples, reference)
    except (OSError, ValueError, KeyError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_CONFIG
    print(f"batch {args.batch} n={report['n']}")
    for name, entry in report["fields"].items():
        js = f" js_ref={entry['js_distance_bits']:.3f}" if "js_distance_bits" in entry else ""
        print(f"{name:<8} entropy={entry['entropy_bits']:.3f}bit cats={entry['categories']}{js}")
    print(f"single-source share={report['single_source_share']:.2f} "
          f"(limit {distcheck.SINGLE_SOURCE_LIMIT:.2f}) {'OK' if report['ok'] else 'VIOLATION'}")
    return EXIT_OK if report["ok"] else EXIT_VIOLATION


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="synthgen", description="合成数据生成注册器（spec §3.7）")
    sub = parser.add_subparsers(dest="command", required=True)
    p = sub.add_parser("register", help="注册生成器（(id, version) 唯一）")
    p.add_argument("--id", required=True)
    p.add_argument("--version", required=True)
    p.add_argument("--seed-policy", required=True)
    p.add_argument("--outputs-manifest", required=True)
    p.set_defaults(func=cmd_register)
    p = sub.add_parser("generate", help="生成 N 条带溯源戳样本，8:2 切分并写 manifest")
    p.add_argument("--id", required=True, help="生成器 id（须已注册）")
    p.add_argument("--n", type=int, required=True, help="生成条数")
    p.add_argument("--seed", type=int, required=True, help="随机种子（同 seed 完全复现）")
    p.set_defaults(func=cmd_generate)
    p = sub.add_parser("dist-check", help="多样性：分布熵 + 参考集 JS 距离 + 单源占比门槛 0.30")
    p.add_argument("--batch", required=True, help="批次 id")
    p.add_argument("--reference", help="真实参考集 jsonl")
    p.set_defaults(func=cmd_dist_check)
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    return args.func(args)
