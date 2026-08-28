package synthgen

import (
	"math"
	"sort"
)

// DiversityFields 三字段多样性维度（spec §3.7：说话人/语速/主题）。
var DiversityFields = [...]string{"speaker", "speed", "topic"}

// SingleSourceLimit 单一上游模型占比上限（spec：单源占比 ≤30%）。
const SingleSourceLimit = 0.30

// FieldReport 单字段多样性：分布熵 + 类别数 +（可选）与参考集的 JS 距离。
type FieldReport struct {
	EntropyBits    float64  `json:"entropy_bits"`
	Categories     int      `json:"categories"`
	JSDistanceBits *float64 `json:"js_distance_bits,omitempty"`
}

// DistReport dist-check 报告：三字段指标 + 单源占比 + 门槛判定。
type DistReport struct {
	N                 int                    `json:"n"`
	Fields            map[string]FieldReport `json:"fields"`
	SingleSourceShare float64                `json:"single_source_share"`
	OK                bool                   `json:"ok"`
}

// Distribution 值序列 → 经验分布 {取值: 概率}；空序列 → 空分布。
func Distribution(values []string) map[string]float64 {
	dist := make(map[string]float64)
	if len(values) == 0 {
		return dist
	}
	for _, v := range values {
		dist[v]++
	}
	for k := range dist {
		dist[k] /= float64(len(values))
	}
	return dist
}

// ShannonEntropy Shannon 熵（bit）：单点分布 0，k 类均匀分布达最大 log2(k)；只依赖概率多重集。
func ShannonEntropy(dist map[string]float64) float64 {
	var h float64
	for _, k := range sortedKeys(dist) {
		if p := dist[k]; p > 0 {
			h -= p * math.Log2(p)
		}
	}
	return h
}

// JSDistance Jensen–Shannon 距离（bit，上界 1）：同分布 0、不相交 1；对称。
func JSDistance(a, b map[string]float64) float64 {
	mid := make(map[string]float64, len(a)+len(b))
	for k, p := range a {
		mid[k] += p
	}
	for k, p := range b {
		mid[k] += p
	}
	var acc float64 // 0.5·KL(a‖m) + 0.5·KL(b‖m)
	for _, k := range sortedKeys(mid) {
		m := mid[k] / 2
		if pa := a[k]; pa > 0 {
			acc += 0.5 * pa * math.Log2(pa/m)
		}
		if pb := b[k]; pb > 0 {
			acc += 0.5 * pb * math.Log2(pb/m)
		}
	}
	return math.Sqrt(math.Max(0, acc))
}

// SingleSourceShare 单源占比：同一上游模型的最大占比；空输入 0。
func SingleSourceShare(models []string) float64 {
	if len(models) == 0 {
		return 0
	}
	counts := make(map[string]int)
	for _, m := range models {
		counts[m]++
	}
	max := 0
	for _, c := range counts {
		if c > max {
			max = c
		}
	}
	return float64(max) / float64(len(models))
}

// FieldValues 取多样性字段值：payload 优先，兼容扁平记录（真实参考集）；缺字段忽略。
func FieldValues(records []map[string]any, field string) []string {
	var values []string
	for _, r := range records {
		if payload, ok := r["payload"].(map[string]any); ok {
			if v, ok := payload[field].(string); ok {
				values = append(values, v)
			}
			continue
		}
		if v, ok := r[field].(string); ok {
			values = append(values, v)
		}
	}
	return values
}

// Evaluate 多样性报告：三字段熵（含可选参考集 JS 距离）+ 单源占比 + 门槛判定（>30% 不通过）。
func Evaluate(samples, reference []map[string]any) DistReport {
	report := DistReport{N: len(samples), Fields: make(map[string]FieldReport, len(DiversityFields))}
	for _, field := range DiversityFields {
		dist := Distribution(FieldValues(samples, field))
		entry := FieldReport{EntropyBits: ShannonEntropy(dist), Categories: len(dist)}
		if reference != nil {
			refDist := Distribution(FieldValues(reference, field))
			js := JSDistance(dist, refDist)
			entry.JSDistanceBits = &js
		}
		report.Fields[field] = entry
	}
	report.SingleSourceShare = SingleSourceShare(upstreamModelValues(samples))
	report.OK = report.SingleSourceShare <= SingleSourceLimit
	return report
}

// upstreamModelValues 取每条样本溯源戳里的上游模型（缺戳跳过）。
func upstreamModelValues(records []map[string]any) []string {
	var models []string
	for _, r := range records {
		prov, ok := r["provenance"].(map[string]any)
		if !ok {
			continue
		}
		if m, ok := prov["upstream_model"].(string); ok {
			models = append(models, m)
		}
	}
	return models
}

// sortedKeys 键排序：map 迭代序随机，排序求和保证熵/距离输出字节级确定。
func sortedKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
