package toyjudge

// llmjudge_test —— LLM 评审后端：正常打分、位序无关性（A/B 同调用）、
// API 失败与非法输出计数（记 1,1 且 errs++）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Cloudbird-Software/AI_Toy/tools/llmclient/llmclient"
)

const llmTestRubricYAML = `id: 7a
high_risk: false
criteria:
  - name: 亲和力
    levels:
      - {level: 1, anchor: "生硬冷淡"}
      - {level: 2, anchor: "自然得体"}
      - {level: 3, anchor: "温暖生动"}
`

func llmTestServer(t *testing.T, status int, reply string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		body, _ := json.Marshal(map[string]any{"choices": []any{
			map[string]any{"message": map[string]string{"role": "assistant", "content": reply}}}})
		_, _ = w.Write(body)
	}))
}

func llmTestJudge(t *testing.T, srv *httptest.Server) (Judge, *atomic.Int64) {
	t.Helper()
	rubric, err := ParseRubric([]byte(llmTestRubricYAML), "7a")
	if err != nil {
		t.Fatal(err)
	}
	client := llmclient.New(llmclient.Config{BaseURL: srv.URL, APIKey: "k", Model: "test-model"})
	return NewLLMJudge(client, rubric, testWriter{})
}

type testWriter struct{}

func (testWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestLLMJudgeOK(t *testing.T) {
	srv := llmTestServer(t, 0, `{"a":1,"b":3}`)
	defer srv.Close()
	judge, errs := llmTestJudge(t, srv)
	call := PairwiseCall{
		RubricID: "7a", Criterion: "亲和力",
		Judge:  JudgeInfo{Model: "test-model", Temperature: 0},
		First:  Target{ID: "x", Content: []byte("生硬回复")},
		Second: Target{ID: "y", Content: []byte("温暖生动的回复")},
	}
	got := judge(call)
	if got.First != 1 || got.Second != 3 {
		t.Fatalf("got %+v", got)
	}
	if errs.Load() != 0 {
		t.Fatalf("errs=%d", errs.Load())
	}
}

func TestLLMJudgeFencedJSON(t *testing.T) {
	srv := llmTestServer(t, 0, "```json\n{\"a\":2,\"b\":2}\n```")
	defer srv.Close()
	judge, errs := llmTestJudge(t, srv)
	got := judge(PairwiseCall{RubricID: "7a", Criterion: "亲和力",
		Judge:  JudgeInfo{Model: "m"},
		First:  Target{ID: "x", Content: []byte("a")},
		Second: Target{ID: "y", Content: []byte("b")}})
	if got.First != 2 || got.Second != 2 || errs.Load() != 0 {
		t.Fatalf("got %+v errs=%d", got, errs.Load())
	}
}

func TestLLMJudgeErrors(t *testing.T) {
	// API 500（重试耗尽）。
	srv := llmTestServer(t, 500, "")
	defer srv.Close()
	judge, errs := llmTestJudge(t, srv)
	got := judge(PairwiseCall{RubricID: "7a", Criterion: "亲和力",
		Judge:  JudgeInfo{Model: "m"},
		First:  Target{ID: "x", Content: []byte("a")},
		Second: Target{ID: "y", Content: []byte("b")}})
	if got.First != 1 || got.Second != 1 || errs.Load() == 0 {
		t.Fatalf("失败应记 (1,1) 且 errs>0，got %+v errs=%d", got, errs.Load())
	}
}

func TestLLMJudgeOutOfBand(t *testing.T) {
	srv := llmTestServer(t, 0, `{"a":5,"b":0}`) // 越界
	defer srv.Close()
	judge, errs := llmTestJudge(t, srv)
	got := judge(PairwiseCall{RubricID: "7a", Criterion: "亲和力",
		Judge:  JudgeInfo{Model: "m"},
		First:  Target{ID: "x", Content: []byte("a")},
		Second: Target{ID: "y", Content: []byte("b")}})
	if errs.Load() != 1 {
		t.Fatalf("越界应计 1 错误，errs=%d", errs.Load())
	}
	if got.First != 1 || got.Second != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestLLMJudgeConsistentAcrossSwap(t *testing.T) {
	// 确定性模型（temperature=0 意图）：同一对内容无论 AB/BA 位序应同分
	// ——这是 run.go judgePair 一致性检查的前提。
	srv := llmTestServer(t, 0, `{"a":2,"b":3}`)
	defer srv.Close()
	judge, _ := llmTestJudge(t, srv)
	x := Target{ID: "x", Content: []byte(strings.Repeat("x", 100))}
	y := Target{ID: "y", Content: []byte(strings.Repeat("y", 100))}
	ab := judge(PairwiseCall{RubricID: "7a", Criterion: "亲和力", Judge: JudgeInfo{Model: "m"}, First: x, Second: y})
	ba := judge(PairwiseCall{RubricID: "7a", Criterion: "亲和力", Judge: JudgeInfo{Model: "m"}, First: y, Second: x})
	// 回复固定 a=2,b=3（按呈现位序）：AB 面记 x=2,y=3；BA 面（y 先手）记
	// y=2,x=3——judgePair 映射回原对得 x=3,y=2，与 AB 不一致 → 记 tie
	// （协议行为：位序偏置后端被 AB/BA 一致性检查拦截）。
	if ab.First != 2 || ab.Second != 3 || ba.First != 2 || ba.Second != 3 {
		t.Fatalf("AB/BA 位序映射错误: ab=%+v ba=%+v", ab, ba)
	}
}
