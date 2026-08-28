"""evalkit.stats —— 门禁统计底座（spec §3.2）。全部精确方法、纯标准库，不做近似捷径。

poisson_upper95 / binom_upper95 为单侧 95% 上界（上尾 0.05，对应 §3.1 零事件
门禁的泊松 3/N 与二项 rule-of-three）；wilson / paired_bootstrap 为 95% 双侧。
"""

import math
import random
import statistics
from collections import Counter
from itertools import combinations

_ALPHA = 0.05
_Z975 = 1.959963984540054  # Φ⁻¹(0.975)
_PERM_EXACT_MAX = 20_000  # C(n,k) <= 该值时精确枚举，否则蒙特卡洛
_PERM_MC_DRAWS = 100_000

def _require(ok: bool, msg: str) -> None:
    if not ok:
        raise ValueError(msg)

def _require_nonempty(values, name: str) -> list:
    seq = list(values or ())
    _require(bool(seq), f"{name} 不能为空")
    return seq

def _check_count(value: object, name: str) -> int:
    _require(isinstance(value, int) and not isinstance(value, bool) and value >= 0,
             f"{name} 必须为非负整数: {value!r}")
    return value

def _check_kn(k: int, n: int) -> None:
    _check_count(k, "k")
    _check_count(n, "n")
    _require(k <= n and n >= 1, f"要求 0<=k<=n 且 n>=1: k={k}, n={n}")

def _check_positive(value: object, name: str) -> float:
    _require(isinstance(value, (int, float)) and not isinstance(value, bool)
             and math.isfinite(float(value)) and float(value) > 0.0,
             f"{name} 必须为正有限数: {value!r}")
    return float(value)

# ---- 正则化不完全伽马（Poisson Garwood 精确界） ----
def _reg_gamma_p(a: float, x: float, eps: float = 1e-15, itmax: int = 1000) -> float:
    # 正则化下不完全伽马 P(a,x)：级数（x < a+1 收敛快）
    ap, total, term = a, 1.0 / a, 1.0 / a
    for _ in range(itmax):
        ap += 1.0
        term *= x / ap
        total += term
        if abs(term) < abs(total) * eps:
            break
    return total * math.exp(-x + a * math.log(x) - math.lgamma(a))

def _reg_gamma_q_cf(a: float, x: float, eps: float = 1e-15, itmax: int = 1000) -> float:
    # 正则化上不完全伽马 Q(a,x)：修正 Lentz 连分式（x >= a+1 收敛快）
    b, c = x + 1.0 - a, 1e300
    d = 1e300 if b == 0.0 else 1.0 / b
    h = d
    for i in range(1, itmax + 1):
        an = -i * (i - a)
        b += 2.0
        d = an * d + b
        if abs(d) < 1e-300:
            d = 1e-300
        c = b + an / c
        if abs(c) < 1e-300:
            c = 1e-300
        d = 1.0 / d
        delta = d * c
        h *= delta
        if abs(delta - 1.0) < eps:
            break
    return math.exp(-x + a * math.log(x) - math.lgamma(a)) * h

def _reg_gamma_q(a: float, x: float) -> float:
    # Q(a,x) = Γ(a,x)/Γ(a)，a>0、x>=0，对 x 单调递减
    if x == 0.0:
        return 1.0
    return 1.0 - _reg_gamma_p(a, x) if x < a + 1.0 else _reg_gamma_q_cf(a, x)

def _gamma_q_quantile(a: float, p: float) -> float:
    # 二分求解 Q(a,x) = p
    lo, hi = 0.0, max(a, 1.0)
    while _reg_gamma_q(a, hi) > p:
        hi *= 2.0
    for _ in range(200):
        mid = 0.5 * (lo + hi)
        if _reg_gamma_q(a, mid) > p:
            lo = mid
        else:
            hi = mid
    return 0.5 * (lo + hi)

# ---- 公共 API ----
def zero_fail_n(q: float) -> int:
    """0 失败时以 95% 置信宣称成功率>=q 的最小 n：ceil(ln 0.05 / ln q)。"""
    _require(isinstance(q, (int, float)) and not isinstance(q, bool)
             and math.isfinite(float(q)) and 0.0 < float(q) < 1.0, f"q 必须在 (0,1) 内: {q!r}")
    return math.ceil(math.log(_ALPHA) / math.log(float(q)))

def poisson_upper95(k: int, N: float) -> float:
    """泊松比率 λ 的 Garwood 精确单侧 95% 上界（k=0 即 rule-of-three ≈ 3/N）。"""
    _check_count(k, "k")
    return _gamma_q_quantile(k + 1, _ALPHA) / _check_positive(N, "N")

def _binom_cdf(k: int, n: int, p: float) -> float:
    # P(X<=k)，X~Bin(n,p)；对数空间逐项求和，大 n 不下溢
    if p <= 0.0:
        return 1.0
    if p >= 1.0:
        return 1.0 if k >= n else 0.0
    lp, lq, lnf = math.log(p), math.log1p(-p), math.lgamma(n + 1)
    return min(1.0, math.fsum(
        math.exp(lnf - math.lgamma(j + 1) - math.lgamma(n - j + 1) + j * lp + (n - j) * lq)
        for j in range(k + 1)))

def binom_upper95(k: int, n: int) -> float:
    """二项比率 p 的 Clopper-Pearson 精确单侧 95% 上界（k=n 时为 1.0）。"""
    _check_kn(k, n)
    if k == n:
        return 1.0
    lo, hi = 0.0, 1.0
    for _ in range(200):  # CDF 对 p 单调递减，二分求解 P(X<=k;p)=0.05
        mid = 0.5 * (lo + hi)
        if _binom_cdf(k, n, mid) > _ALPHA:
            lo = mid
        else:
            hi = mid
    return 0.5 * (lo + hi)

def wilson(k: int, n: int) -> tuple[float, float]:
    """Wilson 得分区间（95% 双侧）；k=0 下界、k=n 上界解析上恰为 0/1。"""
    _check_kn(k, n)
    z2, phat = _Z975 * _Z975, k / n
    denom = 1.0 + z2 / n
    center = (phat + z2 / (2.0 * n)) / denom
    margin = (_Z975 / denom) * math.sqrt(phat * (1.0 - phat) / n + z2 / (4.0 * n * n))
    # 根式浮点残差 ~1ulp：端点取解析精确值而非截断
    lo = 0.0 if k == 0 else max(0.0, center - margin)
    hi = 1.0 if k == n else min(1.0, center + margin)
    return (lo, hi)

def _percentile(sorted_vals: list[float], q: float) -> float:
    # 线性插值分位数（numpy 默认定义），输入须已升序
    pos = q * (len(sorted_vals) - 1)
    lo_i, hi_i = math.floor(pos), math.ceil(pos)
    if lo_i == hi_i:
        return sorted_vals[lo_i]
    frac = pos - lo_i
    return sorted_vals[lo_i] * (1.0 - frac) + sorted_vals[hi_i] * frac

def paired_bootstrap(a, b, iters: int = 10000, seed: int | None = None):
    """配对 bootstrap：(mean(a)-mean(b), 95% 双侧 CI)；给定 seed 结果可复现。"""
    a = [float(x) for x in _require_nonempty(a, "a")]
    b = [float(x) for x in _require_nonempty(b, "b")]
    _require(len(a) == len(b), f"a 与 b 必须等长（配对数据）: {len(a)} vs {len(b)}")
    _require(isinstance(iters, int) and not isinstance(iters, bool) and iters >= 1,
             f"iters 必须为 >=1 的整数: {iters!r}")
    n, pairs, rng = len(a), list(zip(a, b)), random.Random(seed)
    dist = sorted(  # 对配对索引重采样，收集 mean 差
        (math.fsum(x for x, _ in s) - math.fsum(y for _, y in s)) / n
        for s in (rng.choices(pairs, k=n) for _ in range(iters)))
    diff = statistics.fmean(a) - statistics.fmean(b)
    return diff, (_percentile(dist, 0.025), _percentile(dist, 0.975))

def permutation_p(a, b) -> float:
    """两组均值差双侧置换检验 p 值；组合数<=2e4 精确枚举，否则 1e5 次蒙特卡洛。"""
    a = [float(x) for x in _require_nonempty(a, "a")]
    b = [float(x) for x in _require_nonempty(b, "b")]
    na, nb, pooled = len(a), len(b), a + b
    n, total_sum = len(pooled), math.fsum(a + b)
    obs_abs = abs(statistics.fmean(a) - statistics.fmean(b))
    tol = 1e-9 * (obs_abs + 1.0)
    if math.comb(n, na) <= _PERM_EXACT_MAX:
        hits = 0
        for idx in combinations(range(n), na):
            sum_a = math.fsum(pooled[i] for i in idx)
            hits += abs(sum_a / na - (total_sum - sum_a) / nb) >= obs_abs - tol
        return hits / math.comb(n, na)
    rng, hits = random.Random(), 1  # 观测排列自身计入，保证 p>=1/总数
    for _ in range(_PERM_MC_DRAWS):
        sh = rng.sample(pooled, n)
        hits += abs(statistics.fmean(sh[:na]) - statistics.fmean(sh[na:])) >= obs_abs - tol
    return hits / (_PERM_MC_DRAWS + 1)

def eer(scores, labels) -> tuple[float, int, int]:
    """等错误率：FAR/FRR 交叉点（相邻 ROC 点线性插值）及该处漏报/误报计数。"""
    _require(len(scores) == len(labels) and len(scores) > 0, "scores/labels 须非空且等长")
    pos, neg = [], []
    for s, lab in zip(scores, labels):
        lab = int(lab) if isinstance(lab, bool) else lab
        _require(isinstance(lab, int) and lab in (0, 1), f"labels 只允许 0/1: {lab!r}")
        (pos if lab == 1 else neg).append(float(s))
    _require(bool(pos) and bool(neg), "计算 EER 需要同时包含正负两类样本")
    events = sorted(  # 阈值从 +∞ 向下扫，并列分数整组处理
        [(s, True) for s in pos] + [(s, False) for s in neg],
        key=lambda t: t[0], reverse=True)
    pts, tp, fp, i = [], 0, 0, 0  # 每个离散阈值的 (far, frr, fp, fn)
    while i < len(events):
        s = events[i][0]
        while i < len(events) and events[i][0] == s:
            tp, fp, i = tp + events[i][1], fp + (not events[i][1]), i + 1
        pts.append((fp / len(neg), (len(pos) - tp) / len(pos), fp, len(pos) - tp))
    prev = (0.0, 1.0, 0, len(pos))  # 阈值 +∞：(far=0, frr=1)
    for pt in pts:
        if pt[0] == pt[1]:  # FAR 与 FRR 恰好相等
            return pt[0], pt[3], pt[2]
        if prev[1] > prev[0] and pt[1] < pt[0]:  # 交叉区间，线性插值
            u = (prev[1] - prev[0]) / ((prev[1] - pt[1]) + (pt[0] - prev[0]))
            best = prev if abs(prev[0] - prev[1]) <= abs(pt[0] - pt[1]) else pt
            return prev[0] + u * (pt[0] - prev[0]), best[3], best[2]
        prev = pt
    raise ArithmeticError("ROC 未找到 FAR/FRR 交叉点")  # 理论不可达（终点 (1,0)）

def noise_band(values) -> tuple[float, float]:
    """噪声带基线 (mean, sigma)：样本标准差，n=1 时 sigma=0。"""
    vals = [float(v) for v in _require_nonempty(values, "values")]
    mean = statistics.fmean(vals)
    return mean, statistics.stdev(vals) if len(vals) > 1 else 0.0

def cohens_kappa(r1, r2) -> float:
    """Cohen's kappa；pe=1（边际退化）时约定完全一致=1.0、否则 0.0。"""
    r1, r2 = _require_nonempty(r1, "r1"), _require_nonempty(r2, "r2")
    _require(len(r1) == len(r2), f"r1 与 r2 必须等长: {len(r1)} vs {len(r2)}")
    c1, c2, n = Counter(r1), Counter(r2), len(r1)
    po = sum(x == y for x, y in zip(r1, r2)) / n
    pe = sum(c1[cat] * c2[cat] for cat in set(c1) | set(c2)) / (n * n)
    if pe == 1.0:
        return 1.0 if po == 1.0 else 0.0
    return (po - pe) / (1.0 - pe)
