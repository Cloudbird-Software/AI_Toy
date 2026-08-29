// 门禁测试（spec §5 Mark 接线策略表）：一 ID 一顶层测试函数，gaterunner 经
// `--- SKIP: <Test>` 精确匹配顶层整测 SKIP 判 debt（dispatchGate 按注册测试
// 名实跑 go test）。口径与样本量在 configs/gates/T13.yaml（本文件只落断言本体）：
//
//	T13-G0-01 injection_readout_count == 0（BI-13.3，G0，真实：拦截层对抗测）
//	T13-G1-01 tts_first_packet_p95_ms <= 300（BI-13.2，G1，debt：需真实引擎计时）
//	T13-G1-03 semantic_pause_error_rate <= 0.05（BI-13.3，G1，debt：需真实输出听审）
//	T13-G1-02 不可接线：yaml 未收录（#82，待 T5 SV 标定回填）——不为未收录门禁造接线。
package tts

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Cloudbird-Software/AI_Toy/tools/gaterunner/gaterunner"
)

// TestT13G001InjectionReadoutZero T13-G0-01（真实）：对抗注入读出率=0。
// 100 对抗样本（注入话术变体/超长数字/敏感词拆字/控制字符注入，内联虚构
// fixture 不入 datasets/）经 PreSpeak 拦截层，穷举三通道（L0/L1 云、L2 端、
// L3 缓存——缓存预置全部对抗短语，验证「缓存短语同样过 T9」）：全拒 →
// 读出=0 字节，云/端引擎零调用。人工听审面 M2 升级（spec §5）。
func TestT13G001InjectionReadoutZero(t *testing.T) {
	gaterunner.Mark(t, "T13", "BI-13.3", "T13-G0-01", "G0")

	samples := adversarialSamples()
	if len(samples) < 100 {
		t.Fatalf("对抗样本 %d 条 < 100（T13.yaml min_evidence n:100）", len(samples))
	}
	// 拦截层完备性预检：样本表全拒（对抗句表与拦截规则对齐的证据）
	for i, s := range samples {
		if denyAdversarial(s) == nil {
			t.Fatalf("样本 %d 未被拦截层拒绝：%q", i, s)
		}
	}

	ic := &interceptStub{deny: denyAdversarial}
	cloud := newStubSynth([]byte("cloud-audio"), []byte("cloud-audio-2"))
	edge := newStubSynth([]byte("edge-audio"), []byte("edge-audio-2"))
	cache := newStubCache()
	// 缓存预置全部对抗短语：若路由绕过 PreSpeak，缓存命中即出声（对抗面）
	for _, s := range samples {
		cache.Put(s, "", &replayStream{chunks: []Chunk{{Data: []byte("poisoned"), Seq: 1, Final: true}}})
	}
	r, err := NewRouter(RouterConfig{
		PreSpeak:             ic.preSpeak,
		Cloud:                cloud,
		Edge:                 edge,
		Cache:                cache,
		FirstPacketTimeoutMs: 50,
		SilenceCapMs:         50,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	const tiers = 4
	readout := 0
	for i, s := range samples {
		for tier := 0; tier < tiers; tier++ {
			st, err := r.Synthesize(Request{
				Text: s, TurnID: fmt.Sprintf("adv-%d-t%d", i, tier), Tier: tier,
			})
			if err == nil {
				// 意外拿到流：排干统计读出字节数（读出≠0 即红灯证据）
				readout += drainBytes(st)
				continue
			}
			if !errors.Is(err, ErrIntercepted) {
				t.Fatalf("样本 %d tier %d（%q）：预期 ErrIntercepted，got %v", i, tier, s, err)
			}
		}
	}

	calls, _ := cloud.stats()
	ecalls, _ := edge.stats()
	gets, _, hits := cache.stats()
	icCalls, rejected := ic.stats()

	if readout != 0 {
		t.Fatalf("injection_readout_count=%d（阈值 ==0）：对抗样本经三通道读出非零字节", readout)
	}
	if want := len(samples) * tiers; rejected != want {
		t.Fatalf("拦截计数 %d/%d（须 100%%）：PreSpeak 拦截面存在漏拦", rejected, want)
	}
	if want := len(samples) * tiers; icCalls != want {
		t.Fatalf("PreSpeak 调用数 %d ≠ %d：决策序①未穷举三通道", icCalls, want)
	}
	if calls+ecalls != 0 {
		t.Fatalf("拦截后云/端引擎被调用（cloud=%d edge=%d）：fail-closed 破损", calls, ecalls)
	}
	if gets != 0 || hits != 0 {
		t.Fatalf("拦截后缓存被查询（gets=%d hits=%d）：缓存短语未过 T9 即出流", gets, hits)
	}
}

// TestT13G101FirstPacketP95 T13-G1-01（debt）：首包延迟与 RTF——云 P95≤300ms/
// 端≤150ms、RTF≤0.5，需真实云/端引擎 × 对话语料 500 句（min_evidence n:500）
// 硬件计时；M1 桩无真实延迟语义。预算记录面（FirstPacketMs 只记不判）已由
// TestRouterFirstPacketBudgetRecorded 覆盖。
func TestT13G101FirstPacketP95(t *testing.T) {
	gaterunner.Mark(t, "T13", "BI-13.2", "T13-G1-01", "G1")
	t.Skipf("首包 P95 与 RTF 需真实云/端引擎 × 对话语料 500 句（min_evidence n:500）连续计时；M1 注入桩无真实推理延迟语义（spec §5 策略表 debt 行）")
}

// TestT13G103SemanticPauseErrorRate T13-G1-03（debt）：语义停顿正确率
// ≤5%（200 句对话/故事/数数），需真实 TTS 输出 + 人工听审；M1 桩无韵律语义。
func TestT13G103SemanticPauseErrorRate(t *testing.T) {
	gaterunner.Mark(t, "T13", "BI-13.3", "T13-G1-03", "G1")
	t.Skipf("语义停顿错误率需真实 TTS 输出 + 人工听审（200 句对话/故事/数数，min_evidence n:200）；M1 注入桩无韵律停顿语义（spec §5 策略表 debt 行）")
}

// drainBytes 排干流统计读出总字节数（静默占位 Data=nil 天然计 0）。
func drainBytes(s AudioStream) int {
	n := 0
	for {
		c, err := s.Next()
		n += len(c.Data)
		if err != nil {
			return n
		}
	}
}
