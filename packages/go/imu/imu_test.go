// T6 单元测试（m3-spec §6 包契约 E：三态状态机+摔落+风暴+Guard 边界盒）。
// 合成加速度口径：检测只依赖总幅值 ‖a‖（玩具姿态任意），方向随机化仅为
// 流真实性；时间基状态机（帧周期=测试常量，50Hz 名义=20ms）。
package imu

import (
	"math"
	"math/rand"
	"strings"
	"testing"
)

const testFrameMs = 20 // 50Hz 名义帧周期

// mkSample 构造样本：总幅值 mag、随机方向（种子化——确定性重放）。
func mkSample(at int64, mag float64, r *rand.Rand) Sample {
	var dx, dy, dz, n float64
	for n < 1e-9 {
		dx, dy, dz = r.NormFloat64(), r.NormFloat64(), r.NormFloat64()
		n = math.Sqrt(dx*dx + dy*dy + dz*dz)
	}
	return Sample{AtMs: at, Ax: dx / n * mag, Ay: dy / n * mag, Az: dz / n * mag}
}

// axisSample 固定姿态样本（长流性能口径：挂机姿态固定，仅幅值噪声——检测
// 只依赖 ‖a‖，方向不影响判定）。
func axisSample(at int64, mag float64) Sample {
	return Sample{AtMs: at, Ax: 0, Ay: 0, Az: mag}
}

// pushAll 推整条样本流，返回事件序列。
func pushAll(d *Detector, ss []Sample) []Event {
	evs := make([]Event, 0, len(ss))
	for _, s := range ss {
		evs = append(evs, d.Push(s))
	}
	return evs
}

// countKind 事件序列中某类事件数。
func countKind(evs []Event, k EventKind) int {
	n := 0
	for _, e := range evs {
		if e.Kind == k {
			n++
		}
	}
	return n
}

// firstKind 首个某类事件（不存在返回零值 Event）。
func firstKind(evs []Event, k EventKind) Event {
	for _, e := range evs {
		if e.Kind == k {
			return e
		}
	}
	return Event{}
}

// TestNewDetectorConfig 配置校验（仅构造面返回 error；零值字段取默认）。
func TestNewDetectorConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"零值全默认", Config{}, ""},
		{"PickupThreshG 负", Config{PickupThreshG: -0.1}, "PickupThreshG"},
		{"PickupThreshG 超上界", Config{PickupThreshG: 5.1}, "PickupThreshG"},
		{"FallThreshG ≤1", Config{FallThreshG: 1.0}, "FallThreshG"},
		{"FallThreshG 超上界", Config{FallThreshG: 16.1}, "FallThreshG"},
		{"DebounceMs 负", Config{DebounceMs: -1}, "DebounceMs"},
		{"QuietMs 负", Config{QuietMs: -1}, "QuietMs"},
		{"IdleMs<QuietMs", Config{IdleMs: 100, QuietMs: 200}, "IdleMs"},
		{"StormPerSec<2", Config{StormPerSec: 1}, "StormPerSec"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewDetector(c.cfg)
			if c.want == "" && err != nil {
				t.Fatalf("合法配置被拒: %v", err)
			}
			if c.want != "" && (err == nil || !contains(err.Error(), c.want)) {
				t.Fatalf("want 错误含 %q, got %v", c.want, err)
			}
		})
	}
	d, err := NewDetector(Config{})
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	if d.cfg.PickupThreshG != defaultPickupThreshG || d.cfg.FallThreshG != defaultFallThreshG ||
		d.cfg.DebounceMs != defaultDebounceMs || d.cfg.QuietMs != defaultQuietMs ||
		d.cfg.IdleMs != defaultIdleMs || d.cfg.StormPerSec != defaultStormPerSec {
		t.Fatalf("零值默认未生效: %+v", d.cfg)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// TestPickupDebounce 拿起检出：阈值+去抖（≥DebounceMs 持续复确认）、活动期
// 一次、静默 ≥DebounceMs 重臂、短促/亚阈值运动不触发。
func TestPickupDebounce(t *testing.T) {
	r := rand.New(rand.NewSource(20260829))
	stream := func(parts ...[2]any) []Event { // {mag, frames} 段拼接
		d, err := NewDetector(Config{})
		if err != nil {
			t.Fatalf("NewDetector: %v", err)
		}
		var ss []Sample
		at := int64(0)
		for _, p := range parts {
			mag := p[0].(float64)
			n := p[1].(int)
			for i := 0; i < n; i++ {
				ss = append(ss, mkSample(at, mag, r))
				at += testFrameMs
			}
		}
		return pushAll(d, ss)
	}
	// 去抖窗语义：静置 1s → 运动 700ms（mag 1.6，dev 0.6 ≥0.3）→ 恰一次
	// EvPickup，时刻 ∈ [运动起点+250, +270]（帧粒度 20ms）。
	evs := stream([2]any{1.0, 50}, [2]any{1.6, 35}, [2]any{1.0, 50})
	if got := countKind(evs, EvPickup); got != 1 {
		t.Fatalf("EvPickup=%d want 1", got)
	}
	if p := firstKind(evs, EvPickup); p.AtMs < 1250 || p.AtMs > 1270 {
		t.Fatalf("EvPickup 时刻=%d want ∈[1250,1270]（去抖 250ms+帧粒度）", p.AtMs)
	}
	// 短促运动（200ms < DebounceMs）不触发。
	if got := countKind(stream([2]any{1.0, 50}, [2]any{1.6, 10}, [2]any{1.0, 50}), EvPickup); got != 0 {
		t.Fatalf("短促运动 EvPickup=%d want 0", got)
	}
	// 亚阈值持续运动（dev 0.2 <0.3）不触发。
	if got := countKind(stream([2]any{1.0, 50}, [2]any{1.2, 50}, [2]any{1.0, 50}), EvPickup); got != 0 {
		t.Fatalf("亚阈值 EvPickup=%d want 0", got)
	}
	// 重臂：拿起→静默 400ms（≥DebounceMs）→再拿起=第二次 EvPickup。
	evs = stream([2]any{1.0, 50}, [2]any{1.6, 20}, [2]any{1.0, 20}, [2]any{1.6, 20}, [2]any{1.0, 50})
	if got := countKind(evs, EvPickup); got != 2 {
		t.Fatalf("重臂后 EvPickup=%d want 2", got)
	}
	// 不重臂：拿起→静默 100ms（<DebounceMs）→再拿起=活动期延续，仍一次。
	evs = stream([2]any{1.0, 50}, [2]any{1.6, 20}, [2]any{1.0, 5}, [2]any{1.6, 20}, [2]any{1.0, 50})
	if got := countKind(evs, EvPickup); got != 1 {
		t.Fatalf("不重臂 EvPickup=%d want 1", got)
	}
}

// TestIdleSuppression 静置超时：QuietMs 无运动→EvIdle（静默抑制信号）；
// 持续静置每 IdleMs 心跳重发；任意运动解除（重新计静置）。
func TestIdleSuppression(t *testing.T) {
	// 默认口径：静置 3s → EvIdle 恰在 QuietMs=2000ms（帧粒度内）。
	d, err := NewDetector(Config{})
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	var evs []Event
	for i := 0; i < 150; i++ {
		evs = append(evs, d.Push(axisSample(int64(i)*testFrameMs, 1.0)))
	}
	if got := countKind(evs, EvIdle); got != 1 {
		t.Fatalf("EvIdle=%d want 1", got)
	}
	if e := firstKind(evs, EvIdle); e.AtMs < 2000 || e.AtMs > 2020 {
		t.Fatalf("EvIdle 时刻=%d want ∈[2000,2020]", e.AtMs)
	}
	// 心跳：QuietMs=500/IdleMs=1000，静置 3.5s → 首发于 500，心跳于 1500/2500。
	d, _ = NewDetector(Config{QuietMs: 500, IdleMs: 1000})
	evs = evs[:0]
	for i := 0; i < 175; i++ {
		evs = append(evs, d.Push(axisSample(int64(i)*testFrameMs, 1.0)))
	}
	if got := countKind(evs, EvIdle); got != 3 {
		t.Fatalf("心跳 EvIdle=%d want 3（首发+2 心跳）", got)
	}
	// 运动解除：静置 1.5s（未超时）→运动 0.7s→静置 2.5s → EvIdle 于运动后 2000ms。
	d, _ = NewDetector(Config{})
	r := rand.New(rand.NewSource(1))
	var ss []Sample
	at := int64(0)
	for _, p := range [][2]any{{1.0, 75}, {1.6, 35}, {1.0, 125}} {
		for i := 0; i < p[1].(int); i++ {
			ss = append(ss, mkSample(at, p[0].(float64), r))
			at += testFrameMs
		}
	}
	evs = pushAll(d, ss)
	if got := countKind(evs, EvIdle); got != 1 {
		t.Fatalf("运动解除后 EvIdle=%d want 1", got)
	}
	if e := firstKind(evs, EvIdle); e.AtMs < 4200 || e.AtMs > 4220 {
		t.Fatalf("EvIdle 时刻=%d want ∈[4200,4220]（运动结束 2900+2000）", e.AtMs)
	}
}

// TestFallProfile 摔落剖面：自由落体（≥100ms）→冲击尖峰（≥FallThreshG）→
// EvFall 即发于冲击帧；保护窗 fallHoldMs=2s 内 ImpactSevere（停马达静音
// ≤2s），窗后解除；无落体/短落体/软着陆均不触发。
func TestFallProfile(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	g := NewGuard()
	cmd := Cmd{Duty: 1, AngleDeg: 90, Sound: true}
	// 主剖面：静置 300ms → 落体 460ms → 冲击 3 帧 → 静置至 fall+2.2s（保护
	// 窗+解除面全程在流内观测——仿真时钟断言）。
	d, err := NewDetector(Config{})
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	var ss []Sample
	at := int64(0)
	for _, p := range [][2]any{{1.0, 15}, {0.05, 23}, {3.5, 3}, {1.0, 110}} {
		for i := 0; i < p[1].(int); i++ {
			ss = append(ss, mkSample(at, p[0].(float64), r))
			at += testFrameMs
		}
	}
	var evs []Event
	fallAt := int64(-1)
	severeWrong, motorWrong, recoveredAt := 0, 0, int64(-1)
	for _, s := range ss {
		ev := d.Push(s)
		evs = append(evs, ev)
		if ev.Kind == EvFall && fallAt < 0 {
			fallAt = s.AtMs
		}
		if fallAt >= 0 && s.AtMs > fallAt {
			lv := d.ImpactLevel()
			if s.AtMs <= fallAt+2000 { // 保护窗内：severe+停马达静音（≤2s）
				if lv != ImpactSevere {
					severeWrong++
				}
				if c := g.Clamp(lv, cmd); c.Duty != 0 || c.AngleDeg != 0 || c.Sound {
					motorWrong++
				}
			} else if recoveredAt < 0 && lv != ImpactSevere {
				recoveredAt = s.AtMs
			}
		}
	}
	if got := countKind(evs, EvFall); got != 1 {
		t.Fatalf("EvFall=%d want 1", got)
	}
	if fallAt != 760 { // 静置 300ms + 落体 460ms → 冲击帧 760（帧粒度 20ms）
		t.Fatalf("EvFall 时刻=%d want 760（冲击帧即发）", fallAt)
	}
	if severeWrong != 0 {
		t.Fatalf("保护窗内非 Severe 帧数=%d（停马达静音窗口破裂）", severeWrong)
	}
	if motorWrong != 0 {
		t.Fatalf("保护窗内停马达静音失效帧数=%d", motorWrong)
	}
	if recoveredAt != 2780 { // fall+2000（2760）下一帧（2780）解除——停马达静音 ≤2s
		t.Fatalf("保护窗解除时刻=%d want 2780（fall+2000 下一帧）", recoveredAt)
	}
	if c := g.Clamp(d.ImpactLevel(), cmd); c.Duty == 0 {
		t.Fatalf("保护窗结束后马达仍停转（恢复失效）")
	}
	// 负面：无自由落体的孤立尖峰不触发。
	d, _ = NewDetector(Config{})
	if got := countKind(pushAll(d, seg2(r, 1.0, 15, 3.5, 3, 1.0, 60)), EvFall); got != 0 {
		t.Fatalf("孤立尖峰 EvFall=%d want 0", got)
	}
	// 负面：短落体（60ms <minFreeFallMs）+尖峰不触发。
	d, _ = NewDetector(Config{})
	if got := countKind(pushAll(d, seg2(r, 1.0, 15, 0.05, 3, 3.5, 3, 1.0, 60)), EvFall); got != 0 {
		t.Fatalf("短落体 EvFall=%d want 0", got)
	}
	// 负面：软着陆（冲击 <FallThreshG）不触发。
	d, _ = NewDetector(Config{})
	if got := countKind(pushAll(d, seg2(r, 1.0, 15, 0.05, 23, 1.5, 3, 1.0, 60)), EvFall); got != 0 {
		t.Fatalf("软着陆 EvFall=%d want 0", got)
	}
}

// seg2 多段等幅值流（幅值/帧数交替）。
func seg2(r *rand.Rand, vals ...any) []Sample {
	var ss []Sample
	at := int64(0)
	for i := 0; i+1 < len(vals); i += 2 {
		mag := vals[i].(float64)
		n := vals[i+1].(int)
		for j := 0; j < n; j++ {
			ss = append(ss, mkSample(at, mag, r))
			at += testFrameMs
		}
	}
	return ss
}

// TestStormFilterAndRate 风暴滤除：高频上穿（≥StormPerSec/s）→EvStorm 限流
// （窗内至多 1/s）；风暴期 EvPickup 滤除（高频冲击序列不误触发）；带内平静
// 1s 后自动恢复（再拿起可检出）。
func TestStormFilterAndRate(t *testing.T) {
	d, err := NewDetector(Config{})
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	r := rand.New(rand.NewSource(3))
	// 振荡流：静置 300ms → 2 帧超阈（1.6g）/2 帧回静（1.0g）交替 2.7s →
	// 上穿速率 12.5/s ≥StormPerSec=10 → 风暴；→ 静置 1.5s（恢复）→ 持续
	// 运动 700ms（真实拿起）。
	var ss []Sample
	at := int64(0)
	add := func(mag float64, n int) {
		for i := 0; i < n; i++ {
			ss = append(ss, mkSample(at, mag, r))
			at += testFrameMs
		}
	}
	add(1.0, 15)
	for i := 0; i < 34; i++ { // 136 帧=2.72s 振荡
		add(1.6, 2)
		add(1.0, 2)
	}
	add(1.0, 75)  // 1.5s 平静（1s 窗上穿过期→风暴解除）
	add(1.6, 35)  // 真实拿起
	add(1.0, 100) // 收尾
	evs := pushAll(d, ss)
	storms := 0
	pickupsInStorm, stormStart, stormEnd := 0, int64(-1), int64(-1)
	for _, e := range evs {
		if e.Kind == EvStorm {
			storms++
			if stormStart < 0 {
				stormStart = e.AtMs
			}
			stormEnd = e.AtMs
		}
		if e.Kind == EvPickup && e.AtMs < 3020+1500 { // 振荡+平静期内的拿起=风暴期误触发
			pickupsInStorm++
		}
	}
	if storms == 0 {
		t.Fatalf("高频振荡未检出风暴（上穿速率通道失效）")
	}
	if storms > 3 { // 2.7s 风暴期限流 ≤1/s → 至多 3
		t.Fatalf("风暴限流失效：EvStorm=%d（2.7s 内至多 3）", storms)
	}
	if pickupsInStorm != 0 {
		t.Fatalf("风暴期 EvPickup=%d want 0（高频冲击序列不误触发）", pickupsInStorm)
	}
	_ = stormStart
	_ = stormEnd
	// 恢复：平静后真实拿起可检出。
	lastPickup := int64(-1)
	for _, e := range evs {
		if e.Kind == EvPickup {
			lastPickup = e.AtMs
		}
	}
	if lastPickup < 3020+1500+250 || lastPickup > 3020+1500+270 {
		t.Fatalf("恢复后 EvPickup 时刻=%d want ∈[4770,4790]（平静 1.5s+去抖 250ms）", lastPickup)
	}
}

// TestDistortedInput 畸变输入（NaN/±Inf 任分量）按中性重力处理：不 panic、
// 静置语义成立（EvIdle 照发）、后续正常流检测不受污染。
func TestDistortedInput(t *testing.T) {
	d, err := NewDetector(Config{})
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	r := rand.New(rand.NewSource(4))
	evs := make([]Event, 0, 200)
	for i := 0; i < 200; i++ {
		at := int64(i) * testFrameMs
		var s Sample
		switch i % 4 {
		case 0:
			s = Sample{AtMs: at, Ax: math.NaN(), Ay: 0, Az: 1}
		case 1:
			s = Sample{AtMs: at, Ax: 0, Ay: math.Inf(1), Az: 1}
		case 2:
			s = Sample{AtMs: at, Ax: 0, Ay: 0, Az: math.Inf(-1)}
		default:
			s = axisSample(at, 1.0)
		}
		evs = append(evs, d.Push(s)) // 不 panic 即契约
	}
	if got := countKind(evs, EvPickup) + countKind(evs, EvFall) + countKind(evs, EvStorm); got != 0 {
		t.Fatalf("畸变输入触发事件=%d want 0（按静置处理）", got)
	}
	if got := countKind(evs, EvIdle); got != 1 { // 4s 流 → EvIdle 于 2000ms（中性重力=静置）
		t.Fatalf("畸变输入静置语义失效：EvIdle=%d want 1", got)
	}
	// 污染检查：畸变流后正常拿起流照常检出。
	for i := 0; i < 35; i++ {
		if ev := d.Push(mkSample(4000+int64(i)*testFrameMs, 1.6, r)); ev.Kind == EvPickup {
			return
		}
	}
	t.Fatalf("畸变输入后拿起检测失效（状态被污染）")
}

// TestLevelOfBands 冲击分级带（Guard 行键取值面；NaN/负值→最低带）。
func TestLevelOfBands(t *testing.T) {
	cases := []struct {
		dev  float64
		want ImpactLevel
	}{
		{0, ImpactNone}, {0.59, ImpactNone}, {0.6, ImpactLight}, {0.99, ImpactLight},
		{1.0, ImpactMedium}, {1.49, ImpactMedium}, {1.5, ImpactSevere}, {4, ImpactSevere},
		{-1, ImpactNone}, {math.NaN(), ImpactNone},
	}
	for _, c := range cases {
		if got := LevelOf(c.dev); got != c.want {
			t.Fatalf("LevelOf(%v)=%v want %v", c.dev, got, c.want)
		}
	}
}

// TestGuardMatrix Guard 边界盒：Clamp 钳入/非法值归零/severe 行停马达静音；
// Violation 判定（Clamp 后恒 false）；越界等级 fail-safe 取 severe 行。
func TestGuardMatrix(t *testing.T) {
	g := NewGuard()
	cases := []struct {
		name string
		lv   ImpactLevel
		in   Cmd
		want Cmd
	}{
		{"平静越界钳上限", ImpactNone, Cmd{2.5, 200, true}, Cmd{1, 120, true}},
		{"轻微降载", ImpactLight, Cmd{0.9, 100, true}, Cmd{0.8, 90, true}},
		{"中等降载", ImpactMedium, Cmd{0.9, 100, true}, Cmd{0.5, 60, true}},
		{"剧烈停马达静音", ImpactSevere, Cmd{1, 120, true}, Cmd{0, 0, false}},
		{"NaN 归零", ImpactNone, Cmd{math.NaN(), math.NaN(), true}, Cmd{0, 0, true}},
		{"Inf 归零", ImpactNone, Cmd{math.Inf(1), math.Inf(-1), true}, Cmd{0, 0, true}},
		{"负值归零", ImpactLight, Cmd{-3, -70, false}, Cmd{0, 0, false}},
		{"盒内直通", ImpactLight, Cmd{0.5, 50, true}, Cmd{0.5, 50, true}},
		{"非法等级 fail-safe", ImpactLevel(99), Cmd{1, 120, true}, Cmd{0, 0, false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := g.Clamp(c.lv, c.in)
			if got != c.want {
				t.Fatalf("Clamp=%+v want %+v", got, c.want)
			}
			if g.Violation(c.lv, got) {
				t.Fatalf("Clamp 后 Violation=true（边界盒失效）")
			}
		})
	}
	if !g.Violation(ImpactLight, Cmd{0.81, 50, true}) {
		t.Fatalf("Violation 对越界占空比未报")
	}
	if !g.Violation(ImpactSevere, Cmd{0, 0, true}) {
		t.Fatalf("Violation 对 severe 行声音未报")
	}
	// 矩阵行上限不超数据表安全值（表驱动穷举面，详证在 gates T6-G0-03）。
	for lv := ImpactNone; lv <= ImpactSevere; lv++ {
		row := g.Row(lv)
		if row.Duty > datasheetDutyMax || row.AngleDeg > datasheetAngleMax {
			t.Fatalf("行 %v 超数据表安全值: %+v", lv, row)
		}
	}
}
