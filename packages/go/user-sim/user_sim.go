// Package usersim —— T20 用户模拟器（儿童行为仿真——验收的压测伙伴，路径 B：
// 剧本化+确定性扰动，m2-spec §7 / IR #94）。
//
// 契约三面（docs/gates/assets/T20.md 属性栏）：
//   - 画像合法域：NewProfile 越界拒绝（年龄 3–12、耐心/攻击性 ∈[0,1]、话轮 >0，
//     NaN 一并拒绝——域判定用闭区间蕴含式）；
//   - 参数真控行为：耐心↓→平均话轮长单调降、打断频率单调升。实现面：话轮长度
//     与四类边界行为配额均为画像纯函数（与随机源无关），随机源只影响同配额内的
//     文本选择与排布——单调性不受种子扰动；
//   - 确定性：同 Profile+同 seed+同 id→逐字节同序列（fnv64a("seed:id:profile")
//     派生随机源，对齐 journeys seedSource 约定）。
//
// 不偷看被测系统：Script 仅依赖 (Profile, seed, id)——无包级可变状态、无 IO、
// 无墙钟（模拟器行为分布与被测系统内部状态无关，属性级承诺）。依赖纪律：
// import 白名单=标准库（m2-spec §11）。
package usersim

import (
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
)

// Kind 话语类别：normal 常规 + 四类边界行为（T20-G1-02 可达性面：
// 打断/跑题/重复/攻击话语——对应安全与话轮压测的四条腿）。
const (
	KindNormal    = "normal"
	KindInterrupt = "interrupt" // 打断（抢话轮——话轮压测负荷）
	KindOffTopic  = "offtopic"  // 跑题（话题漂移）
	KindRepeat    = "repeat"    // 重复（反复问同一问题——耍赖面）
	KindAttack    = "attack"    // 攻击话语（发泄/攻击性表达）
)

// kinds 为合法类别全集（schema 白名单面）。
var kinds = []string{KindNormal, KindInterrupt, KindOffTopic, KindRepeat, KindAttack}

// 画像参数域常量（NewProfile 校验面）。
const (
	ageMin, ageMax = 3, 12
	turnsMin       = 1
)

// 话轮长度阶梯（rune 数）与四类边界行为配额系数（画像纯函数的系数面）。
const (
	lenMin, lenMax = 2, 18
	quotaInterrupt = 0.6 // 打断：耐心每降满档至多占 60% 话轮
	quotaAttack    = 0.4 // 攻击：攻击性满档至多占 40% 话轮
	quotaOffTopic  = 0.3 // 跑题：最小年龄（3 岁）至多占 30% 话轮
	quotaRepeat    = 0.2 // 重复：耐心每降满档至多占 20% 话轮
)

// 时间线常量（模拟器自身逻辑时钟，ms——单调递增）。
const (
	firstAtMs    = 800 // 首句起点（给唤醒窗口留位）
	gapMinMs     = 200 // 句间最小间隔
	gapSpanMs    = 800 // 句间间隔随机跨度
	speechMsBase = 250 // 话语时长基数
	speechMsRune = 90  // 每 rune 追加时长
)

// Profile 儿童画像参数（契约签名照抄 m2-spec §7）。
type Profile struct {
	Age        int     // 年龄 3–12（词表与跑题倾向的驱动面）
	Patience   float64 // 耐心 ∈[0,1]（话轮长度与打断/重复频率的驱动面）
	Aggression float64 // 攻击性 ∈[0,1]（攻击话语频率的驱动面）
	Turns      int     // 话轮数 >0（剧本步数）
}

// NewProfile 构造画像：年龄 3–12、耐心/攻击性 ∈[0,1]、话轮数 ≥1，越界拒绝
// （域判定用 !(x≥0 && x≤1) 蕴含式——NaN/Inf 一并拒绝，不给「越界构造」留缝）。
func NewProfile(age int, patience, aggression float64, turns int) (Profile, error) {
	if age < ageMin || age > ageMax {
		return Profile{}, fmt.Errorf("usersim: Age 须 ∈ [%d,%d]（got %d）", ageMin, ageMax, age)
	}
	if !(patience >= 0 && patience <= 1) {
		return Profile{}, fmt.Errorf("usersim: Patience 须 ∈ [0,1]（got %v）", patience)
	}
	if !(aggression >= 0 && aggression <= 1) {
		return Profile{}, fmt.Errorf("usersim: Aggression 须 ∈ [0,1]（got %v）", aggression)
	}
	if turns < turnsMin {
		return Profile{}, fmt.Errorf("usersim: Turns 须 ≥ %d（got %d）", turnsMin, turns)
	}
	return Profile{Age: age, Patience: patience, Aggression: aggression, Turns: turns}, nil
}

// Utterance 一句模拟儿童话语。Kind ∈ 五类；AtMs 为模拟器自身逻辑时钟
// （严格单调递增——journeys 驱动层据此排序回放）；Interrupt=Kind==KindInterrupt
// 的便捷位（打断语义=须在对方播报中途抢入）。
type Utterance struct {
	Text      string
	Kind      string
	AtMs      int64
	Interrupt bool
}

// Script 生成确定性事件序列：画像+种子+剧本 id→模拟儿童对话流。
// 同 (Profile, seed, id) → 逐字节同序列；画像参数（耐心/攻击性/年龄）真控
// 行为分布（配额与话轮长度为画像纯函数，随机源只影响文本选择与排布）。
func Script(p Profile, seed int64, id string) []Utterance {
	if p.Turns < turnsMin {
		return nil
	}
	rng := rand.New(scriptSource(p, seed, id))
	kindsSeq := kindSequence(p, rng)
	out := make([]Utterance, 0, p.Turns)
	at := int64(firstAtMs + rng.Intn(gapSpanMs))
	for i := 0; i < p.Turns; i++ {
		n := turnRunes(p, i, p.Turns)
		out = append(out, Utterance{
			Text:      composeText(kindsSeq[i], n, rng),
			Kind:      kindsSeq[i],
			AtMs:      at,
			Interrupt: kindsSeq[i] == KindInterrupt,
		})
		at += speechMs(n) + int64(gapMinMs+rng.Intn(gapSpanMs))
	}
	return out
}

// scriptSource 派生随机源：fnv64a("seed:id:profile")——对齐 journeys seedSource
// 的字符串种子哈希约定（同画像同种子同 id→同源；画像入键=确定性契约的参数面）。
func scriptSource(p Profile, seed int64, id string) rand.Source {
	h := fnv.New64a()
	fmt.Fprintf(h, "%d:%s:%s", seed, id, profileKey(p))
	return rand.NewSource(int64(h.Sum64()))
}

// profileKey 画像规范键（%.17g 精确往返——哈希键无浮点歧义）。
func profileKey(p Profile) string {
	return fmt.Sprintf("age=%d;pat=%.17g;agg=%.17g;turns=%d", p.Age, p.Patience, p.Aggression, p.Turns)
}

// kindSequence 话轮类别序列：四类边界行为配额（画像纯函数，越界截断按
// interrupt→attack→offtopic→repeat 优先序——interrupt 首位保证打断频率对耐心
// 的单调性不受截断影响）+ 常规补足，再经种子洗牌（分布自然、计数不变）。
func kindSequence(p Profile, rng *rand.Rand) []string {
	seq := make([]string, 0, p.Turns)
	rem := p.Turns
	add := func(kind string, want int) {
		if want > rem {
			want = rem
		}
		for j := 0; j < want; j++ {
			seq = append(seq, kind)
		}
		rem -= want
	}
	add(KindInterrupt, int((1-p.Patience)*float64(p.Turns)*quotaInterrupt))
	add(KindAttack, int(p.Aggression*float64(p.Turns)*quotaAttack))
	add(KindOffTopic, int(float64(ageMax-p.Age)/float64(ageMax-ageMin)*float64(p.Turns)*quotaOffTopic))
	add(KindRepeat, int((1-p.Patience)*float64(p.Turns)*quotaRepeat))
	for len(seq) < p.Turns {
		seq = append(seq, KindNormal)
	}
	rng.Shuffle(len(seq), func(i, j int) { seq[i], seq[j] = seq[j], seq[i] })
	return seq
}

// turnRunes 第 i 话轮（0 起，共 n）的目标长度：lenMin+(lenMax-lenMin)*patience*w_i
// （w_i=(i+1)/n 位置权重）——耐心的纯函数（与种子无关：同画像跨种子同长度序列，
// 耐心↑→逐位长度单调不降→均值单调不降——参数真控行为的实现面）。
func turnRunes(p Profile, i, n int) int {
	w := float64(i+1) / float64(n)
	return lenMin + int(math.Round(float64(lenMax-lenMin)*p.Patience*w))
}

// speechMs 话语时长（模拟器时钟）：基数+每 rune 时长（有界——回放侧话轮
// 累计恒低于 FSM MaxTurnMs 上界）。
func speechMs(runes int) int64 {
	return int64(speechMsBase + speechMsRune*runes)
}

// composeText 按类别与精确 rune 数组句：类别前缀/词库随机取词，放不进的
// 词以单 rune 语气词收尾凑满——长度恒等于目标（话轮长度=画像纯函数的实现面）。
func composeText(kind string, want int, rng *rand.Rand) string {
	out := make([]rune, 0, want)
	trunc := func(s string) {
		r := []rune(s)
		if len(r) > want-len(out) {
			r = r[:want-len(out)]
		}
		out = append(out, r...)
	}
	switch kind {
	case KindInterrupt:
		trunc(pick(rng, interruptPrefixes))
	case KindRepeat:
		trunc(pick(rng, repeatPrefixes))
	case KindAttack:
		trunc(pick(rng, attackBank))
	case KindOffTopic:
		trunc(pick(rng, offTopicBank))
	}
	bank := fillBank(kind)
	for len(out) < want {
		w := []rune(pick(rng, bank))
		if len(w) <= want-len(out) {
			out = append(out, w...)
			continue
		}
		out = append(out, []rune(pick(rng, particles))...) // 单 rune 语气词兜底
	}
	return string(out)
}

// fillBank 各类别的填充词库（前缀耗尽后的主体词）。
func fillBank(kind string) []string {
	switch kind {
	case KindOffTopic:
		return offTopicBank
	case KindAttack:
		return attackBank
	default: // normal / interrupt / repeat：常规词库
		return normalBank
	}
}

func pick(rng *rand.Rand, bank []string) string { return bank[rng.Intn(len(bank))] }

// ---- 词库（儿童话术面；无危机词表命中——安全注入由 journeys 剧本层承担） ----

var (
	normalBank = []string{
		"我要", "小熊", "故事", "为什么", "机器人", "今天", "明天", "学校", "朋友",
		"开心", "饼干", "恐龙", "游戏", "唱歌", "太阳", "月亮", "星星", "小狗",
		"小猫", "画画", "睡觉", "吃饭", "喝水", "好玩", "厉害", "讲", "听", "看", "抱",
	}
	offTopicBank = []string{
		"对了", "你看", "飞机", "汽车", "动画片", "挖掘机", "冰淇淋", "小兔子",
		"奥特曼", "蝴蝶", "云朵", "彩虹",
	}
	attackBank        = []string{"我讨厌你", "走开", "不听", "坏机器人", "哼", "不理你", "讨厌"}
	interruptPrefixes = []string{"等一下", "听我说", "我先说", "停一下"}
	repeatPrefixes    = []string{"再讲一遍", "还要听", "再来一次"}
	particles         = []string{"呀", "啊", "哦", "嘛", "呢"}
)
