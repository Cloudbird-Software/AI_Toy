// LLM 真推理测试（非门禁）：中文问答 sanity、吞吐（10 次 P50/P95 tok/s）、
// 内存观测、降级桩。模型/库缺失时 Skip（基础设施面，非数据面）。
// 实测数字入 reports/eval/T14/latency-report.md LLM 段。
package inference

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newRealLLM(t *testing.T) *Qwen3LLM {
	t.Helper()
	p := DefaultLLMModelPath()
	if _, err := os.Stat(p); err != nil {
		t.Skipf("LLM 模型未就位（基础设施面，缺 %s）: %v", p, err)
	}
	if DefaultLLMLibPath() == "" {
		t.Skip("libllama 未就位（基础设施面，缺 libllama.so.0.3.0）")
	}
	q := NewQwen3LLM(p)
	if q.Err() != nil {
		t.Skipf("LLM 引擎初始化失败（基础设施面）: %v", q.Err())
	}
	t.Cleanup(func() { _ = q.Destroy() })
	return q
}

// rssKB 读 /proc/self/status 的 RSS/HWM（KiB）—— llama 侧内存在 C 堆，
// Go runtime 视角不可见，进程 RSS 为准。
func rssKB(t *testing.T) (rss, hwm int64) {
	t.Helper()
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Skipf("无 /proc/self/status（非 Linux？）: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case strings.HasPrefix(line, "VmRSS:"):
			rss, _ = strconv.ParseInt(strings.Fields(line[6:])[0], 10, 64)
		case strings.HasPrefix(line, "VmHWM:"):
			hwm, _ = strconv.ParseInt(strings.Fields(line[6:])[0], 10, 64)
		}
	}
	return rss, hwm
}

func TestLLMRealChineseQASanity(t *testing.T) {
	q := newRealLLM(t)
	if q.InFallback() {
		t.Fatal("模型在位却降级桩（基础设施面异常）")
	}
	qa := []struct{ in, wantSub string }{
		{"你好，你是谁呀？", "云雀"},
		{"1加1等于几？", "2"},
		{"天上的月亮为什么会跟着我们走？", ""},
		{"给我讲一个关于小兔子的短故事。", ""},
	}
	for _, c := range qa {
		got, err := q.Generate(c.in)
		if err != nil {
			t.Errorf("Generate(%q): %v", c.in, err)
			continue
		}
		if strings.TrimSpace(got) == "" {
			t.Errorf("Generate(%q): 空回复", c.in)
			continue
		}
		if strings.Contains(got, llmThinkOpen) {
			t.Errorf("Generate(%q): 思考链漏出（stripThink 失效）: %q", c.in, got)
			continue
		}
		if c.wantSub != "" && !strings.Contains(got, c.wantSub) {
			t.Errorf("Generate(%q) = %q, 不含锚点 %q", c.in, got, c.wantSub)
			continue
		}
		pw, gw, pt, gt := q.LastGenStats()
		t.Logf("Q: %s\n   A: %s\n   [prompt %d tok / %s | gen %d tok / %s ≈ %.1f tok/s]",
			c.in, got, pt, pw.Round(time.Millisecond), gt,
			gw.Round(time.Millisecond), tokPerSec(gw, gt))
	}
}

// TestLLMRealThroughput 吞吐实测：固定 prompt 生成 10 次，取生成 tok/s 的 P50/P95。
// 断言仅合理性校验（非预算门）：P50 ≥ 1 tok/s（远低于 0.6B Q4 预期，容忍 nice-19 抖动）。
func TestLLMRealThroughput(t *testing.T) {
	q := newRealLLM(t)
	prompt := "给我讲一个适合睡前听的小故事，主角是一只小刺猬。"
	if _, err := q.Generate(prompt); err != nil { // 预热（页缓存/后端初始化）
		t.Fatalf("预热: %v", err)
	}
	if _, err := q.Generate(prompt); err != nil { // 预热（页缓存/后端初始化）
		t.Fatalf("预热: %v", err)
	}
	const runs = 10
	tps := make([]float64, 0, runs)
	var lastA string
	for i := 0; i < runs; i++ {
		a, err := q.Generate(prompt)
		if err != nil {
			t.Fatalf("第 %d 次 Generate: %v", i, err)
		}
		lastA = a
		_, gw, _, gt := q.LastGenStats()
		if gt == 0 {
			t.Fatalf("第 %d 次: 0 token 生成", i)
		}
		tps = append(tps, tokPerSec(gw, gt))
	}
	p50, p95 := percentile(tps, 0.5), percentile(tps, 0.95)
	t.Logf("LLM 生成吞吐（%q, threads=%d, maxNew=%d, %d 次）: P50=%.2f tok/s P95=%.2f tok/s",
		prompt, q.threads, q.maxNew, runs, p50, p95)
	pw, gw, pt, gt := q.LastGenStats()
	t.Logf("末次: prompt %d tok / %s | gen %d tok / %s | 样本回复: %q",
		pt, pw.Round(time.Millisecond), gt, gw.Round(time.Millisecond), lastA)
	if p50 < 1.0 {
		t.Errorf("吞吐 P50 %.2f tok/s < 1（远低于 0.6B Q4 合理区间，实现异常）", p50)
	}
}

// TestLLMRealMemory 内存占用实测：加载增量（权重+ctx）与生成增量（KV/激活）
// 两个口径断言；绝对 RSS 进报告日志（合跑时进程含其他引擎残留，绝对值不作断言）。
func TestLLMRealMemory(t *testing.T) {
	rss0, _ := rssKB(t)
	q := newRealLLM(t)
	rssLoaded, hwm := rssKB(t)
	if _, err := q.Generate("你好"); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	rssAfter, _ := rssKB(t)
	loadDelta := (rssLoaded - rss0) >> 10
	genDelta := (rssAfter - rssLoaded) >> 10
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	t.Logf("内存: 加载增量=%dMB（462MB 权重+ggml 计算缓冲） 生成增量=%dMB | 进程 RSS: 基线=%dMB → 加载后=%dMB → 生成后=%dMB（HWM=%dMB）",
		loadDelta, genDelta, rss0>>10, rssLoaded>>10, rssAfter>>10, hwm>>10)
	t.Logf("Go 堆（对照）: HeapInuse=%dMB（llama 权重/KV 在 C 堆，进程 RSS 为准）", ms.HeapInuse>>20)
	if loadDelta > 2048 {
		t.Errorf("加载增量 %dMB 超合理上界 2GB", loadDelta)
	}
	if genDelta > 512 {
		t.Errorf("生成增量 %dMB 超合理上界 512MB", genDelta)
	}
}

func TestLLMStubFallback(t *testing.T) {
	q := NewQwen3LLM("/nonexistent/model.gguf")
	defer func() { _ = q.Destroy() }()
	if q.Err() == nil {
		t.Fatal("不存在模型路径应产生构造期错误")
	}
	if !q.InFallback() {
		t.Fatal("构造失败应降级桩模式")
	}
	got, err := q.Generate("你好")
	if err != nil {
		t.Fatalf("桩 Generate 不应报错: %v", err)
	}
	if got != "你好呀，我是小云雀" {
		t.Fatalf("桩语义被改: got %q, want 你好呀，我是小云雀", got)
	}
}

func tokPerSec(d time.Duration, n int) float64 {
	if n == 0 || d <= 0 {
		return 0
	}
	return float64(n) / d.Seconds()
}
