package synthgen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
)

// HoldoutRatio synth-holdout 切分比例（spec §3.7：生成即 8:2 切出）。
const HoldoutRatio = 0.2

// Provenance 溯源戳：每条合成样本强制携带，恰四个字段。
type Provenance struct {
	GeneratorID      string `json:"generator_id"`
	GeneratorVersion string `json:"generator_version"`
	Seed             int64  `json:"seed"`
	UpstreamModel    string `json:"upstream_model"`
}

// Sample 合成样本：sample_id + 溯源戳 + payload 桩。
type Sample struct {
	SampleID   string            `json:"sample_id"`
	Provenance Provenance        `json:"provenance"`
	Payload    map[string]string `json:"payload"`
}

// Batch 批次 manifest：记录生成参数与 8:2 切分结果（可重建数据集）。
type Batch struct {
	ID               string `json:"batch_id"`
	GeneratorID      string `json:"generator_id"`
	GeneratorVersion string `json:"generator_version"`
	Seed             int64  `json:"seed"`
	N                int    `json:"n"`
	TrainN           int    `json:"train_n"`
	HoldoutN         int    `json:"holdout_n"`
}

// 桩词表（说话人/语速/主题）+ 上游模型池：6 模型均匀抽取 → 单源占比远低于 30%。
var (
	speakers       = [...]string{"spk-01", "spk-02", "spk-03", "spk-04", "spk-05", "spk-06", "spk-07", "spk-08"}
	speeds         = [...]string{"slow", "normal", "fast"}
	topics         = [...]string{"bedtime", "play", "learning", "emotion", "safety"}
	upstreamModels = [...]string{"toy-llm-a", "toy-llm-b", "toy-llm-c", "toy-llm-d", "toy-llm-e", "toy-llm-f"}
)

// GenerateSamples 生成 n 条带溯源戳的桩样本：rand.NewSource(seed) → 同 seed 逐条完全复现。
func GenerateSamples(g Generator, n int, seed int64) []Sample {
	r := rand.New(rand.NewSource(seed))
	samples := make([]Sample, n)
	for i := range samples {
		samples[i] = Sample{
			SampleID: fmt.Sprintf("%s-%d-%06d", g.ID, seed, i),
			Provenance: Provenance{GeneratorID: g.ID, GeneratorVersion: g.Version, Seed: seed,
				UpstreamModel: upstreamModels[r.Intn(len(upstreamModels))]},
			Payload: map[string]string{
				"speaker": speakers[r.Intn(len(speakers))],
				"speed":   speeds[r.Intn(len(speeds))],
				"topic":   topics[r.Intn(len(topics))],
				"text":    fmt.Sprintf("stub utterance %d", i),
			},
		}
	}
	return samples
}

// SplitHoldout 确定性 8:2 切分：按 seed 洗牌，前 floor(n×0.2) 为 holdout，其余为 train；不动入参。
func SplitHoldout(sampleIDs []string, seed int64) (train, holdout []string) {
	shuffled := append([]string(nil), sampleIDs...)
	r := rand.New(rand.NewSource(seed))
	r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	n := int(float64(len(shuffled)) * HoldoutRatio)
	return shuffled[n:], shuffled[:n]
}

// GenerateBatch 生成 n 条样本并落盘批次目录（samples + synth-train/synth-holdout + manifest）。
func GenerateBatch(g Generator, n int, seed int64) (Batch, string, error) {
	samples := GenerateSamples(g, n, seed)
	byID := make(map[string]Sample, len(samples))
	ids := make([]string, len(samples))
	for i, s := range samples {
		byID[s.SampleID] = s
		ids[i] = s.SampleID
	}
	trainIDs, holdoutIDs := SplitHoldout(ids, seed)
	pick := func(batchIDs []string) []Sample {
		picked := make([]Sample, len(batchIDs))
		for i, id := range batchIDs {
			picked[i] = byID[id]
		}
		return picked
	}
	dir := filepath.Join(BatchesDir, fmt.Sprintf("%s-%s-seed%d-n%d", g.ID, g.Version, seed, n))
	b := Batch{ID: filepath.Base(dir), GeneratorID: g.ID, GeneratorVersion: g.Version,
		Seed: seed, N: n, TrainN: len(trainIDs), HoldoutN: len(holdoutIDs)}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return b, dir, err
	}
	for _, out := range [...]struct {
		name    string
		records []Sample
	}{
		{"samples.jsonl", samples},
		{"synth-train.jsonl", pick(trainIDs)},
		{"synth-holdout.jsonl", pick(holdoutIDs)},
	} {
		if err := writeJSONL(filepath.Join(dir, out.name), out.records); err != nil {
			return b, dir, err
		}
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return b, dir, err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), append(data, '\n'), 0o644); err != nil {
		return b, dir, err
	}
	return b, dir, nil
}

// writeManifest 落盘批次 manifest（generate 与 generate-llm 共用）。
func writeManifest(path string, b Batch) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// writeJSONL 写 jsonl（每行一条 JSON；struct 字段序 + map 键序确定 → 同 seed 字节级复现）。
func writeJSONL(path string, records []Sample) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
