package inference

// Qwen3LLM Qwen3-0.6B GGUF 桩实现（后续替换为 llama.cpp Go 绑定或 sherpa-onnx LLM 通道）。
type Qwen3LLM struct {
	modelPath string
}

// NewQwen3LLM 构造 LLM 引擎（modelPath 为 GGUF 模型路径）。
func NewQwen3LLM(modelPath string) *Qwen3LLM {
	return &Qwen3LLM{modelPath: modelPath}
}

// Generate 流式生成回复（桩：固定返回"你好呀，我是小云雀"）。
func (q *Qwen3LLM) Generate(prompt string) (string, error) {
	return "你好呀，我是小云雀", nil
}
