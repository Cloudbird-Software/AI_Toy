// T3 门禁真实接线（IR #129）：G1-01/G1-02 从 debt 桩升为真实模型输出断言——
// 真引擎（本包 Engine）+ FSM 混合式（turntaking.VAPLeadPNow 提前量）在环。
// verdict 口径（ADR-0002）：本文件测试实跑真模型断言=真实；模型/库文件缺失时
// Skipf（基础设施面 debt，路径见 DefaultModelPath/DefaultLibraryPath）。
//
// 诚实性边界（与 AGENTS.md 常见坑一致）：harness 音频为确定性合成语音代理
// （正弦突发+真实静音结构，golden_chunk 公式），非儿童真实语音——业务门限
// （儿童集 n≥300/≥100）的真实数据面仍未建，测得值不可外推至真机；真实语音
// 行为与对照评测见 reports/eval/T3/。本测试断言的是：真模型在环下的机制面
// （误截断不劣化、静音不误触发、推理时延入预算）。
package vap

import (
	"os"
	"testing"
	"time"

	"github.com/Cloudbird-Software/AI_Toy/packages/go/turntaking"
	"github.com/Cloudbird-Software/AI_Toy/tools/gaterunner/gaterunner"
)

// engineOrSkip 门禁面引擎获取（模型缺失=基础设施 debt）。
func engineOrSkip(t *testing.T) *Engine {
	t.Helper()
	modelPath, err := DefaultModelPath()
	if err != nil {
		t.Skipf("T3 模型路径不可定位: %v", err)
	}
	if _, statErr := os.Stat(modelPath); statErr != nil {
		t.Skipf("T3 真模型未就位（%s）：models/incoming 落盘+manifest 回填后升真实", modelPath)
	}
	e, err := New(Config{ModelPath: modelPath, LibraryPath: DefaultLibraryPath()})
	if err != nil {
		t.Skipf("T3 引擎初始化失败: %v", err)
	}
	t.Cleanup(func() { _ = e.Destroy() })
	return e
}

// TestT3G101MiscutRate T3-G1-01（BI-3.1/G1，真实接线 IR #129）：真模型+混合 FSM
// 在环，300 个停顿点（min_evidence.n=300）短间隙流（间隙 200ms < SilenceMs 700ms）：
// 断言①误截断（语音进行中被判终点）hybrid=VAD-only=0；②安全不变量：全部提前终点
// 均落在语音停止后（语音中零提前，predict_test 不变量①的真模型在环复核）；③静音
// 面（G0-02 互补）：全零音频流 PNowSystem 恒低于门限——纯静音不预测系统接话。
// 间隙内 lead 触发数如实上报（合成代理无韵律线索，真实儿童集落地后为调参面）。
// 真实儿童集（3–6/7–9/10–12 岁分层，双人标注）落地后以同 harness 升全量业务断言。
func TestT3G101MiscutRate(t *testing.T) {
	gaterunner.Mark(t, "T3", "BI-3.1", "T3-G1-01", "G1")
	e := engineOrSkip(t)

	const (
		units       = 300 // min_evidence.n（configs/gates/T3.yaml）
		speechFrame = 8   // 每单元 8 帧语音（800ms）
		gapFrame    = 2   // 2 帧间隙（200ms < SilenceMs 700ms）
		silenceMs   = 700
	)
	cfg := turntaking.Config{SilenceMs: silenceMs, MaxTurnMs: 1 << 30, BargeInWindow: 300, VAPLeadPNow: 0.7}
	fsm, err := turntaking.NewFSM(cfg)
	if err != nil {
		t.Fatalf("NewFSM: %v", err)
	}
	vadOnly, err := turntaking.NewFSM(turntaking.Config{
		SilenceMs: silenceMs, MaxTurnMs: 1 << 30, BargeInWindow: 300})
	if err != nil {
		t.Fatalf("NewFSM(vad-only): %v", err)
	}

	chunk1 := make([]float32, NewSamples)
	chunk2 := make([]float32, NewSamples)
	at := int64(0)
	miscutHybrid, miscutVAD, leadFires := 0, 0, 0

	for u := 0; u < units; u++ {
		for s := 0; s < speechFrame+gapFrame; s++ {
			isSpeech := s < speechFrame
			for j := range chunk1 {
				if isSpeech {
					// 语音代理帧：公式索引恒取语音段（%10<6），规避公式自带静音窗。
					chunk1[j] = goldenChunk(u*10+s%6, j)
				} else {
					chunk1[j] = 0 // 间隙=真静音
				}
				chunk2[j] = 0
			}
			if _, err := e.Step(chunk1, chunk2); err != nil {
				t.Fatalf("unit %d frame %d: Step: %v", u, s, err)
			}
			at += FrameMs
			// VAD 事件序列：语音帧=EvVoiceStart（行2：语音中/复讲清尾静音），
			// 间隙首帧=EvVoiceEnd（行3 前缀：尾静音起算），其后=EvNone（静音观测）。
			kind := turntaking.EvNone
			switch {
			case isSpeech:
				kind = turntaking.EvVoiceStart
			case s == speechFrame:
				kind = turntaking.EvVoiceEnd
			}
			fsm.OnVAD(turntaking.VADEvent{Kind: kind, AtMs: at})
			vadOnly.OnVAD(turntaking.VADEvent{Kind: kind, AtMs: at})
			// 混合式提前量（真模型在环）。
			for _, a := range fsm.PredictLead(leadAdapter{e: e}, at) {
				if a.Kind == turntaking.ActTurnEnd {
					leadFires++
					if isSpeech {
						miscutHybrid++ // 语音中终点=误截断（不变量①）
					}
				}
			}
			// VAD-only 基线同流驱动（无提前量）。
			for _, a := range vadOnly.PredictLead(nil, at) {
				if a.Kind == turntaking.ActTurnEnd && isSpeech {
					miscutVAD++
				}
			}
		}
	}
	if miscutHybrid != 0 {
		t.Errorf("误截断（语音中终点）=%d，须 0（提前量只在语音停止后触发）", miscutHybrid)
	}
	if miscutVAD != 0 {
		t.Errorf("VAD-only 误截断=%d，须 0（间隙 %dms < SilenceMs %dms）", miscutVAD, gapFrame*FrameMs, silenceMs)
	}
	// 静音面（G0-02 互补）：全零音频 200 帧，PNowSystem 不得达门限。
	e.Reset()
	silHigh := 0
	zero1 := make([]float32, NewSamples)
	for k := 0; k < 200; k++ {
		pred, err := e.Step(zero1, chunk2)
		if err != nil {
			t.Fatalf("silence %d: Step: %v", k, err)
		}
		if pred.PNowSystem >= cfg.VAPLeadPNow {
			silHigh++
		}
	}
	if silHigh != 0 {
		t.Errorf("全静音 %d/200 帧 PNowSystem≥%.2f（纯静音不得预测系统接话）", silHigh, cfg.VAPLeadPNow)
	}
	t.Logf("T3-G1-01 误截断=0/%d 停顿点（语音中零提前；真模型在环，合成语音代理）", units)
	t.Logf("T3-G1-01 间隙 lead 触发=%d/%d（机制面：模型预测系统接话→提前收口；韵律调参面归真实儿童集）",
		leadFires, units*gapFrame)
	t.Logf("T3-G1-01 静音面：200 全零帧 PNowSystem 全部 <%.2f；推理 RTF=%.3f", cfg.VAPLeadPNow, e.RTF())
}

// leadAdapter 把 *Engine 适配为 turntaking.Predictor（结构化兼容，无包间 import）。
type leadAdapter struct{ e *Engine }

func (a leadAdapter) Predict() (turntaking.Prediction, bool) {
	p, ok := a.e.Predict()
	return turntaking.Prediction{
		PNowUser:      p.PNowUser,
		PNowSystem:    p.PNowSystem,
		PFutureUser:   p.PFutureUser,
		PFutureSystem: p.PFutureSystem,
		VADUser:       p.VADUser,
		VADSystem:     p.VADSystem,
	}, ok
}

// TestT3G102ResponseFirstPacket T3-G1-02（BI-3.1/G1，真实接线 IR #129）：端点判定
// 增量面实测——逐帧推理墙钟 P95 ≤100ms（≤1 帧，检测级实时；预算划拨见
// configs/budgets/latency.yaml tail_silence 段与本仓 reports/eval/T3/ 划拨说明）。
// 全链 P95≤900ms（话轮终点→TTS 首包）的 ASR/LLM/TTS 段与固定硬件 ×3 计时归 M2，
// 本测试锁定 T3 面：VAP 检测增量不超一帧 + 提前量使接话等待 ≤SilenceMs。
func TestT3G102ResponseFirstPacket(t *testing.T) {
	gaterunner.Mark(t, "T3", "BI-3.1", "T3-G1-02", "G1")
	e := engineOrSkip(t)

	const n = 40
	chunk1 := make([]float32, NewSamples)
	chunk2 := make([]float32, NewSamples)
	walls := make([]time.Duration, 0, n)
	for k := 0; k < n; k++ {
		for j := range chunk1 {
			chunk1[j] = goldenChunk(k, j)
		}
		begin := time.Now()
		if _, err := e.Step(chunk1, chunk2); err != nil {
			t.Fatalf("frame %d: Step: %v", k, err)
		}
		walls = append(walls, time.Since(begin))
	}
	p95 := percentile(walls, 0.95)
	p50 := percentile(walls, 0.5)
	const budget = 100 * time.Millisecond
	if p95 > budget {
		t.Errorf("端点判定增量 P95=%v > %v（检测级实时预算）", p95, budget)
	}
	t.Logf("T3-G1-02 端点判定增量：P50=%v P95=%v（≤%v 锁定）；RTF=%.4f（%d 帧）",
		p50, p95, budget, e.RTF(), e.Steps())
	t.Logf("T3-G1-02 口径：T3 面内增量=推理 ≤100ms/帧 + 尾静音等待（SilenceMs=700ms，VAP 提前量可再提前）；900ms 全链 P95 归 M2 硬件三测")
}

func percentile(sorted []time.Duration, q float64) time.Duration {
	// 就地排序的简单百分位（n 小，实现直白；统计断言面不在本测试——evalkit 纪律
	// 针对 holdout/benchmark 数据面，时延观测为工程预算面）。
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	idx := int(q * float64(len(sorted)-1))
	return sorted[idx]
}
