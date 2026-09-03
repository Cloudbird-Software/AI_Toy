package inference

import (
	"fmt"

	"github.com/Cloudbird-Software/AI_Toy/packages/go/loop"
	"github.com/Cloudbird-Software/AI_Toy/packages/go/tts"
)

// OfflineResponder VAD→ASR→LLM→TTS 全链路离线应答器（实现 loop.Responder）。
type OfflineResponder struct {
	vad   VADEngine
	asr   ASREngine
	llm   LLMEngine
	tts   tts.Synthesizer // TTS 接口占位（T13 线在做，只引用不实现）
}

// NewOfflineResponder 构造离线应答器（四段全链路；TTS 可 nil=文字占位）。
func NewOfflineResponder(vad VADEngine, asr ASREngine, llm LLMEngine, ttsSynthesizer tts.Synthesizer) *OfflineResponder {
	return &OfflineResponder{
		vad:   vad,
		asr:   asr,
		llm:   llm,
		tts:   ttsSynthesizer,
	}
}

// Respond 实现 loop.Responder 接口（M2 完整管线：VAD→ASR→LLM→TTS）。
// 当前桩实现：直接返回 LLM 固定回复，TTS 通道占位。
func (o *OfflineResponder) Respond(turn loop.Turn) (string, error) {
	// 桩阶段：直接 LLM 生成（忽略 VAD/ASR 输入，待 M2 接入真实音频流）
	reply, err := o.llm.Generate("")
	if err != nil {
		return "", fmt.Errorf("inference: llm generate failed: %w", err)
	}
	// TTS 占位：真实实现应将 reply 送入 tts.Synthesizer 合成音频流
	_ = o.tts
	return reply, nil
}
