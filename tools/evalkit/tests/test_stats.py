"""evalkit.stats 测试：差分对照（独立推导的已知数学值）+ hypothesis 属性 + 边界。

参照值独立推导、不复用被测代码：泊松 rule-of-three 与 Garwood chi2 表值；CP 定义
方程（math.comb 直算 CDF）；Wilson 闭式手算锚定；kappa 教科书 2x2（κ=0.4）。
"""

import math

import pytest
from evalkit.stats import *
from hypothesis import given, settings
from hypothesis import strategies as st

PROPS = settings(max_examples=50, deadline=None)
finite = st.floats(min_value=-1e6, max_value=1e6, allow_nan=False, allow_infinity=False)

# ---- 差分对照：泊松（3/N 规则 + Garwood 表值 + 定义方程） ----
def test_poisson_rule_of_three():
    assert poisson_upper95(0, 6) == pytest.approx(-math.log(0.05) / 6, abs=1e-12)
    assert poisson_upper95(0, 6) <= 0.5
    assert poisson_upper95(0, 30) == pytest.approx(-math.log(0.05) / 30, abs=1e-12)
    assert poisson_upper95(0, 30) <= 0.1
    assert poisson_upper95(10, 1) == pytest.approx(16.962, abs=5e-3)  # chi2_{0.95,22}/2

@pytest.mark.parametrize("k,N", [(1, 1), (3, 6), (10, 1), (25, 30)])
def test_poisson_defining_equation(k, N):  # 独立差分：P(X<=k; λ_u·N)=0.05
    x = poisson_upper95(k, N) * N
    cdf = math.exp(-x) * sum(x**j / math.factorial(j) for j in range(k + 1))
    assert cdf == pytest.approx(0.05, abs=1e-8)

# ---- 差分对照：零失败 n 与二项（rule-of-three + CP 定义方程） ----
@pytest.mark.parametrize("q,n", [(0.95, 59), (0.98, 149), (0.99, 299)])
def test_zero_fail_n(q, n):
    assert zero_fail_n(q) >= n and zero_fail_n(q) == n  # ceil(ln0.05/lnq)
    assert q**zero_fail_n(q) <= 0.05 < q ** (n - 1)  # 定义：n 恰好充分
    assert binom_upper95(0, n) <= 1 - q  # 与二项上界交叉一致

def test_binom_rule_of_three():
    assert binom_upper95(0, 59) == pytest.approx(1 - 0.05 ** (1 / 59), abs=1e-10)
    assert binom_upper95(0, 59) <= 0.05
    assert binom_upper95(10, 10) == 1.0

@pytest.mark.parametrize("k,n", [(1, 5), (3, 20), (7, 30), (0, 299)])
def test_binom_clopper_pearson_equation(k, n):  # 独立差分：P(X<=k; p_u)=0.05
    p = binom_upper95(k, n)
    cdf = sum(math.comb(n, j) * p**j * (1 - p) ** (n - j) for j in range(k + 1))
    assert cdf == pytest.approx(0.05, abs=1e-8)

# ---- 差分对照：Wilson / bootstrap / 置换 / EER / 噪声带 / kappa ----
def test_wilson_known_values():
    lo, hi = wilson(0, 100)  # p̂(1-p̂)=0 使根式与常数项相消，下界解析上恰为 0
    assert lo == pytest.approx(0.0, abs=1e-12)
    assert hi == pytest.approx(0.0369935, abs=1e-4)
    lo_n, hi_n = wilson(100, 100)  # k=0/k=n 对称
    assert hi_n == pytest.approx(1.0, abs=1e-12)
    assert hi + lo_n == pytest.approx(1.0, abs=1e-9)
    lo5, hi5 = wilson(50, 100)  # 手算锚定（statsmodels 0.40383/0.59617）
    assert lo5 == pytest.approx(0.403830, abs=1e-3)
    assert hi5 == pytest.approx(0.596170, abs=1e-3)
    assert (lo5 + hi5) / 2 == pytest.approx(0.5, abs=1e-12)

def test_bootstrap_known_case_and_seed():
    a, b = [2.0, 3.0, 4.0, 5.0], [1.0, 2.0, 3.0, 4.0]  # 逐项差恒 1
    assert paired_bootstrap(a, b, iters=300, seed=7) == (1.0, (1.0, 1.0))
    x = [1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 7.0, 8.0]  # 有变化的数据才可比 CI
    y = [0.8, 1.9, 3.2, 3.9, 5.1, 6.4, 6.8, 8.3]
    assert paired_bootstrap(x, y, iters=500, seed=123) == \
        paired_bootstrap(x, y, iters=500, seed=123)  # 同 seed 逐位复现
    assert paired_bootstrap(x, y, iters=500, seed=1)[1] != \
        paired_bootstrap(x, y, iters=500, seed=2)[1]
    d, (lo, hi) = paired_bootstrap(a, a, iters=300, seed=0)
    assert d == 0.0 and lo <= 0.0 <= hi

def test_permutation_reference_values():
    a = [3.1, 4.2, 5.0, 6.3, 2.8]  # 两组相同分布 → p=1
    assert permutation_p(a, list(a)) == 1.0
    assert permutation_p([1, 2, 3], [3, 1, 2]) == 1.0
    p = permutation_p([10.0] * 4, [0.0] * 4)  # 极端差仅 2 种取法，精确枚举
    assert p == pytest.approx(2 / math.comb(8, 4), abs=1e-12)
    assert p <= 0.05

def test_eer_reference_values():
    scores = [0.98, 0.95, 0.92, 0.90, 0.30, 0.25, 0.12, 0.05]  # 完美分离
    assert eer(scores, [1, 1, 1, 1, 0, 0, 0, 0]) == (0.0, 0, 0)
    e, misses, fa = eer([0.9, 0.7, 0.5, 0.6, 0.4], [1, 1, 1, 0, 0])  # 手算交叉 1/3
    assert e == pytest.approx(1 / 3, abs=1e-12)
    assert (misses, fa) == (1, 1)
    assert eer([0.6, 0.5, 0.5, 0.4], [1, 1, 0, 0])[0] == pytest.approx(0.25, abs=1e-12)

def test_noise_band_known_values():
    mean, sigma = noise_band([1.0, 2.0, 3.0, 4.0, 5.0])
    assert mean == pytest.approx(3.0)
    assert sigma == pytest.approx(math.sqrt(2.5), abs=1e-12)  # 样本标准差
    assert noise_band([4.2]) == (4.2, 0.0)

def test_kappa_reference_values():
    r1 = ["yes"] * 20 + ["yes"] * 5 + ["no"] * 10 + ["no"] * 15  # 教科书 2x2
    r2 = ["yes"] * 20 + ["no"] * 5 + ["yes"] * 10 + ["no"] * 15  # po=0.7, pe=0.5
    assert cohens_kappa(r1, r2) == pytest.approx(0.4, abs=1e-12)
    perfect = ["a", "b", "a", "b", "c", "a", "c", "b"]  # 完全一致
    assert cohens_kappa(perfect, list(perfect)) == pytest.approx(1.0, abs=1e-12)
    assert cohens_kappa(["a", "b"], ["b", "a"]) == pytest.approx(-1.0, abs=1e-12)
    assert cohens_kappa(["a", "a", "b", "b"], ["a", "b", "a", "b"]) == \
        pytest.approx(0.0, abs=1e-12)  # 机会水平

# ---- 属性测试（hypothesis） ----
@pytest.mark.property
@PROPS
@given(data=st.data())
def test_binom_upper_in_unit_interval(data):
    n = data.draw(st.integers(1, 60))
    k = data.draw(st.integers(0, n))
    assert 0.0 <= binom_upper95(k, n) <= 1.0

@pytest.mark.property
@PROPS
@given(
    base=st.lists(finite, min_size=5, max_size=20),
    shift=st.floats(0.0, 50.0),
    seed=st.integers(0, 2**30),
)
def test_bootstrap_diff_monotone_in_shift(base, shift, seed):
    d0, _ = paired_bootstrap(base, list(base), iters=200, seed=seed)
    d1, _ = paired_bootstrap(base, [x - shift for x in base], iters=200, seed=seed)
    assert d1 >= d0  # 数据差异大 → diff 大
    assert d1 == pytest.approx(d0 + shift, abs=1e-6, rel=1e-6)

@pytest.mark.property
@PROPS
@given(base=st.lists(finite, min_size=3, max_size=15), seed=st.integers(0, 2**30))
def test_bootstrap_same_seed_same_result(base, seed):
    assert paired_bootstrap(base, base, iters=300, seed=seed) == \
        paired_bootstrap(base, list(base), iters=300, seed=seed)

@pytest.mark.property
@PROPS
@given(value=finite, length=st.integers(1, 50))
def test_noise_band_constant_sigma_zero(value, length):
    mean, sigma = noise_band([value] * length)
    assert sigma == 0.0
    assert mean == pytest.approx(value, abs=1e-6)

# ---- 边界：非法输入显式 ValueError ----
INVALID = [
    (zero_fail_n, (0.0,)), (zero_fail_n, (1.0,)), (zero_fail_n, (1.5,)),
    (poisson_upper95, (-1, 6)), (poisson_upper95, (0, 0)), (poisson_upper95, (0, -3)),
    (poisson_upper95, (1.5, 6)), (binom_upper95, (5, 3)), (binom_upper95, (-1, 3)),
    (binom_upper95, (0, 0)), (wilson, (3, 2)), (wilson, (-1, 5)), (wilson, (0, 0)),
    (paired_bootstrap, ([], [])), (paired_bootstrap, ([1.0], [1.0, 2.0])),
    (paired_bootstrap, ([1.0], [2.0], 0)), (permutation_p, ([], [1.0])),
    (permutation_p, ([1.0], [])), (eer, ([], [])), (eer, ([0.1, 0.2], [1, 0, 1])),
    (eer, ([0.1] * 3, [1] * 3)), (eer, ([0.1, 0.2], [1, 2])), (noise_band, ([],)),
    (cohens_kappa, ([], [])), (cohens_kappa, (["a"], ["a", "b"])),
]

@pytest.mark.parametrize("fn,args", INVALID)
def test_invalid_inputs_raise_value_error(fn, args):
    with pytest.raises(ValueError):
        fn(*args)
