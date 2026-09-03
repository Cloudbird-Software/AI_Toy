// Qwen3LLM M1 桩：模型/库不可用时的降级实现（语义与 M1 llm.go 桩一致，
// 消费方面向 LLMEngine 接口，行为不变）。
package inference

// qwen3LLMStub LLM 桩（固定返回人设问候语）。
type qwen3LLMStub struct{}

// Generate 流式生成回复（桩：固定返回"你好呀，我是小云雀"）。
func (q *qwen3LLMStub) Generate(prompt string) (string, error) {
	return "你好呀，我是小云雀", nil
}
