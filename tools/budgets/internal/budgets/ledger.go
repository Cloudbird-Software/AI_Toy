// ledger —— 延迟趋势台账（spec §3.6）。
//
// 对每段取最近 N 份夜间报告（--days，默认 30 即「近 30 天」）的 P95，在窗口内
// （含最新值）计算均值 μ 与总体标准差 σ；最新值 > μ+2σ 判为劣化标红
// （组合级 G1 红 → exit 20）。σ=0 ⟺ 窗口全等值，此时最新值即均值，不可能
// 劣化，无需除零特判。
package budgets

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
)

// HistoryFile 是 ledger 单文件历史：{"history": [报告, ...]}（按时间升序，末尾最新）。
type HistoryFile struct {
	History *[]LatencyReport `json:"history"` // 指针区分「缺 history 键」与空数组
}

// LoadHistory 读取并校验 history 文件。
func LoadHistory(path string) ([]LatencyReport, error) {
	var file HistoryFile
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, inputErrorf("history 文件不存在或不可读: %s", path)
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, inputErrorf("history 不可解析: %v", err)
	}
	if file.History == nil {
		return nil, inputErrorf(`history 文件必须是 {"history": [报告, ...]}`)
	}
	for i, item := range *file.History {
		if item.Segments == nil {
			return nil, inputErrorf("history[%d] 缺 segments 列表", i)
		}
	}
	return *file.History, nil
}

// TrendRow 是台账趋势一行：窗口内某段的 μ/σ/最新值与劣化判定。
type TrendRow struct {
	ID     string
	N      int      // 窗口内出现次数
	Mean   float64  // 窗口内 P95 均值 μ（含最新值）
	Sigma  float64  // 窗口内 P95 总体标准差 σ
	Latest *float64 // 最新一份报告中的 P95；该段缺席则为 nil
	Z      float64  // (最新−μ)/σ；σ=0 或无最新值时为 0
	Red    bool     // 最新值 > μ+2σ → 劣化标红
}

// ComputeTrends 对窗口内每段计算 μ/σ/最新值/z（段按首次出现顺序入表）。
func ComputeTrends(window []LatencyReport) ([]TrendRow, error) {
	if len(window) == 0 {
		return nil, nil
	}
	var order []string
	series := make(map[string][]float64)
	for _, item := range window {
		seen := make(map[string]bool, len(item.Segments))
		for _, seg := range item.Segments {
			if seg.ID == "" {
				return nil, inputErrorf("segment 缺 id: %+v", seg)
			}
			if seen[seg.ID] {
				return nil, inputErrorf("单份报告内 segment id 重复: %s", seg.ID)
			}
			if math.IsNaN(seg.P95) || seg.P95 < 0 {
				return nil, inputErrorf("segment %s 的 p95 须为非负数: %v", seg.ID, seg.P95)
			}
			seen[seg.ID] = true
			if _, ok := series[seg.ID]; !ok {
				order = append(order, seg.ID)
			}
			series[seg.ID] = append(series[seg.ID], seg.P95)
		}
	}
	latestByID := make(map[string]float64, len(window[len(window)-1].Segments))
	for _, seg := range window[len(window)-1].Segments {
		latestByID[seg.ID] = seg.P95
	}
	rows := make([]TrendRow, 0, len(order))
	for _, id := range order {
		values := series[id]
		mean, sigma := noiseBand(values)
		row := TrendRow{ID: id, N: len(values), Mean: mean, Sigma: sigma}
		if latest, ok := latestByID[id]; ok {
			row.Latest = &latest
			if sigma > 0 {
				row.Z = (latest - mean) / sigma
			}
		}
		row.Red = row.Z > 2.0
		rows = append(rows, row)
	}
	return rows, nil
}

// noiseBand 返回均值与总体标准差（pstdev，语义同 spec §3.2 evalkit.noise_band）。
func noiseBand(values []float64) (mean, sigma float64) {
	for _, v := range values {
		mean += v
	}
	mean /= float64(len(values))
	for _, v := range values {
		sigma += (v - mean) * (v - mean)
	}
	return mean, math.Sqrt(sigma / float64(len(values)))
}

// FormatTrendTable 渲染趋势台账：n/μ/σ/最新/z/状态 + 标红段清单。
func FormatTrendTable(rows []TrendRow, days, nReports int) string {
	lines := []string{
		fmt.Sprintf("延迟台账趋势（近 %d 天，%d 份报告）", days, nReports),
		fmt.Sprintf("%-14s%4s%10s%8s%10s%7s  状态", "段", "n", "均值P95", "σP95", "最新P95", "z"),
	}
	var red []string
	for _, row := range rows {
		status := "正常"
		switch {
		case row.Red:
			status = "红 ←劣化>2σ"
		case row.Latest == nil:
			status = "无最新值"
		}
		latest := "—"
		if row.Latest != nil {
			latest = fmtMS(*row.Latest)
		}
		lines = append(lines, fmt.Sprintf("%-14s%4d%10s%8s%10s%7.2f  %s",
			row.ID, row.N, fmtMS(row.Mean), fmtMS(row.Sigma), latest, row.Z, status))
		if row.Red {
			red = append(red, row.ID)
		}
	}
	if len(red) > 0 {
		lines = append(lines, fmt.Sprintf("标红段: %s（最新值 > μ+2σ，无划拨说明 → 组合级 G1 红，进延迟负债表）", strings.Join(red, ", ")))
	} else {
		lines = append(lines, "无 >2σ 劣化段")
	}
	return strings.Join(lines, "\n")
}
