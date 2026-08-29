// T5 属性测试（m3-spec §5 + docs/gates/assets/T5.md 属性行，testing/quick）：
//
//	P1 确定性（同特征同判定——回放可复现，含拒判事件序列）
//	P2 增益不变（特征整体缩放判定不变——余弦打分器尺度不变的实现面）
//	P3 对称性（A→B 与 B→A 判定同分）
//	P4 文本无关性（同人不同内容扰动=同簇不跨阈值：判定仍绑定该成员）
//	P5 拒判↔只读一一对应（N 次拒判=N 个 MemoryReadOnly 事件，判定成功 0 事件）
//	P6 阈值单调（阈值低→通过集单调不缩——分越高越易通过）
package voiceprint

import (
	"math"
	"testing"
	"testing/quick"
)

// propFamily 合成家庭原料（quick 不支持 slice 形参，用导出字段结构体承载
// 种子；维度冻结 32=合成代理口径，与打分器解耦——生成参数不经打分器调参）。
type propFamily struct {
	S1, S2, S3, S4 int64 // 成员基向量种子
	Probe          int64 // 待判特征种子
}

func (p propFamily) bases() []Feat {
	return []Feat{unitVec(32, abs64(p.S1)), unitVec(32, abs64(p.S2)),
		unitVec(32, abs64(p.S3)), unitVec(32, abs64(p.S4))}
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// buildEngine 注册 4 成员家庭（各 3 句注册，σ=0.02 冻结——属性口径非门禁协议）。
func buildEngine(threshold float64, fam propFamily) (*Engine, error) {
	e, err := NewEngine(Config{Threshold: threshold, MinEnroll: 3})
	if err != nil {
		return nil, err
	}
	for i, base := range fam.bases() {
		fs := []Feat{jitter(base, 0.02, abs64(fam.S1)+int64(i)), jitter(base, 0.02, abs64(fam.S2)), jitter(base, 0.02, abs64(fam.S3))}
		if err := e.Enroll(userOf(i), fs); err != nil {
			return nil, err
		}
	}
	return e, nil
}

func userOf(i int) string { return []string{"u0", "u1", "u2", "u3"}[i] }

// TestPropertyDeterminism P1：同特征同判定（Decision 逐字段全等）+ 拒判事件
// 序列逐次全等（分数含浮点位级一致——纯函数打分器的承诺）。
func TestPropertyDeterminism(t *testing.T) {
	prop := func(fam propFamily) bool {
		e1, err := buildEngine(0.9, fam)
		if err != nil {
			t.Logf("buildEngine: %v", err)
			return false
		}
		e2, _ := buildEngine(0.9, fam)
		n1, n2 := 0, 0
		e1.BindReadOnly(hookFunc(func(RejectEvent) { n1++ }))
		e2.BindReadOnly(hookFunc(func(RejectEvent) { n2++ }))
		probe := jitter(fam.bases()[0], 0.03, abs64(fam.Probe))
		stranger := unitVec(32, abs64(fam.Probe)+7)
		for i := 0; i < 4; i++ {
			var f Feat
			switch i % 2 {
			case 0:
				f = probe
			default:
				f = stranger
			}
			d1, d2 := e1.Verify(f), e2.Verify(f)
			if d1 != d2 {
				t.Logf("P1 判定漂移: %+v vs %+v", d1, d2)
				return false
			}
			if i%2 == 1 && d1.Rejected != true {
				t.Logf("P1 陌生人样本未构成拒判（属性空转风险）: %+v", d1)
			}
		}
		return n1 == n2
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 150}); err != nil {
		t.Errorf("P1 确定性失效: %v", err)
	}
}

// TestPropertyGainInvariance P2：特征整体缩放（正增益）判定不变——增益不变
// （资产卡：同人/跨人判定不随录音增益漂移）。缩放域 [0.05, 50]。
func TestPropertyGainInvariance(t *testing.T) {
	prop := func(fam propFamily, gainRaw uint32) bool {
		g := 0.05 + float64(gamut(gainRaw))/float64(math.MaxUint32)*49.95
		e, err := buildEngine(0.9, fam)
		if err != nil {
			t.Logf("buildEngine: %v", err)
			return false
		}
		probe := jitter(fam.bases()[1], 0.03, abs64(fam.Probe))
		scaled := make(Feat, len(probe))
		for i, v := range probe {
			scaled[i] = v * g
		}
		d0, d1 := e.Verify(probe), e.Verify(scaled)
		// 判定（绑定/拒判+成员）必须不变；分数允许缩放引入的浮点尾差（对齐
		// kws P1「置信度数值允许量化微漂」口径——判定序列才是比较面）。
		if d0.Rejected != d1.Rejected || d0.UserID != d1.UserID {
			t.Logf("P2 判定漂移: %+v vs %+v（gain=%.3f）", d0, d1, g)
			return false
		}
		return math.Abs(d0.Score-d1.Score) <= 1e-9
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 150}); err != nil {
		t.Errorf("P2 增益不变失效: %v", err)
	}
}

func gamut(v uint32) uint32 {
	if v > math.MaxUint32/2 {
		return math.MaxUint32 - v // 展宽对称域
	}
	return v
}

// TestPropertyScoreSymmetry P3：Score(a,b)==Score(b,a)（A→B 与 B→A 判定对称
// ——资产卡回归主力）。随机家庭特征两两互检。
func TestPropertyScoreSymmetry(t *testing.T) {
	prop := func(fam propFamily) bool {
		e, err := buildEngine(0.9, fam)
		if err != nil {
			t.Logf("buildEngine: %v", err)
			return false
		}
		bases := fam.bases()
		fa := jitter(bases[0], 0.02, abs64(fam.S1))
		fb := jitter(bases[2], 0.02, abs64(fam.S3))
		ab, ba := e.Score(fa, fb), e.Score(fb, fa)
		if ab != ba {
			t.Logf("P3 打分不对称: %.12f vs %.12f", ab, ba)
			return false
		}
		// 同人对（对角）也对称
		fc := jitter(bases[0], 0.02, abs64(fam.S2))
		return e.Score(fa, fc) == e.Score(fc, fa)
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 150}); err != nil {
		t.Errorf("P3 对称性失效: %v", err)
	}
}

// TestPropertyTextIndependence P4：同人不同内容=同簇不跨阈值——同成员基向量
// 的独立扰动句（不同「内容」）仍判定绑定该成员（文本无关性；σ=0.03 冻结口
// 径：内容扰动幅度远小于成员间距离）。
func TestPropertyTextIndependence(t *testing.T) {
	prop := func(fam propFamily, contentSeed int64) bool {
		e, err := buildEngine(0.9, fam)
		if err != nil {
			t.Logf("buildEngine: %v", err)
			return false
		}
		for i, base := range fam.bases() {
			utter := jitter(base, 0.03, abs64(contentSeed)+int64(i)*31)
			d := e.Verify(utter)
			if d.Rejected || d.UserID != userOf(i) {
				t.Logf("P4 同人不同内容跨簇: 成员 %d 判定 %+v", i, d)
				return false
			}
		}
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 150}); err != nil {
		t.Errorf("P4 文本无关性失效: %v", err)
	}
}

// TestPropertyRejectReadOnlyBijection P5：拒判↔只读联动一一对应——任意验证
// 序列中，MemoryReadOnly 收到的事件数 == 拒判次数（判定成功零事件）。
func TestPropertyRejectReadOnlyBijection(t *testing.T) {
	prop := func(fam propFamily, mixRaw uint8) bool {
		e, err := buildEngine(0.85, fam)
		if err != nil {
			t.Logf("buildEngine: %v", err)
			return false
		}
		evts, rejects := 0, 0
		e.BindReadOnly(hookFunc(func(RejectEvent) { evts++ }))
		for i := 0; i < 12; i++ {
			var f Feat
			if (int(mixRaw)+i)%3 == 0 {
				f = unitVec(32, int64(i)+500) // 陌生人（大概率拒判）
			} else {
				f = jitter(fam.bases()[(int(mixRaw)+i)%4], 0.03, int64(i))
			}
			if d := e.Verify(f); d.Rejected {
				rejects++
			}
		}
		return evts == rejects && evts > 0 // 非空转：至少一次拒判发生
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("P5 拒判↔只读一一对应失效: %v", err)
	}
}

// TestPropertyThresholdMonotonic P6：阈值单调（分越高越易通过）——同一注册库
// 与同一待判特征下，阈值 θ1≤θ2 ⇒ θ1 的通过集 ⊇ θ2 的通过集（通过=非拒判）。
func TestPropertyThresholdMonotonic(t *testing.T) {
	prop := func(fam propFamily, tRaw uint8) bool {
		lo := 0.5 + float64(tRaw%64)/64.0*0.25 // θ1 ∈ [0.5, 0.75]
		hi := lo + 0.1 + float64(tRaw%32)/32.0*0.15
		if hi > 1 {
			hi = 1
		}
		eLo, err := buildEngine(lo, fam)
		if err != nil {
			t.Logf("buildEngine: %v", err)
			return false
		}
		eHi, _ := buildEngine(hi, fam)
		for i := 0; i < 6; i++ {
			var f Feat
			if i%2 == 0 {
				f = jitter(fam.bases()[i%4], 0.03, int64(i)+abs64(fam.Probe))
			} else {
				f = unitVec(32, int64(i)+900)
			}
			dLo, dHi := eLo.Verify(f), eHi.Verify(f)
			if dHi.Rejected == false && dLo.Rejected == true {
				t.Logf("P6 阈值单调破坏: θ_lo=%.3f 通过而 θ_hi=%.3f 拒判（score=%.4f）", lo, hi, dLo.Score)
				return false
			}
			if dLo.Rejected != dHi.Rejected && dLo.Score != dHi.Score {
				t.Logf("P6 同特征两引擎分数漂移: %.6f vs %.6f", dLo.Score, dHi.Score)
				return false
			}
		}
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("P6 阈值单调失效: %v", err)
	}
}
