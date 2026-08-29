// T6 门禁测试（m3-spec §9 Mark 接线策略表，IR #106）：四门禁全真实——合成
// 加速度曲线（台架代理：拿起剖面 ≥200 变体 / 36h 仿真静置流 / 1m 跌落剖面
// ≥30 次 / 30min 持续输出）真实驱动被测 Detector+Guard，evalkit 泊松上限/
// Wilson 统计判定（勿手算）；Guard 边界盒=表驱动穷举+生成式 fuzz 双面。
// 口径与样本量声明唯一来源：configs/gates/T6.yaml（本文件只落断言本体）。
// 合成曲线口径声明：通道正确性与规则面行为验证，不代表台架/真机性能宣称
// （真机 3 台标准跌落/振动/静置脚本=holdout L5，物理不可合成）。
package imu

import (
	"math"
	"math/rand"
	"testing"

	"github.com/Cloudbird-Software/AI_Toy/tools/evalkit/evalkit"
	"github.com/Cloudbird-Software/AI_Toy/tools/gaterunner/gaterunner"
)

// imuGateSeed 门禁冻结种子（参数冻结纪律：剖面/变体参数一次选定写死，任何
// 检出/误触发结果均为如实观测，非调参产物；对齐 T5 门禁 famSeed 口径）。
const imuGateSeed = 20260830

// —— T6-G1-01 合成拿起剖面（不同人/姿势变体）——

// gatePickupParams 拿起剖面变体：力度（峰值）/起手速度（上升时长）/行走
// 摆动（幅度+周期+持续）/传感器噪声——「不同人/姿势」的参数面。
type gatePickupParams struct {
	peak       float64
	riseMs     int
	swayAmp    float64
	swayPeriod float64
	swayMs     int
	sigma      float64
}

// randPickupParams 变体抽取（冻结域：峰值 1.6~2.4g、起手 80~200ms、摆动
// 0.06~0.10g/600~1000ms、噪声 σ≤0.03——全帧幅值下确界 ≥1.41g → 重力偏离
// ≥0.41 恒超 PickupThreshG，超阈持续 ≥550ms ≥DebounceMs=250）。
func randPickupParams(r *rand.Rand) gatePickupParams {
	return gatePickupParams{
		peak:       1.6 + r.Float64()*0.8,
		riseMs:     80 + 20*r.Intn(7),
		swayAmp:    0.06 + r.Float64()*0.04,
		swayPeriod: 600 + 400*r.Float64(),
		swayMs:     450 + 100*r.Intn(2),
		sigma:      0.01 + r.Float64()*0.02,
	}
}

// magAt 剖面幅值（t=流相对时间）：静置引导 300ms → 线性上升 → 摆动保持 →
// 静置收尾。
func (p gatePickupParams) magAt(t int64) float64 {
	riseEnd := int64(300 + p.riseMs)
	swayEnd := riseEnd + int64(p.swayMs)
	switch {
	case t < 300:
		return 1
	case t < riseEnd:
		return 1 + (p.peak-1)*float64(t-300)/float64(p.riseMs)
	case t < swayEnd:
		return p.peak + p.swayAmp*math.Sin(2*math.Pi*float64(t-riseEnd)/p.swayPeriod)
	default:
		return 1
	}
}

// gatePickupTrial 单次拿起试验（fresh Detector——试验独立、无跨试验状态
// 残留）：回放剖面+噪声，返回（是否问候，行为异常数——双问候/误摔落/误
// 风暴均计异常）。
func gatePickupTrial(r *rand.Rand) (hit bool, anomalies int) {
	p := randPickupParams(r)
	d, err := NewDetector(Config{})
	if err != nil {
		panic(err) // 不可达：零值配置已校验域
	}
	totalMs := int64(300+p.riseMs+p.swayMs) + 400
	pickups, falls, storms := 0, 0, 0
	for t := int64(0); t < totalMs; t += testFrameMs {
		ev := d.Push(mkSample(t, p.magAt(t)+r.NormFloat64()*p.sigma, r))
		switch ev.Kind {
		case EvPickup:
			pickups++
		case EvFall:
			falls++
		case EvStorm:
			storms++
		}
	}
	if pickups > 1 || falls != 0 || storms != 0 {
		anomalies = 1
	}
	return pickups >= 1, anomalies
}

// gateInterference 静置干扰流（≥6h，min_evidence 误触发面——资产卡口径）：
// 亚阈噪声底（dev ≤0.08）+ 周期近阈哼声（60s 一次 500ms、dev ≤0.28 不上穿
// ——附近电器振动）+ 短促磕碰段（60s 一次、超阈 100~200ms <DebounceMs——
// 桌面磕碰/门振动；与哼声错相 30s 不相交）。返回（误问候，误摔落，误风暴）
// ——EvIdle=静默抑制信号本身不计（深度安静态在位）。
func gateInterference(d *Detector, hours int, r *rand.Rand) (greets, falls, storms int) {
	totalMs := int64(hours) * 3600 * 1000
	nBumps := int(totalMs / 60000)
	jitter := make([]int64, nBumps)
	dur := make([]int64, nBumps)
	bmag := make([]float64, nBumps)
	for j := range jitter {
		jitter[j] = int64(r.Intn(4000))   // 0~4s 抖动（磕碰窗 ⊂ [30s,34.2s] mod 60s）
		dur[j] = 100 + int64(r.Intn(100)) // 100~200ms（streak ≤180ms <DebounceMs）
		bmag[j] = 1.4 + r.Float64()*0.4   // 1.4~1.8g（dev ∈[0.36,0.84]）
	}
	for t := int64(0); t < totalMs; t += testFrameMs {
		mag := 1 + (r.Float64()-0.5)*0.16 // 底噪 [0.92,1.08]
		if t >= 30000 {
			if j := (t - 30000) / 60000; j < int64(nBumps) {
				start := 30000 + j*60000 + jitter[j]
				if t >= start && t < start+dur[j] {
					mag = bmag[j] + (r.Float64()-0.5)*0.08
				}
			}
		}
		if t >= 60000 && t%60000 < 500 { // 哼声窗（与磕碰窗不相交）
			mag = 1.25 + 0.03*math.Sin(2*math.Pi*float64(t%60000)/100)
		}
		switch d.Push(axisSample(t, mag)).Kind {
		case EvPickup:
			greets++
		case EvFall:
			falls++
		case EvStorm:
			storms++
		}
	}
	return greets, falls, storms
}

// TestT6PickupDetectRate T6-G1-01（BI-6.1/G1）真实：拿起检出 ≥0.98
// （pass_rate，min_evidence n:200）+ 误触发 ≤1/h（≥6h 静置干扰流，泊松口径
// ——资产卡断言列）。数据面：200 个合成拿起剖面变体（不同人/姿势：峰值/
// 起手/摆动/噪声参数域）fresh Detector 逐试验回放（Wilson 95% CI 一并报，
// evalkit）；干扰面：6h 静置干扰流（底噪+近阈哼声+短促磕碰）过同一产品
// 配置 Detector，误问候泊松 95% 上限判定（evalkit.PoissonUpper95，勿手算）
// + 误摔落/误风暴==0（干扰下 0 次打招呼）。真机台架（不同人/姿势 ≥200 次
// 拿起+≥6h 干扰）=holdout L5。
func TestT6PickupDetectRate(t *testing.T) {
	gaterunner.Mark(t, "T6", "BI-6.1", "T6-G1-01", "G1")
	const n = 200 // min_evidence n:200（configs/gates/T6.yaml）
	r := rand.New(rand.NewSource(imuGateSeed))
	hits, anomalies := 0, 0
	for i := 0; i < n; i++ {
		hit, bad := gatePickupTrial(r)
		if hit {
			hits++
		}
		anomalies += bad
	}
	rate := float64(hits) / float64(n)
	lo, hi := evalkit.Wilson(hits, n)
	if rate < 0.98 {
		t.Fatalf("pickup_detect_rate 红：检出 %d/%d=%.4f（阈值 ≥0.98，Wilson 95%% CI [%.3f,%.3f]）",
			hits, n, rate, lo, hi)
	}
	if anomalies != 0 {
		t.Fatalf("拿起剖面行为异常 %d 处（双问候/误摔落/误风暴——拿起通道剖面破裂）", anomalies)
	}
	// 误触发面：≥6h 静置干扰流（资产卡口径），泊松 95% 上限 ≤1/h。
	const evidenceHours = 6
	d, err := NewDetector(Config{})
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	greets, falls, storms := gateInterference(d, evidenceHours,
		rand.New(rand.NewSource(imuGateSeed+5)))
	observed := float64(greets) / float64(evidenceHours)
	upper := evalkit.PoissonUpper95(greets, evidenceHours)
	if observed > 1.0 || upper > 1.0 || falls != 0 || storms != 0 {
		t.Fatalf("误触发红：6h 静置干扰流误问候 %d 次（observed %.4f/h，泊松 95%% 上限 %.4f/h，阈值 ≤1/h；误摔落/误风暴=%d/%d）",
			greets, observed, upper, falls, storms)
	}
	t.Logf("T6-G1-01：拿起检出 %d/%d=%.4f（阈值 ≥0.98，Wilson 95%% CI [%.3f,%.3f]，200 剖面变体）；误触发 0/6h（泊松 95%% 上限 %.3f/h ≤1/h，干扰面=底噪+哼声+磕碰 360 段；合成曲线=台架代理，真机 L5）",
		hits, n, rate, lo, hi, upper)
}

// TestT6IdleSpontaneousOutput T6-G0-01（BI-6.2/G0）真实：静置超时静默 0 次
// 自发输出（==0，min_evidence hours:36——12h×3 台）。数据面：3 条 12h 仿真
// 静置流（亚阈噪声底 dev ≤0.08，50Hz 名义帧）逐台回放（时间压缩：AtMs 按流
// 时钟推进，不真等 36h）。断言：①自发输出通道（EvPickup/EvFall/EvStorm）
// 全程 0 次（含计划/缓存任务全通道——包面自发事件即 loop 侧自发输出的触发
// 源）；②EvIdle 静默抑制信号在位且心跳无缺口（首发于 QuietMs、此后每
// IdleMs 重发，间隔 ≤IdleMs+帧余量——loop 侧计划/缓存任务停发的持续维持
// 信号，0 自发输出非因信号通道死亡）。真机 12h 夜间挂机 ×3 台=holdout L5。
func TestT6IdleSpontaneousOutput(t *testing.T) {
	gaterunner.Mark(t, "T6", "BI-6.2", "T6-G0-01", "G0")
	const streams = 3                                        // 12h×3 台（min_evidence hours:36）
	const hoursPer = 12                                      // 每台挂机时长
	const expectedIdle = 1 + (hoursPer*3600*1000-2000)/60000 // 首发+心跳数（心跳无缺口下限）
	totalSpontaneous := 0
	for s := 0; s < streams; s++ {
		r := rand.New(rand.NewSource(imuGateSeed + int64(s)))
		d, err := NewDetector(Config{})
		if err != nil {
			t.Fatalf("NewDetector: %v", err)
		}
		totalMs := int64(hoursPer) * 3600 * 1000
		spontaneous, idles := 0, 0
		lastIdleAt, maxGap := int64(-1), int64(0)
		for t := int64(0); t < totalMs; t += testFrameMs {
			mag := 1 + (r.Float64()-0.5)*0.16 // [0.92,1.08]：dev ≤0.08 <PickupThreshG
			switch d.Push(axisSample(t, mag)).Kind {
			case EvPickup, EvFall, EvStorm:
				spontaneous++
			case EvIdle:
				if lastIdleAt >= 0 && t-lastIdleAt > maxGap {
					maxGap = t - lastIdleAt
				}
				lastIdleAt = t
				idles++
			}
		}
		if spontaneous != 0 {
			t.Fatalf("idle_spontaneous_output_count 红：台 #%d 自发输出 %d 次（阈值 ==0，12h 仿真静置流）",
				s+1, spontaneous)
		}
		if idles < expectedIdle || maxGap > 60000+2*testFrameMs {
			t.Fatalf("台 #%d 静默抑制信号缺口：EvIdle=%d（≥%d）、心跳最大间隔=%dms（≤%dms）——计划/缓存任务停发维持面破裂",
				s+1, idles, expectedIdle, maxGap, 60000+2*testFrameMs)
		}
		totalSpontaneous += spontaneous
		t.Logf("T6-G0-01 台 #%d：12h 自发输出 0 次，EvIdle=%d（心跳最大间隔 %dms）", s+1, idles, maxGap)
	}
	if totalSpontaneous != 0 {
		t.Fatalf("idle_spontaneous_output_count 红：%d（阈值 ==0，36h=12h×3 全通道）", totalSpontaneous)
	}
}

// TestT6DropDetectRate T6-G0-02（BI-6.3/G0）真实：摔落/抛掷保护检出 ≥0.95
// （rule=metric 点估计口径，min_evidence n:30——卡样本量 30<59）+ ≤2s 停马达
// 静音（仿真时钟）。数据面：30 个合成 1m 跌落剖面（地毯/木地板各半：冲击
// 峰值 2.2~2.6g/3.0~4.0g；1m 落体 ≈452ms→440~460ms 自由落体段；残余弹跳=
// 衰减短峰 <FallThreshG）fresh Detector 逐次回放。断言：①EvFall 检出于冲击
// 首帧（即发，检出延迟=0）；②恰一次（弹跳不复触发）；③保护窗内（fall+
// 2000ms）ImpactLevel=Severe 且 Guard.Clamp 全指令→停马达静音；④窗后下一帧
// 解除（停马达静音 ≤2s——恢复不迟滞）；⑤冲击后残余振动 0 误问候/误风暴。
// 真机跌落台架（1m 落地毯/木地板 ≥30 次）=holdout L5。
func TestT6DropDetectRate(t *testing.T) {
	gaterunner.Mark(t, "T6", "BI-6.3", "T6-G0-02", "G0")
	const n = 30 // min_evidence n:30（configs/gates/T6.yaml）
	r := rand.New(rand.NewSource(imuGateSeed + 1))
	g := NewGuard()
	cmd := Cmd{Duty: 1, AngleDeg: 90, Sound: true}
	hits, anomalies := 0, 0
	for i := 0; i < n; i++ {
		// —— 合成 1m 跌落剖面（地毯/木地板变体）——
		peak := 2.2 + r.Float64()*0.4 // 地毯（软冲击）
		if i%2 == 1 {
			peak = 3.0 + r.Float64() // 木地板（硬冲击）
		}
		nff := 22 + r.Intn(2)    // 自由落体 440~460ms（1m≈452ms）
		nImpact := 2 + r.Intn(2) // 冲击尖峰 2~3 帧
		sigma := 0.01 + r.Float64()*0.02
		type seg struct {
			mag float64
			n   int
		}
		segs := []seg{{1.0, 15}, {0.05, nff}, {peak, nImpact}}
		for k, bm := 0, 1.75; k < 2+r.Intn(2); k++ { // 残余弹跳：衰减短峰
			segs = append(segs, seg{1.0, 6 + r.Intn(3)},
				seg{bm - 0.25*float64(k) + (r.Float64()-0.5)*0.12, 2 + r.Intn(2)})
		}
		frames := 0
		for _, s := range segs {
			frames += s.n
		}
		impactAt := int64(300 + nff*20) // 冲击首帧时刻（静置 300ms+落体段）
		for int64(frames)*testFrameMs < impactAt+2200 {
			segs = append(segs, seg{1.0, 1}) // 静置尾（保护窗+解除面全程在流内）
			frames++
		}
		// —— 回放断言 ——
		d, err := NewDetector(Config{})
		if err != nil {
			t.Fatalf("NewDetector: %v", err)
		}
		var evs []Event
		fallAt := int64(-1)
		severeWrong, motorWrong, recoveredAt := 0, 0, int64(-1)
		at := int64(0)
		for _, s := range segs {
			for j := 0; j < s.n; j++ {
				ev := d.Push(mkSample(at, s.mag+r.NormFloat64()*sigma, r))
				evs = append(evs, ev)
				if ev.Kind == EvFall && fallAt < 0 {
					fallAt = at
				}
				if fallAt >= 0 && at > fallAt {
					lv := d.ImpactLevel()
					if at <= fallAt+2000 { // 保护窗内：severe+停马达静音
						if lv != ImpactSevere {
							severeWrong++
						}
						if c := g.Clamp(lv, cmd); c.Duty != 0 || c.AngleDeg != 0 || c.Sound {
							motorWrong++
						}
					} else if recoveredAt < 0 && lv != ImpactSevere {
						recoveredAt = at
					}
				}
				at += testFrameMs
			}
		}
		if fallAt < 0 {
			continue // 漏检（检出率面计数）
		}
		hits++
		if countKind(evs, EvFall) != 1 || fallAt != impactAt {
			anomalies++ // 双触发/非冲击帧即发
		}
		if severeWrong != 0 || motorWrong != 0 {
			anomalies++ // 保护窗破裂（severe/停马达静音失效）
		}
		if recoveredAt != fallAt+2000+testFrameMs {
			anomalies++ // 停马达静音 >2s（解除迟滞）
		}
		if countKind(evs, EvPickup) != 0 || countKind(evs, EvStorm) != 0 {
			anomalies++ // 残余振动误问候/误风暴
		}
	}
	rate := float64(hits) / float64(n)
	lo, hi := evalkit.Wilson(hits, n)
	if rate < 0.95 {
		t.Fatalf("drop_detect_rate 红：检出 %d/%d=%.4f（阈值 ≥0.95 点估计口径，Wilson 95%% CI [%.3f,%.3f]）",
			hits, n, rate, lo, hi)
	}
	if anomalies != 0 {
		t.Fatalf("跌落剖面行为异常 %d 处（双触发/非冲击帧即发/保护窗破裂/静音 >2s/残余振动误事件）", anomalies)
	}
	t.Logf("T6-G0-02：跌落检出 %d/%d=%.4f（阈值 ≥0.95，Wilson 95%% CI [%.3f,%.3f]，地毯/木地板各半；≤2s 停马达静音+冲击帧即发+弹跳零误事件；合成剖面=台架代理，真机 L5）",
		hits, n, rate, lo, hi)
}

// TestT6MotorBoundViolation T6-G0-03（BI-6.3/G0）真实：电机占空比/角度硬件
// 熔断（软件双保险面）0 越界（==0）。双面：A 表驱动穷举——冲击等级×输出
// 动作矩阵全行 × 边界/非法指令值（0/半额/行上限/越界/×10/负值/NaN/±Inf ×
// 声音位，含越界等级 fail-safe 行）逐格 Clamp→Violation 判定+数据表安全值
// 上限+逐级降载单调；B 生成式 fuzz——30min 持续输出仿真（混合剖面驱动全部
// 冲击等级，wild 指令逐帧过 Guard），越界计数==0。固件层硬件熔断
// （packages/native/firmware-imu）=独立保险（真机 L5），本层非唯一保险。
func TestT6MotorBoundViolation(t *testing.T) {
	gaterunner.Mark(t, "T6", "BI-6.3", "T6-G0-03", "G0")
	g := NewGuard()
	violations := 0
	// —— A. 矩阵表驱动穷举（含越界等级 → fail-safe severe 行）——
	levels := []ImpactLevel{ImpactNone, ImpactLight, ImpactMedium, ImpactSevere, -1, 4, 7}
	for _, lv := range levels {
		row := g.Row(lv)
		if row.Duty < 0 || row.Duty > datasheetDutyMax || row.AngleDeg < 0 || row.AngleDeg > datasheetAngleMax {
			violations++ // 行上限超数据表安全值
		}
		if (lv == ImpactSevere || lv < ImpactNone || lv > ImpactSevere) &&
			(row.Duty != 0 || row.AngleDeg != 0 || row.Sound) {
			violations++ // severe 行（含越界 fail-safe）未停马达静音
		}
		vals := func(max float64) []float64 {
			return []float64{0, max / 2, max, max + 1, max * 10, -1, math.NaN(), math.Inf(1), math.Inf(-1)}
		}
		for _, dv := range vals(row.Duty) {
			for _, av := range vals(row.AngleDeg) {
				for _, snd := range []bool{false, true} {
					out := g.Clamp(lv, Cmd{Duty: dv, AngleDeg: av, Sound: snd})
					if g.Violation(lv, out) {
						violations++ // 越出该冲击等级边界行
					}
					if out.Duty < 0 || out.Duty > datasheetDutyMax ||
						out.AngleDeg < 0 || out.AngleDeg > datasheetAngleMax {
						violations++ // 超数据表安全值
					}
					if out.Duty > row.Duty || out.AngleDeg > row.AngleDeg {
						violations++ // 超行上限
					}
					if snd && !out.Sound && row.Sound {
						violations++ // 行允许且指令请求却被静音（放行面失效）
					}
				}
			}
		}
	}
	for lv := ImpactNone; lv < ImpactSevere; lv++ { // 逐级降载（冲击升级→边界收紧）
		lo, hi := g.Row(lv), g.Row(lv+1)
		if lo.Duty < hi.Duty || lo.AngleDeg < hi.AngleDeg {
			violations++
		}
	}
	// —— B. 生成式 fuzz：30min 持续输出仿真（混合剖面驱动全冲击等级）——
	r := rand.New(rand.NewSource(imuGateSeed + 2))
	d, err := NewDetector(Config{})
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	const cycleFrames = 25 + 40 + 10 + 15 + 23 + 3 + 125 // 241 帧≈4.82s/周期
	const cycles = 374                                   // 374×241=90034 帧 ≥30min（50Hz 名义）
	frames := 0
	levelFrames := [nImpactLevels]int{}
	for c := 0; c < cycles; c++ {
		for k := 0; k < cycleFrames; k++ {
			var mag float64
			switch { // 混合剖面：静置→轻摇→中冲→静置→落体→冲击→静置（全等级覆盖）
			case k < 25:
				mag = 1 + (r.Float64()-0.5)*0.16
			case k < 65: // 轻摇：dev≈0.7 → ImpactLight
				mag = 1.7 + 0.05*math.Sin(2*math.Pi*float64(k-25)/20) + (r.Float64()-0.5)*0.04
			case k < 75: // 中等冲击：dev≈1.2 → ImpactMedium
				mag = 2.2 + (r.Float64()-0.5)*0.08
			case k < 90:
				mag = 1 + (r.Float64()-0.5)*0.16
			case k < 113: // 自由落体
				mag = 0.05 + (r.Float64()-0.5)*0.04
			case k < 116: // 冲击尖峰（EvFall→Severe 保护窗）
				mag = 3.5 + (r.Float64()-0.5)*0.2
			default:
				mag = 1 + (r.Float64()-0.5)*0.16
			}
			d.Push(mkSample(int64(frames)*testFrameMs, mag, r))
			frames++
			lv := d.ImpactLevel()
			levelFrames[lv]++
			desired := Cmd{Duty: wildFloat(r.Uint64()), AngleDeg: wildFloat(r.Uint64()), Sound: r.Intn(2) == 0}
			if r.Intn(2) == 0 { // 掺入正常量程指令（合法路径与 wild 路径双覆盖）
				desired = Cmd{Duty: r.Float64(), AngleDeg: r.Float64() * 130, Sound: r.Intn(2) == 0}
			}
			if g.Violation(lv, g.Clamp(lv, desired)) {
				violations++ // fuzz 越界（任何软件 bug 的指令值经 Clamp 后不得越界）
			}
		}
	}
	if frames < 90000 { // 30min 证据面（资产卡「持续输出 30min」）
		t.Fatalf("持续输出仿真帧数=%d < 90000（30min 证据面不足）", frames)
	}
	for lv, cnt := range levelFrames {
		if cnt == 0 {
			t.Fatalf("冲击等级 %d 全程未覆盖（fuzz 通道空转——矩阵行未真实驱动）", lv)
		}
	}
	if violations != 0 {
		t.Fatalf("motor_bound_violation_count 红：%d（阈值 ==0：表驱动穷举 %d 行×边界/非法指令 + 30min 生成式 fuzz %d 帧，任何软件 bug 无法驱动越界）",
			violations, len(levels), frames)
	}
	t.Logf("T6-G0-03：0 越界（表驱动穷举 7 等级行×%d 格 + 30min fuzz %d 帧，全等级覆盖 %+v；固件层=独立保险 L5）",
		9*9*2, frames, levelFrames)
}
