// T8 表驱动单测（m2-spec §6 包契约 E）：Load/校验（越界输入拒绝）、Compile
// 产物（SystemSeg/Lexicon/Sampling 声明域）、Apply（禁忌替换/口癖注入位/
// 防复读/确定性）。口径来源 docs/gates/assets/T8.md 属性行 + m2-spec §6。
package persona

import (
	"errors"
	"strings"
	"testing"
)

// validCardYAML 合法人格卡（夜间安抚角色，schema 对齐 assets-packs/<role>/persona）。
const validCardYAML = `
id: night-bear
big5:
  O: 3.5
  C: 4.2
  E: 1.8
  A: 4.6
  N: 1.5
catchphrases:
  - "呼——呼——，月亮升起来啦"
  - "把眼睛交给小熊保管吧"
tone_rules:
  - 句子短，一次只说一件事
  - 少用感叹号，多用轻声词
taboos:
  - 去死
  - 蠢货
  - 滚出去
  - 鬼故事
values:
  - 睡前故事（小动物、星星、月亮）
  - 今天发生的三件小事
closeness:
  initial: 0.2
  max: 0.9
  warmup_turns: 12
`

// cloneCard 深拷贝（map/slice 重建——确定性属性用「重建值等卡」通道）。
func cloneCard(c Card) Card {
	out := Card{
		ID:           c.ID,
		Big5:         make(map[string]float64, len(c.Big5)),
		Catchphrases: append([]string(nil), c.Catchphrases...),
		ToneRules:    append([]string(nil), c.ToneRules...),
		Taboos:       append([]string(nil), c.Taboos...),
		Values:       append([]string(nil), c.Values...),
		Closeness:    c.Closeness,
	}
	for k, v := range c.Big5 {
		out.Big5[k] = v
	}
	return out
}

func TestLoadValidCard(t *testing.T) {
	c, err := Load([]byte(validCardYAML))
	if err != nil {
		t.Fatalf("Load 合法卡: %v", err)
	}
	if c.ID != "night-bear" || len(c.Catchphrases) != 2 || len(c.Taboos) != 4 {
		t.Fatalf("Load 字段不对: %+v", c)
	}
	if c.Big5[DimExtraversion] != 1.8 || c.Closeness.WarmupTurns != 12 {
		t.Fatalf("Load 数值字段不对: big5=%v closeness=%+v", c.Big5, c.Closeness)
	}
}

func TestLoadRejects(t *testing.T) {
	replace := func(old, new string) string {
		return strings.Replace(validCardYAML, old, new, 1)
	}
	cases := []struct {
		name    string
		yaml    string
		wantErr error // nil=仅断言非 nil 错误（如 YAML 语法/严格解码错误）
	}{
		{"坏 YAML 语法", "id: [unclosed", nil},
		{"未知顶层字段（严格解码）", validCardYAML + "\nextra_field: 1\n", nil},
		{"ID 为空", replace("id: night-bear", "id: \"\""), ErrEmptyID},
		{"Big5 缺维", replace("  N: 1.5\n", "\n"), ErrBig5Incomplete},
		{"Big5 多余维", replace("  N: 1.5\n", "  N: 1.5\n  X: 3\n"), ErrBig5UnknownDim},
		{"Big5 越界下限", replace("E: 1.8", "E: 0.5"), ErrBig5OutOfRange},
		{"Big5 越界上限", replace("E: 1.8", "E: 5.5"), ErrBig5OutOfRange},
		{"Big5 NaN", replace("E: 1.8", "E: .nan"), ErrBig5OutOfRange},
		{"禁忌表为空", replace("taboos:\n  - 去死\n  - 蠢货\n  - 滚出去\n  - 鬼故事\n", "taboos: []\n"), ErrTabooEmpty},
		{"禁忌含空串", replace("  - 去死\n", "  - \"\"\n"), ErrBadWord},
		{"禁忌含省略号填充符", replace("  - 去死\n", "  - 去…死\n"), ErrBadWord},
		{"口癖含空串", replace("  - \"呼——呼——，月亮升起来啦\"\n", "  - \"\"\n"), ErrBadWord},
		{"口癖与禁忌冲突", replace("  - \"呼——呼——，月亮升起来啦\"\n", "  - 去死\n"), ErrLexiconConflict},
		{"亲密度 initial 越界", replace("initial: 0.2", "initial: 1.2"), ErrClosenessInvalid},
		{"亲密度 max 越界", replace("max: 0.9", "max: 1.5"), ErrClosenessInvalid},
		{"亲密度上限低于初始", replace("max: 0.9", "max: 0.1"), ErrClosenessInvalid},
		{"亲密度升温轮数 0", replace("warmup_turns: 12", "warmup_turns: 0"), ErrClosenessInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("Load 未拒绝：%s", tc.name)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("Load 错误类型不对：got %v want %v", err, tc.wantErr)
			}
		})
	}
}

func validCard(t *testing.T) Card {
	t.Helper()
	c, err := Load([]byte(validCardYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

func TestCompileSystemSeg(t *testing.T) {
	cs, err := Compile(validCard(t))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	s := cs.SystemSeg
	for _, want := range []string{
		"【角色：night-bear】",
		"开放性", "尽责性", "外向性", "宜人性", "神经质", // 五维全展示
		"安静内敛，话少而轻",   // E=1.8 → 低档描述
		"温和体贴，先共情再引导", // A=4.6 → 高档描述
		"呼——呼——，月亮升起来啦",
		"句子短，一次只说一件事",
		"睡前故事（小动物、星星、月亮）",
		"亲密度设定：初始 0.2，上限 0.9，约 12 轮升温到位",
		"去死",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("SystemSeg 缺 %q：\n%s", want, s)
		}
	}
	if strings.Contains(s, "热络爱表达") {
		t.Fatalf("SystemSeg 外向性档位描述错（E=1.8 不得出现高档描述）：\n%s", s)
	}
}

func TestCompileLexicon(t *testing.T) {
	cs, err := Compile(validCard(t))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(cs.Lexicon) != 2+4 { // 口癖 2 + 禁忌 4，恰好覆盖
		t.Fatalf("Lexicon 规模 %d != 6: %v", len(cs.Lexicon), cs.Lexicon)
	}
	for _, p := range []string{"呼——呼——，月亮升起来啦", "把眼睛交给小熊保管吧"} {
		if cs.Lexicon[p] != LexiconEncourage {
			t.Fatalf("口癖 %q 应为 LexiconEncourage", p)
		}
	}
	for _, w := range []string{"去死", "蠢货", "滚出去", "鬼故事"} {
		if cs.Lexicon[w] != LexiconForbid {
			t.Fatalf("禁忌 %q 应为 LexiconForbid", w)
		}
	}
}

func TestCompileSamplingDomain(t *testing.T) {
	cs, err := Compile(validCard(t))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(cs.Sampling) != len(samplingDomain) {
		t.Fatalf("Sampling 键集 %d != 声明域 %d: %v", len(cs.Sampling), len(samplingDomain), cs.Sampling)
	}
	for k, v := range cs.Sampling {
		dom, ok := samplingDomain[k]
		if !ok {
			t.Fatalf("Sampling 未声明参数 %q", k)
		}
		if v < dom[0] || v > dom[1] {
			t.Fatalf("Sampling[%s]=%.4g 越声明域 [%.2f,%.2f]", k, v, dom[0], dom[1])
		}
	}
	// 手算锚点：uE=0.2 uO=0.625 uN=0.125 → temperature=0.2+0.12+0.25−0.025=0.545
	if got := cs.Sampling["temperature"]; got < 0.544 || got > 0.546 {
		t.Fatalf("temperature=%.4g want≈0.545（Big5 确定性派生）", got)
	}
	if len(cs.Hash) != 16 {
		t.Fatalf("Hash 长度 %d != 16: %q", len(cs.Hash), cs.Hash)
	}
}

func TestApplyTabooReplace(t *testing.T) {
	cs, err := Compile(validCard(t))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	in := "你这个蠢货，快滚出去，讲个鬼故事吧，说去死也一样"
	out := cs.Apply(in)
	for _, taboo := range []string{"蠢货", "滚出去", "鬼故事", "去死"} {
		if strings.Contains(out, taboo) {
			t.Fatalf("taboo %q 残留：%q", taboo, out)
		}
	}
	if !strings.Contains(out, safeFiller) {
		t.Fatalf("替换后应含安全省略符：%q", out)
	}
	if !strings.HasPrefix(out, "你这个") {
		t.Fatalf("非禁忌前缀应保留：%q", out)
	}
}

func TestApplyCatchphraseInjection(t *testing.T) {
	cs, err := Compile(validCard(t))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	// 无尾标点：主体保留 + 逗号衔接口癖
	out := cs.Apply("今天月亮很圆")
	body := "今天月亮很圆"
	if !strings.HasPrefix(out, body) {
		t.Fatalf("主体被改：%q", out)
	}
	injected := strings.TrimPrefix(out, body)
	if !strings.HasPrefix(injected, "，") || len(injected) <= len("，") {
		t.Fatalf("无尾标点应「，+口癖」注入：%q", out)
	}
	// 尾标点：直接追加口癖（不再补逗号）
	out2 := cs.Apply("睡觉吧。")
	if !strings.HasPrefix(out2, "睡觉吧。") || strings.Contains(out2, "睡觉吧。，") {
		t.Fatalf("尾标点注入位衔接错：%q", out2)
	}
	// 已含口癖：防复读，不重复注入
	has := "呼——呼——，月亮升起来啦，我们睡觉吧"
	if got := cs.Apply(has); got != has {
		t.Fatalf("已含口癖应原样返回：got %q", got)
	}
	// 确定性：同文本同输出
	for i := 0; i < 32; i++ {
		if cs.Apply(body) != out {
			t.Fatalf("Apply 注入不确定：%q vs %q", cs.Apply(body), out)
		}
	}
}

func TestApplyDegenerateInput(t *testing.T) {
	cs, err := Compile(validCard(t))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if got := cs.Apply(""); got != "" {
		t.Fatalf("空文本应原样返回：%q", got)
	}
	var nilCS *ConstraintSet
	if got := nilCS.Apply("任意文本"); got != "任意文本" {
		t.Fatalf("nil 约束集应透传：%q", got)
	}
	// 无口癖卡：注入位退化为纯过滤（不 panic、主体保留）
	noCp := cloneCard(validCard(t))
	noCp.Catchphrases = nil
	cs2, err := Compile(noCp)
	if err != nil {
		t.Fatalf("Compile 无口癖卡: %v", err)
	}
	if got := cs2.Apply("晚安，小朋友"); got != "晚安，小朋友" {
		t.Fatalf("无口癖卡注入位应透传：%q", got)
	}
}
