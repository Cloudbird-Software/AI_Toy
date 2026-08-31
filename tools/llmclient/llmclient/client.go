// Package llmclient —— OpenAI 兼容 LLM API 客户端（训练前置准备卡：LLM 接入面）。
//
// 纯标准库实现（net/http + encoding/json），零新增 Go 依赖（AGENTS.md：无
// license 台账的新依赖为 G0 红线，本包不引入）。供 synthgen generate-llm（真实
// 合成数据）与 toyjudge LLM 评审后端共用；凭据只经环境变量注入，不落仓。
//
// 环境变量契约（configs/llm/api.env.example 为模板）：
//
//	LLM_API_BASE     兼容端点，如 https://api.example.com/v1（必填）
//	LLM_API_KEY      API key（必填）
//	LLM_MODEL_TEXT   文本模型名（必填；judge/synthgen 缺省用）
//	LLM_MODELS_TEXT_POOL  逗号分隔多模型池（可选；synthgen 轮换取用，
//	                     满足 T2 单源占比 ≤30% 的多样性门禁）
package llmclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"
)

// Env 变量名（集中定义，测试可 t.Setenv 覆盖）。
const (
	EnvAPIBase     = "LLM_API_BASE"
	EnvAPIKey      = "LLM_API_KEY"
	EnvModelText   = "LLM_MODEL_TEXT"
	EnvModelsPool  = "LLM_MODELS_TEXT_POOL"
	DefaultTimeout = 120 * time.Second
)

// ErrNotConfigured —— FromEnv 缺必填变量（调用方应给出配置指引后 exit 2）。
var ErrNotConfigured = errors.New("llmclient: LLM API 未配置（需要 LLM_API_BASE/LLM_API_KEY/LLM_MODEL_TEXT）")

// Config 是客户端配置。
type Config struct {
	BaseURL string        // 不带尾斜杠，如 https://api.example.com/v1
	APIKey  string        //
	Model   string        // 缺省文本模型
	Timeout time.Duration // 缺省 DefaultTimeout
}

// FromEnv 从环境变量构造配置；任一必填缺失返回 ErrNotConfigured。
func FromEnv() (Config, error) {
	cfg := Config{
		BaseURL: strings.TrimRight(strings.TrimSpace(os.Getenv(EnvAPIBase)), "/"),
		APIKey:  strings.TrimSpace(os.Getenv(EnvAPIKey)),
		Model:   strings.TrimSpace(os.Getenv(EnvModelText)),
		Timeout: DefaultTimeout,
	}
	if cfg.BaseURL == "" || cfg.APIKey == "" || cfg.Model == "" {
		return cfg, ErrNotConfigured
	}
	return cfg, nil
}

// TextModelPool 返回文本模型池：优先 LLM_MODELS_TEXT_POOL（逗号分隔、去空），
// 否则退化为单模型 [LLM_MODEL_TEXT]。空池返回 nil。
func TextModelPool() []string {
	if pool := os.Getenv(EnvModelsPool); pool != "" {
		var models []string
		for _, m := range strings.Split(pool, ",") {
			if m = strings.TrimSpace(m); m != "" {
				models = append(models, m)
			}
		}
		return models
	}
	if m := strings.TrimSpace(os.Getenv(EnvModelText)); m != "" {
		return []string{m}
	}
	return nil
}

// Message 是一条 chat 消息（role: system|user|assistant）。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Opts 是单次调用的可选项（nil 时全取缺省）。
type Opts struct {
	Temperature float64 // 缺省 0（评审要求确定性）
	MaxTokens   int     // 0 = 不传
	Model       string  // 覆盖缺省模型（synthgen 轮换池用）
}

// Client 是 OpenAI 兼容 chat completions 客户端。
type Client struct {
	cfg  Config
	http *http.Client
	rng  *rand.Rand
}

// New 构造客户端。
func New(cfg Config) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout}, rng: rand.New(rand.NewSource(time.Now().UnixNano()))}
}

// Model 返回缺省模型名。
func (c *Client) Model() string { return c.cfg.Model }

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
}

type chatChoice struct {
	Message Message `json:"message"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Chat 调用 POST {BaseURL}/chat/completions，返回首个 choice 的 content。
// 429/5xx 指数退避重试（≤2 次）；4xx 其它错误立即返回。
func (c *Client) Chat(ctx context.Context, messages []Message, opts *Opts) (string, error) {
	o := Opts{}
	if opts != nil {
		o = *opts
	}
	model := c.cfg.Model
	if o.Model != "" {
		model = o.Model
	}
	body, err := json.Marshal(chatRequest{
		Model: model, Messages: messages,
		Temperature: o.Temperature, MaxTokens: o.MaxTokens,
	})
	if err != nil {
		return "", err
	}
	const maxAttempts = 3
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 { // 指数退避 + 抖动（1-3s 起）
			d := time.Duration(1<<attempt) * time.Second
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(d):
			}
		}
		content, retryable, err := c.attempt(ctx, body)
		if err == nil {
			return content, nil
		}
		lastErr = err
		if !retryable {
			return "", err
		}
	}
	return "", fmt.Errorf("llmclient: %d 次尝试均失败: %w", maxAttempts, lastErr)
}

func (c *Client) attempt(ctx context.Context, body []byte) (content string, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", true, err // 网络错误可重试
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return "", true, err
	}
	if resp.StatusCode >= 400 {
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return "", retry, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(raw, 512))
	}
	var out chatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", true, fmt.Errorf("响应非法 JSON: %w", err)
	}
	if out.Error != nil {
		return "", false, fmt.Errorf("API 错误: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", true, errors.New("响应无 choices")
	}
	return out.Choices[0].Message.Content, false, nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

// ExtractJSON 从模型输出中提取首个完整 JSON 值（对象或数组；容忍 ```json 围栏
// 与前后缀文本）。返回错误当且仅当找不到平衡的 {...} / [...]。
func ExtractJSON(s string) ([]byte, error) {
	start, open := -1, byte(0)
	for i := 0; i < len(s); i++ {
		if s[i] == '{' || s[i] == '[' {
			start, open = i, s[i]
			break
		}
	}
	if start < 0 {
		return nil, errors.New("llmclient: 输出中未找到 JSON 值")
	}
	closeCh := byte('}')
	if open == '[' {
		closeCh = ']'
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		ch := s[i]
		if esc {
			esc = false
			continue
		}
		if ch == '\\' && inStr {
			esc = true
			continue
		}
		if ch == '"' {
			inStr = !inStr
			continue
		}
		if inStr {
			continue
		}
		if ch == open {
			depth++
		} else if ch == closeCh {
			depth--
			if depth == 0 {
				return []byte(s[start : i+1]), nil
			}
		}
	}
	return nil, errors.New("llmclient: 输出中未找到平衡的 JSON 值")
}
