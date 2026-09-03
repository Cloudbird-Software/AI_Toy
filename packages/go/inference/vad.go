package inference

// SileroVAD Silero VAD 桩实现（后续替换为 sherpa-onnx ONNX Runtime 调用）。
type SileroVAD struct {
	modelPath string
	frameSize int
	voiceSamples int
	totalSamples int
}

// NewSileroVAD 构造 VAD 引擎（modelPath 为 ONNX 模型路径）。
func NewSileroVAD(modelPath string) *SileroVAD {
	return &SileroVAD{
		modelPath: modelPath,
		frameSize: 512,          // 16kHz/30ms = 480 samples，512 字节对齐
		voiceSamples: 32000,     // 2s 固定语音（32000 samples @16kHz）
	}
}

// PushFrame 推送音频帧，返回 VAD 事件（桩：每 30ms 一帧，累计 2s 后触发 VoiceEnd）。
func (v *SileroVAD) PushFrame(data []byte) VADEvent {
	v.totalSamples += v.frameSize
	if v.totalSamples >= v.voiceSamples {
		return VADEvent{Kind: VADVoiceEnd, Score: 0.99}
	}
	return VADEvent{Kind: VADNone}
}
