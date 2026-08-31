package synthgen

// generate_llm —— LLM 合成正样本（训练前置准备卡）：用真实 LLM（OpenAI 兼容
// API，tools/llmclient）替代 generate.go 的桩词表，产出与桩批次同构的批次
// 目录（samples + synth-train/synth-holdout + manifest，8:2 确定性切分）。
//
// 溯源纪律（AGENTS.md / T2）：UpstreamModel 记录该样本实际使用的上游模型
// （多模型池轮转，LLM_MODELS_TEXT_POOL），dist-check 的单源占比 ≤30% 门禁
// 对真实模型名照常执法——单模型池会诚实失败（exit 20），须配 ≥4 个模型。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ChatFn 是 LLM 调用抽象（测试注入假实现；生产经 llmclient）。
// 返回模型输出的原始文本。model 参数用于多模型轮转。
type ChatFn func(ctx context.Context, prompt, model string) (string, error)

// llmBatchSize 每次 LLM 调用请求的条数（按 topic 分组，控制往返次数）。
const llmBatchSize = 16

// llmPrompt 构造合成 prompt：按 topic+speed 生成儿童玩具语境的中文话轮语料。
func llmPrompt(topic, speed string, n int) string {
	speedHint := map[string]string{
		"slow":   "语速慢、口齿不清（3-4 岁低龄）",
		"normal": "语速正常",
		"fast":   "语速快、话密（7 岁以上）",
	}[speed]
	return fmt.Sprintf(`你是儿童 AI 玩具的对话语料合成器。请生成恰好 %d 条中国家庭儿童对玩具说的话。
主题：%s。说话人设定：%s。
要求：每条 4-20 个汉字，口语化、符合该年龄段表达，可直接作 TTS 文本；不要重复句式。
只输出一个 JSON 数组，如 ["...", "..."]，不要任何其他文字。`, n, topic, speedHint)
}

// GenerateLLMSamples 生成 n 条 LLM 合成样本：speaker/speed/topic 由 seed 确定性
// 分配（与桩批次同分布），text 由 LLM 生成（按 topic 分组、每组一次调用、
// 模型池轮转）。任何一次 LLM 调用或解析失败即整体失败（不落部分批次）。
func GenerateLLMSamples(ctx context.Context, g Generator, n int, seed int64, models []string, chat ChatFn) ([]Sample, error) {
	if len(models) == 0 {
		return nil, fmt.Errorf("模型池为空（LLM_MODELS_TEXT_POOL 或 --models）")
	}
	if chat == nil {
		return nil, fmt.Errorf("chat 函数为 nil")
	}
	// 与桩批次同源的确定性 slot 分配（复用 GenerateSamples 的随机流语义）。
	slots := GenerateSamples(g, n, seed) // 只取 payload 的 speaker/speed/topic 分布
	type slot struct{ speaker, speed, topic string }
	ss := make([]slot, n)
	for i, s := range slots {
		ss[i] = slot{s.Payload["speaker"], s.Payload["speed"], s.Payload["topic"]}
	}
	// 模型按样本轮转（i%len(models) → 单源占比 ≈ 1/len(models)，dist-check 门禁
	// 可过），再按 (topic, model) 分组批量调用——组内同 topic 同模型，prompt 只差条数。
	used := make([]string, n)
	groups := map[string]map[string][]int{}
	for i := range ss {
		used[i] = models[i%len(models)]
		if groups[ss[i].topic] == nil {
			groups[ss[i].topic] = map[string][]int{}
		}
		groups[ss[i].topic][used[i]] = append(groups[ss[i].topic][used[i]], i)
	}
	texts := make([]string, n)
	for _, topic := range [...]string{"bedtime", "play", "learning", "emotion", "safety"} {
		for _, model := range models {
			idx := groups[topic][model]
			for start := 0; start < len(idx); start += llmBatchSize {
				end := start + llmBatchSize
				if end > len(idx) {
					end = len(idx)
				}
				batch := idx[start:end]
				out, err := chat(ctx, llmPrompt(topic, ss[batch[0]].speed, len(batch)), model)
				if err != nil {
					return nil, fmt.Errorf("LLM 调用失败（topic=%s model=%s）: %w", topic, model, err)
				}
				var utterances []string
				if err := json.Unmarshal([]byte(out), &utterances); err != nil {
					return nil, fmt.Errorf("LLM 输出非 JSON 数组（topic=%s model=%s）: %w", topic, model, err)
				}
				if len(utterances) != len(batch) {
					return nil, fmt.Errorf("LLM 条数不符（topic=%s 期望 %d 实得 %d）", topic, len(batch), len(utterances))
				}
				for k, i := range batch {
					if utterances[k] == "" {
						return nil, fmt.Errorf("LLM 输出含空条目（topic=%s）", topic)
					}
					texts[i] = utterances[k]
				}
			}
		}
	}
	samples := make([]Sample, n)
	for i := range samples {
		samples[i] = Sample{
			SampleID: fmt.Sprintf("%s-%d-%06d", g.ID, seed, i),
			Provenance: Provenance{GeneratorID: g.ID, GeneratorVersion: g.Version, Seed: seed,
				UpstreamModel: used[i]},
			Payload: map[string]string{
				"speaker": ss[i].speaker,
				"speed":   ss[i].speed,
				"topic":   ss[i].topic,
				"text":    texts[i],
			},
		}
	}
	return samples, nil
}

// GenerateBatchLLM 生成并落盘 LLM 批次（目录结构与 GenerateBatch 完全一致）。
func GenerateBatchLLM(ctx context.Context, g Generator, n int, seed int64, models []string, chat ChatFn) (Batch, string, error) {
	samples, err := GenerateLLMSamples(ctx, g, n, seed, models, chat)
	if err != nil {
		return Batch{}, "", err
	}
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
	return b, dir, writeManifest(filepath.Join(dir, "manifest.json"), b)
}
