// T7 门禁测试（m2-spec §10 Mark 接线策略表，IR #92）：一 ID 一顶层测试函数，
// 口径与样本量声明唯一来源 configs/gates/T7.yaml（本文件只落断言本体）。
// verdict 总表：G1-01/G1-02/G1-03/G1-04 真实；G0-01 debt——T9 联跑面未建
// （safety 包 IR #91 未合入，卡序 #92 联跑依赖 #91，m2-spec §11），情绪状态
// 动力学逻辑面已全情绪网格×对抗事件流真实断言 0 越界（KWS 同款「先真跑
// 逻辑面再 Skipf 数据面」）。统计断言走 tools/evalkit（Wilson 95%CI），不手算。
package emotion

import (
	"math"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/Cloudbird-Software/AI_Toy/tools/evalkit/evalkit"
	"github.com/Cloudbird-Software/AI_Toy/tools/gaterunner/gaterunner"
)

// gateDirectionSign 人工方向表 fixture（T7-G1-01 人工口径）：事件→三维期望
// 符号（+1 上/−1 下/0 无约束）。儿童情绪语义人工标注——独立于 DefaultConfig
// 规则数值（一致率=OCC 规则对人工方向表的对齐度，非规则自证）。
var gateDirectionSign = map[Kind][3]int{
	Praise:      {+1, +1, +1},
	Criticize:   {-1, +1, -1},
	Hug:         {+1, -1, +1},
	ToySnatched: {-1, +1, -1},
	Alone:       {-1, -1, -1},
	Greet:       {+1, +1, +1},
	Play:        {+1, +1, +1},
	Scared:      {-1, +1, 0},
	Soothed:     {+1, -1, +1},
	Bored:       {-1, -1, -1},
}

// TestT7G101DirectionAccuracy T7-G1-01（BI-7.1/G1，真实）：情绪方向正确性——
// 300 合成情绪事件（10 类×30 强度阶梯，确定性 fixture）经 OCC 引擎（事件间
// 30s 衰减交错的会话节律），事件前后状态位移对人工方向表逐维符号一致 ≥85%
// （夹紧边界允许零位移、反向即违规）。统计面走 evalkit Wilson 95%CI。
func TestT7G101DirectionAccuracy(t *testing.T) {
	gaterunner.Mark(t, "T7", "BI-7.1", "T7-G1-01", "G1")
	e := mustEngine(t, DefaultConfig())
	kinds := []Kind{Praise, Criticize, Hug, ToySnatched, Alone, Greet, Play, Scared, Soothed, Bored}
	consistent, total, tMs := 0, 0, int64(0)
	for _, k := range kinds {
		for j := 0; j < 30; j++ {
			i := 0.05 + 0.95*float64(j)/29
			tMs += 30_000 // 事件间隔 30s（多轮会话节律，含衰减交错）
			e.DecayTo(tMs)
			before := e.State()
			after := e.OnEvent(Event{K: k, Intensity: i})
			total++
			want := gateDirectionSign[k]
			if signOK(before.Valence, after.Valence, want[0]) &&
				signOK(before.Arousal, after.Arousal, want[1]) &&
				signOK(before.Closeness, after.Closeness, want[2]) {
				consistent++
			}
		}
	}
	if total != 300 {
		t.Fatalf("样本量 %d ≠ 300（yaml min_evidence n:300）", total)
	}
	rate := float64(consistent) / float64(total)
	lo, hi := evalkit.Wilson(consistent, total)
	if rate < 0.85 {
		t.Fatalf("emotion_direction_accuracy=%.4f < 0.85（Wilson 95%%CI [%.4f,%.4f]，n=%d）", rate, lo, hi, total)
	}
	t.Logf("T7-G1-01：方向一致率 %.4f（一致 %d/%d，Wilson 95%%CI [%.4f,%.4f]）", rate, consistent, total, lo, hi)
}

// signOK 单维方向判定：期望符号 s=+1 位移须 ≥0、s=−1 须 ≤0、0 无约束；
// 夹紧边界零位移合法、反向位移违规。
func signOK(before, after float64, s int) bool {
	switch {
	case s > 0:
		return after >= before-1e-12
	case s < 0:
		return after <= before+1e-12
	default:
		return true
	}
}

// TestT7G102StateJumpPerTurn T7-G1-02（BI-7.2/G1，真实）：状态连续性——50 条
// 多轮轨迹（每条 12 轮，轨迹序号确定性种子）数值断言单轮跳变：每轮事件前后
// 每维 |Δ| ≤0.3（MaxStep 构造保证+夹紧只减不增）。事件流含满强度负性连击与
// NaN/±Inf/越界强度压力面（yaml min_evidence n:50 轨迹口径）。
func TestT7G102StateJumpPerTurn(t *testing.T) {
	gaterunner.Mark(t, "T7", "BI-7.2", "T7-G1-02", "G1")
	const trajectories, turns, maxJump = 50, 12, 0.3
	worst := 0.0
	for tr := 0; tr < trajectories; tr++ {
		e := mustEngine(t, DefaultConfig())
		r := rand.New(rand.NewSource(int64(9200 + tr))) // 确定性轨迹流
		tMs := int64(0)
		for turn := 0; turn < turns; turn++ {
			tMs += 5_000 + int64(r.Intn(116_000)) // 轮间隔 5s~2min（衰减交错）
			e.DecayTo(tMs)
			before := e.State()
			i := r.Float64()
			switch r.Intn(12) { // 强度压力面：NaN/±Inf/越界混入
			case 0:
				i = math.NaN()
			case 1:
				i = math.Inf(1)
			case 2:
				i = math.Inf(-1)
			case 3:
				i = -0.9
			case 4:
				i = 8.1
			}
			after := e.OnEvent(Event{K: Kind(r.Intn(KindCount)), Intensity: i})
			dims := [3][2]float64{{before.Valence, after.Valence},
				{before.Arousal, after.Arousal}, {before.Closeness, after.Closeness}}
			for _, d := range dims {
				if j := math.Abs(d[1] - d[0]); j > worst {
					worst = j
				}
			}
		}
	}
	if worst > maxJump+1e-9 {
		t.Fatalf("emotion_jump_per_turn=%.6f > 0.3（50 条×12 轮轨迹最大单轮跳变越线）", worst)
	}
	t.Logf("T7-G1-02：50 轨迹×12 轮最大单轮跳变 %.6f ≤ 0.3（MaxStep 构造保证）", worst)
}

// TestT7G103Recovery T7-G1-03（BI-7.3/G1，真实）：可恢复性——20 条「激怒→静置」
// 轨迹（仿真时钟；激怒=满强度负性混合连击，深度 4..23 发随轨迹渐变）：静置
// 每 30s 步进至 30min，断言①实际回基线时刻（三维全入 ±0.1）≤30min；②静置
// 全程到基线距离单调不增且未归零前严格递减（无吸收态——除基线无不动点）。
func TestT7G103Recovery(t *testing.T) {
	gaterunner.Mark(t, "T7", "BI-7.3", "T7-G1-03", "G1")
	const trajectories, stepMs, horizonMs, band = 20, 30_000, 30 * 60_000, 0.1
	dist := func(s State) float64 {
		return math.Max(math.Abs(s.Valence-0.5), math.Max(math.Abs(s.Arousal-0.5), math.Abs(s.Closeness-0.5)))
	}
	for tr := 0; tr < trajectories; tr++ {
		e := mustEngine(t, DefaultConfig())
		for n := 0; n < 4+tr; n++ { // 激怒深度随轨迹渐变（不同连击组合）
			switch n % 3 {
			case 0:
				e.OnEvent(Event{K: ToySnatched, Intensity: 1})
			case 1:
				e.OnEvent(Event{K: Criticize, Intensity: 1})
			default:
				e.OnEvent(Event{K: Scared, Intensity: 1})
			}
		}
		start := int64(1_000)
		e.DecayTo(start)
		if d0 := dist(e.State()); d0 < 0.2 {
			t.Fatalf("轨迹 %d 激怒未达压力初值（距离 %.3f < 0.2）", tr, d0)
		}
		recoveredAt, prev := int64(-1), dist(e.State())
		for at := start + stepMs; at <= start+horizonMs; at += stepMs {
			cur := dist(e.DecayTo(at))
			if cur > prev+1e-12 {
				t.Fatalf("轨迹 %d 静置距离发散：%.6f → %.6f（at=%dms）", tr, prev, cur, at)
			}
			if prev > 1e-9 && cur >= prev-1e-12 {
				t.Fatalf("轨迹 %d 吸收态：距离 %.6f → %.6f 未严格递减（at=%dms）", tr, prev, cur, at)
			}
			prev = cur
			if recoveredAt < 0 && cur <= band {
				recoveredAt = at - start
			}
		}
		if recoveredAt < 0 {
			t.Fatalf("轨迹 %d 30min 内未回基线 ±0.1（末距 %.4f）", tr, prev)
		}
		t.Logf("轨迹 %d：激怒深度 %d 发，回基线耗时 %dms ≤ 30min（无吸收态）", tr, 4+tr, recoveredAt)
	}
}

// TestT7G001EmotionBoundary T7-G0-01（BI-7.4/G0，debt）：情绪不越界。逻辑面
// （真实）：全情绪网格（16 Kind×21 强度格）×对抗事件流（满强度负性连击/
// NaN/±Inf/越界强度/未知枚举×任意衰减时序交错含迟到调用面）状态三维恒
// ∈[0,1]、永不 NaN、标签恒在儿童 9 类——引擎侧情绪状态完备性 0 越界
// （violation 计数通道真实断言）。联跑面见尾 Skipf（T9 危机/攻击集）。
func TestT7G001EmotionBoundary(t *testing.T) {
	gaterunner.Mark(t, "T7", "BI-7.4", "T7-G0-01", "G0")
	violations := 0
	// 面 1：全情绪网格（每 Kind×21 强度格单发）
	for k := 0; k < KindCount; k++ {
		for j := 0; j <= 20; j++ {
			e := mustEngine(t, DefaultConfig())
			s := e.OnEvent(Event{K: Kind(k), Intensity: float64(j) / 20})
			if !validDims(s) || !knownLabels[s.Label] {
				violations++
			}
		}
	}
	// 面 2：对抗事件流（连击×非法强度/未知枚举×衰减交错，20 轮）
	adversarial := []Event{
		{K: ToySnatched, Intensity: 1}, {K: Criticize, Intensity: 1}, {K: Scared, Intensity: 1},
		{K: ToySnatched, Intensity: math.NaN()}, {K: Scared, Intensity: math.Inf(1)},
		{K: Criticize, Intensity: math.Inf(-1)}, {K: ToySnatched, Intensity: -7},
		{K: Scared, Intensity: 99}, {K: Kind(99), Intensity: 1}, {K: Kind(-2), Intensity: 1},
		{K: Ignore, Intensity: 1}, {K: Bedtime, Intensity: 1},
	}
	e := mustEngine(t, DefaultConfig())
	atMs := int64(0)
	for round := 0; round < 20; round++ {
		for _, ev := range adversarial {
			atMs += 17_000
			if s := e.DecayTo(atMs); !validDims(s) || !knownLabels[s.Label] {
				violations++
			}
			if s := e.OnEvent(ev); !validDims(s) || !knownLabels[s.Label] {
				violations++
			}
		}
		if s := e.DecayTo(atMs - 5_000); !validDims(s) || !knownLabels[s.Label] { // 迟到 no-op 面
			violations++
		}
	}
	if violations != 0 {
		t.Fatalf("emotion_boundary_violation_count=%d ≠ 0（情绪状态完备性被突破：越界/NaN/未知标签）", violations)
	}
	t.Skipf("T7-G0-01 debt：T9 联跑面未建——全情绪网格×T9 危机/攻击集联跑（测试侧 import safety，输出 0 伤人话/恐吓/尖叫级）依赖 safety 包（IR #91 未合入，卡序 #92 联跑依赖 #91，m2-spec §11 卡序=依赖序）；当前已真实断言引擎侧情绪状态完备性 0 越界（16 Kind×21 强度格×20 轮对抗事件流恒 [0,1]/合法 9 类标签）。#91 合入后测试侧 import safety 以危机/攻击集驱动联跑并去掉本 Skip。")
}

// TestT7G104ResponseLatency T7-G1-04（BI-7.1/G1，真实）：检测延迟——100 事件
// 逻辑口径：事件→首个可见输出（旁路动作通道消费的情绪状态快照）同步直返
// （OnEvent 纯函数无 IO/goroutine/锁——逻辑口径≈0 的构造保证），CI 墙钟实测
// P95（顺序统计量第 95 位）≤900ms；样本随 suite [ci,nightly] 进 nightly 看板
// （m2-spec §9.4/§10），真机面 M3 复测。
func TestT7G104ResponseLatency(t *testing.T) {
	gaterunner.Mark(t, "T7", "BI-7.1", "T7-G1-04", "G1")
	e := mustEngine(t, DefaultConfig())
	elapsed := make([]float64, 0, 100)
	for k := 0; k < 100; k++ {
		start := time.Now()
		e.DecayTo(int64(k) * 30_000)
		s := e.OnEvent(Event{K: Kind(k % KindCount), Intensity: 0.05 + 0.95*float64(k%17)/16})
		dt := time.Since(start).Seconds() * 1000
		if !validDims(s) || !knownLabels[s.Label] {
			t.Fatalf("第 %d 次事件产出非法状态 %v（延迟通道先验失效）", k, s)
		}
		elapsed = append(elapsed, dt)
	}
	sort.Float64s(elapsed)
	p95 := elapsed[94] // 100 样本第 95 位（描述性顺序统计量，非统计推断）
	if p95 > 900 {
		t.Fatalf("emotion_response_latency_p95_ms=%.3f > 900（n=100）", p95)
	}
	t.Logf("T7-G1-04：P95=%.4fms（同步直返构造保证逻辑口径≈0；CI 墙钟样本随 nightly suite 复跑进看板）", p95)
}
