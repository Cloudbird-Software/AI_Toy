// T4 门禁测试（m2-spec §10 Mark 接线策略表，IR #90）：G0-01/02 真实——gen-tneg
// 6h 家庭音景帧流 / gen-kwsadv 30min 对抗同音节流真实驱动被测 Detector（内置
// 启发式打分器，无注入），零唤醒断言 + evalkit 泊松上限统计判定；G1-01 debt
// （每 SNR 档 500 合成阳性样本实测唤醒率如实记录——桩无唤醒语义，正样本合成不
// 可冒充唤醒率，spec §10）；G1-02/03 debt——真实童声/目标硬件数据面未建，每条
// 先真实执行逻辑面（统计/计时通道，失败即红）再对数据面 t.Skipf（写明缺失物与
// 消解路径；dispatchGate 按顶层整测 SKIP 判 debt，IR #76/ADR-0002）。
// 口径与样本量声明唯一来源：configs/gates/T4.yaml（本文件只落断言本体）。
package kws

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/Cloudbird-Software/AI_Toy/tools/evalkit/evalkit"
	"github.com/Cloudbird-Software/AI_Toy/tools/gaterunner/gaterunner"
	"github.com/Cloudbird-Software/AI_Toy/tools/synthgen/synthgen"
)

const gateFrameMs = 30

// 门禁通用被测配置（产品面：内置启发式打分器、防抖 3 帧、不应期 60ms、阈 0.55
// ——阈值/帧参数为检测器配置非门禁阈值，门禁线在 configs/gates/T4.yaml）。
const (
	gateConfirm    = 3
	gateRefractory = 60
	gateThreshold  = 0.55
)

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

// pushNegStream 消费整条负样本帧流（PCM 复用缓冲：Push 逐帧同步消费即契约内
// 用法），返回唤醒事件数。
func pushNegStream(t *testing.T, d *Detector, st *synthgen.NegStream) int {
	t.Helper()
	wakes := 0
	for {
		f, ok := st.Next()
		if !ok {
			return wakes
		}
		if ev := d.Push(Frame{TS: f.TS, PCM: f.PCM}); ev.Kind == EvWake {
			wakes++
		}
	}
}

// TestT4FalseWakePerHour T4-G0-01（BI-4.2/G0）真实：误唤醒率 ≤0.5/h（zero_event，
// min_evidence hours:6——泊松 3/N 上限须 ≤0.5）。数据面（m2-spec §10）：gen-tneg
// 冻结参数集 6h 家庭音景负样本帧流（speech_like 远场语音状谐波列车 / tv_noise 宽带
// 平稳噪声 SNR −20~0dB / burst 突发尖峰 ≥近讲声级 / mixed 三源叠加，等额轮转；
// 远场底噪 ≥2.6×语音幅度）过 Detector.Push 断言零唤醒；统计判定走 evalkit
// PoissonUpper95（不手算）。种子 FNV-1a 64 标签约定（T4-G0-01）——canonical 批
// datasets/synth/batches/gen-tneg-1.0.0-* 同参确定性重建。时间压缩：帧 TS 按音频
// 时钟推进（6h=720000 帧），测试墙钟不真等 6h。真模型（M3 ONNX）接入后同测重跑。
func TestT4FalseWakePerHour(t *testing.T) {
	gaterunner.Mark(t, "T4", "BI-4.2", "T4-G0-01", "G0")
	const evidenceHours = 6 // min_evidence hours:6（configs/gates/T4.yaml）
	d := gateDetector(t, Config{FrameMs: gateFrameMs, ConfirmFrames: gateConfirm,
		RefractoryMs: gateRefractory, Threshold: gateThreshold})
	st, err := synthgen.NewTNegStream(evidenceHours*3600*1000, synthgen.NegSeed("T4-G0-01"))
	if err != nil {
		t.Fatalf("gen-tneg 6h 帧流构造失败: %v", err)
	}
	if want := evidenceHours * 3600 * 1000 / synthgen.NegFrameMs; st.Frames() != want {
		t.Fatalf("6h 帧数 = %d, want %d（min_evidence hours:6 证据面不足）", st.Frames(), want)
	}
	falseWakes := pushNegStream(t, d, st)
	observed := float64(falseWakes) / float64(evidenceHours)
	upper := evalkit.PoissonUpper95(falseWakes, evidenceHours)
	if observed > 0.5 || upper > 0.5 {
		t.Fatalf("false_wake_per_hour 红：6h 家庭音景负样本误唤醒 %d 次（observed %.4f/h，泊松 95%% 上限 %.4f/h，阈值 ≤0.5/h，min_evidence hours:6）",
			falseWakes, observed, upper)
	}
}

// TestT4AdversarialTriggerCount T4-G0-02（BI-4.2/G0）真实：定向对抗负样本 0 次
// 触发（==0，≥30min 流）。数据面（m2-spec §10）：gen-kwsadv 冻结参数集 30min
// 对抗同音节负样本帧流（他牌唤醒词 xiaoai/tianmao/xiaodu 音节模式 + 本词音节
// 高混淆近邻 nearconf，f0 贴本词占位带）真实驱动被测 Detector（内置打分器，
// 无置信度注入）断言零触发。种子 FNV-1a 64 标签约定（T4-G0-02）。真模型接入后
// 同测重跑。
func TestT4AdversarialTriggerCount(t *testing.T) {
	gaterunner.Mark(t, "T4", "BI-4.2", "T4-G0-02", "G0")
	const adversarialMs = 30 * 60 * 1000 // ≥30min（资产卡口径）
	d := gateDetector(t, Config{FrameMs: gateFrameMs, ConfirmFrames: gateConfirm,
		RefractoryMs: gateRefractory, Threshold: gateThreshold})
	st, err := synthgen.NewKWSAdvStream(adversarialMs, synthgen.NegSeed("T4-G0-02"))
	if err != nil {
		t.Fatalf("gen-kwsadv 30min 帧流构造失败: %v", err)
	}
	if want := adversarialMs / synthgen.NegFrameMs; st.Frames() != want {
		t.Fatalf("30min 帧数 = %d, want %d（≥30min 证据面不足）", st.Frames(), want)
	}
	triggers := pushNegStream(t, d, st)
	if triggers != 0 {
		t.Fatalf("adversarial_trigger_count 红：30min 对抗同音节流触发 %d 次（阈值 ==0）", triggers)
	}
}

// gateWakeTrial 单次合成阳性唤醒试验（T4-G1-01 实测口径）：pad 静音 + 6 帧唤醒
// 模式占位（200Hz 正弦 amp=p1Amp，P1/P5 同口径）+ SNR 档加噪（固定种子确定可
// 复现），fresh Detector（试验独立、无跨试验不应期/滑窗残留）。返回是否唤醒。
func gateWakeTrial(snrDb float64, seed int64) bool {
	d, err := NewDetector(Config{FrameMs: gateFrameMs, ConfirmFrames: gateConfirm,
		RefractoryMs: gateRefractory, Threshold: gateThreshold})
	if err != nil {
		panic(err) // 不可达：配置常量已校验域
	}
	sigma := p1Amp / math.Pow(10, snrDb/20)
	r := rand.New(rand.NewSource(seed))
	wake := false
	const pad = 3
	for i := 0; i < pad+6; i++ {
		var f Frame
		if i >= pad {
			f = synthSineFrame(int64(i)*gateFrameMs, gateFrameMs, i-pad, p1Freq, p1Amp)
			for j, v := range f.PCM {
				f.PCM[j] = int16(math.Round(clampF(float64(v)+r.NormFloat64()*sigma, -32768, 32767)))
			}
		} else {
			f = synthSilenceFrame(int64(i)*gateFrameMs, gateFrameMs)
		}
		if ev := d.Push(f); ev.Kind == EvWake {
			wake = true
		}
	}
	return wake
}

// TestT4WakeRateNear T4-G1-01（BI-4.1/G1）debt：唤醒率近讲 ≥97%（pass_rate，
// min_evidence n:500，SNR 5/10/20dB 分档）。实测面：每档 500 合成阳性样本
// （synthgen 时代数据面未建，测试内构造特征帧——200Hz 正弦占位 + 档位加噪）真实
// 驱动内置打分器测唤醒率（Wilson 95% CI 一并报，evalkit）；逻辑面锚点：40dB
// 近净空档须全唤醒（统计通道非空转）。debt 原因（m2-spec §10）：桩无唤醒语义
// ——正弦占位帧不是唤醒词发音，合成阳性不可冒充唤醒率证据（即便实测 100%）；门
// 线 0.97 不因实测调整（configs/gates/T4.yaml 法典禁改）。
func TestT4WakeRateNear(t *testing.T) {
	gaterunner.Mark(t, "T4", "BI-4.1", "T4-G1-01", "G1")
	const n = 500 // min_evidence n:500（每词 ≥500 合成，年龄/口音分层未建）
	// 逻辑面锚点：40dB 近净空档全唤醒（打分器对唤醒模式占位可触发——P1 端点同口径）。
	for i := 0; i < n; i++ {
		if !gateWakeTrial(40, 20260829+int64(i)) {
			t.Fatalf("wake_rate_near 逻辑面红：40dB 档样本 %d 未唤醒（唤醒通道空转）", i)
		}
	}
	// 实测面：门禁三档（20/10/5dB）各 500 试验，报唤醒率 + Wilson 95% CI（如实记录）。
	report := ""
	for _, snr := range []float64{20, 10, 5} {
		wakes := 0
		for i := 0; i < n; i++ {
			if gateWakeTrial(snr, 20260829+int64(i)) {
				wakes++
			}
		}
		rate := float64(wakes) / float64(n)
		lo, hi := evalkit.Wilson(wakes, n)
		report += fmt.Sprintf("%.0fdB: %d/%d=%.3f (Wilson 95%% CI [%.3f,%.3f]); ", snr, wakes, n, rate, lo, hi)
	}
	t.Skipf("T4-G1-01 debt：桩无唤醒语义——200Hz 正弦占位帧非唤醒词发音，合成阳性实测不可冒充唤醒率证据（m2-spec §10：需真模型+每词 ≥500 合成正样本（年龄/口音分层）+真实童声 ≥200 分 SNR 档，noise_band 校准）。启发式打分器天花板，待 M3 ONNX 推理器接入后以真实正样本重测去本 Skip。实测值（内置打分器+合成阳性占位，500 试验/档，如实记录非证据）：%s", report)
}

// TestT4ChildAdultWakeRateGap T4-G1-02（BI-4.1/G1）debt：儿童/成人公平性
// 儿童 ≥成人−5pp（child_adult_wake_rate_gap ≤0.05，min_evidence n:300）。
// 逻辑面：注入儿童/成人两档置信度（0.92/0.90），各 n 流走通 gap 统计通道
// 并断言 gap 计算正确；数据面：儿童/成人各 ≥300 真实正样本未建（真实童声与
// 合成须分列，禁合并——资产卡）。
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
	t.Skipf("T4-G1-02 debt：儿童/成人公平性证据面未建——同协议各 ≥300 真实正样本（3–4/5–6/7–9 岁分段，各段不低于总体线 8pp）未建，真实童声与合成须分列（禁合并）；当前仅注入置信度走通 gap 统计通道。随 T2 数据飞轮建设消解：真实正样本集就位后以真实儿童/成人流替换注入流并去掉本 Skip。")
}

// TestT4RTF T4-G1-03（BI-4.3/G1）debt：端侧 RTF ≤0.1 + 常驻内存 ≤T14 预算无增长
// （min_evidence hours:1 目标硬件连续推理）。逻辑面：合成帧流（60s 等效音频）
// 真实计时跑满推理循环，RTF 框架值=处理时长/音频时长，断言有限正值且滑窗
// 内存有界（无增长通道真实）；数据面：目标硬件 1h 实测未建（M2 无硬件目标，
// m2-spec §10）。
func TestT4RTF(t *testing.T) {
	gaterunner.Mark(t, "T4", "BI-4.3", "T4-G1-03", "G1")
	d := gateDetector(t, Config{FrameMs: gateFrameMs, ConfirmFrames: gateConfirm,
		RefractoryMs: gateRefractory, Threshold: gateThreshold})
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
	t.Skipf("T4-G1-03 debt：RTF/常驻内存证据面未建——目标硬件连续推理 1h（min_evidence hours:1，RTF≤0.1 且内存 ≤T14 预算无增长）须真机实测，M2 无硬件目标（m2-spec §10）；当前仅合成帧流走通真实计时循环与有界性检查（通用 CPU 计时非目标硬件证据）。硬件面待真机接入后去掉本 Skip。")
}
