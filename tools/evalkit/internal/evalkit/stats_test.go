package evalkit

import (
	"math"
	"testing"
	"testing/quick"
)

// 差分对照值独立推导，不复用被测代码：泊松 rule-of-three 与 Garwood χ² 表值、
// Clopper-Pearson 定义方程直算、Wilson 手算锚定（statsmodels）、kappa 教科书 2×2。

// combExact 测试侧独立直算 C(n,k)。
func combExact(n, k int) float64 {
	c := 1.0
	for i := 1; i <= k; i++ {
		c = c * float64(n-k+i) / float64(i)
	}
	return c
}

func TestPoissonUpper95(t *testing.T) { // rule-of-three、χ² 表值与定义方程 P(X≤k; λ_u·N)=0.05
	if got, want := PoissonUpper95(0, 6), -math.Log(0.05)/6; math.Abs(got-want) > 1e-12 || got > 0.5 {
		t.Errorf("PoissonUpper95(0,6)=%v, want ≈%v 且 ≤0.5", got, want)
	}
	if got, want := PoissonUpper95(0, 30), -math.Log(0.05)/30; math.Abs(got-want) > 1e-12 || got > 0.1 {
		t.Errorf("PoissonUpper95(0,30)=%v, want ≈%v 且 ≤0.1", got, want)
	}
	if got := PoissonUpper95(10, 1); math.Abs(got-16.962) > 5e-3 { // χ²_{0.95,22}/2
		t.Errorf("PoissonUpper95(10,1)=%v, want ≈16.962", got)
	}
	for _, tc := range [][2]int{{1, 1}, {3, 6}, {10, 1}, {25, 30}} {
		k, N := tc[0], tc[1]
		x := PoissonUpper95(k, N) * float64(N)
		cdf, term := 0.0, 1.0
		for j := 0; j <= k; j++ { // 直算 P(X≤k; x)
			cdf += term
			term *= x / float64(j+1)
		}
		cdf *= math.Exp(-x)
		if math.Abs(cdf-0.05) > 1e-8 {
			t.Errorf("P(X≤%d; %.6f)=%v, want 0.05", k, x, cdf)
		}
	}
}

func TestZeroFailN(t *testing.T) {
	for _, tc := range [][2]float64{{0.95, 59}, {0.98, 149}, {0.99, 299}} {
		q, want := tc[0], int(tc[1])
		if n := ZeroFailN(q); n != want { // ceil(ln0.05/lnq) 恰好充分且必要
			t.Errorf("ZeroFailN(%v)=%d, want %d", q, n, want)
		} else if math.Pow(q, float64(n)) > 0.05 || math.Pow(q, float64(n-1)) <= 0.05 {
			t.Errorf("ZeroFailN(%v)=%d 不满足 q^n≤0.05<q^(n-1)", q, n)
		} else if u := BinomUpper95(0, n); u > 1-q { // 与二项上界交叉一致
			t.Errorf("BinomUpper95(0,%d)=%v 超 1-q=%v", n, u, 1-q)
		}
	}
}

func TestBinomUpper95(t *testing.T) { // k=0 rule-of-three、k=n 为 1、Clopper-Pearson 定义方程 P(X≤k; p_u)=0.05
	if got, want := BinomUpper95(0, 59), 1-math.Pow(0.05, 1.0/59); math.Abs(got-want) > 1e-10 || got > 0.05 {
		t.Errorf("BinomUpper95(0,59)=%v, want ≈%v 且 ≤0.05", got, want)
	}
	if got := BinomUpper95(10, 10); got != 1.0 {
		t.Errorf("BinomUpper95(10,10)=%v, want 1.0", got)
	}
	for _, tc := range [][2]int{{1, 5}, {3, 20}, {7, 30}, {0, 299}} {
		k, n, p := tc[0], tc[1], BinomUpper95(tc[0], tc[1])
		cdf := 0.0
		for j := 0; j <= k; j++ { // 直算 P(X≤k; n, p)
			cdf += combExact(n, j) * math.Pow(p, float64(j)) * math.Pow(1-p, float64(n-j))
		}
		if math.Abs(cdf-0.05) > 1e-8 {
			t.Errorf("P(X≤%d; n=%d, p=%.6f)=%v, want 0.05", k, n, p, cdf)
		}
	}
}

func TestWilson(t *testing.T) { // k=0 下界解析恰为 0；statsmodels 锚定 0.40383/0.59617
	lo, hi := Wilson(0, 100)
	if lo != 0 || math.Abs(hi-0.0369935) > 1e-4 {
		t.Errorf("Wilson(0,100)=(%v,%v), want (0, ≈0.0369935)", lo, hi)
	}
	loN, hiN := Wilson(100, 100)
	if hiN != 1 || math.Abs(hi+loN-1) > 1e-9 {
		t.Errorf("Wilson(100,100)=(%v,%v), want (≈0.9630065, 1)", loN, hiN)
	}
	if lo5, hi5 := Wilson(50, 100); math.Abs(lo5-0.403830) > 1e-3 || math.Abs(hi5-0.596170) > 1e-3 {
		t.Errorf("Wilson(50,100)=(%v,%v), want ≈(0.40383, 0.59617)", lo5, hi5)
	}
}

func TestPairedBootstrap(t *testing.T) { // 逐项恒差 → CI 退化；不同 seed 抽样不同
	a, b := []float64{2, 3, 4, 5}, []float64{1, 2, 3, 4}
	if d, lo, hi := PairedBootstrap(a, b, 300, 7); d != 1 || lo != 1 || hi != 1 {
		t.Errorf("PairedBootstrap(恒差1)=(%v,%v,%v), want (1,1,1)", d, lo, hi)
	}
	if d, lo, hi := PairedBootstrap(a, a, 300, 0); d != 0 || lo > 0 || hi < 0 {
		t.Errorf("PairedBootstrap(a,a)=(%v,%v,%v), want diff=0 且 CI 含 0", d, lo, hi)
	}
	x := []float64{1, 2, 3, 4, 5, 6, 7, 8}
	y := []float64{0.8, 1.9, 3.2, 3.9, 5.1, 6.4, 6.8, 8.3}
	_, lo1, hi1 := PairedBootstrap(x, y, 500, 123)
	_, lo2, hi2 := PairedBootstrap(x, y, 500, 1)
	if lo1 == lo2 && hi1 == hi2 {
		t.Errorf("不同 seed 得到相同 CI (%v,%v)", lo1, hi1)
	}
}

func TestPermutationP(t *testing.T) { // 相同分布 p=1；C(8,4)=70 精确枚举 2/70；C(20,10)>2e4 走蒙特卡洛
	if p := PermutationP([]float64{3.1, 4.2, 5.0, 6.3, 2.8}, []float64{3.1, 4.2, 5.0, 6.3, 2.8}, 0); p != 1.0 {
		t.Errorf("PermutationP(a,a)=%v, want 1.0", p)
	}
	if p := PermutationP([]float64{1, 2, 3}, []float64{3, 1, 2}, 0); p != 1.0 {
		t.Errorf("PermutationP 同分布=%v, want 1.0", p)
	}
	if p, want := PermutationP([]float64{10, 10, 10, 10}, []float64{0, 0, 0, 0}, 0), 2.0/70.0; math.Abs(p-want) > 1e-12 || p > 0.05 {
		t.Errorf("PermutationP 极端差=%v, want ≈%v 且 ≤0.05", p, want)
	}
	if p := PermutationP([]float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
		[]float64{1.5, 2.5, 3.5, 4.5, 5.5, 6.5, 7.5, 8.5, 9.5, 10.5}, 42); p < 0.5 {
		t.Errorf("PermutationP 相近分布=%v, want ≈1", p)
	}
}

func TestEER(t *testing.T) { // 完美分离=0；手算交叉 1/3 与 0.25
	if e, m, fa := EER([]float64{0.98, 0.95, 0.92, 0.90, 0.30, 0.25, 0.12, 0.05},
		[]bool{true, true, true, true, false, false, false, false}); e != 0 || m != 0 || fa != 0 {
		t.Errorf("EER 完美分离=(%v,%d,%d), want (0,0,0)", e, m, fa)
	}
	if e, m, fa := EER([]float64{0.9, 0.7, 0.5, 0.6, 0.4}, []bool{true, true, true, false, false}); math.Abs(e-1.0/3) > 1e-12 || m != 1 || fa != 1 {
		t.Errorf("EER=(%v,%d,%d), want (1/3,1,1)", e, m, fa)
	}
	if e, _, _ := EER([]float64{0.6, 0.5, 0.5, 0.4}, []bool{true, true, false, false}); math.Abs(e-0.25) > 1e-12 {
		t.Errorf("EER=%v, want 0.25", e)
	}
}

func TestNoiseBand(t *testing.T) {
	if m, s := NoiseBand([]float64{1, 2, 3, 4, 5}); m != 3 || math.Abs(s-math.Sqrt(2.5)) > 1e-12 {
		t.Errorf("NoiseBand(1..5)=(%v,%v), want (3, √2.5)", m, s)
	}
	if m, s := NoiseBand([]float64{4.2}); m != 4.2 || s != 0 {
		t.Errorf("NoiseBand 单值=(%v,%v), want (4.2,0)", m, s)
	}
}

func TestCohensKappa(t *testing.T) { // 教科书 2×2：po=0.7、pe=0.5 → κ=0.4
	r1, r2 := make([]int, 0, 50), make([]int, 0, 50)
	for i := 0; i < 50; i++ { // 20 yes/yes、5 yes/no、10 no/yes、15 no/no
		a, b := 0, 0
		switch {
		case i < 20:
			a, b = 1, 1
		case i < 25:
			a, b = 1, 0
		case i < 35:
			a, b = 0, 1
		}
		r1, r2 = append(r1, a), append(r2, b)
	}
	if k := CohensKappa(r1, r2); math.Abs(k-0.4) > 1e-12 {
		t.Errorf("CohensKappa 教科书=%v, want 0.4", k)
	}
	for _, tc := range []struct {
		x, y []int
		want float64
	}{
		{[]int{1, 2, 1, 2, 3, 1, 3, 2}, []int{1, 2, 1, 2, 3, 1, 3, 2}, 1}, // 完全一致
		{[]int{1, 2}, []int{2, 1}, -1},                                    // 完全不一致
		{[]int{1, 1, 2, 2}, []int{1, 2, 1, 2}, 0},                         // 机会水平
	} {
		if k := CohensKappa(tc.x, tc.y); math.Abs(k-tc.want) > 1e-12 {
			t.Errorf("CohensKappa(%v,%v)=%v, want %v", tc.x, tc.y, k, tc.want)
		}
	}
}

// ---- 属性测试（testing/quick） ----

func TestQuickProperties(t *testing.T) { // 同 seed 逐位复现；常数序列 σ=0；BinomUpper95∈[0,1]
	a := []float64{1, 2, 3, 4, 5, 6, 7, 8}
	b := []float64{0.8, 1.9, 3.2, 3.9, 5.1, 6.4, 6.8, 8.3}
	if err := quick.Check(func(seed int64) bool {
		d1, lo1, hi1 := PairedBootstrap(a, b, 300, seed)
		d2, lo2, hi2 := PairedBootstrap(a, b, 300, seed)
		return d1 == d2 && lo1 == lo2 && hi1 == hi2
	}, nil); err != nil {
		t.Error(err)
	}
	if err := quick.Check(func(v float64, n32 int32) bool {
		n := int(n32) % 50
		if n < 0 {
			n += 50
		}
		vals := make([]float64, n+1)
		for i := range vals {
			vals[i] = v
		}
		m, s := NoiseBand(vals)
		return s == 0 && m == v
	}, nil); err != nil {
		t.Error(err)
	}
	if err := quick.Check(func(k32, n32 int32) bool {
		n := int(n32) % 60
		if n < 0 {
			n += 60
		}
		k := int(k32) % (n + 1)
		if k < 0 {
			k += n + 1
		}
		p := BinomUpper95(k, n+1)
		return 0 <= p && p <= 1
	}, nil); err != nil {
		t.Error(err)
	}
}

// ---- 边界：非法输入显式 panic（对应原 Python ValueError） ----

func TestInvalidInputsPanic(t *testing.T) {
	cases := []struct {
		name string
		call func()
	}{
		{"zero_fail_n q=0", func() { ZeroFailN(0) }},
		{"zero_fail_n q=1", func() { ZeroFailN(1) }},
		{"zero_fail_n q=1.5", func() { ZeroFailN(1.5) }},
		{"zero_fail_n q=NaN", func() { ZeroFailN(math.NaN()) }},
		{"poisson k<0", func() { PoissonUpper95(-1, 6) }},
		{"poisson N=0", func() { PoissonUpper95(0, 0) }},
		{"poisson N<0", func() { PoissonUpper95(0, -3) }},
		{"binom k>n", func() { BinomUpper95(5, 3) }},
		{"binom k<0", func() { BinomUpper95(-1, 3) }},
		{"binom n=0", func() { BinomUpper95(0, 0) }},
		{"wilson k>n", func() { Wilson(3, 2) }},
		{"wilson k<0", func() { Wilson(-1, 5) }},
		{"wilson n=0", func() { Wilson(0, 0) }},
		{"bootstrap 空", func() { PairedBootstrap(nil, nil, 10, 1) }},
		{"bootstrap 不等长", func() { PairedBootstrap([]float64{1}, []float64{1, 2}, 10, 1) }},
		{"bootstrap iters=0", func() { PairedBootstrap([]float64{1}, []float64{2}, 0, 1) }},
		{"bootstrap NaN", func() { PairedBootstrap([]float64{math.NaN()}, []float64{1}, 10, 1) }},
		{"permutation a 空", func() { PermutationP(nil, []float64{1}, 1) }},
		{"permutation b 空", func() { PermutationP([]float64{1}, nil, 1) }},
		{"eer 空", func() { EER(nil, nil) }},
		{"eer 不等长", func() { EER([]float64{0.1, 0.2}, []bool{true}) }},
		{"eer 单类", func() { EER([]float64{0.1, 0.2}, []bool{true, true}) }},
		{"noise_band 空", func() { NoiseBand(nil) }},
		{"kappa 空", func() { CohensKappa(nil, nil) }},
		{"kappa 不等长", func() { CohensKappa([]int{1}, []int{1, 2}) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: 期望 panic", tc.name)
				}
			}()
			tc.call()
		})
	}
}
