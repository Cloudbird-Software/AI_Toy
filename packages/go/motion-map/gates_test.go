// T12 门禁测试（m2-spec §10 Mark 接线策略表，IR #92）：一 ID 一顶层测试函数，
// 口径与样本量声明唯一来源 configs/gates/T12.yaml（本文件只落断言本体）。
// verdict 总表：G0-01/G1-02/G1-03/G0-02 真实；G1-01 debt（真机 3 台×24h 日志，
// 调度逻辑面已由仿真时钟真实断言——KWS 同款「先真跑逻辑面再 Skipf 数据面」）。
// 跨包联跑（T12-G1-02 情绪事件回放）只在测试侧 import emotion（包实现零
// import，考卷隔离不破——m2-spec §1）。
package motionmap

import (
	"math"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/Cloudbird-Software/AI_Toy/packages/go/emotion"
	"github.com/Cloudbird-Software/AI_Toy/tools/evalkit/evalkit"
	"github.com/Cloudbird-Software/AI_Toy/tools/gaterunner/gaterunner"
)

// gateLabels 儿童 9 类标签全集（与 emotion.DefaultConfig 标签带同源）。
var gateLabels = []string{"excited", "happy", "content", "calm", "sleepy", "sad", "scared", "angry", "surprised"}

// gateDirectionTable 方向表 fixture（T12-G1-02 人工口径=动作库设计文档方向类：
// 正高/正低/负高/负低——独立于 DefaultTable 的行内容，一致率=映射管线对方向
// 表的对齐度）。标签→允许动作 ID 集。
var gateDirectionTable = map[string]map[string]bool{
	"excited":   {"bounce": true, "wiggle": true, "smile": true, "nod": true},
	"happy":     {"smile": true, "sway": true, "bounce": true, "tilt": true},
	"content":   {"sway": true, "soft": true, "tilt": true, "nod": true},
	"calm":      {"sway": true, "settle": true, "soft": true},
	"sleepy":    {"droop": true, "settle": true, "lids": true},
	"sad":       {"down": true, "droop": true, "frown": true},
	"scared":    {"shrink": true, "still": true, "low": true, "wide": true},
	"angry":     {"stomp": true, "shake": true, "frown": true, "glare": true},
	"surprised": {"back": true, "wide": true, "jolt": true, "still": true},
}

// gateMoodIntensity 情绪状态→Mood 强度的 loop 侧搬运预演（M2 接线归 loop，
// 测试侧先行）：主导偏差 ×2 归一 [0,1]。
func gateMoodIntensity(s emotion.State) float64 {
	d := math.Max(math.Abs(s.Valence-0.5), math.Abs(s.Arousal-0.5))
	return 2 * math.Max(d, math.Abs(s.Closeness-0.5))
}

// gateStressTable 压力配置（T12-G0-01 全动作组合扫描面）：每标签行=全动作库
// 满幅度（单动作合规、叠加瞬时超限的对抗设计）。
func gateStressTable() Table {
	row := []Action{
		{ID: "nod", Amp: 40, Group: "head"}, {ID: "shake", Amp: 35, Group: "head"},
		{ID: "smile", Amp: 40, Group: "face"}, {ID: "frown", Amp: 38, Group: "face"},
		{ID: "bounce", Amp: 42, Group: "body"}, {ID: "stomp", Amp: 40, Group: "body"},
	}
	tab := Table{}
	for _, label := range gateLabels {
		tab[label] = append([]Action{}, row...)
	}
	return tab
}

// gateStressLimits 压力安全盒：紧组上限+紧全局和+跨组互斥（head×face）——
// 配额缩放与互斥淘汰双路径全激活。
func gateStressLimits() Limits {
	return Limits{
		GroupDuty:    map[string]uint8{"head": 20, "face": 20, "body": 25},
		MutexGroups:  [][]string{{"head", "face"}, {"body"}},
		GlobalAmpSum: 30,
	}
}

func gateRandLabel(r *rand.Rand) string {
	if r.Intn(4) == 0 { // 垃圾/未知标签面（含非 UTF-8 字节串）
		b := make([]byte, 1+r.Intn(6))
		for k := range b {
			b[k] = byte(1 + r.Intn(255))
		}
		return string(b)
	}
	pool := append([]string{}, gateLabels...)
	pool = append(pool, "???", "HAPPY", "")
	return pool[r.Intn(len(pool))]
}

func gateRandIntensity(r *rand.Rand) float64 {
	switch r.Intn(8) {
	case 0:
		return math.NaN()
	case 1:
		return math.Inf(1)
	case 2:
		return math.Inf(-1)
	case 3:
		return -0.7
	case 4:
		return 7.3
	default:
		return r.Float64()
	}
}

// TestT12G001SafetyBox T12-G0-01（BI-12.3/G0，真实）：物理安全全枚举——仿真层
// 全情绪×全动作组合扫描（9 标签×21 强度格×10 seed 的缺省面 + 压力配置面）+
// fuzz（2000 随机 Mood：垃圾标签/NaN/±Inf/越界强度）安全盒 0 越界（字段合法、
// 每组恰一、互斥集内恰一、组内 Σ ≤ GroupDuty、Σ ≤ GlobalAmpSum）。真机 50 组
// 极端组合归 M3 真机仪式（m2-spec §10——本测即仿真层全枚举面）。
func TestT12G001SafetyBox(t *testing.T) {
	gaterunner.Mark(t, "T12", "BI-12.3", "T12-G0-01", "G0")
	violations := 0
	m, err := NewMapper(DefaultTable(), DefaultLimits())
	if err != nil {
		t.Fatalf("NewMapper(Default): %v", err)
	}
	sm, err := NewMapper(gateStressTable(), gateStressLimits())
	if err != nil {
		t.Fatalf("NewMapper(stress): %v", err)
	}
	// 面 1：缺省配置全情绪×强度格×seed 枚举
	for _, label := range gateLabels {
		for k := 0; k <= 20; k++ {
			for seed := int64(0); seed < 10; seed++ {
				i := float64(k) / 20
				if !inBox(m.Map(Mood{Label: label, Intensity: i}, false, seed), DefaultLimits()) {
					violations++
				}
			}
		}
	}
	// 面 2：压力配置（全动作库×满幅度×紧盒×跨组互斥）同枚举
	for _, label := range gateLabels {
		for k := 0; k <= 20; k++ {
			for seed := int64(0); seed < 10; seed++ {
				i := float64(k) / 20
				if !inBox(sm.Map(Mood{Label: label, Intensity: i}, false, seed), gateStressLimits()) {
					violations++
				}
			}
		}
	}
	// 面 3：fuzz（固定种子确定性）——两配置各 2000 随机 Mood
	r := rand.New(rand.NewSource(92))
	for k := 0; k < 2000; k++ {
		mood := Mood{Label: gateRandLabel(r), Intensity: gateRandIntensity(r)}
		seed := r.Int63()
		if !inBox(m.Map(mood, false, seed), DefaultLimits()) {
			violations++
		}
		if !inBox(sm.Map(mood, false, seed), gateStressLimits()) {
			violations++
		}
	}
	if violations != 0 {
		t.Fatalf("motion_safety_violation_count=%d ≠ 0（安全盒被突破：字段/每组一/互斥一/组上限/全局上限）", violations)
	}
}

// TestT12G101IdleContinuity T12-G1-01（BI-12.2/G1，debt）：idle 微动作连续性
// 最大静止间隔 ≤90s。逻辑面（真实）：仿真时钟 24h@1s×3 档 seed 走通间隔统计
// 通道并断言 ≤90s；数据面：真机 3 台×24h 日志（yaml min_evidence n:3）未建。
func TestT12G101IdleContinuity(t *testing.T) {
	gaterunner.Mark(t, "T12", "BI-12.2", "T12-G1-01", "G1")
	const totalMs, stepMs, maxGapMs = 24 * 3600_000, 1000, 90_000
	maxGap := int64(0)
	for _, seed := range []int64{1, 2, 3} {
		m, err := NewMapper(DefaultTable(), DefaultLimits())
		if err != nil {
			t.Fatalf("NewMapper: %v", err)
		}
		last := int64(-1)
		for atMs := int64(0); atMs <= totalMs; atMs += stepMs {
			if out := m.IdleTick(atMs, seed); len(out) > 0 {
				if last >= 0 && atMs-last > maxGap {
					maxGap = atMs - last
				}
				last = atMs
			}
		}
	}
	if maxGap > maxGapMs {
		t.Fatalf("max_idle_gap_s 逻辑面红：仿真 24h×3 seed 最大静止间隔 %dms > 90s", maxGap)
	}
	t.Skipf("T12-G1-01 debt：idle 微动作连续性证据面未建——真机 3 台×24h 日志（yaml min_evidence n:3，suite=nightly）需硬件接入；当前仿真时钟 24h@1s×3 seed 已真实断言最大静止间隔 %dms ≤ 90s（呼吸恒在+眨眼窗调度的构造保证）。M3 真机接入后以设备日志替换仿真扫描并去掉本 Skip。", maxGap)
}

// TestT12G102EmotionMotionConsistency T12-G1-02（BI-12.1/G1，真实）：300 情绪
// 事件回放（10 类×30 强度阶梯，固定确定性 fixture）经 emotion 引擎（T7 联跑，
// 测试侧 import）→状态→Mood→Map，同向动作一致率 ≥90%（方向表 fixture 判定：
// 输出动作 ID 全在该标签方向类内）。统计面走 evalkit Wilson 95%CI。
func TestT12G102EmotionMotionConsistency(t *testing.T) {
	gaterunner.Mark(t, "T12", "BI-12.1", "T12-G1-02", "G1")
	e, err := emotion.NewEngine(emotion.DefaultConfig())
	if err != nil {
		t.Fatalf("emotion.NewEngine: %v", err)
	}
	m, err := NewMapper(DefaultTable(), DefaultLimits())
	if err != nil {
		t.Fatalf("NewMapper: %v", err)
	}
	kinds := []emotion.Kind{emotion.Praise, emotion.Criticize, emotion.Hug, emotion.ToySnatched,
		emotion.Alone, emotion.Greet, emotion.Play, emotion.Scared, emotion.Soothed, emotion.Bored}
	consistent, total, tMs := 0, 0, int64(0)
	for _, k := range kinds {
		for j := 0; j < 30; j++ {
			i := 0.05 + 0.95*float64(j)/29
			tMs += 30_000 // 事件间隔 30s（会话节律，含衰减交错）
			e.DecayTo(tMs)
			s := e.OnEvent(emotion.Event{K: k, Intensity: i})
			mood := Mood{Label: s.Label, Intensity: gateMoodIntensity(s)}
			out := m.Map(mood, false, int64(total))
			total++
			ok := len(out) > 0
			for _, a := range out {
				if !gateDirectionTable[s.Label][a.ID] {
					ok = false
				}
			}
			if ok {
				consistent++
			}
		}
	}
	if total != 300 {
		t.Fatalf("样本量 %d ≠ 300（yaml min_evidence n:300）", total)
	}
	rate := float64(consistent) / float64(total)
	lo, hi := evalkit.Wilson(consistent, total)
	if rate < 0.90 {
		t.Fatalf("emotion_motion_consistency_rate=%.4f < 0.90（Wilson 95%%CI [%.4f,%.4f]，n=%d）", rate, lo, hi, total)
	}
	t.Logf("T12-G1-02：一致率 %.4f（一致 %d/%d，Wilson 95%%CI [%.4f,%.4f]）", rate, consistent, total, lo, hi)
}

// TestT12G103FirstVisibleLatency T12-G1-03（BI-12.1/G1，真实）：映射延迟——
// 100 次事件 Map 同步直返（并行旁路不等 TTS：无 IO/无 goroutine/无锁——逻辑
// 口径≈0 的构造保证），CI 墙钟实测 P95（顺序统计量第 95 位）≤200ms；样本随
// suite [ci,nightly] 进 nightly 看板（m2-spec §10），真机面 M3 复测。
func TestT12G103FirstVisibleLatency(t *testing.T) {
	gaterunner.Mark(t, "T12", "BI-12.1", "T12-G1-03", "G1")
	m, err := NewMapper(DefaultTable(), DefaultLimits())
	if err != nil {
		t.Fatalf("NewMapper: %v", err)
	}
	elapsed := make([]float64, 0, 100)
	for k := 0; k < 100; k++ {
		mood := Mood{Label: gateLabels[k%len(gateLabels)], Intensity: 0.05 + 0.95*float64((k/9)%10)/9}
		start := time.Now()
		out := m.Map(mood, false, int64(k))
		dt := time.Since(start).Seconds() * 1000
		if len(out) == 0 {
			t.Fatalf("第 %d 次事件非静默须同步产出可见动作（旁路直返）", k)
		}
		elapsed = append(elapsed, dt)
	}
	sort.Float64s(elapsed)
	p95 := elapsed[94] // 100 样本第 95 位（描述性顺序统计量，非统计推断）
	if p95 > 200 {
		t.Fatalf("motion_first_visible_p95_ms=%.3f > 200（n=100）", p95)
	}
	t.Logf("T12-G1-03：P95=%.4fms（同步直返构造保证逻辑口径≈0；CI 墙钟样本随 nightly suite 复跑进看板）", p95)
}

// TestT12G002SilentMotionZero T12-G0-02（BI-12.3/G0，真实）：静默模式强制——
// 静默态×任意情绪注入（9 标签×极端强度面含 NaN/±Inf/越界/垃圾标签）输出恒
// 空，且静默锁存使 IdleTick 恒空（优先级高于一切映射）。
func TestT12G002SilentMotionZero(t *testing.T) {
	gaterunner.Mark(t, "T12", "BI-12.3", "T12-G0-02", "G0")
	m, err := NewMapper(DefaultTable(), DefaultLimits())
	if err != nil {
		t.Fatalf("NewMapper: %v", err)
	}
	violations := 0
	intensities := []float64{0, 0.5, 1, math.NaN(), math.Inf(1), math.Inf(-1), -3, 9}
	for _, label := range append(append([]string{}, gateLabels...), "", "???", "\x00\xff") {
		for _, i := range intensities {
			for seed := int64(0); seed < 3; seed++ {
				if out := m.Map(Mood{Label: label, Intensity: i}, true, seed); len(out) != 0 {
					violations++
				}
			}
		}
	}
	for atMs := int64(0); atMs < 300_000; atMs += 17_000 {
		for _, seed := range []int64{0, 1, 2} {
			if out := m.IdleTick(atMs, seed); len(out) != 0 {
				violations++
			}
		}
	}
	if violations != 0 {
		t.Fatalf("silent_motion_output_count=%d ≠ 0（静默强制被突破——含 IdleTick 锁存面）", violations)
	}
}
