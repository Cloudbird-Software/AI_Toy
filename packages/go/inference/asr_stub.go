// M1 桩实现保留（fallback 面）：encoder/decoder 不可用时 NewFireRedASR2
// 降级到此实现，保住 M1 消费方（loop/OfflineResponder）的行为契约。
package inference

// fireRedStub FireRedASR2 识别桩（M1 语义：固定返回「你好」）。
type fireRedStub struct{}

func (fireRedStub) Recognize(_ []byte) (string, error) { return "你好", nil }
