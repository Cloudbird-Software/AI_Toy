// calibrate —— 金标校准：per-criterion Cohen's κ 一律走 evalkit（spec §3.3，
// 「不可自实现」），任一 κ < 0.61 未达自动化门禁（CLI 映射 exit 20）。
package toyjudge

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"

	"github.com/Cloudbird-Software/AI_Toy/tools/evalkit/evalkit"
)

// KappaGateAutomation 是自动化门禁 κ 阈值（spec §4.2 kappa_gate.automation）。
const KappaGateAutomation = 0.61

// GoldRow 是金标 jsonl 的一行：同一 criterion 下人工分与 judge 分的配对评分。
type GoldRow struct {
	Criterion string
	Human     int
	Judge     int
}

type goldFileRow struct {
	Criterion *string `json:"criterion"`
	Human     *int    `json:"human"`
	Judge     *int    `json:"judge"`
}

// LoadGold 读取金标 jsonl 并按 rubric 校验：criterion 必须在 rubric 中、
// 评分必须落在三级量表 1..3 内、且覆盖 rubric 全部 criterion。
func LoadGold(path string, rubric *Rubric) ([]GoldRow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("gold 不可读: %w", err)
	}
	known := make(map[string]bool, len(rubric.Criteria))
	for _, c := range rubric.Criteria {
		known[c.Name] = true
	}
	var rows []GoldRow
	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var f goldFileRow
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			return nil, fmt.Errorf("gold 第 %d 行非法 JSON: %w", i+1, err)
		}
		if f.Criterion == nil || !known[*f.Criterion] {
			got := "<缺失>"
			if f.Criterion != nil {
				got = *f.Criterion
			}
			return nil, fmt.Errorf("gold 第 %d 行 criterion 缺失或不在 rubric 中: %s", i+1, got)
		}
		if f.Human == nil || f.Judge == nil {
			return nil, fmt.Errorf("gold 第 %d 行 human/judge 评分缺失", i+1)
		}
		for name, v := range map[string]int{"human": *f.Human, "judge": *f.Judge} {
			if v < wantLevels[0] || v > wantLevels[len(wantLevels)-1] {
				return nil, fmt.Errorf("gold 第 %d 行 %s=%d 越界（三级量表 1..3）", i+1, name, v)
			}
		}
		rows = append(rows, GoldRow{Criterion: *f.Criterion, Human: *f.Human, Judge: *f.Judge})
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("gold 无有效评分行: %s", path)
	}
	for _, c := range rubric.Criteria {
		if !slices.ContainsFunc(rows, func(r GoldRow) bool { return r.Criterion == c.Name }) {
			return nil, fmt.Errorf("gold 未覆盖 criterion %q", c.Name)
		}
	}
	return rows, nil
}

// CriterionKappa 是单 criterion 的校准结果。
type CriterionKappa struct {
	Criterion string  `json:"criterion"`
	Kappa     float64 `json:"kappa"`
	N         int     `json:"n"`
}

// CalibrateReport 是 calibrate 的 stdout 产物：per-criterion κ + 最低 κ + 门禁判定。
type CalibrateReport struct {
	Rubric       string           `json:"rubric"`
	RubricSHA256 string           `json:"rubric_sha256"`
	KappaGate    float64          `json:"kappa_gate"`
	Judge        JudgeInfo        `json:"judge"`
	Criteria     []CriterionKappa `json:"criteria"`
	MinKappa     float64          `json:"min_kappa"`
	Pass         bool             `json:"pass"`
}

// Calibrate 逐 criterion 计算 judge 与人工金标的 Cohen's κ（evalkit.CohensKappa），
// 任一 criterion κ < KappaGateAutomation 即 Pass=false。
func Calibrate(rubric *Rubric, judge JudgeInfo, rows []GoldRow) CalibrateReport {
	rep := CalibrateReport{Rubric: rubric.ID, RubricSHA256: rubric.SHA256,
		KappaGate: KappaGateAutomation, Judge: judge,
		Criteria: make([]CriterionKappa, 0, len(rubric.Criteria)), MinKappa: math.Inf(1)}
	for _, c := range rubric.Criteria {
		var human, model []int
		for _, r := range rows {
			if r.Criterion == c.Name {
				human = append(human, r.Human)
				model = append(model, r.Judge)
			}
		}
		k := evalkit.CohensKappa(human, model)
		rep.Criteria = append(rep.Criteria, CriterionKappa{Criterion: c.Name, Kappa: k, N: len(human)})
		rep.MinKappa = math.Min(rep.MinKappa, k)
	}
	rep.Pass = rep.MinKappa >= KappaGateAutomation
	return rep
}
