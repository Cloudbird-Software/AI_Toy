// 门禁接线（m1-spec §5 策略表 + §6 三件套之三）：一 ID 一顶层测试函数，Mark 注册
// 调用单行书写（collect 源码扫描正则约定）。verdict 语义（ADR-0002/IR #76）：
// 真实=注册测试实跑断言；debt=整测 t.Skipf（写明数据依赖原因，不计 pass 不阻断、
// 不占豁免台账；gaterunner 经 -v 输出 `--- SKIP: <Test>` 判 debt）。阈值与口径
// 唯一来源 configs/gates/T3.yaml（本文件只落断言本体，不复制阈值语义）。
package turntaking

import (
	"math/rand"
	"testing"
	"time"

	"github.com/Cloudbird-Software/AI_Toy/tools/gaterunner/gaterunner"
)

// TestT3G001InterruptDetectRate T3-G0-01（BI-3.2/G0，真实接线）：合成 VAD 事件流
// 注入 50 次人工打断（min_evidence.n；异态=4 种前序场景/异时=打断延迟 0..3000ms/
// 异长=前序话轮与静音时长随机；固定种子可复现）：Speaking+EvVoiceStart→首
// Action=ActStopTTS 即检出，检出率点估计 ≥0.95（rule=metric，n=50<59 点估计口径，
// 样本量对齐后升 pass_rate）；响应=同步单步（逻辑延迟 0 ≤ BargeInWindow ≤300ms
// 契约；链路实测延迟 M2 硬件计时，音频面不同音量/距离同 M2 升级口径）。
func TestT3G001InterruptDetectRate(t *testing.T) {
	gaterunner.Mark(t, "T3", "BI-3.2", "T3-G0-01", "G0")
	const n = 50                        // min_evidence.n（configs/gates/T3.yaml：回放对话注入 50 次人工打断）
	rng := rand.New(rand.NewSource(80)) // 固定种子：注入流可复现
	sils := [3]int64{400, 800, 1200}    // 异态：场景级 SilenceMs
	maxs := [3]int64{5000, 10000, 20000}
	wins := [3]int{1, 150, 300} // 打断响应窗口契约边界（均 ≤300ms）
	detected, maxLat := 0, int64(0)
	var wallMax time.Duration
	for i := 0; i < n; i++ {
		cfg := Config{SilenceMs: int(sils[i%3]), MaxTurnMs: int(maxs[(i/3)%3]), BargeInWindow: wins[(i/9)%3]}
		sil, maxTurn := int64(cfg.SilenceMs), int64(cfg.MaxTurnMs)
		fsm, err := NewFSM(cfg)
		if err != nil {
			t.Fatalf("场景 %d：NewFSM 合法配置被拒：%v", i, err)
		}
		// 前序流（异态四型，时间戳全程单调）：
		// 0 唤醒即播报｜1 完整话轮（静音终点→Idle）后播报（闭环 Idle+SpeakStart）
		// 2 未终点仍在 Listening 时开播（行5）｜3 先打断一次再播报（二次打断链）
		t0, prior := int64(100), int64(200+rng.Intn(1800)) // prior=前序话轮时长（异长）
		var speakAt int64
		switch i % 4 {
		case 0:
			speakAt = t0 + 50
		case 1:
			fsm.OnVAD(VADEvent{Kind: EvVoiceStart, AtMs: t0 + 100})
			fsm.OnVAD(VADEvent{Kind: EvVoiceEnd, AtMs: t0 + 100 + prior})
			fsm.OnVAD(VADEvent{Kind: EvNone, AtMs: t0 + 100 + prior + sil}) // 行3 终点→Idle
			speakAt = t0 + 100 + prior + sil + 10
		case 2:
			fsm.OnVAD(VADEvent{Kind: EvVoiceStart, AtMs: t0 + 100})
			fsm.OnVAD(VADEvent{Kind: EvVoiceEnd, AtMs: t0 + 100 + prior})
			fsm.OnVAD(VADEvent{Kind: EvNone, AtMs: t0 + 100 + prior + sil - 1}) // 差 1ms 不终点
			speakAt = t0 + 100 + prior + sil - 1 + 10
		default:
			s0 := t0 + 50
			fsm.OnSpeakStart(s0)
			fsm.OnVAD(VADEvent{Kind: EvVoiceStart, AtMs: s0 + 30}) // 第一次打断（不计入样本）
			fsm.OnVAD(VADEvent{Kind: EvVoiceEnd, AtMs: s0 + 30 + prior})
			fsm.OnVAD(VADEvent{Kind: EvNone, AtMs: s0 + 30 + prior + sil}) // 行3 终点→Idle
			speakAt = s0 + 30 + prior + sil + 10
		}
		fsm.OnSpeakStart(speakAt)
		if fsm.State() != StSpeaking {
			t.Fatalf("场景 %d（型 %d）：注入打断前须 Speaking，got %d", i, i%4, fsm.State())
		}
		delay := int64(rng.Intn(3001)) // 异时：0..3000ms（含与开播同刻 0）
		if i%17 == 0 {
			delay = 0
		}
		bargAt := speakAt + delay
		begin := time.Now()
		acts := fsm.OnVAD(VADEvent{Kind: EvVoiceStart, AtMs: bargAt})
		if w := time.Since(begin); w > wallMax {
			wallMax = w
		}
		if len(acts) >= 2 && acts[0].Kind == ActStopTTS && acts[1].Kind == ActOpenMic &&
			fsm.State() == StListening && acts[0].AtMs == bargAt {
			detected++
			lat := acts[0].AtMs - bargAt // 同步单步：逻辑延迟 0
			if lat > maxLat {
				maxLat = lat
			}
			if lat > int64(cfg.BargeInWindow) || fsm.BargeInLatencyMs() > int64(cfg.BargeInWindow) {
				t.Errorf("场景 %d：打断响应 %dms 超出 BargeInWindow=%dms 契约", i, lat, cfg.BargeInWindow)
			}
			continue
		}
		t.Errorf("场景 %d（型 %d，sil=%d maxTurn=%d delay=%d）：打断未被检出——actions=%v state=%d",
			i, i%4, sil, maxTurn, delay, acts, fsm.State())
	}
	rate := float64(detected) / float64(n)
	t.Logf("T3-G0-01 interrupt_detect_rate=%.4f（%d/%d，点估计口径 n=50；yaml rule=metric 阈值 0.95）", rate, detected, n)
	t.Logf("T3-G0-01 打断响应：逻辑延迟 max=%dms（同步单步 ≤ BargeInWindow ≤300ms；链路实测 M2）；OnVAD 调用 wall max=%v（仅证据记录）",
		maxLat, wallMax)
	if rate < 0.95 {
		t.Errorf("interrupt_detect_rate=%.4f < 0.95（检出 %d/%d，一次「不理睬」=失败样本）", rate, detected, n)
	}
}

// TestT3G002SilenceNoiseTrigger T3-G0-02（BI-3.1/G0，debt）：全静音/纯噪声永不
// 触发接话（0 次触发；泊松 3/N：≥6h 零事件）。
// 注（IR #129）：模型面已补真实证据——VAP 引擎（turntaking/vap）在纯静音流实测
// PNowSystem 恒低于提前量门限（200 全零帧，见 TestT3G101MiscutRate 静音面）；
// 本门禁的 ≥6h 负样本音景流数据面仍缺，维持 debt。
func TestT3G002SilenceNoiseTrigger(t *testing.T) {
	gaterunner.Mark(t, "T3", "BI-3.1", "T3-G0-02", "G0")
	t.Skipf("T3-G0-02 debt：音频 VAD 前端未建——FSM 由 VAD 事件驱动（OnVAD），负样本音景流（gen-tneg ≥6h，datasets/synth/batches，已被 T4-G0-01 真实消费）需经 VAD 前端转事件才能驱动 zero_event 泊松口径（3/N）联跑；M1 桩无音频输入面，VAD 前端落地后升真实接线")
}

// TestT3G103ContextRetention T3-G1-03（BI-3.2/G1，debt）：打断后上下文保持
// ≥90%（50 个打断场景后续追问引用被打断内容）。
func TestT3G103ContextRetention(t *testing.T) {
	gaterunner.Mark(t, "T3", "BI-3.2", "T3-G1-03", "G1")
	t.Skipf("T3-G1-03 debt：打断后上下文保持需对话链（LLM，M2 接入）——打断-追问场景集与对话记忆均未建；M2 对话链接入后升真实接线")
}

// TestT3G104MidpauseTolerance T3-G1-04（BI-3.3/G1，debt）：中停顿容忍
// midpause_miscut_rate ≤8%（1.5–3s 思考停顿，与 T3-G1-01 同线、不许单独放宽）。
// 注（IR #129）：VAP 提前量只加速收口、不解决中停顿误截断——该面需儿童语速
// 自适应静音门限（资产卡路径 A/C 的自适应层）+ 真实儿童中停顿集 ≥100，均未建。
func TestT3G104MidpauseTolerance(t *testing.T) {
	gaterunner.Mark(t, "T3", "BI-3.3", "T3-G1-04", "G1")
	t.Skipf("T3-G1-04 debt：中停顿样本 ≥100（1.5–3s 思考停顿）与自适应静音门限机制均未建（VAP 提前量不解决此面，见 packages/go/turntaking/predict.go）；样本集+机制落地后升真实接线（阈值与 T3-G1-01 同线，不单独放宽）")
}
