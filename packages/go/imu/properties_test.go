// T6 属性测试（m3-spec §6 属性行 + docs/gates/assets/T6.md，testing/quick）：
//
//	P1 幅值单调（合成加速度幅值单调增→活动得分/剧烈置信度单调不降）
//	P2 边界盒 fuzz（任意输入输出指令在边界盒内——Guard.Clamp 后 Violation 恒 false）
//	P3 时移/重采样（事件序列整体时移/重采样检出集合不变）
//	P4 静置零自发（静置流 0 自发输出——非 EvIdle 事件为零）
//	P5 确定性（同曲线同事件——重放逐字段全等）
//
// 时间基状态机（DebounceMs/QuietMs 等以 ms 计）= P3 重采样不变性的实现面。
package imu

import (
	"math"
	"math/rand"
	"testing"
	"testing/quick"
)

// rampStream 单调增幅值流：mag 从 1 线性升至 endMag（帧周期 testFrameMs）。
func rampStream(endMag float64, frames int) []Sample {
	ss := make([]Sample, frames)
	for i := range ss {
		mag := 1 + (endMag-1)*float64(i)/float64(frames-1)
		ss[i] = axisSample(int64(i)*testFrameMs, mag)
	}
	return ss
}

// TestPropertyAmplitudeMonotonic P1：幅值单调增→Activity/Violence 单调不降
// （观测窗滑动均值/峰值对单调序列单调——幅值单调属性的实现面）。
func TestPropertyAmplitudeMonotonic(t *testing.T) {
	prop := func(endPct uint8, durRaw uint16) bool {
		endMag := 1 + (float64(endPct)+1)/256*3 // (1, 4]：单调增至超平静带
		frames := 10 + int(durRaw)%190          // 10~199 帧（200ms~4s）
		d, err := NewDetector(Config{})
		if err != nil {
			t.Logf("P1 NewDetector: %v", err)
			return false
		}
		prevA, prevV := -1.0, -1.0
		for _, s := range rampStream(endMag, frames) {
			d.Push(s)
			a, v := d.Activity(), d.Violence()
			if a < prevA-1e-12 || v < prevV-1e-12 {
				t.Logf("P1 单调失效：Activity %.4f→%.4f / Violence %.4f→%.4f", prevA, a, prevV, v)
				return false
			}
			prevA, prevV = a, v
		}
		return prevV > 0 // 锚点：终点幅值偏离>0 → Violence 非零（通道非空转）
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 150}); err != nil {
		t.Errorf("P1 幅值单调失效: %v", err)
	}
}

// wildFloat 生成式 fuzz 值域：NaN/±Inf/负值/大正值（覆盖软件 bug 可能产出的
// 任意指令值——P2 的输入面）。
func wildFloat(bits uint64) float64 {
	switch bits % 5 {
	case 0:
		return math.NaN()
	case 1:
		return math.Inf(1)
	case 2:
		return math.Inf(-1)
	case 3:
		return -float64(bits%1000) - 0.5
	default:
		return float64(bits%100000) / 7 // [0, ~14285] 越界正值
	}
}

// TestPropertyGuardBoundBox P2：任意输入（含非法等级/NaN/±Inf/越界值）下
// Guard.Clamp 输出恒在边界盒内（Violation==false 且不超数据表安全值；severe
// 行恒停马达静音）——任何软件 bug 无法驱动越界（T6-G0-03 属性面）。
func TestPropertyGuardBoundBox(t *testing.T) {
	prop := func(lvRaw uint8, dutyBits, angleBits uint64, sound bool) bool {
		lv := ImpactLevel(lvRaw % 8) // 0..3 合法 + 4..7 越界（fail-safe 面）
		g := NewGuard()
		out := g.Clamp(lv, Cmd{Duty: wildFloat(dutyBits), AngleDeg: wildFloat(angleBits), Sound: sound})
		if g.Violation(lv, out) {
			t.Logf("P2 越界：lv=%d out=%+v", lv, out)
			return false
		}
		if out.Duty < 0 || out.Duty > datasheetDutyMax || out.AngleDeg < 0 || out.AngleDeg > datasheetAngleMax {
			t.Logf("P2 超数据表安全值：lv=%d out=%+v", lv, out)
			return false
		}
		if lv == ImpactSevere && (out.Duty != 0 || out.AngleDeg != 0 || out.Sound) {
			t.Logf("P2 severe 行未停马达静音：%+v", out)
			return false
		}
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("P2 边界盒 fuzz 失效: %v", err)
	}
}

// p3Curve P3 共用连续曲线：静置 600ms → 运动 700ms（1.6g）→ 静置 2900ms
// （含一次拿起检出 + 运动后静置 EvIdle——两通道锚点）。
func p3Curve(t int64) float64 {
	switch {
	case t < 600:
		return 1.0
	case t < 1300:
		return 1.6
	default:
		return 1.0
	}
}

// sampleCurve 按帧周期采样连续曲线（offset=整体时移量——曲线按流相对时间
// 取值、仅 AtMs 平移；静置姿态固定——检测只依赖 ‖a‖）。
func sampleCurve(frameMs int, offset int64) []Sample {
	const total = int64(4200)
	var ss []Sample
	for t := int64(0); t < total; t += int64(frameMs) {
		ss = append(ss, axisSample(t+offset, p3Curve(t)))
	}
	return ss
}

// kindSeq 事件种类序列（检出集合口径——时移/重采样不变性的比较面）。
func kindSeq(evs []Event) []EventKind {
	ks := make([]EventKind, len(evs))
	for i, e := range evs {
		ks[i] = e.Kind
	}
	return ks
}

// detected 非 EvNone 事件序列（检出集合——重采样改变帧数，检出集合不变）。
func detected(evs []Event) []Event {
	var out []Event
	for _, e := range evs {
		if e.Kind != EvNone {
			out = append(out, e)
		}
	}
	return out
}

// TestPropertyTimeShiftResample P3：事件序列整体时移（AtMs+Δ → 事件恰移 Δ、
// 种类序列不变）/重采样（帧周期 10~50ms → 检出集合不变）。时间基状态机的
// 设计约束（帧周期非帧计数计 debounce/quiet）。
func TestPropertyTimeShiftResample(t *testing.T) {
	base := func() []Event {
		d, _ := NewDetector(Config{})
		return pushAll(d, sampleCurve(testFrameMs, 0))
	}
	// 锚点：基准流必须 1 次 EvPickup + ≥1 次 EvIdle（属性非空转）。
	b := base()
	if countKind(b, EvPickup) != 1 || countKind(b, EvIdle) < 1 {
		t.Fatalf("P3 基准流锚点失效：pickup=%d idle=%d（通道空转）",
			countKind(b, EvPickup), countKind(b, EvIdle))
	}
	prop := func(shiftRaw uint32, periodRaw uint8) bool {
		shift := int64(shiftRaw) % 5000 // [0, 5s) 整体时移
		periods := []int{10, 20, 25, 40, 50}
		p := periods[int(periodRaw)%len(periods)]
		// 时移：同帧周期，AtMs+Δ → 种类序列不变 + 事件时刻恰移 Δ。
		d, _ := NewDetector(Config{})
		shifted := pushAll(d, sampleCurve(testFrameMs, shift))
		if len(shifted) != len(b) {
			t.Logf("P3 时移流长漂移：%d vs %d", len(shifted), len(b))
			return false
		}
		for i := range b {
			if shifted[i].Kind != b[i].Kind || shifted[i].AtMs != b[i].AtMs+shift {
				t.Logf("P3 时移失效：#%d %+v vs %+v+Δ%d", i, shifted[i], b[i], shift)
				return false
			}
		}
		// 重采样：帧周期 p → 检出集合（非 EvNone 种类序列）不变。
		d, _ = NewDetector(Config{})
		resampled := pushAll(d, sampleCurve(p, 0))
		ksB, ksR := kindSeq(detected(b)), kindSeq(detected(resampled))
		if len(ksB) != len(ksR) {
			t.Logf("P3 重采样检出集合漂移：%d vs %d（周期 %dms）", len(ksR), len(ksB), p)
			return false
		}
		for i := range ksB {
			if ksB[i] != ksR[i] {
				t.Logf("P3 重采样失效：#%d %v vs %v（周期 %dms）", i, ksB[i], ksR[i], p)
				return false
			}
		}
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 150}); err != nil {
		t.Errorf("P3 时移/重采样不变性失效: %v", err)
	}
}

// TestPropertyQuietZeroSpontaneous P4：静置流（亚阈值噪声）0 自发输出——
// 拿起/摔落/风暴事件为零（EvIdle=抑制信号本身不计）；抑制信号在位（≥1 次
// EvIdle——深度安静态通道非空转）。
func TestPropertyQuietZeroSpontaneous(t *testing.T) {
	prop := func(seed int64, noisePct uint8, durRaw uint16) bool {
		if seed < 0 {
			seed = -seed
		}
		r := rand.New(rand.NewSource(seed))
		frames := 160 + int(durRaw)%140 // 3.2s~6s（≥QuietMs+心跳余量）
		d, err := NewDetector(Config{})
		if err != nil {
			t.Logf("P4 NewDetector: %v", err)
			return false
		}
		spontaneous, idles := 0, 0
		for i := 0; i < frames; i++ {
			mag := 1 + (r.Float64()-0.5)*0.24 // [0.88, 1.12]：dev ≤0.12 <PickupThreshG
			switch d.Push(axisSample(int64(i)*testFrameMs, mag)).Kind {
			case EvPickup, EvFall, EvStorm:
				spontaneous++
			case EvIdle:
				idles++
			}
		}
		return spontaneous == 0 && idles >= 1
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 150}); err != nil {
		t.Errorf("P4 静置零自发出错: %v", err)
	}
}

// TestPropertyDeterminism P5：同曲线同事件——随机流（含自由落体/冲击/振荡
// 混合剖面）重放两次，事件序列逐字段全等（含 Conf 浮点位级一致）。
func TestPropertyDeterminism(t *testing.T) {
	prop := func(seed int64) bool {
		if seed < 0 {
			seed = -seed
		}
		r := rand.New(rand.NewSource(seed))
		const n = 300
		ss := make([]Sample, n)
		at := int64(0)
		for i := range ss {
			at += 5 + int64(r.Intn(46))            // 5~50ms 帧距（含重采样剖面）
			ss[i] = mkSample(at, r.Float64()*4, r) // [0,4]g 全域混合
		}
		run := func() []Event {
			d, err := NewDetector(Config{})
			if err != nil {
				t.Fatalf("P5 NewDetector: %v", err)
			}
			return pushAll(d, ss)
		}
		a, b := run(), run()
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				t.Logf("P5 确定性失效：#%d %+v vs %+v", i, a[i], b[i])
				return false
			}
		}
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 120}); err != nil {
		t.Errorf("P5 确定性失效: %v", err)
	}
}
