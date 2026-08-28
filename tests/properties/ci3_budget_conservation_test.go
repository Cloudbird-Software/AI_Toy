// 运行时镜像，实现落地后替换
// CI-3：Σ分段 P95 − 并行重叠 ≤ 1500；>2σ 劣化无划拨 → 红（spec §8.2）。
package properties

import (
	"math/rand"
	"testing"
	"testing/quick"
)

const BudgetLimit = 1500

// TestCI3_BudgetWithin1500 quick 属性：任何由（P95、Overlap）组成的合法分段列表，
// 其 ΣP95 − ΣOverlap 不会超过 sum(p95)；以及显式构造预算内样本通过。
// 注意：quick 生成可能给出违法大 P95，这里属性是 —— 若每段 P95≤某上限（如云端 5 段各 400ms
// 且合理重叠扣除），预算总额不应超过 1500ms。
func TestCI3_BudgetWithin1500(t *testing.T) {
	// 随机 1~5 段（P95 取 0~400ms 之间、Overlap ≤ P95/2），验证
	// 1) BudgetTotal ≥ 0（重叠不会把总额扣成负）
	// 2) 若 ΣP95 ≤ 1500 则即使扣完重叠也≤1500（重叠非负）
	prop := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))
		n := 1 + r.Intn(5)
		segs := make([]BudgetSegment, n)
		var sumP95 int
		for i := 0; i < n; i++ {
			p95 := r.Intn(401) // 0..400
			overlap := r.Intn(p95/2 + 1)
			segs[i] = BudgetSegment{Name: "s", P95: p95, Overlap: overlap}
			sumP95 += p95
		}
		total := BudgetTotal(segs)
		if total < 0 {
			return false
		}
		if sumP95 <= BudgetLimit && total > BudgetLimit {
			return false
		}
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 300}); err != nil {
		t.Errorf("CI-3 预算上限计算 失效: %v", err)
	}
}

// TestCI3_TotalExact 表驱动：分段预算总额精确值。
func TestCI3_TotalExact(t *testing.T) {
	cases := []struct {
		name string
		segs []BudgetSegment
		want int
	}{
		{
			name: "空分段=0",
			segs: nil,
			want: 0,
		},
		{
			name: "单段300无重叠",
			segs: []BudgetSegment{{"VAD", 300, 0}},
			want: 300,
		},
		{
			name: "典型云档四段（VAD+LLM+TTS首包+发送）含重叠",
			segs: []BudgetSegment{
				{"VAD", 200, 0},
				{"LLM", 800, 50},  // LLM 与 VAD 尾 50ms 并行
				{"TTS", 400, 200}, // TTS 首包与 LLM 后半并行
				{"NET", 200, 100}, // 发送重叠
			},
			want: (200 + 800 + 400 + 200) - (0 + 50 + 200 + 100),
		},
		{
			name: "1500刚好达标（无重叠）",
			segs: []BudgetSegment{{"a", 1000, 0}, {"b", 500, 0}},
			want: 1500,
		},
		{
			name: "理论P95超，但并行重叠充分扣除 → ≤1500",
			// 1600-200=1400
			segs: []BudgetSegment{{"a", 900, 100}, {"b", 700, 100}},
			want: 1400,
		},
		{
			name: "重叠>P95时被裁剪（不会扣成负数）",
			segs: []BudgetSegment{{"a", 200, 9999}},
			want: 0,
		},
		{
			name: "P95负数按0计",
			segs: []BudgetSegment{{"a", -50, 0}, {"b", 400, 0}},
			want: 400,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := BudgetTotal(c.segs)
			if got != c.want {
				t.Errorf("BudgetTotal=%d want %d", got, c.want)
			}
		})
	}
}

// TestCI3_TwoSigmaRedOnNoReallocation quick 属性：当前采样均值显著高于基线（>2σ）
// 且无划拨时，BudgetCheck 必须返回 BudgetRed。
func TestCI3_TwoSigmaRedOnNoReallocation(t *testing.T) {
	prop := func(seed int64) bool {
		r := rand.New(rand.NewSource(seed))
		// 构造基线：10 个样本，均值≈500 σ≈30
		base := make([]int, 10)
		for i := range base {
			base[i] = 470 + r.Intn(61) // 470..530
		}
		baseline := LatencySample{Values: base}
		baseMean, baseStd := baseline.MeanStd()
		// 当前：μ ≈ baseMean + 3σ
		shift := int(baseMean + 3*baseStd)
		if shift < 10 {
			shift = 10
		}
		cur := make([]int, 10)
		for i := range cur {
			cur[i] = shift - 5 + r.Intn(11)
		}
		current := LatencySample{Values: cur}
		// 无划拨 → 必须红
		if BudgetCheck(baseline, current, true) != BudgetRed {
			return false
		}
		// 有划拨 → 不应红
		if BudgetCheck(baseline, current, false) == BudgetRed {
			return false
		}
		// 正常范围内样本 → 不应红/黄
		norm := make([]int, 10)
		for i := range norm {
			norm[i] = int(baseMean) - 10 + r.Intn(21)
		}
		if BudgetCheck(baseline, LatencySample{Values: norm}, true) == BudgetRed {
			return false
		}
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("CI-3 >2σ 劣化无划拨→红 失效: %v", err)
	}
}

// TestCI3_BudgetStatusBoundary 表驱动：预算状态边界。
func TestCI3_BudgetStatusBoundary(t *testing.T) {
	cases := []struct {
		name     string
		baseline LatencySample
		current  LatencySample
		noAlloc  bool
		want     BudgetStatus
	}{
		{
			name:     "基线=当前（完全不劣化）→绿",
			baseline: LatencySample{[]int{500, 500, 500, 500}},
			current:  LatencySample{[]int{500, 500, 500, 500}},
			noAlloc:  true,
			want:     BudgetGreen,
		},
		{
			name:     "劣化4σ 无划拨→红",
			baseline: LatencySample{Values: []int{500, 500, 500, 500, 500, 500, 500, 500, 500, 500}},
			current:  LatencySample{Values: repeated(510, 100)}, // 偏移10，std=0时实际无穷倍σ
			noAlloc:  true,
			want:     BudgetRed,
		},
		{
			name:     "劣化4σ 有划拨→黄",
			baseline: LatencySample{Values: repeated(500, 10)},
			current:  LatencySample{Values: repeated(1000, 10)},
			noAlloc:  false,
			want:     BudgetYellow,
		},
		{
			name:     "空采样 →绿",
			baseline: LatencySample{},
			current:  LatencySample{},
			noAlloc:  true,
			want:     BudgetGreen,
		},
		{
			name:     "基线σ很大，当前轻微超→未到2σ→绿（noAlloc=true 不红）",
			baseline: LatencySample{Values: []int{100, 900, 100, 900, 100, 900, 100, 900}},
			// mean=500 std≈400；2σ=800；阈值=1300；current均值600<1300
			current: LatencySample{Values: []int{600, 600, 600, 600}},
			noAlloc: true,
			want:    BudgetGreen,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := BudgetCheck(c.baseline, c.current, c.noAlloc)
			if got != c.want {
				t.Errorf("BudgetCheck=%v want %v", got, c.want)
			}
		})
	}
}

// repeated 辅助：n 个 v 组成的切片。
func repeated(v, n int) []int {
	s := make([]int, n)
	for i := range s {
		s[i] = v
	}
	return s
}

// TestCI3_MeanStdTable 表驱动：MeanStd 边界（验证运行时镜像统计函数）。
func TestCI3_MeanStdTable(t *testing.T) {
	cases := []struct {
		name    string
		s       LatencySample
		wantAvg float64
		wantMax float64 // std 不超过的上限（用于自检验）
	}{
		{"全同值 std=0", LatencySample{Values: []int{7, 7, 7, 7}}, 7, 0.0001},
		{"{0,4} avg=2 std=2", LatencySample{Values: []int{0, 4}}, 2, 2.0001},
		{"空样本=0,0", LatencySample{}, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, s := c.s.MeanStd()
			if m != c.wantAvg {
				t.Errorf("mean=%v want %v", m, c.wantAvg)
			}
			if s > c.wantMax {
				t.Errorf("std=%v > 上限 %v", s, c.wantMax)
			}
		})
	}
}
