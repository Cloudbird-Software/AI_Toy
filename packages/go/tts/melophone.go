// melophone —— MeloTTS-Chinese（ZH_MIX_EN）前端的 Go 最小实现：查表法 g2p。
//
// 数据由 tools/tts/gen_melo_phoneme_table.py 生成（melophone_table.go）：
// pypinyin 单字最常用读音 × opencpop-strict 拼音→音素 × config.json symbols。
//
// 诚实局限（ADR-0008 债务表，非静默）：
//   - 多音字取最常用读，无上下文消歧（如「银行」读 xíng）；
//   - 一/不/三声变调（sandhi）未移植——声调为字典本调；
//   - 英文单词不读出（UNK 占位）；数字逐位读（无十百千位级规则）；
//   - mBERT 韵律特征（JaBert）恒零——韵律降质可听，特征供给是 M2 债务。
//
// 与 Python 参考前端的逐句对拍记录见 reports/eval/T13/。
package tts

import (
	"fmt"
	"strings"
)

// meloLangIDZHMixEn ZH_MIX_EN 语言 id（上游 symbols.py language_id_map）。
const meloLangIDZHMixEn = 3

// meloPadID add_blank 间隔符（symbols[0]=="_"）。
const meloPadID = int64(0)

// init 建 symbol→id 索引（生成表保证无重复——config.json 真源）。
var meloSymbolID = func() map[string]int64 {
	m := make(map[string]int64, len(meloSymbols))
	for i, s := range meloSymbols {
		m[s] = int64(i)
	}
	return m
}()

// repMap 单字标点归一（上游 chinese_mix.rep_map 镜像；多字 "..."→"…" 在
// normalize 先行）。全角 ASCII 区（！～）由范围转换统一收半角，不在此表。
var repMap = map[rune]string{
	'，': ",", '。': ".", '！': "!", '？': "?", '、': ",", '·': ",",
	'$': ".", '“': "'", '”': "'", '‘': "'", '’': "'",
	'（': "'", '）': "'", '《': "'", '》': "'", '【': "'", '】': "'",
	'「': "'", '」': "'", '—': "-", '～': "-",
	'\n': ".", '：': ",", '〇': "零",
}

// meloPuncts 音素面标点（上游 punctuation 表）。
var meloPuncts = map[rune]bool{'!': true, '?': true, '…': true, ',': true,
	'.': true, '\'': true, '-': true}

// digitHan 数字逐位读法（无位级规则——ADR-0008 债务）。
var digitHan = map[rune]rune{'0': '零', '1': '一', '2': '二', '3': '三',
	'4': '四', '5': '五', '6': '六', '7': '七', '8': '八', '9': '九'}

// ChinesePhonemizer 查表前端（Phonemizer 实现）。
type ChinesePhonemizer struct{}

// NewChinesePhonemizer 构造（无状态；显式构造以便装配面可读）。
func NewChinesePhonemizer() *ChinesePhonemizer { return &ChinesePhonemizer{} }

// Phonemize 文本→音素序列（ZH_MIX_EN 规约）。永不 panic：表外字/表外读音
// →UNK（可听失败面，不吞字不崩溃；空序列由调用方按空文本裁决）。
func (p *ChinesePhonemizer) Phonemize(text string) (MeloPhonemes, error) {
	norm := normalizeMeloText(text)
	var tokens, tones, langs []int64
	push := func(sym string, tone int64) {
		id, ok := meloSymbolID[sym]
		if !ok { // 生成表与 symbols 真源失配——防御（不可达，表同源生成）
			id = meloSymbolID["UNK"]
		}
		tokens = append(tokens, id)
		tones = append(tones, tone)
		langs = append(langs, meloLangIDZHMixEn)
	}
	pushHan := func(r rune) {
		py, ok := meloCharPinyin[r]
		if !ok || len(py) < 2 {
			push("UNK", 0)
			return
		}
		tone := int64(py[len(py)-1] - '0')
		phones, ok := meloPinyinPhones[py[:len(py)-1]]
		if !ok || tone < 1 || tone > 5 {
			push("UNK", 0)
			return
		}
		// 音节每个子音素同调（上游 _g2p：tones_list += [tone]*len(phone)）
		for _, sym := range strings.Fields(phones) {
			push(sym, tone)
		}
	}
	for _, r := range norm {
		switch {
		case r == ' ':
			continue // 空格不进音素面（pad 符承担间隔）
		case r >= '0' && r <= '9':
			pushHan(digitHan[r]) // 逐位读（位级规则债务）
		case isMeloHan(r):
			pushHan(r)
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			push("UNK", 0) // 英文面未移植（债务）——可听占位
		case meloPuncts[r]:
			push(string(r), 0)
		default:
			continue // 其余字符剔除（上游 replace_punctuation 同语义）
		}
	}
	if len(tokens) == 0 {
		return MeloPhonemes{}, nil
	}
	// add_blank：pad 包夹每音素（commons.intersperse 语义 [0,a,0,b,0]）
	n := len(tokens)
	tk := make([]int64, 0, 2*n+1)
	tn := make([]int64, 0, 2*n+1)
	lg := make([]int64, 0, 2*n+1)
	tk, tn, lg = append(tk, meloPadID), append(tn, 0), append(lg, meloLangIDZHMixEn)
	for i := 0; i < n; i++ {
		tk = append(tk, tokens[i], meloPadID)
		tn = append(tn, tones[i], 0)
		lg = append(lg, langs[i], meloLangIDZHMixEn)
	}
	return MeloPhonemes{Tokens: tk, Tones: tn, LangIDs: lg}, nil
}

// normalizeMeloText 归一：嗯/呣替换 + "..." 折叠 + 单字 repMap + 全角 ASCII
// 收半角。比上游窄：仅覆盖中文内容面所需映射。
func normalizeMeloText(s string) string {
	s = strings.ReplaceAll(s, "嗯", "恩")
	s = strings.ReplaceAll(s, "呣", "母")
	s = strings.ReplaceAll(s, "...", "…")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if rep, ok := repMap[r]; ok {
			b.WriteString(rep)
			continue
		}
		if r >= '！' && r <= '～' { // 全角 ASCII 区
			r = r - '！' + '!'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isMeloHan CJK 表意字判定（覆盖生成表扫描范围 + 兼容区）。
func isMeloHan(r rune) bool {
	return (r >= 0x3400 && r < 0xA000) || (r >= 0xF900 && r < 0xFB00)
}

// SymbolsForDump 音素表只读视图（tools/tts/dumpphonemes 对拍工具用；产品
// 代码不需要——内部经 meloSymbolID 索引）。
func SymbolsForDump() []string { return meloSymbols }

// meloSymbolIDMust 测试面：symbol→id（缺表 panic——测试期希望炸出来）。
func meloSymbolIDMust(sym string) int64 {
	id, ok := meloSymbolID[sym]
	if !ok {
		panic(fmt.Sprintf("tts: symbol %q not in table", sym))
	}
	return id
}
