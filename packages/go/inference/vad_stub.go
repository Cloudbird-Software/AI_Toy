// M1 桩实现保留（fallback 面）：模型/库不可用时 NewSileroVAD 降级到此实现，
// 保住 M1 消费方（loop/OfflineResponder）的行为契约。
package inference

// sileroStub Silero VAD 桩（M1 语义：每 30ms 一帧，累计 2s 后触发 VoiceEnd）。
type sileroStub struct {
	totalSamples int
}

func (s *sileroStub) PushFrame(_ []byte) VADEvent {
	s.totalSamples += 512
	if s.totalSamples >= 32000 { // 2s @16kHz
		return VADEvent{Kind: VADVoiceEnd, Score: 0.99}
	}
	return VADEvent{Kind: VADNone}
}
