// 流式 ASR 真推理测试（非门禁）：golden 转写对拍（块语义经 Python ORT 流式原型
// 实证，见 asr_streaming_zipformer.go 头注；1.wav 与 FireRed golden 逐字一致）、
// 8 wav 扫描、增量文本前缀单调性、三延迟观测面（首字/RTF/定稿）、降级桩。
// 模型/库缺失时 Skip（基础设施面，非数据面；路径见 DefaultStreamingASRModelDir）。
package inference

import (
	"strings"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// streamGolden1wav 流式模型 1.wav golden（Python ORT 原型对拍产出，与 FireRed
// 非流式 golden golden1wav 逐字一致——两代模型在该句上互为印证）。
const streamGolden1wav = "这是第一种第二种叫呃与▁ALWAYS▁ALWAYS什么意思啊"

func newRealStreamingASR(t *testing.T) *StreamingZipformer {
	t.Helper()
	dir := DefaultStreamingASRModelDir()
	enc := filepath.Join(dir, "encoder-epoch-99-avg-1.int8.onnx")
	dec := filepath.Join(dir, "decoder-epoch-99-avg-1.onnx")
	joi := filepath.Join(dir, "joiner-epoch-99-avg-1.int8.onnx")
	for _, p := range []string{enc, dec, joi, filepath.Join(dir, "tokens.txt")} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("流式 ASR 模型未就位（基础设施面，缺 %s）: %v", p, err)
		}
	}
	z := NewStreamingZipformer(enc, dec, joi)
	if z.Err() != nil {
		t.Skipf("流式 ASR 引擎初始化失败（基础设施面）: %v", z.Err())
	}
	t.Cleanup(func() { _ = z.Destroy() })
	return z
}

// testWav16k 读 test_wavs 下 16kHz wav（非 16kHz 返回 nil，调用方跳过）。
func testWav16k(t *testing.T, name string) []byte {
	t.Helper()
	pcm, rate := readTestWav16k(t, filepath.Join(DefaultASRModelDir(), "test_wavs", name))
	if rate != fbankSampleRate {
		t.Logf("%s 采样率 %d ≠ 16kHz（口径外，跳过）", name, rate)
		return nil
	}
	return pcm
}

func TestASRStreamingGoldenTranscript(t *testing.T) {
	z := newRealStreamingASR(t)
	if z.InFallback() {
		t.Fatal("模型在位却降级桩（基础设施面异常）")
	}
	pcm := testWav16k(t, "1.wav")
	if pcm == nil {
		t.Skip("1.wav 非 16kHz（口径外）")
	}
	got, err := z.Recognize(pcm)
	if err != nil {
		t.Fatalf("Recognize(1.wav): %v", err)
	}
	if got != streamGolden1wav {
		t.Errorf("1.wav 转写 ≠ golden:\n got  %q\n want %q", got, streamGolden1wav)
	} else {
		t.Logf("1.wav 精确对拍 ✓ %q", got)
	}
}

func TestASRStreamingSweep8Wavs(t *testing.T) {
	z := newRealStreamingASR(t)
	// 方言/口音条目只锚定稳定特征子串（流式模型转写与 FireRed 非逐字一致），
	// 8k.wav 重采样口径外自动跳过。
	pins := map[string]string{
		"0.wav":        "是星期三",
		"1.wav":        "第二种",
		"2.wav":        "频繁的",
		"3.wav":        "一般现在时",
		"3-sichuan.wav": "演的特别好",
		"4-tianjin.wav": "法律意识",
		"5-henan.wav":   "七八层楼高",
	}
	names := []string{"0.wav", "1.wav", "2.wav", "3.wav", "3-sichuan.wav", "4-tianjin.wav", "5-henan.wav", "8k.wav"}
	for _, name := range names {
		pcm := testWav16k(t, name)
		if pcm == nil {
			continue
		}
		got, err := z.Recognize(pcm)
		if err != nil {
			t.Errorf("%s: Recognize: %v", name, err)
			continue
		}
		if got == "" {
			t.Errorf("%s: 空转写", name)
			continue
		}
		if pin := pins[name]; pin != "" && !strings.Contains(got, pin) {
			t.Errorf("%s 转写缺特征子串 %q:\n got %q", name, pin, got)
			continue
		}
		t.Logf("%s: %q", name, got)
	}
}

func TestASRStreamingIncrementalMonotonic(t *testing.T) {
	z := newRealStreamingASR(t)
	pcm := testWav16k(t, "1.wav")
	if pcm == nil {
		t.Skip("1.wav 非 16kHz（口径外）")
	}
	if err := z.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	var prev string
	firstAtFeed := -1
	fed := 0
	total := 0
	for off := 0; off < len(pcm); off += feedChunkBytes {
		total++
	}
	for off := 0; off < len(pcm); off += feedChunkBytes {
		end := off + feedChunkBytes
		if end > len(pcm) {
			end = len(pcm)
		}
		partial, err := z.FeedChunk(pcm[off:end])
		if err != nil {
			t.Fatalf("FeedChunk(块 %d): %v", fed, err)
		}
		fed++
		if partial != "" && !hasPrefix(partial, prev) {
			t.Fatalf("部分文本非前缀单调:\n prev %q\n got  %q", prev, partial)
		}
		if partial != prev && firstAtFeed < 0 {
			firstAtFeed = fed
		}
		prev = partial
	}
	final, err := z.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	// 定稿刷新（尾帧零填充块）只允许在已出文本上追加，不得改写
	if !hasPrefix(final, prev) {
		t.Errorf("定稿改写了已出文本:\n last   %q\n finish %q", prev, final)
	}
	if firstAtFeed < 0 || firstAtFeed > total/2 {
		t.Errorf("首字出现于第 %d/%d 块（流式增量失效或过晚）", firstAtFeed, total)
	}
	t.Logf("增量出字：首字于第 %d/%d 块（%.0fms 音频处），终稿 %q",
		firstAtFeed, total, float64(firstAtFeed*feedChunkBytes)/2/fbankSampleRate*1000, final)
	whole, err := z.Recognize(pcm)
	if err != nil {
		t.Fatalf("Recognize(整句口径): %v", err)
	}
	if whole != final {
		t.Errorf("整句口径 ≠ 增量口径:\n whole %q\n incr  %q", whole, final)
	}
}

// hasPrefix strings.HasPrefix 的 nil 安全薄封装（空 prev 恒真）。
func hasPrefix(s, prefix string) bool {
	if prefix == "" {
		return true
	}
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func TestASRStreamingLatencyRTF(t *testing.T) {
	z := newRealStreamingASR(t)
	pcm := testWav16k(t, "1.wav")
	if pcm == nil {
		t.Skip("1.wav 非 16kHz（口径外）")
	}
	audioDur := float64(len(pcm)/2) / fbankSampleRate
	if _, err := z.Recognize(pcm); err != nil { // 预热（ORT 图优化/分配）
		t.Fatalf("预热: %v", err)
	}
	const runs = 10
	rtfs := make([]float64, 0, runs)
	firsts := make([]float64, 0, runs)
	fins := make([]float64, 0, runs)
	var lastText string
	for i := 0; i < runs; i++ {
		begin := time.Now()
		text, err := z.Recognize(pcm)
		if err != nil {
			t.Fatalf("第 %d 次 Recognize: %v", i, err)
		}
		wall := time.Since(begin)
		lastText = text
		rtfs = append(rtfs, wall.Seconds()/audioDur)
		ftl, ok := z.FirstTokenLatency()
		if !ok {
			t.Fatalf("第 %d 次未出字（流式失效）", i)
		}
		firsts = append(firsts, ftl.Seconds())
		fins = append(fins, z.FinishWall().Seconds())
	}
	p50, p95 := percentile(rtfs, 0.5), percentile(rtfs, 0.95)
	fP50, fP95 := percentile(firsts, 0.5), percentile(firsts, 0.95)
	nP50, nP95 := percentile(fins, 0.5), percentile(fins, 0.95)
	enc, join, dec := z.Walls()
	t.Logf("流式 ASR(1.wav, %.2fs 音频, intra-op=%d, %d 次):", audioDur, effectiveIntraOpThreads(0), runs)
	t.Logf("  RTF     P50=%.3f P95=%.3f", p50, p95)
	t.Logf("  首字延迟 P50=%.0fms P95=%.0fms（尽速喂入口径，实时节奏另测）", fP50*1000, fP95*1000)
	t.Logf("  定稿延迟 P50=%.0fms P95=%.0fms（Finish 刷新）", nP50*1000, nP95*1000)
	t.Logf("  末轮分段: encoder=%s joiner=%s decoder=%s text=%q", enc, join, dec, lastText)
	if lastText != streamGolden1wav {
		t.Errorf("10 次循环后转写漂移: %q", lastText)
	}
	// 合理性断言（非预算门）：流式模型 int8 块解码应远低于实时；
	// 断言放宽到 1.5 容纳 nice-19 抢占抖动，验收数字以报告实测为准。
	if p50 >= 1.5 {
		t.Errorf("RTF P50 %.3f ≥ 1.5（远超实时，实现异常）", p50)
	}
}

// TestASRStreamingRealtimePaced 实时节奏口径：40ms 块按墙钟节拍喂入（模拟麦克风流），
// 测首字墙钟（t0 起算，含缓冲 390ms）与定稿延迟。较慢（每轮 ≈音频时长），
// 由 T14_REALTIME_BENCH=1 显式开启，报告数字来源。
func TestASRStreamingRealtimePaced(t *testing.T) {
	if os.Getenv("T14_REALTIME_BENCH") == "" {
		t.Skip("需 T14_REALTIME_BENCH=1（实时节奏口径，较慢）")
	}
	z := newRealStreamingASR(t)
	pcm := testWav16k(t, "1.wav")
	if pcm == nil {
		t.Skip("1.wav 非 16kHz（口径外）")
	}
	audioDur := float64(len(pcm)/2) / fbankSampleRate
	const runs = 10
	firsts := make([]float64, 0, runs)
	fins := make([]float64, 0, runs)
	var lastText string
	for i := 0; i < runs; i++ {
		if err := z.Reset(); err != nil {
			t.Fatalf("Reset: %v", err)
		}
		t0 := time.Now()
		var firstToken time.Time
		for off := 0; off < len(pcm); off += feedChunkBytes {
			end := off + feedChunkBytes
			if end > len(pcm) {
				end = len(pcm)
			}
			partial, err := z.FeedChunk(pcm[off:end])
			if err != nil {
				t.Fatalf("第 %d 次 FeedChunk: %v", i, err)
			}
			if firstToken.IsZero() && partial != "" {
				firstToken = time.Now()
			}
			// 按实时节拍对齐下一块（RTF<1 时引擎等待音频）
			if next := t0.Add(time.Duration(off/feedChunkBytes+1) * feedChunkMs); time.Until(next) > 0 {
				time.Sleep(time.Until(next))
			}
		}
		begin := time.Now()
		text, err := z.Finish()
		if err != nil {
			t.Fatalf("第 %d 次 Finish: %v", i, err)
		}
		finalize := time.Since(begin)
		lastText = text
		if firstToken.IsZero() {
			t.Fatalf("第 %d 次未出字", i)
		}
		firsts = append(firsts, firstToken.Sub(t0).Seconds())
		fins = append(fins, finalize.Seconds())
	}
	fP50, fP95 := percentile(firsts, 0.5), percentile(firsts, 0.95)
	nP50, nP95 := percentile(fins, 0.5), percentile(fins, 0.95)
	t.Logf("实时节奏口径(1.wav, %.2fs 音频, %d 次): 首字墙钟 P50=%.0fms P95=%.0fms；定稿延迟 P50=%.0fms P95=%.0fms text=%q",
		audioDur, runs, fP50*1000, fP95*1000, nP50*1000, nP95*1000, lastText)
	if lastText != streamGolden1wav {
		t.Errorf("实时节奏下转写漂移: %q", lastText)
	}
}

func TestASRStreamingStubFallback(t *testing.T) {
	z := NewStreamingZipformer("/nonexistent/e.onnx", "/nonexistent/d.onnx", "/nonexistent/j.onnx")
	defer func() { _ = z.Destroy() }()
	if z.Err() == nil {
		t.Fatal("不存在模型路径应产生构造期错误")
	}
	if !z.InFallback() {
		t.Fatal("构造失败应降级桩模式")
	}
	got, err := z.Recognize([]byte{})
	if err != nil {
		t.Fatalf("桩 Recognize 不应报错: %v", err)
	}
	if got != "你好" {
		t.Fatalf("桩语义被改: got %q, want 你好", got)
	}
}
