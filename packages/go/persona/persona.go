// Package persona —— T8 人格编译器（M2，IR #93 / m2-spec §6 包契约 E）。
//
// 纯 Go 确定性编译：人格卡（YAML schema 对齐 assets-packs/<role>/persona）
// 进 → 约束集（SystemSeg/Lexicon/Sampling/Hash）出，同卡同产物同哈希
// （T8-G1-01）。产物=Responder 注入面：SystemSeg 进 Responder 上下文、
// Apply 过滤输出文本（M2 loop 的 Responder 仍是测试桩接口——本包只定义
// 输出类型、不接线 loop，ADR-0004；BAML 提示层纯落盘 baml/prompts，Go 侧
// 零接线，ADR-0005）。
//
// 确定性构造：Big5 按 canonical 维序遍历（map 迭代序不进入任何产物）、
// 词表过滤最长优先且同长字典序、口癖注入按文本哈希确定性选样。
//
// 错误语义：仅 Load/Compile 校验返回 error（越界输入拒绝）；Apply 无错
// 不 panic。依赖纪律：import 白名单=标准库+gopkg.in/yaml.v3（既有）。
package persona

import (
	"bytes"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Big5 五维键（O/C/E/A/N，值域 [1,5]）。
const (
	DimOpenness          = "O"
	DimConscientiousness = "C"
	DimExtraversion      = "E"
	DimAgreeableness     = "A"
	DimNeuroticism       = "N"
)

// big5Dims canonical 维序——SystemSeg 与哈希遍历均按此序（勿改序：哈希
// 稳定性依赖；map 迭代序不进入产物）。
var big5Dims = [...]string{DimOpenness, DimConscientiousness, DimExtraversion, DimAgreeableness, DimNeuroticism}

// dimCN 维度中文名（SystemSeg 展示）。
var dimCN = map[string]string{
	DimOpenness:          "开放性",
	DimConscientiousness: "尽责性",
	DimExtraversion:      "外向性",
	DimAgreeableness:     "宜人性",
	DimNeuroticism:       "神经质",
}

// bandDescTable 维度×三档语气描述（Big5→语气描述面；档位随值同向递进——
// T8 属性「卡维度单调调→SystemSeg 描述同向单调」的落点）。
var bandDescTable = map[string][3]string{
	DimOpenness:          {"偏低：偏好熟悉的话题与固定流程", "中等：愿意尝试新话题", "偏高：好奇爱想象，主动引入新点子"},
	DimConscientiousness: {"偏低：随性，节奏松散", "中等：有基本章法", "偏高：守时守约，做事有始有终"},
	DimExtraversion:      {"偏低：安静内敛，话少而轻", "中等：亲和适度，不吵不闷", "偏高：热络爱表达，语气明快"},
	DimAgreeableness:     {"偏低：直率，少迁就", "中等：礼让均衡", "偏高：温和体贴，先共情再引导"},
	DimNeuroticism:       {"偏低：情绪平稳，处变不惊", "中等：情绪有波动但可控", "偏高：敏感细腻，需要安抚节奏"},
}

// 校验哨兵（越界输入拒绝面——Load/Compile 同一套）。
var (
	ErrEmptyID          = errors.New("persona: 卡 ID 为空")
	ErrBig5Incomplete   = errors.New("persona: Big5 五维不全（须 O/C/E/A/N 齐备）")
	ErrBig5UnknownDim   = errors.New("persona: Big5 含未知维度键（只许 O/C/E/A/N）")
	ErrBig5OutOfRange   = errors.New("persona: Big5 维度值越界（须 ∈[1,5] 的有限值）")
	ErrTabooEmpty       = errors.New("persona: 禁忌词表为空（空禁忌=无安全编译检查面，拒绝）")
	ErrBadWord          = errors.New("persona: 词表含空串或省略号填充符（替换通道保留字）")
	ErrLexiconConflict  = errors.New("persona: 口癖与禁忌冲突（同一词不得既鼓励又禁止）")
	ErrClosenessInvalid = errors.New("persona: 亲密度设定越界（initial/max∈[0,1] 且 max≥initial、warmup_turns≥1）")
)

// ClosenessSettings 亲密度设定（T7 亲密维度的角色侧初值/上限与升温节奏；
// 编译进 SystemSeg 供 Responder 按角色人格升温）。
type ClosenessSettings struct {
	Initial     float64 `yaml:"initial"`      // 初始亲密度 [0,1]
	Max         float64 `yaml:"max"`          // 亲密度上限 [0,1]（≥Initial）
	WarmupTurns int     `yaml:"warmup_turns"` // 升温轮数（≥1：到上限的参考轮数）
}

// Card 人格卡（YAML schema 对齐 assets-packs/<role>/persona/persona.yaml 的
// 编译子集：Big5/口癖/语气规则/禁忌/价值观/亲密度；锚点例句归 LLM 评审
// rubric 面，不入编译）。
type Card struct {
	ID           string             `yaml:"id"`
	Big5         map[string]float64 `yaml:"big5"`         // O/C/E/A/N ∈[1,5]
	Catchphrases []string           `yaml:"catchphrases"` // 口癖表
	ToneRules    []string           `yaml:"tone_rules"`   // 语气规则（句长/感叹频率/称谓）
	Taboos       []string           `yaml:"taboos"`       // 禁忌词表（非空强制——人格边界=安全边界）
	Values       []string           `yaml:"values"`       // 价值观锚（话题偏好）
	Closeness    ClosenessSettings  `yaml:"closeness"`    // 亲密度设定
}

// Load 从 YAML 构造卡（严格解码：未知字段拒绝）并校验。
func Load(data []byte) (Card, error) {
	var c Card
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return Card{}, fmt.Errorf("persona: 卡 YAML 不可解析: %w", err)
	}
	if err := c.Validate(); err != nil {
		return Card{}, err
	}
	return c, nil
}

// Validate 校验卡合法域：ID 非空；Big5 五维齐且值域 [1,5]（非有限值拒绝）；
// 禁忌非空且无空串/省略号填充符（替换通道保留字）；口癖与禁忌不交；
// 亲密度设定值域自洽。
func (c Card) Validate() error {
	if c.ID == "" {
		return ErrEmptyID
	}
	for _, d := range big5Dims {
		v, ok := c.Big5[d]
		if !ok {
			return fmt.Errorf("%w: 缺 %s", ErrBig5Incomplete, d)
		}
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 1 || v > 5 {
			return fmt.Errorf("%w: %s=%v", ErrBig5OutOfRange, d, v)
		}
	}
	if len(c.Big5) > len(big5Dims) {
		return fmt.Errorf("%w: %d 个键", ErrBig5UnknownDim, len(c.Big5))
	}
	cp := make(map[string]bool, len(c.Catchphrases))
	for _, p := range c.Catchphrases {
		if p == "" {
			return fmt.Errorf("%w: 口癖空串", ErrBadWord)
		}
		cp[p] = true
	}
	if len(c.Taboos) == 0 {
		return ErrTabooEmpty
	}
	for _, w := range c.Taboos {
		if w == "" || strings.Contains(w, safeFillerUnit) {
			return fmt.Errorf("%w: 禁忌词 %q", ErrBadWord, w)
		}
		if cp[w] {
			return fmt.Errorf("%w: %q", ErrLexiconConflict, w)
		}
	}
	cl := c.Closeness
	if badFloat(cl.Initial) || cl.Initial < 0 || cl.Initial > 1 ||
		badFloat(cl.Max) || cl.Max < 0 || cl.Max > 1 || cl.Max < cl.Initial || cl.WarmupTurns < 1 {
		return fmt.Errorf("%w: initial=%v max=%v warmup_turns=%d", ErrClosenessInvalid, cl.Initial, cl.Max, cl.WarmupTurns)
	}
	return nil
}

func badFloat(v float64) bool { return math.IsNaN(v) || math.IsInf(v, 0) }

// Lexicon 词表约束取值。
const (
	LexiconEncourage int8 = 1  // 口癖（注入候选）
	LexiconForbid    int8 = -1 // 禁忌（命中→安全替换，读出面残留=0）
)

// safeFiller 禁忌命中的安全省略替换（Responder 输出的最后词表防线；主
// 拦截归 safety.Engine——M2 loop 组装面，本包不依赖）。
const safeFiller = "……"

// safeFillerUnit 替换通道保留字（禁忌词含它=替换后残留自身，校验拒绝）。
const safeFillerUnit = "…"

// samplingDomain 采样参数声明域（P1 有界性断言面；Responder 消费契约）。
var samplingDomain = map[string][2]float64{
	"temperature":       {0.2, 1.2},
	"top_p":             {0.65, 0.9},
	"frequency_penalty": {0.0, 0.8},
	"presence_penalty":  {0.0, 0.3},
}

// ConstraintSet 编译产物（Responder 注入面）：SystemSeg 进上下文、Lexicon/
// Apply 过滤输出文本、Sampling 供采样参数下发。Hash=规范化卡内容的
// fnv64a（同卡同哈希；卡变更 diff 可经哈希对齐）。
type ConstraintSet struct {
	SystemSeg string
	Lexicon   map[string]int8
	Sampling  map[string]float64
	Hash      string

	// bands 各维语气带 0/1/2（同包属性断言面；与 SystemSeg 描述一一对应）。
	bands map[string]int
}

// Compile 编译人格卡为约束集（纯函数：无 IO/时钟/随机源——同卡同产物同
// 哈希）。非法卡（越界/缺维/空禁忌/冲突）返回哨兵错误。
func Compile(c Card) (*ConstraintSet, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &ConstraintSet{
		SystemSeg: buildSystemSeg(c),
		Lexicon:   lexiconOf(c),
		Sampling:  samplingOf(c.Big5),
		Hash:      hashCard(c),
		bands:     bandsOf(c.Big5),
	}, nil
}

// Apply 对 Responder 输出文本施词表约束：禁忌词命中→安全省略替换（最长
// 优先，读出面残留=0）；口癖注入位——文本不含任何口癖时按文本哈希确定性
// 选一条拼接（已含则不注入，防复读）。无错不 panic；nil 接收者透传。
func (cs *ConstraintSet) Apply(text string) string {
	if cs == nil || text == "" {
		return text
	}
	taboo := make([]string, 0, len(cs.Lexicon))
	for w, s := range cs.Lexicon {
		if s == LexiconForbid {
			taboo = append(taboo, w)
		}
	}
	sort.Slice(taboo, func(i, j int) bool { // 最长优先（rune 数）；同长字典序——确定性
		ri, rj := []rune(taboo[i]), []rune(taboo[j])
		if len(ri) != len(rj) {
			return len(ri) > len(rj)
		}
		return taboo[i] < taboo[j]
	})
	out := text
	for _, w := range taboo {
		out = strings.ReplaceAll(out, w, safeFiller)
	}
	return cs.injectCatchphrase(out)
}

// injectCatchphrase 口癖注入位：无口癖表→透传；已含口癖→原样（防复读）；
// 否则按 fnv64a(文本) 选样拼接——同文本同口癖（确定性），尾句标点直接接、
// 无标点补逗号衔接。
func (cs *ConstraintSet) injectCatchphrase(text string) string {
	phrases := make([]string, 0, len(cs.Lexicon))
	for w, s := range cs.Lexicon {
		if s == LexiconEncourage {
			phrases = append(phrases, w)
		}
	}
	if len(phrases) == 0 {
		return text
	}
	for _, p := range phrases {
		if strings.Contains(text, p) {
			return text
		}
	}
	sort.Strings(phrases)                             // 选样池定序（map 迭代序不进入选样）
	pick := phrases[fnvOf(text)%uint64(len(phrases))] // 先取模再转索引：uint64 直转 int 可溢出为负
	if endsWithSentencePunct(text) {
		return text + pick
	}
	return text + "，" + pick
}

func endsWithSentencePunct(s string) bool {
	r := []rune(s)
	if len(r) == 0 {
		return false
	}
	switch r[len(r)-1] {
	case '。', '！', '？', '!', '?', '.', '…', '~', '～':
		return true
	}
	return false
}

// toneBand 维度值→三档语气带（0 低 / 1 中 / 2 高；归一 u=(v−1)/4）。
func toneBand(v float64) int {
	switch u := (v - 1) / 4; {
	case u < 1.0/3.0:
		return 0
	case u < 2.0/3.0:
		return 1
	default:
		return 2
	}
}

func bandsOf(big5 map[string]float64) map[string]int {
	out := make(map[string]int, len(big5Dims))
	for _, d := range big5Dims {
		out[d] = toneBand(big5[d])
	}
	return out
}

// buildSystemSeg 系统人格段（Big5→语气描述+语气规则+口癖+价值观+亲密度+
// 禁忌，全部字段按固定序拼接——确定性）。
func buildSystemSeg(c Card) string {
	var b strings.Builder
	fmt.Fprintf(&b, "【角色：%s】\n", c.ID)
	b.WriteString("性格基调（大五，1–5 分制）：\n")
	for _, d := range big5Dims {
		fmt.Fprintf(&b, "- %s(%s) %.4g（%s）\n", dimCN[d], d, c.Big5[d], bandDescTable[d][toneBand(c.Big5[d])])
	}
	for _, sec := range []struct {
		head  string
		lines []string
	}{
		{"语气规则：", c.ToneRules},
		{"口癖（自然带出，勿机械复读）：", c.Catchphrases},
		{"价值观锚（话题偏好）：", c.Values},
		{"禁忌（任何情况下绝不说出、绝不引导）：", c.Taboos},
	} {
		if len(sec.lines) == 0 {
			continue
		}
		b.WriteString(sec.head + "\n")
		for _, line := range sec.lines {
			b.WriteString("- " + line + "\n")
		}
	}
	fmt.Fprintf(&b, "亲密度设定：初始 %.4g，上限 %.4g，约 %d 轮升温到位\n",
		c.Closeness.Initial, c.Closeness.Max, c.Closeness.WarmupTurns)
	return b.String()
}

func lexiconOf(c Card) map[string]int8 {
	out := make(map[string]int8, len(c.Catchphrases)+len(c.Taboos))
	for _, p := range c.Catchphrases {
		out[p] = LexiconEncourage
	}
	for _, w := range c.Taboos {
		out[w] = LexiconForbid
	}
	return out
}

// samplingOf Big5 确定性派生采样参数（系数同向：E/O↑→temperature↑、
// C↑→frequency_penalty↑、A↑→presence_penalty↑、N↑→top_p↓；temperature
// clamp 进声明域——截断只压平不反向，单调性保持）。
func samplingOf(big5 map[string]float64) map[string]float64 {
	u := func(d string) float64 { return (big5[d] - 1) / 4 }
	return map[string]float64{
		"temperature":       clampf(0.2+0.6*u(DimExtraversion)+0.4*u(DimOpenness)-0.2*u(DimNeuroticism), samplingDomain["temperature"][0], samplingDomain["temperature"][1]),
		"top_p":             0.9 - 0.25*u(DimNeuroticism),
		"frequency_penalty": 0.8 * u(DimConscientiousness),
		"presence_penalty":  0.3 * u(DimAgreeableness),
	}
}

func clampf(x, lo, hi float64) float64 {
	switch {
	case x < lo:
		return lo
	case x > hi:
		return hi
	default:
		return x
	}
}

// canonicalCard 规范化卡内容（长度前缀编码防定界符碰撞；Big5 按固定维序、
// 列表按卡序——同值卡同串，与 map 迭代序无关）。
func canonicalCard(c Card) string {
	var b strings.Builder
	w := func(s string) {
		b.WriteString(strconv.Itoa(len(s)))
		b.WriteByte(':')
		b.WriteString(s)
	}
	w(c.ID)
	for _, d := range big5Dims {
		w(d)
		w(strconv.FormatFloat(c.Big5[d], 'g', -1, 64))
	}
	for _, list := range [][]string{c.Catchphrases, c.ToneRules, c.Taboos, c.Values} {
		w(strconv.Itoa(len(list)))
		for _, s := range list {
			w(s)
		}
	}
	w(strconv.FormatFloat(c.Closeness.Initial, 'g', -1, 64))
	w(strconv.FormatFloat(c.Closeness.Max, 'g', -1, 64))
	w(strconv.Itoa(c.Closeness.WarmupTurns))
	return b.String()
}

func hashCard(c Card) string {
	return fmt.Sprintf("%016x", fnvOf(canonicalCard(c)))
}

func fnvOf(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s)) // hash.Write 契约：永不返回错误
	return h.Sum64()
}
