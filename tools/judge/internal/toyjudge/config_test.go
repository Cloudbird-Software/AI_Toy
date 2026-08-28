// 契约测试（spec §3.3）：rubric 三级量表纪律与 model.yaml 席位选择。
package toyjudge

import (
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

const testModelYAML = `judges:
  - model: claude-sonnet-4-5
    temperature: 0.0
    prompt: 测试桩 judge prompt
  - model: gpt-4o
    temperature: 0.0
    prompt: 测试桩 judge prompt（第二席）
`

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

// 高风险 rubric 双 judge：异族双席；同族拒绝；单席不足。
func TestSelectJudges(t *testing.T) {
	mk := func(models ...string) *ModelConfig {
		m := &ModelConfig{}
		for _, mo := range models {
			m.Judges = append(m.Judges, JudgeConfig{Model: mo, Temperature: 0, Prompt: "stub"})
		}
		return m
	}
	t.Run("常规单席", func(t *testing.T) {
		js, err := mk("claude-sonnet-4-5", "gpt-4o").SelectJudges(false)
		if err != nil || len(js) != 1 || js[0].Model != "claude-sonnet-4-5" {
			t.Fatalf("judges=%v err=%v", js, err)
		}
	})
	t.Run("高风险异族双席", func(t *testing.T) {
		js, err := mk("claude-sonnet-4-5", "gpt-4o").SelectJudges(true)
		if err != nil || len(js) != 2 || js[0].Model == js[1].Model {
			t.Fatalf("judges=%v err=%v", js, err)
		}
	})
	t.Run("高风险同族拒绝", func(t *testing.T) {
		if _, err := mk("claude-sonnet-4-5", "claude-opus-4").SelectJudges(true); err == nil {
			t.Fatal("同族双 judge 应拒绝")
		}
	})
	t.Run("高风险单席不足", func(t *testing.T) {
		if _, err := mk("claude-sonnet-4-5").SelectJudges(true); err == nil {
			t.Fatal("单 judge 不足以双评审")
		}
	})
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
