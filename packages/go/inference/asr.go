package inference

// FireRedASR2 FireRedASR2 流式识别桩实现（后续替换为 sherpa-onnx FireRedASR2 模型）。
type FireRedASR2 struct {
	encoderPath string
	decoderPath string
}

// NewFireRedASR2 构造 ASR 引擎（encoder/decoder 为 ONNX 模型路径）。
func NewFireRedASR2(encoderPath, decoderPath string) *FireRedASR2 {
	return &FireRedASR2{
		encoderPath: encoderPath,
		decoderPath: decoderPath,
	}
}

// Recognize 识别音频流文本（桩：固定返回"你好"）。
func (f *FireRedASR2) Recognize(stream []byte) (string, error) {
	return "你好", nil
}
