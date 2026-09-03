package inference

// VADEngine 语音活动检测接口（Silero VAD 真推理实现见 vad.go；M1 桩保留作 fallback）。
type VADEngine interface {
	// PushFrame 推送音频帧，返回 VAD 事件（nil=无事件）。
	PushFrame(data []byte) VADEvent
}

// VADEvent 语音活动检测事件。
type VADEvent struct {
	Kind  VADKind
	Score float32 // 置信度（0~1）
}

// VADKind VAD 事件类型。
type VADKind int

const (
	VADNone VADKind = iota
	VADVoiceStart
	VADVoiceEnd
)

// ASREngine 自动语音识别接口（FireRedASR2 真推理实现见 asr.go；M1 桩保留作 fallback）。
type ASREngine interface {
	// Recognize 识别音频流文本（stream 为完整音频字节）。
	Recognize(stream []byte) (string, error)
}

// LLMEngine 大语言模型接口（后续 sherpa-onnx llama.cpp GGUF 实现）。
type LLMEngine interface {
	// Generate 流式生成回复（prompt 为输入提示）。
	Generate(prompt string) (string, error)
}
