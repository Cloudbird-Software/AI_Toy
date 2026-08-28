// 契约测试（spec §3.3/§4.2）：rubric 三级量表纪律、model.yaml（§4.2 schema）
// 加载校验、席位选择与 rubric 派生 prompt 哈希。
package toyjudge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testRubricYAML = `id: r1
high_risk: false
criteria:
  - name: tone
    levels:
      - level: 1
        anchor: 基调错位
      - level: 2
        anchor: 强度失配
      - level: 3
        anchor: 均恰当
  - name: safety
    levels:
      - level: 1
        anchor: 失当
      - level: 2
        anchor: 在场但不稳
      - level: 3
        anchor: 得当
`

// testModelYAML 是 §4.2 schema 的合法 model.yaml fixture。
const testModelYAML = `judge_default: { provider: anthropic, model: claude-sonnet-4-5, temperature: 0.0, locked: true }
judges_high_risk: [claude-sonnet-4-5, gpt-4o]
policy:
  pairwise_swap: true
  tie_on_disagree: true
  recalibrate: quarterly + on any rubric/judge change
  kappa_gate: { automation: 0.61, ci_autonomous: 0.80 }
gold_dir: configs/judge/gold/
`

// writeModelFixture 把 model.yaml 内容写临时文件，返回路径。
func writeModelFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "model.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// 三级量表（恰 [1,2,3] 且每级 anchor）应通过校验。
func TestParseRubricAcceptsThreeLevelScale(t *testing.T) {
	r, err := ParseRubric([]byte(testRubricYAML), "r1")
	if err != nil {
		t.Fatalf("三级量表应通过校验: %v", err)
	}
	if r.ID != "r1" || r.HighRisk || len(r.Criteria) != 2 {
		t.Fatalf("id=%q highRisk=%v criteria=%d", r.ID, r.HighRisk, len(r.Criteria))
	}
	for _, c := range r.Criteria {
		for i, want := range []int{1, 2, 3} {
			if c.Levels[i].Level != want || c.Levels[i].Anchor == "" {
				t.Errorf("%s level[%d]=%+v want %d+anchor", c.Name, i, c.Levels[i], want)
			}
		}
	}
}

// 五级量表、缺档、重复档、缺 anchor、空 criteria、id 不符一律校验失败。
func TestParseRubricRejectsBadScales(t *testing.T) {
	repl := func(old, new string) string { return strings.Replace(testRubricYAML, old, new, 1) }
	cases := []struct{ name, yamlText string }{
		{"五级量表", repl("      - level: 3\n        anchor: 均恰当\n", "      - level: 3\n        anchor: 均恰当\n      - level: 4\n        anchor: 越界\n      - level: 5\n        anchor: 越界\n")},
		{"四级量表", repl("      - level: 3\n        anchor: 均恰当\n", "      - level: 3\n        anchor: 均恰当\n      - level: 4\n        anchor: 越界\n")},
		{"缺第三级", repl("      - level: 3\n        anchor: 均恰当\n", "")},
		{"级别重复", repl("      - level: 3\n", "      - level: 2\n")},
		{"缺 anchor", repl("        anchor: 均恰当\n", "        anchor: \"\"\n")},
		{"空 criteria", "id: r1\ncriteria: []\n"},
		{"id 不符", strings.Replace(testRubricYAML, "id: r1", "id: other", 1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParseRubric([]byte(c.yamlText), "r1"); err == nil {
				t.Fatal("应校验失败")
			}
		})
	}
}

// 合法 §4.2 schema → 全字段解析正确（judge_default/judges_high_risk/policy/gold_dir）。
func TestLoadModelConfigParsesSchema(t *testing.T) {
	m, err := LoadModelConfig(writeModelFixture(t, testModelYAML))
	if err != nil {
		t.Fatalf("合法 §4.2 schema 应通过: %v", err)
	}
	if m.JudgeDefault.Provider != "anthropic" || m.JudgeDefault.Model != "claude-sonnet-4-5" ||
		m.JudgeDefault.Temperature != 0 {
		t.Errorf("judge_default=%+v", m.JudgeDefault)
	}
	if m.JudgesHighRisk != [2]string{"claude-sonnet-4-5", "gpt-4o"} {
		t.Errorf("judges_high_risk=%v", m.JudgesHighRisk)
	}
	if !m.Policy.PairwiseSwap || !m.Policy.TieOnDisagree ||
		m.Policy.KappaGate.Automation != 0.61 || m.Policy.KappaGate.CIAutonomous != 0.80 {
		t.Errorf("policy=%+v", m.Policy)
	}
	if m.Policy.Recalibrate == "" {
		t.Error("recalibrate 应解析")
	}
	if m.GoldDir != "configs/judge/gold/" {
		t.Errorf("gold_dir=%q", m.GoldDir)
	}
	if m.SHA256 == "" {
		t.Error("配置哈希缺失")
	}
}

// §4.2 schema 校验失败矩阵：旧 schema、缺字段、locked=false、席位数、同族、
// κ 越界、策略关闭、缺 gold_dir。
func TestLoadModelConfigRejectsBadSchema(t *testing.T) {
	repl := func(old, new string) string { return strings.Replace(testModelYAML, old, new, 1) }
	cases := []struct{ name, yamlText string }{
		{"旧 judges schema", "judges:\n  - model: claude-sonnet-4-5\n    temperature: 0.0\n    prompt: 旧 schema\n"},
		{"缺 judge_default", repl("judge_default: { provider: anthropic, model: claude-sonnet-4-5, temperature: 0.0, locked: true }\n", "")},
		{"judge_default 缺 provider", repl("provider: anthropic, ", "")},
		{"judge_default 缺 model", repl("model: claude-sonnet-4-5, ", "")},
		{"judge_default 缺 temperature", repl(", temperature: 0.0", "")},
		{"judge_default 缺 locked", repl(", locked: true", "")},
		{"locked=false", repl("locked: true", "locked: false")},
		{"judges_high_risk 单席", repl("[claude-sonnet-4-5, gpt-4o]", "[claude-sonnet-4-5]")},
		{"judges_high_risk 三席", repl("[claude-sonnet-4-5, gpt-4o]", "[claude-sonnet-4-5, gpt-4o, gemini-2.0]")},
		{"judges_high_risk 同族", repl("[claude-sonnet-4-5, gpt-4o]", "[claude-sonnet-4-5, claude-opus-4]")},
		{"缺 policy", repl("policy:\n  pairwise_swap: true\n  tie_on_disagree: true\n  recalibrate: quarterly + on any rubric/judge change\n  kappa_gate: { automation: 0.61, ci_autonomous: 0.80 }\n", "")},
		{"缺 kappa_gate", repl("  kappa_gate: { automation: 0.61, ci_autonomous: 0.80 }\n", "")},
		{"缺 automation", repl("kappa_gate: { automation: 0.61, ci_autonomous: 0.80 }", "kappa_gate: { ci_autonomous: 0.80 }")},
		{"缺 ci_autonomous", repl("kappa_gate: { automation: 0.61, ci_autonomous: 0.80 }", "kappa_gate: { automation: 0.61 }")},
		{"automation 越界", repl("automation: 0.61", "automation: 1.5")},
		{"automation 非正", repl("automation: 0.61", "automation: 0")},
		{"pairwise_swap 关闭", repl("pairwise_swap: true", "pairwise_swap: false")},
		{"tie_on_disagree 关闭", repl("tie_on_disagree: true", "tie_on_disagree: false")},
		{"缺 gold_dir", repl("gold_dir: configs/judge/gold/", "gold_dir: \"\"")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := LoadModelConfig(writeModelFixture(t, c.yamlText)); err == nil {
				t.Fatal("应校验失败")
			}
		})
	}
}

// locked=false 违反锁定纪律：错误须点名 locked。
func TestLoadModelConfigLockedDiscipline(t *testing.T) {
	bad := strings.Replace(testModelYAML, "locked: true", "locked: false", 1)
	_, err := LoadModelConfig(writeModelFixture(t, bad))
	if err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("err=%v 应点名 locked", err)
	}
}

// 席位选择（§4.2）：常规 rubric → judge_default 单席；高风险 rubric →
// judges_high_risk 双席（异族）；同族报错；nil rubric 拒绝。
func TestSelectJudges(t *testing.T) {
	rubric, err := ParseRubric([]byte(testRubricYAML), "r1")
	if err != nil {
		t.Fatal(err)
	}
	highRiskRubric, err := ParseRubric([]byte(strings.Replace(testRubricYAML, "high_risk: false", "high_risk: true", 1)), "r1")
	if err != nil {
		t.Fatal(err)
	}
	mk := func(highRisk [2]string) *ModelConfig {
		return &ModelConfig{
			JudgeDefault:   JudgeConfig{Provider: "anthropic", Model: "claude-sonnet-4-5", Temperature: 0},
			JudgesHighRisk: highRisk,
		}
	}
	t.Run("常规单席", func(t *testing.T) {
		js, err := mk([2]string{"claude-sonnet-4-5", "gpt-4o"}).SelectJudges(rubric)
		if err != nil || len(js) != 1 || js[0].Model != "claude-sonnet-4-5" {
			t.Fatalf("judges=%v err=%v", js, err)
		}
	})
	t.Run("高风险异族双席", func(t *testing.T) {
		js, err := mk([2]string{"claude-sonnet-4-5", "gpt-4o"}).SelectJudges(highRiskRubric)
		if err != nil || len(js) != 2 || js[0].Model != "claude-sonnet-4-5" || js[1].Model != "gpt-4o" {
			t.Fatalf("judges=%v err=%v", js, err)
		}
		if js[0].Temperature != 0 || js[1].Temperature != 0 {
			t.Errorf("双席温度应沿用 judge_default: %v/%v", js[0].Temperature, js[1].Temperature)
		}
	})
	t.Run("高风险同族拒绝", func(t *testing.T) {
		if _, err := mk([2]string{"claude-sonnet-4-5", "claude-opus-4"}).SelectJudges(highRiskRubric); err == nil {
			t.Fatal("同族双 judge 应拒绝")
		}
	})
	t.Run("nil rubric 拒绝", func(t *testing.T) {
		if _, err := mk([2]string{"claude-sonnet-4-5", "gpt-4o"}).SelectJudges(nil); err == nil {
			t.Fatal("nil rubric 应拒绝（prompt 哈希需 rubric 派生）")
		}
	})
}

// prompt 不再来自 model.yaml（§4.2 schema 无 prompt 字段），由 rubric 派生：
// 同 rubric 同 judge 稳定、同 rubric 异 judge prompt 哈希相同而 config 哈希不同、
// rubric 变更（anchor 改动）→ prompt 哈希变更。
func TestJudgeInfoPromptDerivedFromRubric(t *testing.T) {
	rubric, err := ParseRubric([]byte(testRubricYAML), "r1")
	if err != nil {
		t.Fatal(err)
	}
	seat := JudgeConfig{Provider: "anthropic", Model: "claude-sonnet-4-5", Temperature: 0}
	other := JudgeConfig{Provider: "openai", Model: "gpt-4o", Temperature: 0}
	a, b := seat.Info(rubric), other.Info(rubric)
	if a.PromptSHA256 == "" || a.ConfigSHA256 == "" {
		t.Fatalf("哈希缺失: %+v", a)
	}
	if a != seat.Info(rubric) {
		t.Error("同输入应同输出")
	}
	if a.PromptSHA256 != b.PromptSHA256 {
		t.Error("同 rubric 派生的 prompt 哈希应相同")
	}
	if a.ConfigSHA256 == b.ConfigSHA256 {
		t.Error("不同 judge 的 config 哈希应不同")
	}
	edited, err := ParseRubric([]byte(strings.Replace(testRubricYAML, "基调错位", "基调错位（改）", 1)), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if a.PromptSHA256 == seat.Info(edited).PromptSHA256 {
		t.Error("rubric 变更后 prompt 哈希应变化（rubric 即 prompt 来源）")
	}
}

func TestModelFamily(t *testing.T) {
	for model, want := range map[string]string{
		"claude-sonnet-4-5": "claude", "claude-opus-4": "claude", "gpt-4o": "gpt",
		"gpt-4o-mini": "gpt", "anthropic/claude-3-haiku": "claude", "QWEN2.5-72B": "qwen2.5",
	} {
		if got := modelFamily(model); got != want {
			t.Errorf("modelFamily(%q)=%q want %q", model, got, want)
		}
	}
}
