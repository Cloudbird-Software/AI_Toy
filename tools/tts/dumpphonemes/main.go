// dumpphonemes —— ChinesePhonemizer 调试/对拍工具：stdin 每行一句，stdout
// 输出 JSON 数组（text + 音素符号序列 + 声调 + 语言 id）。
// 用途：与 Python 参考 g2p（melo chinese_mix）逐句对拍，见 reports/eval/T13/。
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/Cloudbird-Software/AI_Toy/packages/go/tts"
)

type entry struct {
	Text    string   `json:"text"`
	Symbols []string `json:"symbols"`
	Tones   []int64  `json:"tones"`
	Langs   []int64  `json:"langs"`
	Error   string   `json:"error,omitempty"`
}

func main() {
	p := tts.NewChinesePhonemizer()
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var out []entry
	for sc.Scan() {
		text := sc.Text()
		if text == "" {
			continue
		}
		e := entry{Text: text}
		ph, err := p.Phonemize(text)
		if err != nil {
			e.Error = err.Error()
			out = append(out, e)
			continue
		}
		for _, id := range ph.Tokens {
			if int(id) < len(tts.SymbolsForDump()) {
				e.Symbols = append(e.Symbols, tts.SymbolsForDump()[id])
			}
		}
		e.Tones, e.Langs = ph.Tones, ph.LangIDs
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "stdin:", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		os.Exit(1)
	}
}
