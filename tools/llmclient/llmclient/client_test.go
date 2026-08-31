package llmclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFromEnvMissing(t *testing.T) {
	t.Setenv(EnvAPIBase, "")
	t.Setenv(EnvAPIKey, "")
	t.Setenv(EnvModelText, "")
	if _, err := FromEnv(); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("期望 ErrNotConfigured，got %v", err)
	}
}

func TestFromEnvOK(t *testing.T) {
	t.Setenv(EnvAPIBase, "https://api.example.com/v1/")
	t.Setenv(EnvAPIKey, "sk-test")
	t.Setenv(EnvModelText, "test-model")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "https://api.example.com/v1" { // 尾斜杠剥除
		t.Fatalf("BaseURL=%q", cfg.BaseURL)
	}
}

func TestTextModelPool(t *testing.T) {
	t.Setenv(EnvModelsPool, " a , b ,c,")
	got := strings.Join(TextModelPool(), ",")
	if got != "a,b,c" {
		t.Fatalf("pool=%q", got)
	}
	t.Setenv(EnvModelsPool, "")
	t.Setenv(EnvModelText, "solo")
	if pool := TextModelPool(); len(pool) != 1 || pool[0] != "solo" {
		t.Fatalf("pool=%v", pool)
	}
	t.Setenv(EnvModelText, "")
	if pool := TextModelPool(); pool != nil {
		t.Fatalf("空池期望 nil，got %v", pool)
	}
}

// chatServer 模拟 OpenAI 兼容端点：校验 Authorization/模型名，回显固定 content。
func chatServer(t *testing.T, wantModel, content string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.Error(w, "bad path", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if req.Model != wantModel {
			http.Error(w, "model mismatch: "+req.Model, http.StatusBadRequest)
			return
		}
		if status != 0 { // 注入一次性错误（首次），成功（重试后）
			status = 0
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": content}}},
		})
	}))
}

func TestChatOK(t *testing.T) {
	srv := chatServer(t, "test-model", `{"answer": 42}`, 0)
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL, APIKey: "sk-test", Model: "test-model"})
	out, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, &Opts{Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"answer": 42}` {
		t.Fatalf("out=%q", out)
	}
}

func TestChatRetryOn5xx(t *testing.T) {
	srv := chatServer(t, "m", "ok", 500) // 首次 500，重试成功
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL, APIKey: "sk-test", Model: "m"})
	out, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil || out != "ok" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestChatNoRetryOn4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"bad key"}}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL, APIKey: "sk-test", Model: "m"})
	if _, err := c.Chat(context.Background(), []Message{{Role: "user"}}, nil); err == nil {
		t.Fatal("期望 401 立即失败（不重试）")
	}
}

func TestExtractJSON(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{`前缀 {"a":1} 后缀`, `{"a":1}`, true},
		{"```json\n{\"a\": {\"b\": \"x}y\"}}\n```", `{"a": {"b": "x}y"}}`, true},
		{`数组 ["a", {"b": "]"}] 尾`, `["a", {"b": "]"}]`, true},
		{"```json\n[\"甲\",\"乙\"]\n```", `["甲","乙"]`, true},
		{`没有对象`, ``, false},
		{`{"a":1`, ``, false},
		{`["a"`, ``, false},
	} {
		got, err := ExtractJSON(tc.in)
		if tc.ok != (err == nil) {
			t.Fatalf("in=%q err=%v", tc.in, err)
		}
		if tc.ok && string(got) != tc.want {
			t.Fatalf("in=%q got=%q want=%q", tc.in, got, tc.want)
		}
	}
}
