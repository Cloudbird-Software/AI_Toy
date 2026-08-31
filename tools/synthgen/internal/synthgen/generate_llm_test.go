package synthgen

// generate_llm_test —— LLM 合成批次：批次结构（8:2 切分/manifest/溯源戳）、
// 模型池轮转、错误传播。ChatFn 注入假实现，不打真 API。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeChat 按 topic 关键词返回恰好 k 条假话术的 JSON 数组（记录被轮转到的模型）。
func fakeChat(calls *[]string) ChatFn {
	return func(_ context.Context, prompt, model string) (string, error) {
		*calls = append(*calls, model)
		// prompt 形如 “…恰好 %d 条…”：条数取自 prompt。
		var k int
		if _, err := fmt.Sscanf(prompt, "你是儿童 AI 玩具的对话语料合成器。请生成恰好 %d 条", &k); err != nil {
			return "", err
		}
		utts := make([]string, k)
		for i := range utts {
			utts[i] = fmt.Sprintf("话术_%s_%d", model, i)
		}
		data, _ := json.Marshal(utts)
		return string(data), nil
	}
}

func TestGenerateBatchLLM(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	g := Generator{ID: "gen-llm-test", Version: "1.0.0", SeedPolicy: "fixed", OutputsManifest: "stub"}
	models := []string{"m-a", "m-b", "m-c", "m-d"}
	calls := []string{}
	b, batchDir, err := GenerateBatchLLM(context.Background(), g, 40, 7, models, fakeChat(&calls))
	if err != nil {
		t.Fatal(err)
	}
	if b.N != 40 || b.TrainN+b.HoldoutN != 40 || b.HoldoutN != 8 {
		t.Fatalf("manifest 切分错误: %+v", b)
	}
	// 批次文件齐全。
	for _, f := range []string{"samples.jsonl", "synth-train.jsonl", "synth-holdout.jsonl", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(batchDir, f)); err != nil {
			t.Fatalf("缺 %s: %v", f, err)
		}
	}
	// 溯源与 payload：text 来自 LLM、upstream 在池内、多样性字段非空。
	raw, err := os.ReadFile(filepath.Join(batchDir, "samples.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	seenModels := map[string]bool{}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var s Sample
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(s.Payload["text"], "话术_") {
			t.Fatalf("text 非 LLM 产物: %q", s.Payload["text"])
		}
		if !strings.Contains(strings.Join(models, " "), s.Provenance.UpstreamModel) {
			t.Fatalf("upstream %q 不在池内", s.Provenance.UpstreamModel)
		}
		seenModels[s.Provenance.UpstreamModel] = true
		for _, f := range []string{"speaker", "speed", "topic"} {
			if s.Payload[f] == "" {
				t.Fatalf("多样性字段 %s 为空", f)
			}
		}
		n++
	}
	if n != 40 {
		t.Fatalf("samples 条数=%d", n)
	}
	if len(seenModels) < 2 {
		t.Fatalf("模型池未轮转（只用了 %v）", seenModels)
	}
	// dist-check 兼容：单源占比 ≤0.30（4 模型轮转）。
	records := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var m map[string]any
		_ = json.Unmarshal([]byte(line), &m)
		records = append(records, m)
	}
	if rep := Evaluate(records, nil); !rep.OK {
		t.Fatalf("dist-check 不通过: share=%.2f", rep.SingleSourceShare)
	}
}

func TestGenerateLLMSamplesErrors(t *testing.T) {
	g := Generator{ID: "gen-llm-err", Version: "1.0.0"}
	// 空 model 池。
	if _, err := GenerateLLMSamples(context.Background(), g, 4, 1, nil, fakeChat(&[]string{})); err == nil {
		t.Fatal("空模型池应报错")
	}
	// LLM 返回非 JSON 数组。
	bad := func(context.Context, string, string) (string, error) { return "not json", nil }
	if _, err := GenerateLLMSamples(context.Background(), g, 4, 1, []string{"m"}, bad); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
	// 条数不符。
	wrong := func(_ context.Context, _ string, _ string) (string, error) { return `["a"]`, nil }
	if _, err := GenerateLLMSamples(context.Background(), g, 8, 1, []string{"m"}, wrong); err == nil {
		t.Fatal("条数不符应报错")
	}
	// LLM 错误传播。
	failing := func(context.Context, string, string) (string, error) { return "", fmt.Errorf("boom") }
	if _, err := GenerateLLMSamples(context.Background(), g, 4, 1, []string{"m"}, failing); err == nil {
		t.Fatal("LLM 错误应传播")
	}
}
