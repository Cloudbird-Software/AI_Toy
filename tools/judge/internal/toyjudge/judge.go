// judge —— 可注入的评审后端（LLM 桩）。真实 LLM 经 baml/baml_client_go 客户端
// 接入为后续卡（BAML-1）；本卡默认 DeterministicJudge。
package toyjudge

import (
	"fmt"
	"hash/fnv"
)

// Target 是一个被评产物（targets 目录下的单个文件）。
type Target struct {
	ID      string // 文件名去扩展名
	Content []byte // 原始内容
}

// PairwiseCall 是一次有序对评审调用（AB 或 BA 之一）。
type PairwiseCall struct {
	RubricID  string
	Criterion string
	Judge     JudgeInfo
	First     Target
	Second    Target
}

// LevelPair 是一次调用对先/后手候选各打的级别（1..3）。
type LevelPair struct {
	First  int
	Second int
}

// Judge 是评审后端函数类型：按 rubric 的单个 criterion 对有序对打三级分。
// 可注入（测试/后续卡换真实 LLM 客户端）；nil 时用 DeterministicJudge。
type Judge func(call PairwiseCall) LevelPair

// DeterministicJudge 桩：按目标内容哈希 + judge 配置哈希确定打分（1..3），
// 与呈现位置无关——AB/BA 对同一目标给出同一级别（位置偏置由注入的 Judge 模拟）。
func DeterministicJudge(call PairwiseCall) LevelPair {
	return LevelPair{First: deterministicLevel(call, call.First), Second: deterministicLevel(call, call.Second)}
}

func deterministicLevel(call PairwiseCall, t Target) int {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00", call.RubricID, call.Criterion, call.Judge.ConfigSHA256)
	h.Write(t.Content)
	return 1 + int(h.Sum64()%3)
}
