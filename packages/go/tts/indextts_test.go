// indextts 测试：云端客户端 wire 契约（httptest 注入）——流式读、尾残块、
// 非 2xx 上抛、Cancel 幂等、voice 透传。
package tts

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// newIndexTTSServer 桩服务端：nChunks 个递增 PCM 块（s16le 模式）+ 每块
// delayMs 延迟；voiceSink 记录末次请求 voice（白名单透传断言面）。
func newIndexTTSServer(t *testing.T, nChunks, delayMs int, voiceSink func(voice string)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		var body indexTTSReq
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "body", http.StatusBadRequest)
			return
		}
		if body.Format != "pcm_s16le" {
			http.Error(w, "format", http.StatusBadRequest)
			return
		}
		if voiceSink != nil {
			voiceSink(body.Voice)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		f := w.(http.Flusher)
		for i := 0; i < nChunks; i++ {
			if delayMs > 0 {
				time.Sleep(time.Duration(delayMs) * time.Millisecond)
			}
			var b [4]byte
			binary.LittleEndian.PutUint16(b[:2], uint16(1000+i))
			binary.LittleEndian.PutUint16(b[2:], uint16(2000+i))
			if _, err := w.Write(b[:]); err != nil {
				return
			}
			f.Flush()
		}
	}))
}

func drainIndexStream(t *testing.T, s AudioStream) ([][]byte, int) {
	t.Helper()
	var chunks [][]byte
	total := 0
	for {
		c, err := s.Next()
		if err != nil {
			return chunks, total
		}
		chunks = append(chunks, c.Data)
		total += len(c.Data)
	}
}

func TestIndexTTSStreamRead(t *testing.T) {
	srv := newIndexTTSServer(t, 8, 0, nil)
	defer srv.Close()
	c, err := NewIndexTTSClient(IndexTTSConfig{Endpoint: srv.URL, ChunkBytes: 4})
	if err != nil {
		t.Fatalf("NewIndexTTSClient: %v", err)
	}
	st, err := c.Synthesize(Request{Text: "云端流", Voice: "ZH", Tier: 0})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	chunks, total := drainIndexStream(t, st)
	if total != 32 { // 8 块 × 4B
		t.Fatalf("总字节=%d，须 32", total)
	}
	if len(chunks) != 8 {
		t.Fatalf("块数=%d，须 8", len(chunks))
	}
	for i, c := range chunks {
		_ = c
		_ = i
	}
	// 终止态固化：EOF 恒返
	if _, err := st.Next(); err == nil {
		t.Fatal("流尽后须 err（EOF）")
	}
	if err := st.Cancel(); err != nil {
		t.Fatalf("EOF 后 Cancel 幂等: %v", err)
	}
}

func TestIndexTTSTailPartialChunk(t *testing.T) {
	// 尾残块（ChunkBytes 不整除服务端块）须交付后下一轮 EOF。
	srv := newIndexTTSServer(t, 3, 0, nil) // 12B
	defer srv.Close()
	c, _ := NewIndexTTSClient(IndexTTSConfig{Endpoint: srv.URL, ChunkBytes: 5})
	st, err := c.Synthesize(Request{Text: "尾块", Tier: 0})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	chunks, total := drainIndexStream(t, st)
	if total != 12 || len(chunks) != 3 { // 5+5+2
		t.Fatalf("chunks=%d total=%d，须 3/12", len(chunks), total)
	}
}

func TestIndexTTSErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c, _ := NewIndexTTSClient(IndexTTSConfig{Endpoint: srv.URL})
	if _, err := c.Synthesize(Request{Text: "故障", Tier: 0}); err == nil {
		t.Fatal("非 2xx 须上抛（Router 降级表依赖）")
	}
}

func TestIndexTTSCancelTerminates(t *testing.T) {
	srv := newIndexTTSServer(t, 100, 20, nil) // 长流
	defer srv.Close()
	c, _ := NewIndexTTSClient(IndexTTSConfig{Endpoint: srv.URL, ChunkBytes: 4})
	st, err := c.Synthesize(Request{Text: "长流", Tier: 0})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if _, err := st.Next(); err != nil {
		t.Fatalf("首块: %v", err)
	}
	if err := st.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := st.Cancel(); err != nil {
		t.Fatalf("Cancel 幂等: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := st.Next()
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Cancel 后 Next 须 err")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel 后 Next 挂起（终止未固化）")
	}
}

func TestIndexTTSRequiresEndpoint(t *testing.T) {
	if _, err := NewIndexTTSClient(IndexTTSConfig{}); err == nil {
		t.Fatal("缺 Endpoint 须拒绝构造")
	}
	if _, err := NewIndexTTSClient(IndexTTSConfig{Endpoint: "http://x"}); err != nil {
		t.Fatalf("合法配置: %v", err)
	}
}

func TestIndexTTSVoiceDefaultAndPassthrough(t *testing.T) {
	var mu sync.Mutex
	var got []string
	srv := newIndexTTSServer(t, 1, 0, func(v string) {
		mu.Lock()
		got = append(got, v)
		mu.Unlock()
	})
	defer srv.Close()
	c, _ := NewIndexTTSClient(IndexTTSConfig{Endpoint: srv.URL})
	if _, err := c.Synthesize(Request{Text: "默认", Tier: 0}); err != nil {
		t.Fatalf("默认: %v", err)
	}
	if _, err := c.Synthesize(Request{Text: "透传", Voice: "ZH_CHILD_WARM", Tier: 0}); err != nil {
		t.Fatalf("透传: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != "ZH" || got[1] != "ZH_CHILD_WARM" {
		t.Fatalf("voice 序列=%v，须 [ZH ZH_CHILD_WARM]（服务端白名单裁决，客户端透传）", got)
	}
	if _, err := c.Synthesize(Request{Text: "", Tier: 0}); err == nil {
		t.Fatal("空文本须 ErrEmptyText")
	}
}

// 编译期守卫：两真引擎满足 Synthesizer 接口。
var (
	_ Synthesizer = (*MeloSynthesizer)(nil)
	_ Synthesizer = (*IndexTTSClient)(nil)
	_ Phonemizer  = (*ChinesePhonemizer)(nil)
	_             = fmt.Sprintf
)
