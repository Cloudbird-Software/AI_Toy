"""budgets.check —— 守恒校验与延迟负债表（spec §3.6）。

守恒律：ΣP95 − 并行重叠 ≤ total_p95_budget（基准 configs/budgets/latency.yaml，
当前 1500ms）；违反 → exit 20，并输出「延迟负债表」：各段实际值 vs 预算值、
差值、超标段（只认 P95，p50 不参与守恒计算）。

latency.json 输入格式（单份夜间报告，JSON 对象）：
    {
      "commit": "a1b2c3",                     # 产生该报告的 commit（展示用）
      "timestamp": "2026-08-28T00:00:00Z",   # ISO-8601（展示用）
      "overlap_ms": 50,                       # 可选：并行段重叠（默认 0，非负）
      "segments": [                           # 段 id 须与预算配置一致
        {"id": "tail_silence", "p50": 450, "p95": 600},
        ...
      ]
    }

latency.yaml 是受控扁平子集（顶层标量 + 内联 mapping 列表，值不含逗号/花括号），
用行级极简解析器读取，避免为固定 schema 引入 PyYAML 依赖。"""

from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path


class BudgetInputError(ValueError):
    """预算配置或延迟报告不可读/不符合 schema。"""


def _is_number(value) -> bool:
    return isinstance(value, (int, float)) and not isinstance(value, bool)


def fmt_ms(value: float | None) -> str:
    """毫秒展示：整数去小数点，非整数保留 1 位，缺失用 —。"""
    if value is None:
        return "—"
    return str(int(value)) if float(value).is_integer() else f"{value:.1f}"


def _scalar(token: str):
    token = token.strip().strip("'\"")
    if token in ("", "~", "null") or token.lower() in ("nan", "inf", "-inf", "infinity"):
        return None
    for cast in (int, float):
        try:
            return cast(token)
        except ValueError:
            pass
    return {"true": True, "false": False}.get(token, token)


@dataclass(frozen=True)
class SegmentBudget:
    id: str
    p95: float


@dataclass(frozen=True)
class BudgetConfig:
    total_p95_budget: float
    segments: tuple[SegmentBudget, ...]


def load_config(path: Path) -> BudgetConfig:
    """解析 latency.yaml（total_p95_budget + segments 内联 mapping 列表）。"""
    if not path.is_file():
        raise BudgetInputError(f"预算配置不存在: {path}")
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as exc:
        raise BudgetInputError(f"预算配置不可读: {exc}") from exc

    total, segments, in_segments = None, [], False
    for raw in lines:
        stripped = raw.strip()
        if not stripped or stripped.startswith("#"):
            continue
        if raw[:1] not in (" ", "\t"):  # 顶层键
            key, sep, value = raw.partition(":")
            in_segments = bool(sep) and key.strip() == "segments"
            if sep and key.strip() == "total_p95_budget":
                total = _scalar(value)
            continue
        if not (in_segments and stripped.startswith("- ")):
            continue  # rules 等其它列表项不参与计算
        item = stripped[2:].strip()
        if not (item.startswith("{") and item.endswith("}")):
            continue
        mapping = {}
        for part in item[1:-1].split(","):
            key, sep, value = part.partition(":")
            if sep and key.strip():
                mapping[key.strip()] = _scalar(value)
        seg_id, p95 = mapping.get("id"), mapping.get("p95")
        if not isinstance(seg_id, str) or not seg_id:
            raise BudgetInputError(f"segment 缺 id: {mapping}")
        if not _is_number(p95) or p95 < 0:
            raise BudgetInputError(f"segment {seg_id} 的 p95 须为非负数: {p95!r}")
        segments.append(SegmentBudget(seg_id, p95))

    if not _is_number(total) or total < 0:
        raise BudgetInputError(f"total_p95_budget 须为非负数: {total!r}")
    if not segments:
        raise BudgetInputError("segments 为空")
    return BudgetConfig(total, tuple(segments))


def load_report(path: Path) -> dict:
    if not path.is_file():
        raise BudgetInputError(f"延迟报告不存在: {path}")
    try:
        report = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise BudgetInputError(f"延迟报告不可读: {exc}") from exc
    if not isinstance(report, dict):
        raise BudgetInputError("延迟报告必须是 JSON 对象")
    return report


@dataclass
class SegmentRow:
    id: str
    budget_p95: float
    actual_p95: float
    delta: float
    status: str  # 正常 | 超标


@dataclass
class CheckResult:
    commit: str
    total_actual: float  # ΣP95
    overlap_ms: float
    effective_ms: float  # ΣP95 − 并行重叠
    budget_ms: float
    ok: bool
    over_by_ms: float
    rows: list[SegmentRow]

    @property
    def over_segments(self) -> list[str]:
        return [row.id for row in self.rows if row.status == "超标"]


def evaluate(report: dict, config: BudgetConfig) -> CheckResult:
    segments = report.get("segments")
    if not isinstance(segments, list) or not segments:
        raise BudgetInputError("报告缺 segments 列表")
    actual: dict[str, float] = {}
    for seg in segments:
        seg_id = seg.get("id") if isinstance(seg, dict) else None
        if not isinstance(seg_id, str) or not seg_id:
            raise BudgetInputError(f"segment 缺 id: {seg!r}")
        p95 = seg.get("p95") if isinstance(seg, dict) else None
        if seg_id in actual:
            raise BudgetInputError(f"segment id 重复: {seg_id}")
        if not _is_number(p95) or p95 < 0:
            raise BudgetInputError(f"segment {seg_id} 的 p95 须为非负数: {p95!r}")
        actual[seg_id] = float(p95)

    budget_by_id = {seg.id: seg.p95 for seg in config.segments}
    missing = [seg_id for seg_id in budget_by_id if seg_id not in actual]
    unknown = [seg_id for seg_id in actual if seg_id not in budget_by_id]
    if missing or unknown:
        raise BudgetInputError(f"报告段与预算配置不一致: 缺 {missing or '无'}，多 {unknown or '无'}")
    overlap = report.get("overlap_ms", 0)
    if not _is_number(overlap) or overlap < 0:
        raise BudgetInputError(f"overlap_ms 须为非负数: {overlap!r}")

    rows = []
    for seg in config.segments:
        delta = actual[seg.id] - seg.p95
        rows.append(SegmentRow(seg.id, seg.p95, actual[seg.id], delta,
                               "超标" if delta > 0 else "正常"))

    total_actual = sum(actual.values())
    effective = total_actual - float(overlap)
    ok = effective <= config.total_p95_budget
    return CheckResult(
        commit=str(report.get("commit", "?")),
        total_actual=total_actual,
        overlap_ms=float(overlap),
        effective_ms=effective,
        budget_ms=config.total_p95_budget,
        ok=ok,
        over_by_ms=max(0.0, effective - config.total_p95_budget),
        rows=rows,
    )


def format_debt_table(result: CheckResult, report_path: str = "") -> str:
    where = f"，报告={report_path}" if report_path else ""
    lines = [f"延迟负债表（commit={result.commit}{where}）",
             f"{'段':<14}{'预算P95':>10}{'实际P95':>10}{'差值':>8}  状态"]
    for row in result.rows:
        delta = ("+" if row.delta > 0 else "") + fmt_ms(row.delta)
        lines.append(f"{row.id:<14}{fmt_ms(row.budget_p95):>10}{fmt_ms(row.actual_p95):>10}"
                     f"{delta:>8}  {row.status}")
    lines.append("─" * 58)
    lines.append(f"ΣP95={fmt_ms(result.total_actual)} 并行重叠={fmt_ms(result.overlap_ms)}"
                 f" 有效总延迟={fmt_ms(result.effective_ms)} 总预算={fmt_ms(result.budget_ms)}"
                 f" 超支={fmt_ms(result.over_by_ms)}")
    if result.over_segments:
        lines.append(f"超标段: {', '.join(result.over_segments)}")
        lines.append(f"守恒校验违反：ΣP95−并行重叠={fmt_ms(result.effective_ms)}ms > 总预算"
                     f"{fmt_ms(result.budget_ms)}ms，超支 {fmt_ms(result.over_by_ms)}ms")
    else:
        lines.append("守恒校验通过：ΣP95−并行重叠 ≤ 总预算")
    return "\n".join(lines)
