// 表驱动单测：T20 画像域校验 / Script 形状 / 配额可控性 / 确定性（m2-spec §7）。
package usersim

import (
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestNewProfileDomain(t *testing.T) {
	cases := []struct {
		name                 string
		age                  int
		patience, aggression float64
		turns                int
		wantErr              bool
		wantErrSubstr        string
	}{
		{"合法下界", ageMin, 0, 0, 1, false, ""},
		{"合法上界", ageMax, 1, 1, 100, false, ""},
		{"年龄过小", ageMin - 1, 0.5, 0.5, 5, true, "Age"},
		{"年龄过大", ageMax + 1, 0.5, 0.5, 5, true, "Age"},
		{"耐心负值", 7, -0.01, 0.5, 5, true, "Patience"},
		{"耐心超一", 7, 1.01, 0.5, 5, true, "Patience"},
		{"耐心 NaN", 7, math.NaN(), 0.5, 5, true, "Patience"},
		{"攻击性负值", 7, 0.5, -1, 5, true, "Aggression"},
		{"攻击性超一", 7, 0.5, 2, 5, true, "Aggression"},
		{"攻击性 NaN", 7, 0.5, math.NaN(), 5, true, "Aggression"},
		{"零话轮", 7, 0.5, 0.5, 0, true, "Turns"},
		{"负话轮", 7, 0.5, 0.5, -3, true, "Turns"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := NewProfile(c.age, c.patience, c.aggression, c.turns)
			if c.wantErr {
				if err == nil {
					t.Fatalf("越界画像须拒绝: %+v", p)
				}
				if !strings.Contains(err.Error(), c.wantErrSubstr) {
					t.Fatalf("错误信息应含 %q: %v", c.wantErrSubstr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("合法画像被拒: %v", err)
			}
			if p.Age != c.age || p.Turns != c.turns {
				t.Fatalf("画像字段未原样保留: %+v", p)
			}
		})
	}
}

func TestScriptShapeAndKinds(t *testing.T) {
	p, err := NewProfile(5, 0.3, 0.6, 8)
	if err != nil {
		t.Fatal(err)
	}
	us := Script(p, 42, "J01-morning")
	if len(us) != p.Turns {
		t.Fatalf("话轮数 %d ≠ 画像 Turns %d", len(us), p.Turns)
	}
	valid := map[string]bool{}
	for _, k := range kinds {
		valid[k] = true
	}
	prev := int64(-1)
	for i, u := range us {
		if !valid[u.Kind] {
			t.Errorf("第 %d 句类别非法: %q", i, u.Kind)
		}
		if u.Interrupt != (u.Kind == KindInterrupt) {
			t.Errorf("第 %d 句 Interrupt 位与类别不符: kind=%q interrupt=%v", i, u.Kind, u.Interrupt)
		}
		if u.AtMs <= prev {
			t.Errorf("AtMs 须严格单调递增: 第 %d 句 %d ≤ 前句 %d", i, u.AtMs, prev)
		}
		prev = u.AtMs
		if got := len([]rune(u.Text)); got != turnRunes(p, i, p.Turns) {
			t.Errorf("第 %d 句长度 %d ≠ 目标 %d（话轮长度=画像纯函数）", i, got, turnRunes(p, i, p.Turns))
		}
	}
}

func TestScriptDeterministicReplay(t *testing.T) {
	p, err := NewProfile(7, 0.4, 0.3, 6)
	if err != nil {
		t.Fatal(err)
	}
	a := Script(p, 7, "J03-story")
	b := Script(p, 7, "J03-story")
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("同画像同种子同 id 须逐字节同序列:\n%+v\n%+v", a, b)
	}
	// 不同种子→扰动多样性（长度序列恒同——画像纯函数；文本/排布随机）。
	seen := map[string]bool{}
	for seed := int64(0); seed < 10; seed++ {
		seen[render(Script(p, seed, "J03-story"))] = true
	}
	if len(seen) < 5 {
		t.Fatalf("10 个种子的序列应有多样性（≥5 种），got %d", len(seen))
	}
}

func TestKindQuotaControlledByProfile(t *testing.T) {
	// 边界行为计数=画像纯函数：极端画像下各类别配额精确可预期（T20-G1-02
	// 可达性的实现面——确定性生成构造保证）。
	cases := []struct {
		name    string
		profile Profile
		kind    string
		want    int
	}{
		{"耐心 0 打断", Profile{Age: 12, Patience: 0, Aggression: 0, Turns: 10}, KindInterrupt, 6},
		{"耐心 1 无打断", Profile{Age: 12, Patience: 1, Aggression: 0, Turns: 10}, KindInterrupt, 0},
		{"攻击性满档", Profile{Age: 12, Patience: 1, Aggression: 1, Turns: 10}, KindAttack, 4},
		{"3 岁跑题", Profile{Age: 3, Patience: 1, Aggression: 0, Turns: 10}, KindOffTopic, 3},
		{"耐心 0 重复", Profile{Age: 12, Patience: 0, Aggression: 0, Turns: 10}, KindRepeat, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			us := Script(c.profile, 123, "quota")
			if got := countKind(us, c.kind); got != c.want {
				t.Fatalf("类别 %q 计数 %d ≠ 画像配额 %d（参数失控？）", c.kind, got, c.want)
			}
		})
	}
}

func TestPatienceControlsBehavior(t *testing.T) {
	// 耐心↓→平均话轮长单调降、打断频率单调升（严格端点夹逼——属性面的表驱动锚点）。
	low, err := NewProfile(7, 0.1, 0.3, 6)
	if err != nil {
		t.Fatal(err)
	}
	high, err := NewProfile(7, 0.9, 0.3, 6)
	if err != nil {
		t.Fatal(err)
	}
	if avgRunes(Script(low, 1, "mono")) >= avgRunes(Script(high, 1, "mono")) {
		t.Fatalf("耐心 0.1 平均话轮长 %.2f 须严格小于耐心 0.9 的 %.2f",
			avgRunes(Script(low, 1, "mono")), avgRunes(Script(high, 1, "mono")))
	}
	if countKind(Script(low, 1, "mono"), KindInterrupt) <= countKind(Script(high, 1, "mono"), KindInterrupt) {
		t.Fatalf("耐心 0.1 打断频率须严格高于耐心 0.9")
	}
}

// ---- 测试辅助 ----

func countKind(us []Utterance, kind string) int {
	n := 0
	for _, u := range us {
		if u.Kind == kind {
			n++
		}
	}
	return n
}

func avgRunes(us []Utterance) float64 {
	if len(us) == 0 {
		return 0
	}
	total := 0
	for _, u := range us {
		total += len([]rune(u.Text))
	}
	return float64(total) / float64(len(us))
}

func render(us []Utterance) string {
	var sb strings.Builder
	for _, u := range us {
		sb.WriteString(u.Kind)
		sb.WriteString("|")
		sb.WriteString(u.Text)
		sb.WriteString("|")
		sb.WriteString(strconv.FormatInt(u.AtMs, 10))
		sb.WriteString(";")
	}
	return sb.String()
}
