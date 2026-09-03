// indextts —— IndexTTS-1.5（Apache-2.0）云端高质量档客户端（issue #132 / ADR-0008）。
//
// 定位：L0/L1 云通道的 Synthesizer 实现，与端侧 MeloSynthesizer 同接口插入
// tts.Router 决策序（离线优先=Router L2 端侧直走 / 云首包超时降级表，本客户端
// 不自带重试——路由语义归 Router，客户端只如实上抛故障）。
//
// wire 契约（自部署服务，服务端不在本仓交付范围；ADR-0008 记录契约）：
//
//	POST {Endpoint}
//	body: {"text","voice","format":"pcm_s16le","sample_rate":22050}
//	resp: chunked 二进制 PCM s16le 单声道流，连接关闭=流尽。
//
// voice 仅透传官方音色 ID（服务端白名单裁决；客户端不提供克隆引用面——T13
// 红线：禁克隆任何真实儿童声音）。
package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// HTTPClient 可注入 HTTP 执行面（测试换 httptest；生产可挂熔断/限流）。
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// IndexTTSConfig 云端档客户端配置。
type IndexTTSConfig struct {
	Endpoint   string // 完整 URL（如 https://tts.internal/v1/tts）
	APIKey     string // 持有者注入；不得硬编码/落日志
	SampleRate int    // 期望采样率（服务端协商字段；默认 22050）
	ChunkBytes int    // 流分块字节数（默认 4096 ≈ 2048 样本）
	Client     HTTPClient
}

// IndexTTSClient 云端合成客户端（Synthesizer 实现）。
type IndexTTSClient struct {
	endpoint   string
	apiKey     string
	sampleRate int
	chunkBytes int
	client     HTTPClient
}

// NewIndexTTSClient 构造：Endpoint 必填（缺→error，不静默）。
func NewIndexTTSClient(cfg IndexTTSConfig) (*IndexTTSClient, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("tts: IndexTTSClient 须配置 Endpoint")
	}
	if cfg.SampleRate == 0 {
		cfg.SampleRate = 22050
	}
	if cfg.ChunkBytes == 0 {
		cfg.ChunkBytes = 4096
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{} // 流式响应：不设整体超时，取消走 Cancel
	}
	return &IndexTTSClient{
		endpoint: cfg.Endpoint, apiKey: cfg.APIKey,
		sampleRate: cfg.SampleRate, chunkBytes: cfg.ChunkBytes,
		client: cfg.Client,
	}, nil
}

// indexTTSReq wire 请求体（json 字段=服务端契约，变更须同步 ADR-0008）。
type indexTTSReq struct {
	Text       string `json:"text"`
	Voice      string `json:"voice"`
	Format     string `json:"format"`
	SampleRate int    `json:"sample_rate"`
}

// Synthesize 实现 Synthesizer：POST → 2xx 起流；非 2xx/传输错→error
// （Router 据此降级端侧）。Voice 空→"ZH" 默认官方音色。
func (c *IndexTTSClient) Synthesize(req Request) (AudioStream, error) {
	if req.Text == "" {
		return nil, ErrEmptyText
	}
	voice := req.Voice
	if voice == "" {
		voice = "ZH"
	}
	body, err := json.Marshal(indexTTSReq{
		Text: req.Text, Voice: voice,
		Format: "pcm_s16le", SampleRate: c.sampleRate,
	})
	if err != nil {
		return nil, fmt.Errorf("tts: indextts encode: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("tts: indextts request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.client.Do(httpReq)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("tts: indextts: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)) // 供服务端诊断（截断读）
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("tts: indextts status %d", resp.StatusCode)
	}
	return &httpStream{body: resp.Body, cancel: cancel, chunkBytes: c.chunkBytes}, nil
}

// httpStream 云端 PCM 流：chunked 读，EOF=流尽；中途 err 终止固化（不重播
// 半句——Router passStream 的 partial 语义依赖本契约）；Cancel 幂等。
type httpStream struct {
	body       io.ReadCloser
	cancel     context.CancelFunc
	chunkBytes int

	mu       sync.Mutex
	terminal error
	seq      int
}

func (s *httpStream) Next() (Chunk, error) {
	s.mu.Lock()
	if s.terminal != nil {
		t := s.terminal
		s.mu.Unlock()
		return Chunk{}, t
	}
	s.mu.Unlock()
	buf := make([]byte, s.chunkBytes)
	n, err := io.ReadFull(s.body, buf)
	switch {
	case err == nil:
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		if n == 0 {
			s.finish(io.EOF)
			return Chunk{}, io.EOF
		}
		err = nil // 尾部残块：交付后下轮 EOF
	default:
		s.finish(err)
		return Chunk{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal != nil { // 等待读取期间被 Cancel：终止态优先
		t := s.terminal
		return Chunk{}, t
	}
	s.seq++
	data := make([]byte, n)
	copy(data, buf[:n])
	return Chunk{Data: data, Seq: s.seq, Final: false}, nil
}

// Cancel 打断执行面：终止流（幂等）+ 释放连接（context + Close 双保险）。
func (s *httpStream) Cancel() error {
	s.mu.Lock()
	if s.terminal == nil {
		s.terminal = ErrCanceled
	}
	body, cancel := s.body, s.cancel
	s.mu.Unlock()
	cancel()
	_ = body.Close()
	return nil
}

// finish 流终止固化（EOF/中途 err——Cancel 之外的唯一两条出口）。
func (s *httpStream) finish(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal == nil {
		s.terminal = err
	}
}
