// ASR 真推理测试（非门禁）：golden 转写对拍（Python ORT + knf fbank 参考管线，
// 见 /root/workspace/datasets/jobs/m2-prep-ref/asr_ref.py 与 golden_transcripts.json）、
// 降级桩、RTF 观测面。模型/库缺失时 Skip（基础设施面，非数据面）。
package inference

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	golden1wav    = "这是第一种第二种叫呃与▁ALWAYS▁ALWAYS什么意思啊"
	goldenSichuan = "自己就是在那个在那个就是在情节里面就是感觉戏演得特别好就是好像很真实一样你知道吧"
)

func newRealASR(t *testing.T) *FireRedASR2 {
	t.Helper()
	dir := DefaultASRModelDir()
	enc := filepath.Join(dir, "encoder.int8.onnx")
	dec := filepath.Join(dir, "decoder.int8.onnx")
	for _, p := range []string{enc, dec, filepath.Join(dir, "tokens.txt")} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("ASR 模型未就位（基础设施面，缺 %s）: %v", p, err)
		}
	}
	f := NewFireRedASR2(enc, dec)
	if f.Err() != nil {
		t.Skipf("ASR 引擎初始化失败（基础设施面）: %v", f.Err())
	}
	t.Cleanup(func() { _ = f.Destroy() })
	return f
}

func TestASRRealGoldenTranscript(t *testing.T) {
	f := newRealASR(t)
	if f.InFallback() {
		t.Fatal("模型在位却降级桩（基础设施面异常）")
	}
	// 1.wav：精确对拍（独立 Python 参考管线产出）
	pcm, rate := readTestWav16k(t, filepath.Join(DefaultASRModelDir(), "test_wavs", "1.wav"))
	if rate != fbankSampleRate {
		t.Skipf("测试 wav 非 16kHz（口径外）: %d", rate)
	}
	got, err := f.Recognize(pcm)
	if err != nil {
		t.Fatalf("Recognize(1.wav): %v", err)
	}
	if got != golden1wav {
		t.Errorf("1.wav 转写 ≠ golden:\n got  %q\n want %q", got, golden1wav)
	} else {
		t.Logf("1.wav 精确对拍 ✓ %q", got)
	}
}

func TestASRRealFluencySweep(t *testing.T) {
	f := newRealASR(t)
	cases := map[string]string{
		"3-sichuan.wav": goldenSichuan,
		"4-tianjin.wav": "",
		"5-henan.wav":   "",
	}
	for name, golden := range cases {
		pcm, rate := readTestWav16k(t, filepath.Join(DefaultASRModelDir(), "test_wavs", name))
		if rate != fbankSampleRate {
			continue
		}
		got, err := f.Recognize(pcm)
		if err != nil {
			t.Errorf("%s: Recognize: %v", name, err)
			continue
		}
		if got == "" {
			t.Errorf("%s: 空转写", name)
			continue
		}
		if golden != "" && got != golden {
			t.Errorf("%s 转写 ≠ golden:\n got  %q\n want %q", name, got, golden)
			continue
		}
		t.Logf("%s: %q", name, got)
	}
}

func TestASRRealRTF(t *testing.T) {
	f := newRealASR(t)
	pcm, rate := readTestWav16k(t, filepath.Join(DefaultASRModelDir(), "test_wavs", "1.wav"))
	if rate != fbankSampleRate {
		t.Skipf("测试 wav 非 16kHz（口径外）: %d", rate)
	}
	audioDur := float64(len(pcm)/2) / float64(rate)
	if _, err := f.Recognize(pcm); err != nil { // 预热（ORT 内部分配/图优化）
		t.Fatalf("预热: %v", err)
	}
	rtfs := make([]float64, 0, 10)
	var lastText string
	for i := 0; i < 10; i++ {
		begin := time.Now()
		text, err := f.Recognize(pcm)
		if err != nil {
			t.Fatalf("第 %d 次 Recognize: %v", i, err)
		}
		lastText = text
		rtfs = append(rtfs, time.Since(begin).Seconds()/audioDur)
	}
	p50, p95 := percentile(rtfs, 0.5), percentile(rtfs, 0.95)
	enc, dec := f.LastStageWalls()
	t.Logf("ASR RTF(1.wav, %.2fs 音频, intra-op=%d, 10 次): P50=%.3f P95=%.3f text=%q",
		audioDur, effectiveIntraOpThreads(0), p50, p95, lastText)
	t.Logf("分段墙钟（末次）: encoder=%s decoder=%s", enc, dec)
	// 合理性断言（非预算门）：本机实测 P50≈0.98~1.01（2 线程）/ 0.84（4 线程），
	// 贴实时线；断言放宽到 1.5 容纳 nice-19 抢占抖动，RTF 数值以报告实测为准。
	if p50 >= 1.5 {
		t.Errorf("RTF P50 %.3f ≥ 1.5（远超实时，实现异常）", p50)
	}
}

func percentile(sorted []float64, q float64) float64 {
	// 就地排序副本后线性插值
	s := append([]float64(nil), sorted...)
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
	pos := q * float64(len(s)-1)
	lo := int(pos)
	if lo >= len(s)-1 {
		return s[len(s)-1]
	}
	frac := pos - float64(lo)
	return s[lo] + frac*(s[lo+1]-s[lo])
}

func TestASRStubFallback(t *testing.T) {
	f := NewFireRedASR2("/nonexistent/encoder.onnx", "/nonexistent/decoder.onnx")
	defer func() { _ = f.Destroy() }()
	if f.Err() == nil {
		t.Fatal("不存在模型路径应产生构造期错误")
	}
	if !f.InFallback() {
		t.Fatal("构造失败应降级桩模式")
	}
	got, err := f.Recognize([]byte{})
	if err != nil {
		t.Fatalf("桩 Recognize 不应报错: %v", err)
	}
	if got != "你好" {
		t.Fatalf("桩语义被改: got %q, want 你好", got)
	}
}
