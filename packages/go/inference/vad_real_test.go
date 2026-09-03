// VAD 真推理测试（非门禁）：语音/静音概率区分度、事件生命周期、桩 fallback。
// 模型/库缺失时 Skip（基础设施面，非数据面；路径见 ort.go）。
package inference

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newRealVAD(t *testing.T) *SileroVAD {
	t.Helper()
	modelPath := DefaultVADModelPath()
	if _, err := os.Stat(modelPath); err != nil {
		t.Skipf("VAD 模型未就位（基础设施面，路径 %s）: %v", modelPath, err)
	}
	v := NewSileroVAD(modelPath)
	if v.Err() != nil {
		t.Skipf("VAD 引擎初始化失败（基础设施面）: %v", v.Err())
	}
	t.Cleanup(func() { _ = v.Destroy() })
	return v
}

func speechPcm(t *testing.T) []byte {
	t.Helper()
	pcm, rate := readTestWav16k(t, filepath.Join(DefaultASRModelDir(), "test_wavs", "1.wav"))
	if rate != fbankSampleRate {
		t.Skipf("测试 wav 非 16kHz（口径外）: %d", rate)
	}
	return pcm
}

func TestVADRealSpeechVsSilence(t *testing.T) {
	v := newRealVAD(t)
	if v.InFallback() {
		t.Fatal("模型在位却降级桩（基础设施面异常）")
	}
	pcm := speechPcm(t)
	speech, err := v.SpeechProbabilities(pcm)
	if err != nil {
		t.Fatalf("SpeechProbabilities(语音): %v", err)
	}
	silence, err := v.SpeechProbabilities(make([]byte, 2*fbankSampleRate*2)) // 2s 静音
	if err != nil {
		t.Fatalf("SpeechProbabilities(静音): %v", err)
	}
	var speechMax, speechSum, silMax float32
	for _, p := range speech {
		speechSum += p
		if p > speechMax {
			speechMax = p
		}
	}
	speechMean := speechSum / float32(len(speech))
	for _, p := range silence {
		if p > silMax {
			silMax = p
		}
	}
	t.Logf("语音 %d 帧: max=%.3f mean=%.3f；静音 %d 帧: max=%.4f",
		len(speech), speechMax, speechMean, len(silence), silMax)
	if speechMax < 0.8 {
		t.Errorf("语音段峰值概率 %.3f < 0.8（真推理不可信）", speechMax)
	}
	if silMax > 0.2 {
		t.Errorf("静音段峰值概率 %.3f > 0.2（误报）", silMax)
	}
	if speechMean <= silMax+0.3 {
		t.Errorf("语音均值 %.3f 对静音峰 %.3f 区分度不足", speechMean, silMax)
	}
}

func TestVADRealPushFrameEventLifecycle(t *testing.T) {
	v := newRealVAD(t)
	pcm := speechPcm(t)
	frameBytes := VADFrameSamples * 2 // 576 样本 × PCM16
	// 1s 前置静音 → 语音 → 1.2s 尾静音
	lead := make([]byte, fbankSampleRate*2)
	tail := make([]byte, fbankSampleRate*6/5*2)
	stream := append(append(lead, pcm...), tail...)

	var starts, ends int
	firstStartAt, endAt := -1, -1
	for off := 0; off+frameBytes <= len(stream); off += frameBytes {
		ev := v.PushFrame(stream[off : off+frameBytes])
		switch ev.Kind {
		case VADVoiceStart:
			if starts == 0 {
				firstStartAt = off / frameBytes
			}
			starts++
		case VADVoiceEnd:
			ends++
			endAt = off / frameBytes
		}
	}
	t.Logf("帧步进: VoiceStart×%d（首起帧 %d）VoiceEnd×%d（末帧 %d，总 %d 帧）",
		starts, firstStartAt, ends, endAt, len(stream)/frameBytes)
	if starts == 0 {
		t.Fatal("语音段未触发 VoiceStart")
	}
	if ends == 0 {
		t.Fatal("尾静音未触发 VoiceEnd（hangover 逻辑失效）")
	}
	if firstStartAt < 0 || firstStartAt > (len(stream)/frameBytes)/2 {
		t.Errorf("VoiceStart 触发帧 %d 异常偏晚", firstStartAt)
	}
	if endAt >= 0 && endAt < firstStartAt+vadEndHangoverFrames {
		t.Errorf("VoiceEnd 帧 %d 早于 VoiceStart 帧 %d + hangover %d", endAt, firstStartAt, vadEndHangoverFrames)
	}
}

func TestVADRealResetSemantics(t *testing.T) {
	v := newRealVAD(t)
	pcm := speechPcm(t)
	frameBytes := VADFrameSamples * 2
	for off := 0; off+frameBytes <= len(pcm); off += frameBytes {
		_ = v.PushFrame(pcm[off : off+frameBytes])
	}
	v.Reset()
	if v.speaking || v.lowStreak != 0 || v.buf != nil {
		t.Fatalf("Reset 后流式状态未清: speaking=%v lowStreak=%d buf=%d",
			v.speaking, v.lowStreak, len(v.buf))
	}
}

func TestVADRealRTF(t *testing.T) {
	v := newRealVAD(t)
	pcm := speechPcm(t)
	audioDur := float64(len(pcm)/2) / fbankSampleRate
	if _, err := v.SpeechProbabilities(pcm); err != nil { // 预热
		t.Fatalf("预热: %v", err)
	}
	rtfs := make([]float64, 0, 10)
	var lastN int
	for i := 0; i < 10; i++ {
		begin := time.Now()
		probs, err := v.SpeechProbabilities(pcm)
		if err != nil {
			t.Fatalf("第 %d 次推理: %v", i, err)
		}
		lastN = len(probs)
		rtfs = append(rtfs, time.Since(begin).Seconds()/audioDur)
	}
	p50, p95 := percentile(rtfs, 0.5), percentile(rtfs, 0.95)
	frameMs := float64(VADFrameSamples) / fbankSampleRate * 1000
	t.Logf("VAD RTF(1.wav, %.2fs 音频, %d 帧×%.0fms, intra-op=%d, 10 次): P50=%.4f P95=%.4f",
		audioDur, lastN, frameMs, effectiveIntraOpThreads(0), p50, p95)
	if p50 >= 0.1 {
		t.Errorf("VAD RTF P50 %.4f ≥ 0.1（远超实时预算）", p50)
	}
}

func TestVADStubFallback(t *testing.T) {
	v := NewSileroVAD("/nonexistent/silero_vad.onnx")
	defer func() { _ = v.Destroy() }()
	if v.Err() == nil {
		t.Fatal("不存在模型路径应产生构造期错误")
	}
	if !v.InFallback() {
		t.Fatal("构造失败应降级桩模式")
	}
	// M1 桩语义保留：每帧 512 样本累计 2s（=62.5 帧）触发 VoiceEnd
	for i := 0; i < 62; i++ {
		if ev := v.PushFrame(make([]byte, 1024)); ev.Kind == VADVoiceEnd {
			t.Fatalf("第 %d 帧提前触发 VoiceEnd（桩 2s 语义被改）", i)
		}
	}
	if ev := v.PushFrame(make([]byte, 1024)); ev.Kind != VADVoiceEnd || ev.Score != 0.99 {
		t.Fatalf("2s 处应触发 VoiceEnd(0.99)，got %+v", ev)
	}
}
