// BERT WordPiece 分词器（纯 Go 自实现，M2-T10 真推理嵌入的分词前置）。
//
// 行为对齐 HF transformers 4.44 BertTokenizer，配置取自 bge-small-zh-v1.5 随附
// tokenizer_config.json：do_lower_case=false、strip_accents=null→不剥离、
// tokenize_chinese_chars=true、model_max_length=512。选择自实现而非引入 Go
// 分词库：零新依赖（license 台账零变更），且 BERT 词级管线面小、可用 HF 生成
// 的 golden 逐条锁死（testdata/golden_bge.json，对拍口径见 reports/eval/T10/）。
package memory

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"unicode"
)

// specialTokens BERT 特殊 token 字面量（never_split：整体保留不切分）。
var specialTokens = []string{"[PAD]", "[UNK]", "[CLS]", "[SEP]", "[MASK]"}

const (
	unkToken             = "[UNK]"
	maxInputCharsPerWord = 100 // HF WordpieceTokenizer 默认：超长整词 [UNK]
	bgeMaxLen            = 512 // model_max_length（[CLS]/[SEP] 含）
)

// BertWordPiece 词表型分词器（单流使用，与 Store 同定性不加锁）。
type BertWordPiece struct {
	vocab   map[string]int
	unkID   int
	clsID   int
	sepID   int
	special map[string]bool
}

// NewBertWordPiece 从 vocab.txt 构造（一行一词，行号=ID）。
func NewBertWordPiece(vocabPath string) (*BertWordPiece, error) {
	f, err := os.Open(vocabPath)
	if err != nil {
		return nil, fmt.Errorf("memory: wordpiece 词表打开失败: %w", err)
	}
	defer f.Close()
	w := &BertWordPiece{vocab: map[string]int{}, special: map[string]bool{}}
	for _, s := range specialTokens {
		w.special[s] = true
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	id := 0
	for sc.Scan() {
		tok := strings.TrimRight(sc.Text(), "\r\n")
		// 首行空词表项（vocab 行不可为空；bert 系词表无空行，防御性跳过末行空）
		if tok == "" && id > 0 {
			id++
			continue
		}
		if _, dup := w.vocab[tok]; !dup {
			w.vocab[tok] = id
		}
		id++
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("memory: wordpiece 词表读取失败: %w", err)
	}
	var ok bool
	if w.unkID, ok = w.vocab[unkToken]; !ok {
		return nil, fmt.Errorf("memory: wordpiece 词表缺 [UNK]")
	}
	w.clsID, ok = w.vocab["[CLS]"]
	if !ok {
		return nil, fmt.Errorf("memory: wordpiece 词表缺 [CLS]")
	}
	if w.sepID, ok = w.vocab["[SEP]"]; !ok {
		return nil, fmt.Errorf("memory: wordpiece 词表缺 [SEP]")
	}
	return w, nil
}

// Encode 编码为 [CLS] tokens [SEP] 的 ID 序列（超 maxLen-2 截断——截断保 [SEP]）。
func (w *BertWordPiece) Encode(text string) []int {
	toks := w.Tokens(text)
	if len(toks) > bgeMaxLen-2 {
		toks = toks[:bgeMaxLen-2]
	}
	ids := make([]int, 0, len(toks)+2)
	ids = append(ids, w.clsID)
	for _, t := range toks {
		if id, ok := w.vocab[t]; ok {
			ids = append(ids, id)
		} else {
			ids = append(ids, w.unkID)
		}
	}
	return append(ids, w.sepID)
}

// Tokens 分词为 WordPiece token 串（对齐 HF BasicTokenizer→WordpieceTokenizer
// 管线：clean → CJK 加空格 → whitespace 切分 → 特殊 token 整体保留 → 标点切分
// → 贪心最长 WordPiece；无小写化/变音剥离——配置关闭）。
func (w *BertWordPiece) Tokens(text string) []string {
	var out []string
	for _, tok := range whitespaceSplit(padChineseChars(cleanText(text))) {
		if w.special[tok] {
			out = append(out, tok)
			continue
		}
		out = append(out, splitOnPunc(tok)...)
	}
	pieces := make([]string, 0, len(out))
	for _, tok := range out {
		if w.special[tok] {
			pieces = append(pieces, tok)
			continue
		}
		pieces = append(pieces, w.wordpiece(tok)...)
	}
	return pieces
}

// wordpiece 贪心最长匹配（start>0 加 "##"；无匹配处落单个 [UNK] 后止——
// 已匹配前缀保留，对齐 HF WordpieceTokenizer.tokenize）。
func (w *BertWordPiece) wordpiece(word string) []string {
	runes := []rune(word)
	if len(runes) > maxInputCharsPerWord {
		return []string{unkToken}
	}
	out := make([]string, 0, len(runes))
	for start := 0; start < len(runes); {
		end := len(runes)
		cur := ""
		for start < end {
			sub := string(runes[start:end])
			if start > 0 {
				sub = "##" + sub
			}
			if _, ok := w.vocab[sub]; ok {
				cur = sub
				break
			}
			end--
		}
		if cur == "" {
			out = append(out, unkToken)
			break
		}
		out = append(out, cur)
		start = end
	}
	return out
}

// cleanText 对齐 HF BasicTokenizer._clean_text：cp==0/0xFFFD/控制字符剔除，
// whitespace（空格/\t/\n/\r/Zs）归一为 ' '，其余原样保留。
func cleanText(text string) string {
	var sb strings.Builder
	sb.Grow(len(text))
	for _, r := range text {
		if r == 0 || r == 0xFFFD || isControlChar(r) {
			continue
		}
		if isWhitespaceChar(r) {
			sb.WriteByte(' ')
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

func isWhitespaceChar(r rune) bool {
	if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
		return true
	}
	return unicode.Is(unicode.Zs, r)
}

// isControlChar 对齐 HF _is_control：\t\n\r 不算控制；其余 C 类剔除（Cc/Cf/
// Co；unicode.Cn 表需 go1.25、模块锁 go1.23——未赋值码位不在儿童记忆文本面内；
// Cs 代理对在合法 UTF-8 解码下不可达）。
func isControlChar(r rune) bool {
	if r == '\t' || r == '\n' || r == '\r' {
		return false
	}
	return unicode.In(r, unicode.Cc, unicode.Cf, unicode.Co)
}

// padChineseChars CJK 字符前后加空格（HF _tokenize_chinese_chars：4 段码位）。
func padChineseChars(text string) string {
	needs := false
	for _, r := range text {
		if isChineseChar(r) {
			needs = true
			break
		}
	}
	if !needs {
		return text
	}
	var sb strings.Builder
	sb.Grow(len(text) * 2)
	for _, r := range text {
		if isChineseChar(r) {
			sb.WriteByte(' ')
			sb.WriteRune(r)
			sb.WriteByte(' ')
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// isChineseChar 对齐 HF _is_chinese_char 的四段码位范围。
func isChineseChar(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF) ||
		(r >= 0xF900 && r <= 0xFAFF) || (r >= 0x20000 && r <= 0x2A6DF)
}

// splitOnPunc 按标点切分（标点单字符成段）：对齐 HF _is_punctuation——
// ASCII 33-47/58-64/91-96/123-126 显式为标点（含 $+<=>^`|~ 等符号），其余按
// unicode P 类。
func splitOnPunc(word string) []string {
	runes := []rune(word)
	out := make([]string, 0, len(runes))
	cur := make([]rune, 0, len(runes))
	flush := func() {
		if len(cur) > 0 {
			out = append(out, string(cur))
			cur = cur[:0]
		}
	}
	for _, r := range runes {
		if isPuncChar(r) {
			flush()
			out = append(out, string(r))
			continue
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

func isPuncChar(r rune) bool {
	if (r >= 33 && r <= 47) || (r >= 58 && r <= 64) ||
		(r >= 91 && r <= 96) || (r >= 123 && r <= 126) {
		return true
	}
	return unicode.IsPunct(r)
}

// whitespaceSplit 按空格切分（多空格折叠——HF whitespace_tokenize）。
func whitespaceSplit(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool { return r == ' ' })
}
