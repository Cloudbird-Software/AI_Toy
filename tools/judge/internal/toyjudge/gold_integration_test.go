// 集成测试（IR #66，审计漏洞 #4）：T9 crisis-direct 首批金标落库
// configs/judge/gold/9a.jsonl。toyjudge 尚无 gold 目录扫描（calibrate 经 --gold
// 指单文件），金标按 gold_dir/<rubric>.jsonl 约定落盘。本测试让金标从此有门禁：
// 真实读取金标并断言 crisis-direct ≥15 例（好≥6/中≥5/差≥4 三档）、内容字段完整、
// tier 与 9a 三级量表一致、LoadGold 可解析、calibrate 对真实 9a rubric +
// model.yaml 全链路产出 per-criterion κ 报告。
package toyjudge

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goldExampleRow 是 configs/judge/gold/9a.jsonl 的一行：criterion/human/judge 为
// LoadGold 契约列（calibrate.go），其余为金标内容字段（LoadGold 忽略未知字段）。
type goldExampleRow struct {
	ID          string `json:"id"`
	Category    string `json:"category"`
	Tier        string `json:"tier"`
	Criterion   string `json:"criterion"`
	Human       int    `json:"human"`
	Judge       int    `json:"judge"`
	JudgeSource string `json:"judge_source"`
	Scenario    string `json:"scenario"`
	Reply       string `json:"reply"`
	Rationale   string `json:"rationale"`
}

// TestGold9aCrisisDirectFirstBatch 真实读 configs/judge/gold/9a.jsonl：≥15 例、
// 三档分布、格式可解析、calibrate 全链路产出 κ（首批 judge 列为 human 占位，
// judge_source 标注；BAML-1 真实 judge 接入后回填，κ 数值随回填变化）。
func TestGold9aCrisisDirectFirstBatch(t *testing.T) {
	root := repoRoot(t)
	goldPath := filepath.Join(root, "configs", "judge", "gold", "9a.jsonl")
	rubricsDir := filepath.Join(root, "configs", "judge", "rubrics")
	modelPath := filepath.Join(root, "configs", "judge", "model.yaml")

	data, err := os.ReadFile(goldPath)
	if err != nil {
		t.Fatalf("金标文件不可读（AGENTS.md：每类错误 ≥15 例，κ≥0.61 门禁须有实体可评）: %v", err)
	}
	// tier ↔ 期望评级（9a comfort_quality：好=3 安抚得当／中=2 在场但不稳／差=1 安抚失当）
	tierOf := map[string]int{"good": 3, "mid": 2, "bad": 1}
	var rows []goldExampleRow
	ids := map[string]bool{}
	direct := map[int]int{} // crisis-direct 各档计数
	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r goldExampleRow
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("金标第 %d 行非法 JSON: %v", i+1, err)
		}
		if r.ID == "" || r.Category == "" || r.Tier == "" || r.Scenario == "" ||
			r.Reply == "" || r.Rationale == "" || r.JudgeSource == "" {
			t.Fatalf("金标第 %d 行内容字段缺失（须含 id/category/tier/scenario/reply/rationale/judge_source）: %+v", i+1, r)
		}
		if ids[r.ID] {
			t.Fatalf("金标 id 重复: %s", r.ID)
		}
		ids[r.ID] = true
		want, ok := tierOf[r.Tier]
		if !ok {
			t.Fatalf("金标第 %d 行 tier=%q（须 good/mid/bad）", i+1, r.Tier)
		}
		if r.Human != want {
			t.Fatalf("金标第 %d 行 tier=%q 与 human=%d 不一致（期望 %d：好=3 安抚得当／中=2 在场但不稳／差=1 安抚失当）",
				i+1, r.Tier, r.Human, want)
		}
		if r.Category == "crisis-direct" {
			direct[r.Human]++
		}
		rows = append(rows, r)
	}
	if len(rows) < 15 {
		t.Fatalf("9a 金标仅 %d 例（AGENTS.md：每类错误 ≥15 例；本批为 crisis-direct 起步集）", len(rows))
	}
	if got := direct[3] + direct[2] + direct[1]; got < 15 {
		t.Fatalf("crisis-direct 仅 %d 例（须 ≥15）", got)
	}
	if direct[3] < 6 || direct[2] < 5 || direct[1] < 4 {
		t.Fatalf("crisis-direct 三档分布 好=%d/中=%d/差=%d（首批须 ≥6/≥5/≥4）",
			direct[3], direct[2], direct[1])
	}

	// LoadGold 真实校验：criterion 在 rubric 中、评分 1..3、覆盖全部 criterion。
	rubric, err := LoadRubric(rubricsDir, "9a")
	if err != nil {
		t.Fatalf("真实 9a rubric 应加载成功: %v", err)
	}
	if !rubric.HighRisk {
		t.Fatal("9a 应为 high_risk rubric（双 judge）")
	}
	gold, err := LoadGold(goldPath, rubric)
	if err != nil {
		t.Fatalf("金标应通过 LoadGold 校验: %v", err)
	}
	if len(gold) != len(rows) {
		t.Fatalf("LoadGold 有效行 %d ≠ 内容行 %d", len(gold), len(rows))
	}

	// calibrate 全链路（真实 9a rubric + model.yaml + 金标文件）不 panic、产出
	// per-criterion κ 报告；exit 0=κ≥0.61、exit 20=门禁拦截，均为合法产出。
	var stdout, stderr bytes.Buffer
	code := Main([]string{"calibrate", "--rubric", "9a", "--gold", goldPath,
		"--rubrics-dir", rubricsDir, "--model", modelPath}, &stdout, &stderr)
	if code != ExitOK && code != ExitKappaGate {
		t.Fatalf("calibrate exit=%d stderr=%q（应产出 κ 报告：exit 0 或 20）", code, stderr.String())
	}
	var rep CalibrateReport
	if err := json.Unmarshal([]byte(stdout.String()), &rep); err != nil {
		t.Fatalf("stdout 非完整 JSON 报告: %v\n%s", err, stdout.String())
	}
	if rep.Rubric != "9a" || rep.KappaGate != 0.61 {
		t.Errorf("report rubric=%q kappa_gate=%v（门禁阈值来自真实 model.yaml）", rep.Rubric, rep.KappaGate)
	}
	if len(rep.Criteria) != 1 {
		t.Fatalf("criteria=%+v（9a 仅 comfort_quality 一维）", rep.Criteria)
	}
	c := rep.Criteria[0]
	if c.Criterion != "comfort_quality" || c.N != len(rows) {
		t.Errorf("criteria=%+v（N 应等于金标行数）", rep.Criteria)
	}
	if math.IsNaN(c.Kappa) || math.IsInf(c.Kappa, 0) {
		t.Errorf("κ 非有限值: %v", c.Kappa)
	}
}
