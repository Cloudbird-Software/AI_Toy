// Package evalkit implements the statistics library backing all gates (spec §3.2).
package evalkit

import (
	"math"
	"math/rand"
	"sort"
)

// 纪律：全部精确方法不做近似捷径；区间 95% 双侧、*Upper95 单侧上尾 0.05；非法输入一律 panic（对应原 Python ValueError）。

const (
	alpha                = 0.05
	z975                 = 1.959963984540054 // Φ⁻¹(0.975)
	permExactMax, permMC = 20000, 100000     // C(n,k)≤permExactMax 精确枚举，否则蒙特卡洛 permMC 次
)

func must(cond bool, msg string) {
	if !cond {
		panic("evalkit: " + msg)
	}
}
func checkKN(k, n int) { must(0 <= k && k <= n && n >= 1, "要求 0<=k<=n 且 n>=1") }

func checkData(v []float64, name string) {
	must(len(v) > 0, name+" 不能为空")
	for _, x := range v {
		must(!math.IsNaN(x) && !math.IsInf(x, 0), name+" 含非有限值")
	}
}
func mean(v []float64) (s float64) {
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}
func lgam(a float64) float64 { v, _ := math.Lgamma(a); return v }

// poissonCDF 为 P(X≤k)，X~Poisson(x)；对数空间逐项求和，大 x 不下溢。
func poissonCDF(k int, x float64) float64 {
	if x == 0 {
		return 1
	}
	total := 0.0
	for j := 0; j <= k; j++ {
		total += math.Exp(-x + float64(j)*math.Log(x) - lgam(float64(j+1)))
	}
	return math.Min(1, total)
}

// ZeroFailN 返回 0 失败时以 95% 置信宣称成功率 ≥q 的最小 n：ceil(ln 0.05/ln q)；q∉(0,1) 时 panic。
func ZeroFailN(q float64) int {
	must(q > 0 && q < 1, "q 须在 (0,1) 内")
	return int(math.Ceil(math.Log(alpha) / math.Log(q)))
}

// PoissonUpper95 返回泊松比率的 Garwood 精确单侧 95% 上界：二分求 P(X≤k; λ_u·N)=0.05；k=0 即 3/N 规则。
func PoissonUpper95(k, N int) float64 {
	must(k >= 0 && N > 0, "要求 k>=0 且 N>0")
	lo, hi := 0.0, float64(k)+1 // CDF 对 λ 单调递减
	for poissonCDF(k, hi) > alpha {
		hi *= 2
	}
	for i := 0; i < 200; i++ {
		if mid := (lo + hi) / 2; poissonCDF(k, mid) > alpha {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2 / float64(N)
}

// binomCDF 为 P(X≤k)，X~Bin(n,p)；对数空间逐项求和，大 n 不下溢。
func binomCDF(k, n int, p float64) float64 {
	if p <= 0 {
		return 1
	}
	if p >= 1 {
		if k >= n {
			return 1
		}
		return 0
	}
	lp, lq, lnf := math.Log(p), math.Log1p(-p), lgam(float64(n+1))
	total := 0.0
	for j := 0; j <= k; j++ {
		total += math.Exp(lnf - lgam(float64(j+1)) - lgam(float64(n-j+1)) + float64(j)*lp + float64(n-j)*lq)
	}
	return math.Min(1, total)
}

// BinomUpper95 返回二项比率的 Clopper-Pearson 精确单侧 95% 上界（k=n 时为 1）。
func BinomUpper95(k, n int) float64 {
	checkKN(k, n)
	if k == n {
		return 1
	}
	lo, hi := 0.0, 1.0 // CDF 对 p 单调递减，二分求根
	for i := 0; i < 200; i++ {
		if mid := (lo + hi) / 2; binomCDF(k, n, mid) > alpha {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}

// Wilson 返回 k/n 的 Wilson 得分 95% 双侧区间；k=0 下界与 k=n 上界解析上恰为 0/1。
func Wilson(k, n int) (lo, hi float64) {
	checkKN(k, n)
	nf, z2 := float64(n), z975*z975
	denom := 1 + z2/nf
	center := (float64(k)/nf + z2/(2*nf)) / denom
	margin := z975 / denom * math.Sqrt(float64(k)/nf*(1-float64(k)/nf)/nf+z2/(4*nf*nf))
	lo, hi = center-margin, center+margin
	if k == 0 {
		lo = 0 // 根式浮点残差 ~1ulp：端点取解析精确值
	}
	if k == n {
		hi = 1
	}
	return math.Max(0, lo), math.Min(1, hi)
}

// percentile 为线性插值分位数（numpy 默认定义），输入须已升序。
func percentile(sorted []float64, q float64) float64 {
	pos := q * float64(len(sorted)-1)
	loI, hiI := int(math.Floor(pos)), int(math.Ceil(pos))
	if loI == hiI {
		return sorted[loI]
	}
	frac := pos - float64(loI)
	return sorted[loI]*(1-frac) + sorted[hiI]*frac
}

// PairedBootstrap 对配对索引重采样 iters 次，返回 (mean(a)−mean(b), 95% 双侧 CI)；同 (a,b,iters,seed) 逐位复现。
func PairedBootstrap(a, b []float64, iters int, seed int64) (diff, lo, hi float64) {
	checkData(a, "a")
	checkData(b, "b")
	must(len(a) == len(b) && iters >= 1, "要求 a、b 等长（配对）且 iters>=1")
	n, rng := len(a), rand.New(rand.NewSource(seed))
	dist := make([]float64, iters)
	for i := range dist {
		s := 0.0
		for j := 0; j < n; j++ {
			idx := rng.Intn(n)
			s += a[idx] - b[idx]
		}
		dist[i] = s / float64(n)
	}
	sort.Float64s(dist)
	return mean(a) - mean(b), percentile(dist, 0.025), percentile(dist, 0.975)
}

// combAtMost 饱和计算 C(n,k)：超过 limit 返回 limit+1（无溢出）。
func combAtMost(n, k int, limit int64) int64 {
	if k > n-k {
		k = n - k
	}
	c := int64(1)
	for i := 1; i <= k; i++ {
		if c = c * int64(n-k+i) / int64(i); c > limit {
			return limit + 1
		}
	}
	return c
}

// nextCombo 将 idx 原地推进为 [0,n) 的下一组合（字典序）；到末尾返回 false。
func nextCombo(idx []int, n int) bool {
	i := len(idx) - 1
	for i >= 0 && idx[i] == n-len(idx)+i {
		i--
	}
	if i < 0 {
		return false
	}
	idx[i]++
	for j := i + 1; j < len(idx); j++ {
		idx[j] = idx[j-1] + 1
	}
	return true
}

// PermutationP 返回两组均值差的双侧置换检验 p 值：C(n_a+n_b,n_a)≤2e4 精确枚举，否则以 seed 做 1e5 次随机置换。
func PermutationP(a, b []float64, seed int64) float64 {
	checkData(a, "a")
	checkData(b, "b")
	na, nb, n := len(a), len(b), len(a)+len(b)
	pooled := append(append([]float64{}, a...), b...)
	totalSum := 0.0
	for _, v := range pooled {
		totalSum += v
	}
	obsAbs := math.Abs(mean(a) - mean(b))
	tol := 1e-9 * (obsAbs + 1)
	hit := func(sumA float64) bool { return math.Abs(sumA/float64(na)-(totalSum-sumA)/float64(nb)) >= obsAbs-tol }
	if total := combAtMost(n, na, permExactMax); total <= permExactMax {
		idx := make([]int, na)
		for i := range idx {
			idx[i] = i
		}
		hits := 0
		for {
			sumA := 0.0
			for _, i := range idx {
				sumA += pooled[i]
			}
			if hit(sumA) {
				hits++
			}
			if !nextCombo(idx, n) {
				break
			}
		}
		return float64(hits) / float64(total)
	}
	rng, perm := rand.New(rand.NewSource(seed)), append([]float64{}, pooled...)
	hits := 1 // 观测排列自身计入，保证 p ≥ 1/总数
	for i := 0; i < permMC; i++ {
		rng.Shuffle(n, func(x, y int) { perm[x], perm[y] = perm[y], perm[x] })
		sumA := 0.0
		for _, v := range perm[:na] {
			sumA += v
		}
		if hit(sumA) {
			hits++
		}
	}
	return float64(hits) / float64(permMC+1)
}

// EER 返回等错误率（FAR/FRR 交叉点，相邻 ROC 点线性插值）与交叉区间内距等错线最近工作点的漏报/误报数。
func EER(scores []float64, labels []bool) (eer float64, misses, falseAlarms int) {
	must(len(scores) == len(labels) && len(scores) > 0, "scores/labels 须非空且等长")
	nPos, nNeg := 0, 0
	for i, s := range scores {
		must(!math.IsNaN(s) && !math.IsInf(s, 0), "scores 含非有限值")
		if labels[i] {
			nPos++
		} else {
			nNeg++
		}
	}
	must(nPos > 0 && nNeg > 0, "计算 EER 需同时包含正负两类样本")
	idx := make([]int, len(scores))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(x, y int) bool { return scores[idx[x]] > scores[idx[y]] }) // 阈值从 +∞ 向下扫
	pFar, pFrr, pFN, pFP := 0.0, 1.0, 0, nPos                                       // 上一工作点 (FAR,FRR,FP,FN)
	tp, fp := 0, 0
	for i := 0; i < len(idx); {
		j := i
		for j < len(idx) && scores[idx[j]] == scores[idx[i]] { // 并列分数整组处理
			if labels[idx[j]] {
				tp++
			} else {
				fp++
			}
			j++
		}
		i = j
		far, frr, fn := float64(fp)/float64(nNeg), float64(nPos-tp)/float64(nPos), nPos-tp
		if far == frr {
			return far, fn, fp
		}
		if pFrr > pFar && frr < far { // 交叉区间，线性插值
			u := (pFrr - pFar) / (pFrr - frr + far - pFar)
			eer = pFar + u*(far-pFar)
			if math.Abs(far-frr) < math.Abs(pFar-pFrr) {
				return eer, fn, fp
			}
			return eer, pFN, pFP
		}
		pFar, pFrr, pFN, pFP = far, frr, fn, fp
	}
	panic("evalkit: ROC 未找到 FAR/FRR 交叉点") // 理论不可达（终点 (1,0)）
}

// NoiseBand 返回基线 (mean, sigma)：样本标准差（n−1 分母），n=1 时 sigma=0；以首元素平移求和，常数序列逐位精确。
func NoiseBand(values []float64) (meanVal, sigma float64) {
	checkData(values, "values")
	v0 := values[0]
	sd := 0.0
	for _, v := range values {
		sd += v - v0
	}
	meanVal = v0 + sd/float64(len(values))
	if len(values) == 1 {
		return meanVal, 0
	}
	ss := 0.0
	for _, v := range values {
		d := (v - v0) - (meanVal - v0)
		ss += d * d
	}
	return meanVal, math.Sqrt(ss / float64(len(values)-1))
}

// CohensKappa 返回两位评分者的 Cohen's κ；边际退化（pe=1）时约定完全一致=1.0、否则=0.0。
func CohensKappa(r1, r2 []int) float64 {
	must(len(r1) > 0 && len(r2) > 0 && len(r1) == len(r2), "r1/r2 须非空且等长")
	n, c1, c2, agree := len(r1), map[int]int{}, map[int]int{}, 0
	for i := range r1 {
		c1[r1[i]]++
		c2[r2[i]]++
		if r1[i] == r2[i] {
			agree++
		}
	}
	po, pe := float64(agree)/float64(n), 0.0
	for cat, c := range c1 {
		pe += float64(c * c2[cat])
	}
	pe /= float64(n * n)
	if pe == 1 {
		if po == 1 {
			return 1
		}
		return 0
	}
	return (po - pe) / (1 - pe)
}
