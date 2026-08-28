"""budgets.ledger —— 延迟趋势台账（spec §3.6）。

对每段取最近 N 份夜间报告（默认 30，即「近 30 天」）的 P95，在窗口内
（含最新值）计算均值 μ 与总体标准差 σ（statistics.pstdev）；最新值 > μ+2σ
判为劣化标红（组合级 G1 红 → exit 20）。σ=0 ⟺ 窗口全等值，此时最新值即
均值，不可能劣化，无需除零特判。

history 输入格式（单文件 history 数组方案，按时间升序，末尾为最新）：
    {
      "history": [
        {"commit": "a1b2c3", "timestamp": "2026-07-30T00:00:00Z",
         "segments": [{"id": "tail_silence", "p50": 450, "p95": 600}, ...]},
        ...
      ]
    }
每份报告即 budgets.check 的 latency.json 单报告对象；数组顺序即时间顺序。"""

from __future__ import annotations

import json
import statistics
from dataclasses import dataclass
from pathlib import Path

from .check import _is_number, fmt_ms


class HistoryError(ValueError):
    """history 文件不可读或不符合 schema。"""


def load_history(path: Path) -> list[dict]:
    if not path.is_file():
        raise HistoryError(f"history 文件不存在: {path}")
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise HistoryError(f"history 不可读: {exc}") from exc
    if not isinstance(data, dict) or not isinstance(data.get("history"), list):
        raise HistoryError('history 文件必须是 {"history": [报告, ...]}')
    for index, item in enumerate(data["history"]):
        if not isinstance(item, dict) or not isinstance(item.get("segments"), list):
            raise HistoryError(f"history[{index}] 缺 segments 列表")
    return data["history"]


@dataclass
class TrendRow:
    id: str
    n: int  # 窗口内出现次数
    mean: float
    sigma: float
    latest: float | None  # 最新一份报告中的 P95；该段缺席则为 None
    z: float  # (最新−μ)/σ；σ=0 或无最新值时为 0
    red: bool


def compute_trends(window: list[dict]) -> list[TrendRow]:
    """对窗口内每段计算 μ/σ/最新值/z，最新值 > μ+2σ 标红。"""
    if not window:
        return []
    order: list[str] = []
    series: dict[str, list[float]] = {}
    for item in window:
        seen: set[str] = set()
        for seg in item["segments"]:
            seg_id = seg.get("id") if isinstance(seg, dict) else None
            if not isinstance(seg_id, str) or not seg_id:
                raise HistoryError(f"segment 缺 id: {seg!r}")
            p95 = seg.get("p95") if isinstance(seg, dict) else None
            if seg_id in seen:
                raise HistoryError(f"单份报告内 segment id 重复: {seg_id}")
            if not _is_number(p95) or p95 < 0:
                raise HistoryError(f"segment {seg_id} 的 p95 须为非负数: {p95!r}")
            seen.add(seg_id)
            if seg_id not in series:
                series[seg_id] = []
                order.append(seg_id)
            series[seg_id].append(float(p95))

    latest_by_id = {seg.get("id"): seg.get("p95") for seg in window[-1]["segments"]
                    if isinstance(seg, dict)}
    rows: list[TrendRow] = []
    for seg_id in order:
        values = series[seg_id]
        mean, sigma = statistics.fmean(values), statistics.pstdev(values)
        latest = latest_by_id.get(seg_id)
        latest = float(latest) if _is_number(latest) else None
        z = (latest - mean) / sigma if latest is not None and sigma > 0 else 0.0
        rows.append(TrendRow(seg_id, len(values), mean, sigma, latest, z, z > 2.0))
    return rows


def format_trend_table(rows: list[TrendRow], days: int, n_reports: int) -> str:
    lines = [f"延迟台账趋势（近 {days} 天，{n_reports} 份报告）",
             f"{'段':<14}{'n':>4}{'均值P95':>10}{'σP95':>8}{'最新P95':>10}{'z':>7}  状态"]
    for row in rows:
        status = "红 ←劣化>2σ" if row.red else ("无最新值" if row.latest is None else "正常")
        lines.append(f"{row.id:<14}{row.n:>4}{fmt_ms(row.mean):>10}{fmt_ms(row.sigma):>8}"
                     f"{fmt_ms(row.latest):>10}{row.z:>7.2f}  {status}")
    red = [row.id for row in rows if row.red]
    if red:
        lines.append(f"标红段: {', '.join(red)}（最新值 > μ+2σ，无划拨说明 → 组合级 G1 红，"
                     f"进延迟负债表）")
    else:
        lines.append("无 >2σ 劣化段")
    return "\n".join(lines)
