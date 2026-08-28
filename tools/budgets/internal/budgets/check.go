// check —— 守恒校验与延迟负债表（spec §3.6）。
//
// 守恒律：ΣP95 − 并行重叠 ≤ total_p95_budget（基准 configs/budgets/latency.yaml，
// 当前 1500ms）；违反 → exit 20，并输出「延迟负债表」：各段实际值 vs 预算值、
// 差值、超标段（只认 P95，p50 不参与守恒计算）。
package budgets

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// InputError 表示预算配置 / 延迟报告 / history 不可读或不符合 schema（CLI exit 2）。
type InputError struct{ msg string }

func (e *InputError) Error() string { return e.msg }

func inputErrorf(format string, args ...any) error {
	return &InputError{msg: fmt.Sprintf(format, args...)}
}

// SegmentBudget 是 configs/budgets/latency.yaml 中一段的预算（只认 P95）。
type SegmentBudget struct {
	ID    string  `yaml:"id"`
	Asset string  `yaml:"asset"`
	P50   float64 `yaml:"p50"`
	P95   float64 `yaml:"p95"`
	Note  string  `yaml:"note"`
}

// BudgetConfig 是延迟预算基准：总 P95 预算 + 各段预算（rules 等说明键不参与计算）。
type BudgetConfig struct {
	TotalP95Budget float64         `yaml:"total_p95_budget"`
	Segments       []SegmentBudget `yaml:"segments"`
}

// LoadConfig 经 yaml.v3 解析 latency.yaml 并校验非负性与完整性。
func LoadConfig(path string) (BudgetConfig, error) {
	var config BudgetConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return config, inputErrorf("预算配置不存在或不可读: %s", path)
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return config, inputErrorf("预算配置不可解析: %v", err)
	}
	if math.IsNaN(config.TotalP95Budget) || config.TotalP95Budget < 0 {
		return config, inputErrorf("total_p95_budget 须为非负数: %v", config.TotalP95Budget)
	}
	if len(config.Segments) == 0 {
		return config, inputErrorf("segments 为空")
	}
	for _, seg := range config.Segments {
		if seg.ID == "" {
			return config, inputErrorf("segment 缺 id: %+v", seg)
		}
		if math.IsNaN(seg.P95) || seg.P95 < 0 {
			return config, inputErrorf("segment %s 的 p95 须为非负数: %v", seg.ID, seg.P95)
		}
	}
	return config, nil
}

// SegmentSample 是报告中一段的实测值。
type SegmentSample struct {
	ID  string  `json:"id"`
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
}

// LatencyReport 是单份夜间延迟报告（格式见包 doc 注释）。
type LatencyReport struct {
	Commit    string          `json:"commit"`
	Timestamp string          `json:"timestamp"`
	OverlapMS float64         `json:"overlap_ms"` // 缺省 0
	Segments  []SegmentSample `json:"segments"`
}

func loadReport(path string) (LatencyReport, error) {
	var report LatencyReport
	data, err := os.ReadFile(path)
	if err != nil {
		return report, inputErrorf("延迟报告不存在或不可读: %s", path)
	}
	if err := json.Unmarshal(data, &report); err != nil {
		return report, inputErrorf("延迟报告不可解析: %v", err)
	}
	return report, nil
}

// SegmentRow 是负债表一行：段预算 vs 实际。
type SegmentRow struct {
	ID        string
	BudgetP95 float64
	ActualP95 float64
	Delta     float64 // 实际 − 预算
	Over      bool    // 实际 P95 > 该段预算
}

// CheckResult 是守恒校验结果与负债表数据。
type CheckResult struct {
	Commit      string
	TotalActual float64 // ΣP95
	OverlapMS   float64
	EffectiveMS float64 // ΣP95 − 并行重叠
	BudgetMS    float64
	OK          bool
	OverByMS    float64
	Rows        []SegmentRow
}

// OverSegments 返回实际 P95 超过自身段预算的段 id（负债表「超标段」清单）。
func (r CheckResult) OverSegments() []string {
	var over []string
	for _, row := range r.Rows {
		if row.Over {
			over = append(over, row.ID)
		}
	}
	return over
}

// Evaluate 对照预算基准评估单份报告：段集合须与配置完全一致，p95/overlap 非负。
func Evaluate(report LatencyReport, config BudgetConfig) (CheckResult, error) {
	var result CheckResult
	if len(report.Segments) == 0 {
		return result, inputErrorf("报告缺 segments 列表")
	}
	actual := make(map[string]float64, len(report.Segments))
	for _, seg := range report.Segments {
		if seg.ID == "" {
			return result, inputErrorf("segment 缺 id: %+v", seg)
		}
		if _, dup := actual[seg.ID]; dup {
			return result, inputErrorf("segment id 重复: %s", seg.ID)
		}
		if math.IsNaN(seg.P95) || seg.P95 < 0 {
			return result, inputErrorf("segment %s 的 p95 须为非负数: %v", seg.ID, seg.P95)
		}
		actual[seg.ID] = seg.P95
	}
	budgetByID := make(map[string]float64, len(config.Segments))
	var missing, unknown []string
	for _, seg := range config.Segments {
		budgetByID[seg.ID] = seg.P95
		if _, ok := actual[seg.ID]; !ok {
			missing = append(missing, seg.ID)
		}
	}
	for _, seg := range report.Segments {
		if _, ok := budgetByID[seg.ID]; !ok {
			unknown = append(unknown, seg.ID)
		}
	}
	if len(missing) > 0 || len(unknown) > 0 {
		return result, inputErrorf("报告段与预算配置不一致: 缺 %v，多 %v", missing, unknown)
	}
	if math.IsNaN(report.OverlapMS) || report.OverlapMS < 0 {
		return result, inputErrorf("overlap_ms 须为非负数: %v", report.OverlapMS)
	}

	result = CheckResult{Commit: report.Commit, OverlapMS: report.OverlapMS, BudgetMS: config.TotalP95Budget}
	if result.Commit == "" {
		result.Commit = "?"
	}
	for _, seg := range config.Segments {
		p95 := actual[seg.ID]
		delta := p95 - seg.P95
		result.TotalActual += p95
		result.Rows = append(result.Rows, SegmentRow{ID: seg.ID, BudgetP95: seg.P95, ActualP95: p95, Delta: delta, Over: delta > 0})
	}
	result.EffectiveMS = result.TotalActual - result.OverlapMS
	result.OK = result.EffectiveMS <= result.BudgetMS
	result.OverByMS = math.Max(0, result.EffectiveMS-result.BudgetMS)
	return result, nil
}

// fmtMS 毫秒展示：整数去小数点，非整数保留 1 位。
func fmtMS(v float64) string {
	if v == math.Trunc(v) && !math.IsInf(v, 0) && !math.IsNaN(v) {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

// FormatDebtTable 渲染延迟负债表：段/预算/实际/差值/状态 + 超标段清单 + 守恒判定。
func FormatDebtTable(result CheckResult, reportPath string) string {
	where := ""
	if reportPath != "" {
		where = "，报告=" + reportPath
	}
	lines := []string{
		fmt.Sprintf("延迟负债表（commit=%s%s）", result.Commit, where),
		fmt.Sprintf("%-14s%10s%10s%8s  状态", "段", "预算P95", "实际P95", "差值"),
	}
	for _, row := range result.Rows {
		delta := fmtMS(row.Delta)
		if row.Delta > 0 {
			delta = "+" + delta
		}
		status := "正常"
		if row.Over {
			status = "超标"
		}
		lines = append(lines, fmt.Sprintf("%-14s%10s%10s%8s  %s",
			row.ID, fmtMS(row.BudgetP95), fmtMS(row.ActualP95), delta, status))
	}
	lines = append(lines, strings.Repeat("─", 58))
	lines = append(lines, fmt.Sprintf("ΣP95=%s 并行重叠=%s 有效总延迟=%s 总预算=%s 超支=%s",
		fmtMS(result.TotalActual), fmtMS(result.OverlapMS), fmtMS(result.EffectiveMS), fmtMS(result.BudgetMS), fmtMS(result.OverByMS)))
	if over := result.OverSegments(); len(over) > 0 {
		lines = append(lines, "超标段: "+strings.Join(over, ", "))
	}
	if result.OK {
		lines = append(lines, "守恒校验通过：ΣP95−并行重叠 ≤ 总预算")
	} else {
		lines = append(lines, fmt.Sprintf("守恒校验违反：ΣP95−并行重叠=%sms > 总预算%sms，超支 %sms",
			fmtMS(result.EffectiveMS), fmtMS(result.BudgetMS), fmtMS(result.OverByMS)))
	}
	return strings.Join(lines, "\n")
}
