// Qwen3-0.6B 真推理引擎：Q4_K_M GGUF（Apache 2.0，bartowski 量化）经自研最小
// llama.cpp 动态装载绑定驱动（llm_lib.go，ABI 钉 v0.3.0，装配惯例与 ORT 绑定一致）：
//
//	prompt → Qwen3 ChatML 非思考模板（<think> 空块前置抑制思考链）
//	  → llama_tokenize(parse_special=true) → llama_decode（显式 pos）
//	  → 逐 token 贪心：argmax(llama_get_logits_ith) → token_to_piece，EOG 止
//
// 模型/库缺失时构造不失败——引擎降级为 M1 桩（llm_stub.go），
// Err()/InFallback() 暴露降级原因（消费方面向接口，行为不变）。
// 限制（PoC 口径）：贪心解码（温度面留待路径选项）；单轮生成不跨调用续写
// （每次 Generate 清 KV 重来）；单线程驱动，同 T3 契约不加锁。
package inference

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unsafe"
)

const (
	// llmSystemPrompt 玩具陪伴人设（小云雀，与 T14 runtime-fsm 人设口径一致）。
	llmSystemPrompt = "你是小云雀，一个陪伴儿童的语音助手，回答简短、友好。"
	// llmThinkOpen/Close Qwen3 思考链标记（漏出时兜底剥离）。
	llmThinkOpen  = "<think>"
	llmThinkClose = "</think>"
)

// Qwen3LLM Qwen3-0.6B GGUF 生成引擎（实现 LLMEngine）。
type Qwen3LLM struct {
	model   unsafe.Pointer
	ctx     unsafe.Pointer
	nCtx    int
	nBatch  int
	threads int
	maxNew  int

	// 桩 fallback（模型/库不可用时非 nil）
	stub    *qwen3LLMStub
	initErr error

	// 观测面（最近一次 Generate 的分段墙钟与 token 数）
	lastPromptWall time.Duration
	lastGenWall    time.Duration
	lastPromptTok  int
	lastGenTok     int
}

// NewQwen3LLM 构造 LLM 引擎（modelPath 为 GGUF 模型路径，签名与 M1 桩一致）。
// 初始化失败不报错——降级桩实现，原因经 Err() 查询。
//
// 调参：env T14_LLM_THREADS（默认 4）/ T14_LLM_MAX_TOKENS（默认 128）/
// T14_LLM_CTX（默认 512）；0.6B Q4_K_M 在 4 核 CPU 上按此规模实测（见
// reports/eval/T14/latency-report.md LLM 段）。
func NewQwen3LLM(modelPath string) *Qwen3LLM {
	q := &Qwen3LLM{}
	q.initErr = q.init(modelPath)
	if q.initErr != nil {
		q.stub = &qwen3LLMStub{}
		q.destroyPartial()
	}
	return q
}

func (q *Qwen3LLM) init(modelPath string) error {
	if _, err := os.Stat(modelPath); err != nil {
		return fmt.Errorf("inference: llm: 模型缺失: %w", err)
	}
	if err := llamaDynLoad(); err != nil {
		return err
	}
	if q.model = llamaModelLoad(modelPath); q.model == nil {
		return fmt.Errorf("inference: llm: 模型加载失败: %s", modelPath)
	}
	q.threads = envInt("T14_LLM_THREADS", 4)
	q.maxNew = envInt("T14_LLM_MAX_TOKENS", 128)
	q.nCtx = envInt("T14_LLM_CTX", 512)
	if q.nCtx < q.maxNew+32 {
		q.nCtx = q.maxNew + 32
	}
	q.nBatch = 256
	if q.nBatch > q.nCtx {
		q.nBatch = q.nCtx
	}
	if q.ctx = llamaCtxInit(q.model, q.nCtx, q.nBatch, q.threads); q.ctx == nil {
		return fmt.Errorf("inference: llm: 上下文创建失败 (n_ctx=%d)", q.nCtx)
	}
	if n := llamaVocabNTokens(q.model); n <= 0 {
		return fmt.Errorf("inference: llm: vocab 异常 n=%d", n)
	}
	return nil
}

// Generate 以 prompt 为用户输入，经 Qwen3 ChatML 模板生成中文回复（贪心解码）。
func (q *Qwen3LLM) Generate(prompt string) (string, error) {
	if q.stub != nil {
		return q.stub.Generate(prompt)
	}
	text := qwen3ChatML(llmSystemPrompt, prompt)
	tokens, err := llamaTokenize(q.model, text)
	if err != nil {
		return "", err
	}
	// 预算：prompt+maxNew ≤ n_ctx-1，超限从头截（保留模板尾部与最新输入）
	budget := q.nCtx - 1 - q.maxNew
	if len(tokens) > budget {
		tokens = tokens[len(tokens)-budget:]
	}
	// prompt 解码（整批；KV 先清——单轮语义，不跨调用续写）
	llamaMemoryClear(q.ctx)
	pos := make([]int32, len(tokens))
	for i := range pos {
		pos[i] = int32(i)
	}
	pBegin := time.Now()
	logits, err := llamaDecodeStep(q.model, q.ctx, tokens, pos)
	if err != nil {
		return "", fmt.Errorf("inference: llm: prompt decode: %w", err)
	}
	q.lastPromptWall = time.Since(pBegin)
	q.lastPromptTok = len(tokens)

	// 逐 token 贪心解码
	gBegin := time.Now()
	q.lastGenTok = 0
	var b strings.Builder
	nextPos := int32(len(tokens))
	step := make([]int32, 1)
	stepPos := make([]int32, 1)
	for q.lastGenTok < q.maxNew && int(nextPos) < q.nCtx {
		best, bestV := int32(0), float32(-1e30)
		for i, v := range logits {
			if v > bestV {
				bestV, best = v, int32(i)
			}
		}
		if llamaIsEOG(q.model, best) {
			break
		}
		piece, err := llamaTokenPiece(q.model, best)
		if err != nil {
			return "", err
		}
		b.WriteString(piece)
		q.lastGenTok++
		step[0], stepPos[0] = best, nextPos
		nextPos++
		logits, err = llamaDecodeStep(q.model, q.ctx, step, stepPos)
		if err != nil {
			return "", fmt.Errorf("inference: llm: 生成步 %d: %w", q.lastGenTok, err)
		}
	}
	q.lastGenWall = time.Since(gBegin)
	return stripThink(b.String()), nil
}

// qwen3ChatML Qwen3 ChatML 模板（非思考模式：assistant 起始前置空 <think> 块）。
func qwen3ChatML(system, user string) string {
	var s strings.Builder
	s.WriteString("<|im_start|>system\n")
	s.WriteString(system)
	s.WriteString("<|im_end|>\n<|im_start|>user\n")
	s.WriteString(user)
	s.WriteString("<|im_end|>\n<|im_start|>assistant\n")
	s.WriteString(llmThinkOpen + "\n\n" + llmThinkClose + "\n\n")
	return s.String()
}

// stripThink 兜底剥离漏出的思考链块。
func stripThink(s string) string {
	for {
		open := strings.Index(s, llmThinkOpen)
		if open < 0 {
			break
		}
		end := strings.Index(s[open:], llmThinkClose)
		if end < 0 {
			s = s[:open]
			break
		}
		s = s[:open] + s[open+end+len(llmThinkClose):]
	}
	return strings.TrimSpace(s)
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// DefaultLLMModelPath 定位 Qwen3-0.6B GGUF：env T14_LLM_MODEL → 数据集落盘路径。
func DefaultLLMModelPath() string {
	if p := os.Getenv("T14_LLM_MODEL"); p != "" {
		return p
	}
	return "/root/workspace/datasets/models/qwen3-0.6b-gguf/Qwen_Qwen3-0.6B-Q4_K_M.gguf"
}

// Err 构造期错误（nil=真推理就绪；非 nil=已降级桩）。
func (q *Qwen3LLM) Err() error { return q.initErr }

// InFallback 是否运行在 M1 桩降级模式。
func (q *Qwen3LLM) InFallback() bool { return q.stub != nil }

// LastGenStats 最近一次 Generate 的观测面（prompt/生成墙钟与 token 数）。
func (q *Qwen3LLM) LastGenStats() (promptWall, genWall time.Duration, promptTok, genTok int) {
	return q.lastPromptWall, q.lastGenWall, q.lastPromptTok, q.lastGenTok
}

// Destroy 释放模型与上下文（进程退出面调用一次即可）。
func (q *Qwen3LLM) Destroy() error {
	q.destroyPartial()
	return nil
}

func (q *Qwen3LLM) destroyPartial() {
	if q.ctx != nil {
		llamaFreeCtx(q.ctx)
		q.ctx = nil
	}
	if q.model != nil {
		llamaFreeModel(q.model)
		q.model = nil
	}
}
