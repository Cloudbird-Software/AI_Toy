// 属性测试（testing/quick，spec §11.2 四性质族——m2-spec §7 包契约 F）：
// P1 有界性：画像参数域（越界拒绝构造）；P2 单调性：耐心↓→平均话轮长单调降、
// 打断频率单调升（参数真控行为）；P3 不变性：同 (Profile,seed,id) 重放逐字节
// 全等（含与任意其它调用交错——无隐藏系统状态，兼反「偷看被测系统」）；
// P4 确定性：形状不变量（类别 ∈ 五类白名单、AtMs 严格递增、Interrupt 位一致、
// 话轮长度与种子无关）。
//
// quick 只生成基础类型参数：全域采样经模运算落入画像参数域（属性为全称命题，
// 模采样覆盖域内任意取值对）。
package usersim

import (
	"reflect"
	"testing"
	"testing/quick"
)

func mod(v, m int64) int64 {
	r := v % m
	if r < 0 {
		r += m
	}
	return r
}

// unit 模采样映射到 [0,1]（分辨率 1/1000）。
func unit(v int64) float64 { return float64(mod(v, 1001)) / 1000 }

// propAge 模采样映射到 [3,12]。
func propAge(v int64) int { return ageMin + int(mod(v, ageMax-ageMin+1)) }

// propTurns 模采样映射到 [1,8]。
func propTurns(v int64) int { return 1 + int(mod(v, 8)) }

// TestPropertyProfileDomain P1 有界性：NewProfile 成功 ⇔ 画像全参数在合法域
// （年龄 3–12、耐心/攻击性 ∈[0,1]、话轮 ≥1——NaN/Inf 落域外一并拒绝）。
func TestPropertyProfileDomain(t *testing.T) {
	f := func(age int8, pat, agg float64, turns int16) bool {
		inDomain := int(age) >= ageMin && int(age) <= ageMax &&
			pat >= 0 && pat <= 1 && agg >= 0 && agg <= 1 && int(turns) >= turnsMin
		_, err := NewProfile(int(age), pat, agg, int(turns))
		return (err == nil) == inDomain
	}
	if err := quick.Check(f, nil); err != nil {
		t.Errorf("画像域属性不成立: %v", err)
	}
}

// TestPropertyPatienceMonotonicity P2 单调性：对任意耐心对 p1≤p2（其余画像参数
// 同），平均话轮长单调不增、打断频率单调不减（弱单调——round/int 截断允许平局；
// 严格性由表驱动锚点 TestPatienceControlsBehavior 端点夹逼保证）。
func TestPropertyPatienceMonotonicity(t *testing.T) {
	f := func(seed int64, ageRand, patA, patB, aggRand, turnsRand int64, id string) bool {
		p1, p2 := unit(patA), unit(patB)
		if p1 > p2 {
			p1, p2 = p2, p1
		}
		lo, err := NewProfile(propAge(ageRand), p1, unit(aggRand), propTurns(turnsRand))
		if err != nil {
			return true // 采样域外（理论不可达——防御）
		}
		hi, err := NewProfile(propAge(ageRand), p2, unit(aggRand), propTurns(turnsRand))
		if err != nil {
			return true
		}
		sLo, sHi := Script(lo, seed, id), Script(hi, seed, id)
		return avgRunes(sLo) <= avgRunes(sHi)+1e-9 &&
			countKind(sLo, KindInterrupt) >= countKind(sHi, KindInterrupt)
	}
	if err := quick.Check(f, nil); err != nil {
		t.Errorf("耐心单调性属性不成立: %v", err)
	}
}

// TestPropertyReplayInvariance P3 不变性：同输入重放逐字节全等，且与任意其它
// 画像/种子的调用交错不改结果——Script 无隐藏状态、不偷看被测系统。
func TestPropertyReplayInvariance(t *testing.T) {
	f := func(seed, otherSeed int64, ageRand, patRand, aggRand, turnsRand int64, id string) bool {
		p, err := NewProfile(propAge(ageRand), unit(patRand), unit(aggRand), propTurns(turnsRand))
		if err != nil {
			return true
		}
		first := Script(p, seed, id)
		_ = Script(p, otherSeed, id) // 交错扰动调用
		_ = Script(Profile{Age: 3, Patience: 1, Aggression: 1, Turns: 4}, seed, "other")
		return reflect.DeepEqual(first, Script(p, seed, id))
	}
	if err := quick.Check(f, nil); err != nil {
		t.Errorf("重放不变性属性不成立: %v", err)
	}
}

// TestPropertyShapeInvariants P4 确定性形状面：类别 ∈ 五类白名单、AtMs 严格
// 递增、Interrupt 位与类别一致、话轮长度与种子无关（画像纯函数——跨种子同
// 位置同长度）。
func TestPropertyShapeInvariants(t *testing.T) {
	f := func(seedA, seedB int64, ageRand, patRand, aggRand, turnsRand int64, id string) bool {
		p, err := NewProfile(propAge(ageRand), unit(patRand), unit(aggRand), propTurns(turnsRand))
		if err != nil {
			return true
		}
		a, b := Script(p, seedA, id), Script(p, seedB, id)
		if len(a) != p.Turns || len(b) != p.Turns {
			return false
		}
		prev := int64(-1)
		for i, u := range a {
			valid := false
			for _, k := range kinds {
				if u.Kind == k {
					valid = true
				}
			}
			if !valid || u.Interrupt != (u.Kind == KindInterrupt) || u.AtMs <= prev {
				return false
			}
			prev = u.AtMs
			if len([]rune(u.Text)) != len([]rune(b[i].Text)) {
				return false
			}
		}
		return true
	}
	if err := quick.Check(f, nil); err != nil {
		t.Errorf("形状不变量属性不成立: %v", err)
	}
}
