// 属性测试（m2-spec §11 三件套之二，testing/quick；spec §4 属性清单）：
// P1 有界性（任意事件序列+任意时长衰减后三维 ∈[0,1] 永不 NaN）；P2 单调性
// （正性事件强度↑对应维单调不降、负性对称）；P3 李雅普诺夫（静置到基线距离
// 单调不增）；P4 无关事件（Ignore 及未知枚举）不改状态；P5 确定性回放（同
// 序列+同初始态 → 同态轨迹）。命名与 AGENTS.md「本地命令」的 -run Property 匹配。
package emotion

import (
	"math"
	"testing"
	"testing/quick"
)

// rawEvent 属性测试随机事件原料：K 为 Kind 原料（含越界值——鲁棒面）、I 为
// 强度原料（quick 的 float64 可产 NaN/±Inf/任意值）。字段须导出（quick 经
// reflect 填充）。
type rawEvent struct {
	K int8
	I float64
}

// eventSeq 固定 8 事件的随机序列（quick 不支持 slice 形参，用导出字段结构体承载）。
type eventSeq struct {
	E0, E1, E2, E3, E4, E5, E6, E7 rawEvent
}

func seqEvents(s eventSeq) [8]rawEvent {
	return [8]rawEvent{s.E0, s.E1, s.E2, s.E3, s.E4, s.E5, s.E6, s.E7}
}

// normAt 把随机 int64 归一到 [0,1e9)（衰减时长原料：0~约 11.6 天，覆盖半衰期
// 网格的任意步进组合；负数经 uint64 模运算均匀落段）。
func normAt(v int64) int64 { return int64(uint64(v) % 1_000_000_000) }

// validDims 状态三维合法（[0,1] 且非 NaN）。
func validDims(s State) bool {
	return !math.IsNaN(s.Valence) && !math.IsNaN(s.Arousal) && !math.IsNaN(s.Closeness) &&
		s.Valence >= 0 && s.Valence <= 1 && s.Arousal >= 0 && s.Arousal <= 1 &&
		s.Closeness >= 0 && s.Closeness <= 1
}

// knownLabels 合法标签集（儿童 9 类——DefaultConfig 标签带）。
var knownLabels = map[string]bool{
	"sad": true, "scared": true, "angry": true, "sleepy": true, "calm": true,
	"surprised": true, "content": true, "happy": true, "excited": true,
}

// TestPropertyBounded P1 有界性：任意 8 事件随机序列（含越界 Kind/NaN 强度）
// + 任意时长衰减后，三维 ∈[0,1]、永不 NaN、标签恒在儿童 9 类（状态完备口径）。
func TestPropertyBounded(t *testing.T) {
	f := func(seq eventSeq, decayMs int64) bool {
		e, err := NewEngine(DefaultConfig())
		if err != nil {
			t.Errorf("DefaultConfig 被拒：%v", err)
			return false
		}
		for _, re := range seqEvents(seq) {
			if s := e.OnEvent(Event{K: Kind(re.K), Intensity: re.I}); !validDims(s) || !knownLabels[s.Label] {
				t.Logf("OnEvent 后状态非法：%v（Kind=%d I=%v）", s, re.K, re.I)
				return false
			}
		}
		s := e.DecayTo(normAt(decayMs))
		if !validDims(s) || !knownLabels[s.Label] {
			t.Logf("DecayTo 后状态非法：%v（decayMs=%d）", s, normAt(decayMs))
			return false
		}
		// 事件与衰减交错再验（任意调用序）
		s = e.OnEvent(Event{K: Kind(seq.E0.K), Intensity: seq.E0.I})
		return validDims(s) && knownLabels[s.Label]
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 300}); err != nil {
		t.Errorf("P1 有界性被违反：%v", err)
	}
}

// TestPropertyIntensityMonotone P2 单调性：同类事件强度↑ → 规则增量为正的维
// 单调不降、为负的维单调不降（负性对称=该维单调不升）；夹紧保持序（凸组合
// 的单调性）。正性样本=Praise/Hug/Play，负性样本=Criticize/ToySnatched。
func TestPropertyIntensityMonotone(t *testing.T) {
	type dims struct{ dv, da, dc float64 }
	positive := map[Kind]dims{
		Praise:      {+0.25, +0.15, +0.20},
		Hug:         {+0.20, -0.20, +0.25},
		Play:        {+0.25, +0.30, +0.20},
		Criticize:   {-0.25, +0.15, -0.15},
		ToySnatched: {-0.30, +0.25, -0.20},
	}
	f := func(kRaw int8, i1, i2 float64) bool {
		k := Kind(kRaw)
		d, ok := positive[k]
		if !ok {
			return true // 非样本 Kind：quick 输入域宽，跳过（其余 Kind 由表驱动覆盖）
		}
		lo, hi := i1, i2
		if lo > hi {
			lo, hi = hi, lo
		}
		lo, hi = clamp01(lo), clamp01(hi)
		run := func(i float64) State {
			e, err := NewEngine(DefaultConfig())
			if err != nil {
				t.Errorf("DefaultConfig 被拒：%v", err)
				return State{}
			}
			return e.OnEvent(Event{K: k, Intensity: i})
		}
		slo, shi := run(lo), run(hi)
		if d.dv > 0 && shi.Valence < slo.Valence || d.dv < 0 && shi.Valence > slo.Valence {
			t.Logf("Kind %d 愉悦度单调性破坏：i=%.3f→%.3f V %.3f→%.3f", k, lo, hi, slo.Valence, shi.Valence)
			return false
		}
		if d.da > 0 && shi.Arousal < slo.Arousal || d.da < 0 && shi.Arousal > slo.Arousal {
			t.Logf("Kind %d 唤醒度单调性破坏：i=%.3f→%.3f A %.3f→%.3f", k, lo, hi, slo.Arousal, shi.Arousal)
			return false
		}
		if d.dc > 0 && shi.Closeness < slo.Closeness || d.dc < 0 && shi.Closeness > slo.Closeness {
			t.Logf("Kind %d 亲密度单调性破坏：i=%.3f→%.3f C %.3f→%.3f", k, lo, hi, slo.Closeness, shi.Closeness)
			return false
		}
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 300}); err != nil {
		t.Errorf("P2 单调性被违反：%v", err)
	}
}

// TestPropertyDecayLyapunov P3 李雅普诺夫：任意事件序列后，连续 DecayTo（时刻
// 递增）到基线距离单调不增（快慢半衰期各维同向回归基线——静置无发散）。
func TestPropertyDecayLyapunov(t *testing.T) {
	dist := func(s State) float64 {
		return math.Max(math.Abs(s.Valence-0.5), math.Max(math.Abs(s.Arousal-0.5), math.Abs(s.Closeness-0.5)))
	}
	f := func(seq eventSeq, t0, t1, t2 int64) bool {
		e, err := NewEngine(DefaultConfig())
		if err != nil {
			t.Errorf("DefaultConfig 被拒：%v", err)
			return false
		}
		for _, re := range seqEvents(seq) {
			e.OnEvent(Event{K: Kind(re.K), Intensity: re.I})
		}
		// 三个递增时刻的静置（0~1e9 ms 归一后排序）
		ts := []int64{normAt(t0), normAt(t1), normAt(t2)}
		for i := 1; i < len(ts); i++ {
			if ts[i] < ts[i-1] {
				ts[i], ts[i-1] = ts[i-1], ts[i]
			}
		}
		prev := dist(e.State())
		for _, at := range ts {
			cur := dist(e.DecayTo(at))
			if cur > prev+1e-12 {
				t.Logf("静置距离发散：%.6f → %.6f（at=%d）", prev, cur, at)
				return false
			}
			prev = cur
		}
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 300}); err != nil {
		t.Errorf("P3 李雅普诺夫被违反：%v", err)
	}
}

// TestPropertyIgnoreNoState P4 无关事件不改状态：任意强度（含 NaN/越界）的
// Ignore/未知枚举事件后，状态逐字段不变。
func TestPropertyIgnoreNoState(t *testing.T) {
	f := func(seq eventSeq, i float64, kRaw int8) bool {
		e, err := NewEngine(DefaultConfig())
		if err != nil {
			t.Errorf("DefaultConfig 被拒：%v", err)
			return false
		}
		for _, re := range seqEvents(seq) {
			e.OnEvent(Event{K: Kind(re.K), Intensity: re.I})
		}
		before := e.State()
		if kRaw >= 0 && int(kRaw) < KindCount && Kind(kRaw) != Ignore {
			return true // 样本域=Ignore 与越界枚举（有效非零 Kind 的方向面由 P2/门禁覆盖）
		}
		after := e.OnEvent(Event{K: Kind(kRaw), Intensity: i})
		return after == before
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 300}); err != nil {
		t.Errorf("P4 无关事件不改状态被违反：%v", err)
	}
}

// TestPropertyDeterministicReplay P5 确定性回放：同事件序列+同衰减调度在两个
// 独立 Engine 实例重放 → 每步状态（含标签）全等（无随机、无墙钟、无隐藏状态）。
func TestPropertyDeterministicReplay(t *testing.T) {
	f := func(seq eventSeq, d0, d1 int64) bool {
		run := func() []State {
			e, err := NewEngine(DefaultConfig())
			if err != nil {
				t.Errorf("DefaultConfig 被拒：%v", err)
				return nil
			}
			var trace []State
			for i, re := range seqEvents(seq) {
				trace = append(trace, e.OnEvent(Event{K: Kind(re.K), Intensity: re.I}))
				if i == 2 {
					trace = append(trace, e.DecayTo(normAt(d0)))
				}
			}
			trace = append(trace, e.DecayTo(normAt(d1)))
			return trace
		}
		a, b := run(), run()
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				t.Logf("第 %d 步状态分歧：%v vs %v", i, a[i], b[i])
				return false
			}
		}
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("P5 确定性回放被违反：%v", err)
	}
}
