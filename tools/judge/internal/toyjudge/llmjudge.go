// llmjudge —— LLM 评审后端（训练前置准备卡）：LLM_JUDGE=1 时替代
// DeterministicJudge 桩，经 tools/llmclient 调 OpenAI 兼容 API。评审协议不变
// （docs/gates/judge-protocol.md）：pairwise+swap 由 run.go 的 judgePair 保证，
// 本后端单次调用同时给 A/B 打三级分（无位序偏置面）；judge 身份
// （model/temperature）沿用 model.yaml 锁定值——API 端点须提供同名模型。
package toyjudge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/Cloudbird-Software/AI_Toy/tools/llmclient/llmclient"
)

// LLMJudge 持有 LLM 客户端与 rubric（取 criterion 锚定样例）。
type LLMJudge struct {
	client *llmclient.Client
	rubric *Rubric
	stderr io.Writer
	errs   atomic.Int64
	mu     sync.Mutex // 串行化 API 调用（评审可复现性优先于吞吐）
}

// NewLLMJudge 构造 LLM 评审后端；返回的 Judge 与错误计数器（API/解析失败次数，
// CLI 检查后决定 exit 2——失败调用记 (1,1) 不静默吞掉）。
func NewLLMJudge(client *llmclient.Client, rubric *Rubric, stderr io.Writer) (Judge, *atomic.Int64) {
	j := &LLMJudge{client: client, rubric: rubric, stderr: stderr}
	return j.judge, &j.errs
}

// judge 单次评审：prompt = criterion 三级锚定 + A/B 内容，输出 {"a":1..3,"b":1..3}。
func (j *LLMJudge) judge(call PairwiseCall) LevelPair {
	criterion := j.findCriterion(call.Criterion)
	prompt := fmt.Sprintf(`你是儿童 AI 玩具产物的 LLM 评审员。只依据下面的三级量表评分，不考虑其他标准。
评分维度：%s
三级量表（锚定样例）：
1 = %s
2 = %s
3 = %s
产物 A：
<<<%s>>>
产物 B：
<<<%s>>>
分别给产物 A 与产物 B 打 1-3 级。只输出一个 JSON 对象：{"a": <1-3 的整数>, "b": <1-3 的整数>}`,
		call.Criterion, criterion.Levels[0].Anchor, criterion.Levels[1].Anchor, criterion.Levels[2].Anchor,
		call.First.Content, call.Second.Content)
	j.mu.Lock()
	defer j.mu.Unlock()
	out, err := j.client.Chat(context.Background(),
		[]llmclient.Message{{Role: "user", Content: prompt}},
		&llmclient.Opts{Model: call.Judge.Model, Temperature: call.Judge.Temperature})
	if err != nil {
		j.fail(call, "API 调用失败: %v", err)
		return LevelPair{First: 1, Second: 1}
	}
	raw, err := llmclient.ExtractJSON(out)
	if err != nil {
		j.fail(call, "输出无 JSON: %v（原文 %.120s）", err, out)
		return LevelPair{First: 1, Second: 1}
	}
	var levels struct {
		A int `json:"a"`
		B int `json:"b"`
	}
	if err := json.Unmarshal(raw, &levels); err != nil {
		j.fail(call, "JSON 解析失败: %v", err)
		return LevelPair{First: 1, Second: 1}
	}
	if levels.A < 1 || levels.A > 3 || levels.B < 1 || levels.B > 3 {
		j.fail(call, "评分越界: a=%d b=%d（须 1..3）", levels.A, levels.B)
		return LevelPair{First: 1, Second: 1}
	}
	return LevelPair{First: levels.A, Second: levels.B}
}

func (j *LLMJudge) findCriterion(name string) Criterion {
	for _, c := range j.rubric.Criteria {
		if c.Name == name {
			return c
		}
	}
	return Criterion{Name: name, Levels: []Level{{Level: 1, Anchor: "未知锚定"}, {Level: 2, Anchor: "未知锚定"}, {Level: 3, Anchor: "未知锚定"}}}
}

func (j *LLMJudge) fail(call PairwiseCall, format string, a ...any) {
	j.errs.Add(1)
	fmt.Fprintf(j.stderr, "llmjudge[%s/%s/%s]: %s\n", call.Judge.Model, call.RubricID, call.Criterion, fmt.Sprintf(format, a...))
}
