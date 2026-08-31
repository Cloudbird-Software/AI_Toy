package synthgen

// cli_generate_llm_test —— generate-llm CLI 端到端：httptest 假 OpenAI 端点 +
// 环境变量注入，覆盖 CLI 接线（env 配置、注册表查找、批次落盘、退出码）。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// parsePromptCount 从合成 prompt 首段「…恰好 N 条…」解析条数。
func parsePromptCount(content string) (int, bool) {
	const marker = "恰好 "
	i := strings.Index(content, marker)
	if i < 0 {
		return 0, false
	}
	rest := content[i+len(marker):]
	end := strings.Index(rest, " 条")
	if end < 0 {
		return 0, false
	}
	n := 0
	for _, ch := range rest[:end] {
		if ch < '0' || ch > '9' {
			return 0, false
		}
		n = n*10 + int(ch-'0')
	}
	return n, true
}

func TestCLIGenerateLLM(t *testing.T) {
	chdir(t, t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.Error(w, "bad path", http.StatusNotFound)
			return
		}
		var req struct {
			Messages []struct{ Content string }
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		k := 1
		if len(req.Messages) > 0 {
			if got, ok := parsePromptCount(req.Messages[0].Content); ok {
				k = got
			}
		}
		utts := make([]string, k)
		for i := range utts {
			utts[i] = strings.Repeat("语", 5)
		}
		inner, _ := json.Marshal(utts)           // 数组文本
		quoted, _ := json.Marshal(string(inner)) // 再包成 JSON 字符串（content 是 string）
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":` + string(quoted) + `}}]}`))
	}))
	defer srv.Close()
	t.Setenv("LLM_API_BASE", srv.URL)
	t.Setenv("LLM_API_KEY", "sk-test")
	t.Setenv("LLM_MODEL_TEXT", "m-1")
	t.Setenv("LLM_MODELS_TEXT_POOL", "m-1,m-2,m-3,m-4")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"register", "--id", "gen-cli", "--version", "1.0.0",
		"--seed-policy", "fixed", "--outputs-manifest", "x.json"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("register code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"generate-llm", "--id", "gen-cli", "--n", "24", "--seed", "3"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("generate-llm code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "llm-batch gen-cli-1.0.0-seed3-n24") {
		t.Fatalf("stdout=%s", stdout.String())
	}
	batchDir := filepath.Join(BatchesDir, "gen-cli-1.0.0-seed3-n24")
	for _, f := range []string{"samples.jsonl", "synth-train.jsonl", "synth-holdout.jsonl", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(batchDir, f)); err != nil {
			t.Fatalf("缺 %s: %v", f, err)
		}
	}
}

// TestCLIGenerateLLMUnconfigured —— 未配置 API → exit 2 + 指引。
func TestCLIGenerateLLMUnconfigured(t *testing.T) {
	chdir(t, t.TempDir())
	t.Setenv("LLM_API_BASE", "")
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("LLM_MODEL_TEXT", "")
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"generate-llm", "--id", "x", "--n", "5", "--seed", "1"}, &stdout, &stderr); code != ExitInput {
		t.Fatalf("期望 exit 2，got %d（stderr=%s）", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "api.env.example") {
		t.Fatalf("缺配置指引: %s", stderr.String())
	}
}
