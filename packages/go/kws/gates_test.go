// T4 门禁测试（m1-spec §5 Mark 接线策略表，IR #79）：五条全 debt——负样本音景/
// 真实童声数据集未建（T2 数据飞轮）、RTF 需目标硬件。每条先真实执行逻辑面
// （合成帧流驱动 Detector 的事件统计/计时通道，失败即红——测试必须真实执行到
// 能判断的程度），再对数据面 t.Skipf（写明缺失物与消解路径）；数据就位后仅需
// 去掉 Skip 换真实数据源（dispatchGate 按顶层整测 SKIP 判 debt，IR #76/ADR-0002）。
// 口径与样本量声明唯一来源：configs/gates/T4.yaml（本文件只落断言本体）。
package kws

import (
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/Cloudbird-Software/AI_Toy/tools/gaterunner/gaterunner"
)

const gateFrameMs = 30

// gateDetector 门禁测试通用构造。
func gateDetector(t *testing.T, cfg Config) *Detector {
	t.Helper()
	d, err := NewDetector(cfg)
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	return d
}

// gateWakeOnce 单流注入恒定置信度（防抖 3 帧同值），返回该流是否唤醒。
func gateWakeOnce(t *testing.T, conf, threshold float64) bool {
	t.Helper()
	infer := ConfidenceFunc(func(f Frame) float64 { return conf })
	d := gateDetector(t, Config{FrameMs: gateFrameMs, ConfirmFrames: 3,
		RefractoryMs: 90, Threshold: threshold, Infer: infer})
	wake := false
	for k := 0; k < 3; k++ {
		if ev := d.Push(Frame{TS: int64(k) * gateFrameMs, Feats: []float32{0}}); ev.Kind == EvWake {
			wake = true
		}
	}
	return wake
}

// TestT4FalseWakePerHour T4-G0-01（BI-4.2/G0）：误唤醒率 ≤0.5/h（zero_event，
// min_evidence hours:6——泊松 3/N 上限须 ≤0.5）。逻辑面：内置打分器 + 确定性
// 合成音景（白噪声×静音交替，覆盖 1h 等效帧流）走通误唤醒计数通道并断言零
// 事件（桩对非唤醒模式不触发，真实断言）；数据面：≥6h 家庭音景负样本库未建。
func TestT4FalseWakePerHour(t *testing.T) {
	gaterunner.Mark(t, "T4", "BI-4.2", "T4-G0-01", "G0")
	d := gateDetector(t, Config{FrameMs: gateFrameMs, ConfirmFrames: 3,
		RefractoryMs: 60, Threshold: 0.55})
	// 1h 等效帧流（120000 帧）：每 100 帧一段——60 帧白噪声（σ=8000）+40 帧静音，
	// 交替模拟家庭音景占位（电视/聊天噪声通道）。固定种子：确定可复现。
	const totalFrames = 3600 * 1000 / gateFrameMs
	r := rand.New(rand.NewSource(79))
	falseWakes := 0
	for i := 0; i < totalFrames; i++ {
		var f Frame
		if (i/100)%2 == 0 {
			pcm := make([]int16, gateFrameMs*16)
			for j := range pcm {
				pcm[j] = int16(math.Round(r.NormFloat64() * 8000))
			}
			f = Frame{TS: int64(i) * gateFrameMs, PCM: pcm}
		} else {
			f = synthSilenceFrame(int64(i)*gateFrameMs, gateFrameMs)
		}
		if ev := d.Push(f); ev.Kind == EvWake {
			falseWakes++
		}
	}
	if falseWakes != 0 {
		t.Fatalf("false_wake_per_hour 逻辑面红：合成音景占位流误唤醒 %d 次（阈值 ≤0.5/h，桩须对非唤醒模式零触发）", falseWakes)
	}
	t.Skipf("T4-G0-01 debt：负样本家庭音景库 ≥6h（min_evidence hours:6，泊松 3/N 上限须 ≤0.5/h；30h→≤0.1/h 量产线）未建——与 T3 共用，synthgen 注册流程未走；当前仅合成噪声/静音流走通误唤醒计数通道（内置桩无唤醒语义，非真实误唤醒率证据）。随 T2 数据飞轮建设消解：音景库就位后以真实负样本流替换合成流并去掉本 Skip。")
}

// TestT4AdversarialTriggerCount T4-G0-02（BI-4.2/G0）：定向对抗负样本（他牌
// 唤醒词/广告含同音节）0 次触发。逻辑面：注入判别 Inferencer（对抗样本低置信）
// 跑 30min 等效对抗流断言 0 触发（计数通道真实）；数据面：≥30min 对抗负样本未建。
func TestT4AdversarialTriggerCount(t *testing.T) {
	gaterunner.Mark(t, "T4", "BI-4.2", "T4-G0-02", "G0")
	// 判别占位：他牌唤醒词/广告同音节对齐为低置信（真实判别面归 M2 模型接入）。
	infer := ConfidenceFunc(func(f Frame) float64 {
		if len(f.Feats) > 0 {
			return float64(f.Feats[0])
		}
		return 0.05
	})
	d := gateDetector(t, Config{FrameMs: gateFrameMs, ConfirmFrames: 3,
		RefractoryMs: 60, Threshold: 0.55, Infer: infer})
	const adversarialFrames = 30 * 60 * 1000 / gateFrameMs // 30min 等效
	triggers := 0
	for i := 0; i < adversarialFrames; i++ {
		// 对抗帧：同音节结构占位（有能量、无本词语义——Feats 标注对抗置信）。
		f := Frame{TS: int64(i) * gateFrameMs,
			PCM:   synthSineFrame(int64(i)*gateFrameMs, gateFrameMs, i, 400, 6000).PCM,
			Feats: []float32{0.05}}
		if ev := d.Push(f); ev.Kind == EvWake {
			triggers++
		}
	}
	if triggers != 0 {
		t.Fatalf("adversarial_trigger_count 逻辑面红：对抗流触发 %d 次（阈值 ==0）", triggers)
	}
	t.Skipf("T4-G0-02 debt：对抗负样本 ≥30min（他牌唤醒词/广告含同音节）未建——synthgen 注册流程未走；当前以注入判别通道验证 0 触发计数逻辑（非真实对抗证据，真实判别面须 M2 模型接入）。随 T2 数据飞轮建设消解：对抗样本集就位后替换注入流并去掉本 Skip。")
}

// TestT4WakeRateNear T4-G1-01（BI-4.1/G1）：唤醒率近讲 ≥97%（pass_rate，
// min_evidence n:500，SNR 5/10/20dB 分档）。逻辑面：注入 SNR 分档置信度
// （20/10/5dB→0.97/0.90/0.60），各档合成 n 流走通唤醒率统计通道并按注入
// 期望断言（统计口径真实：conf>Threshold 全唤醒、conf≤Threshold 全不唤醒）；
// 数据面：每词 ≥500 合成+真实童声 ≥200 未建。
func TestT4WakeRateNear(t *testing.T) {
	gaterunner.Mark(t, "T4", "BI-4.1", "T4-G1-01", "G1")
	const n = 100          // 统计通道验证用样本量（真实证据面 n:500+200，见 Skipf）
	const threshold = 0.55 // 门禁线 0.97 由真实数据面断言；逻辑面验证统计通道本身
	snrConf := map[string]float64{"20dB": 0.97, "10dB": 0.90, "5dB": 0.60}
	for snr, conf := range snrConf {
		wakes := 0
		for i := 0; i < n; i++ {
			if gateWakeOnce(t, conf, threshold) {
				wakes++
			}
		}
		rate := float64(wakes) / float64(n)
		want := 0.0
		if conf > threshold {
			want = 1.0
		}
		if rate != want {
			t.Fatalf("wake_rate_near 逻辑面红（%s）：注入置信 %.2f 期望唤醒率 %.2f got %.2f", snr, conf, want, rate)
		}
	}
	t.Skipf("T4-G1-01 debt：唤醒率分档证据面未建——每词 ≥500 合成（年龄/口音分层）+真实童声 ≥200（SNR 5/10/20dB 分档报，近讲 ≥97%%/远场 ≥90%%，noise_band 校准）未建，且推理模型未接（桩无唤醒语义，注入置信度非真实唤醒率）。随 T2 数据飞轮建设消解（synthgen 注册+真实童声经 holdout 侧）：数据集就位后以真实正样本流替换注入流并去掉本 Skip。")
}

// TestT4ChildAdultWakeRateGap T4-G1-02（BI-4.1/G1）：儿童/成人公平性
// 儿童 ≥成人−5pp（child_adult_wake_rate_gap ≤0.05，min_evidence n:300）。
// 逻辑面：注入儿童/成人两档置信度（0.92/0.90），各 n 流走通 gap 统计通道
// 并断言 gap 计算正确；数据面：儿童/成人各 ≥300 正样本未建。
func TestT4ChildAdultWakeRateGap(t *testing.T) {
	gaterunner.Mark(t, "T4", "BI-4.1", "T4-G1-02", "G1")
	const n = 100 // 统计通道验证用样本量（真实证据面各 n:300，见 Skipf）
	const threshold = 0.55
	rateOf := func(conf float64) float64 {
		wakes := 0
		for i := 0; i < n; i++ {
			if gateWakeOnce(t, conf, threshold) {
				wakes++
			}
		}
		return float64(wakes) / float64(n)
	}
	child, adult := rateOf(0.92), rateOf(0.90)
	gap := adult - child
	if gap < 0 || gap > 0.05 {
		t.Fatalf("child_adult_wake_rate_gap 逻辑面红：儿童 %.2f 成人 %.2f gap %.4f（阈值 ≤0.05 且儿童不得低于成人）", child, adult, gap)
	}
	t.Skipf("T4-G1-02 debt：儿童/成人公平性证据面未建——同协议各 ≥300 正样本（3–4/5–6/7–9 岁分段，各段不低于总体线 8pp）未建，真实童声与合成须分列（禁合并）；当前仅注入置信度走通 gap 统计通道。随 T2 数据飞轮建设消解：正样本集就位后以真实儿童/成人流替换注入流并去掉本 Skip。")
}

// TestT4RTF T4-G1-03（BI-4.3/G1）：端侧 RTF ≤0.1 + 常驻内存 ≤T14 预算无增长
// （min_evidence hours:1 目标硬件连续推理）。逻辑面：合成帧流（60s 等效音频）
// 真实计时跑满推理循环，RTF 框架值=处理时长/音频时长，断言有限正值且滑窗
// 内存有界（无增长通道真实）；数据面：目标硬件 1h 实测未建（M1 无硬件目标）。
func TestT4RTF(t *testing.T) {
	gaterunner.Mark(t, "T4", "BI-4.3", "T4-G1-03", "G1")
	d := gateDetector(t, Config{FrameMs: gateFrameMs, ConfirmFrames: 3,
		RefractoryMs: 60, Threshold: 0.55})
	const frames = 60 * 1000 / gateFrameMs // 60s 等效音频
	frame := synthSineFrame(0, gateFrameMs, 0, 200, 4000)
	start := time.Now()
	for i := 0; i < frames; i++ {
		d.Push(Frame{TS: int64(i) * gateFrameMs, PCM: frame.PCM})
	}
	elapsed := time.Since(start).Seconds()
	rtf := elapsed / 60.0 // 处理时长 / 音频时长
	if math.IsNaN(rtf) || math.IsInf(rtf, 0) || rtf <= 0 {
		t.Fatalf("rtf 计时逻辑红：RTF 框架值非法 %v（elapsed=%v）", rtf, elapsed)
	}
	if windowCap := d.WindowLen(); windowCap <= 0 || windowCap > 4096 {
		t.Fatalf("滑窗内存有界性红：窗长 %d（须 ∈ (0, 4096]——常驻内存不随流长增长）", windowCap)
	}
	t.Skipf("T4-G1-03 debt：RTF/常驻内存证据面未建——目标硬件连续推理 1h（min_evidence hours:1，RTF≤0.1 且内存 ≤T14 预算无增长）须真机实测，M1 无硬件目标；当前仅合成帧流走通真实计时循环与有界性检查（通用 CPU 计时非目标硬件证据）。数据面随 T2 数据飞轮建设消解、硬件面待 M2 真机接入后去掉本 Skip。")
}
