// T15 门禁测试（m3-spec §9 Mark 接线策略表，IR #104）：一 ID 一顶层测试函数，
// 口径与样本量声明唯一来源 configs/gates/T15.yaml（本文件只落断言本体）。
// verdict 全真实（规则面：精确键构造+哈希查表——CI 宿主真实代码路径）；
// 语义缓存 θ 权衡曲线=嵌入模型面（L5 注记，ADR-0006，本包不接不宣称）。
// 安全联跑：SafeBypass 由 safety 引擎接线（loop 组装同源——Classify≠Benign
// 或 PreSpeak 拦截 → 旁路），测试侧 import safety 合规（考卷隔离限产线代码）。
// 敏感词面纪律：与 runtime-fsm/gates_test.go 同——探针采样自 T9
// DefaultConfig() 词表，不自造词面（缺口见仓库 issue）。
package routecache

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/Cloudbird-Software/AI_Toy/packages/go/safety"
	"github.com/Cloudbird-Software/AI_Toy/tools/gaterunner/gaterunner"
)

// gateBypass T15 门禁统一安全旁路判定（loop 组装同源：Crisis/Sensitive 分型
// 或攻击拦截 → 恒云+永不缓存）。
func gateBypass(eng *safety.Engine) func(string) bool {
	return func(q string) bool {
		return eng.Classify(q) != safety.Benign || eng.PreSpeak(q).Intercepted
	}
}

// gateCache 门禁统一缓存（固定配置面——n 上限宽裕，聚焦键面而非容量面）。
func gateCache(t *testing.T, bypass func(string) bool) *Cache {
	t.Helper()
	c, err := NewCache(Config{MaxEntries: 4096, MaxBytes: 1 << 20, TTLMs: 1 << 30,
		SafeBypass: bypass})
	if err != nil {
		t.Fatalf("缓存组装失败: %v", err)
	}
	return c
}

// gateRouter 门禁统一路由器（安全旁路与缓存同源注入）。
func gateRouter(t *testing.T, bypass func(string) bool) *Router {
	t.Helper()
	r, err := NewRouter(bypass)
	if err != nil {
		t.Fatalf("路由器组装失败: %v", err)
	}
	return r
}

// gateKey 四元组键构造（默认身份上下文）。
func gateKey(q, uid, role, emo string) Key {
	return Key{NormQuery: Normalize(q), UserID: uid, Role: role, EmoLabel: emo}
}

// ---- T15-G0-01 缓存误命中（对抗口径=0）----

// TestT15G001AdversarialCacheHit T15-G0-01（BI-15.3/G0，真实）：缓存误命中
// 对抗口径=0——200 组近形异义对抗对（100 文面变异：内容字差/词位差；100
// 上下文跨界：同 query 异身份/异角色/异情绪）双向探测 + 自然流 1000 轮
// （任意混合流下命中值恒等于该精确键最近 Put 值）+ 安全类 query 永不缓存
// 命中（引擎接线旁路：Put 拒收+Get 恒 miss）。误命中=事故不是省钱。
func TestT15G001AdversarialCacheHit(t *testing.T) {
	gaterunner.Mark(t, "T15", "BI-15.3", "T15-G0-01", "G0")
	eng, err := safety.NewEngine(safety.DefaultConfig())
	if err != nil {
		t.Fatalf("safety 引擎组装失败: %v", err)
	}
	bypass := gateBypass(eng)
	c := gateCache(t, bypass)
	misHits := 0

	// 对抗对 200 组（yaml min_evidence n:200）。
	const pairs = 200
	type adv struct{ a, b Key }
	var adversarial []adv
	for i := 0; i < 100; i++ { // 文面变异：近形异义（数字位/名词位差）
		a := gateKey(fmt.Sprintf("我想听第%d个睡前故事", i+1), "u1", "r0", "e0")
		b := gateKey(fmt.Sprintf("我想听第%d首睡前儿歌", i+1), "u1", "r0", "e0")
		adversarial = append(adversarial, adv{a, b})
	}
	for i := 0; i < 100; i++ { // 上下文跨界：同 query 异四元组维度
		q := fmt.Sprintf("今天我们在公园玩了第%d次滑梯", i+1)
		dims := [][3]string{
			{"u1", "r0", "e0"}, {"u2", "r0", "e0"}, // 异身份
			{"u1", "r1", "e0"}, {"u1", "r0", "e1"}, // 异角色/异情绪
		}
		base := dims[i%4]
		other := dims[(i+1)%4]
		adversarial = append(adversarial, adv{
			a: gateKey(q, base[0], base[1], base[2]),
			b: gateKey(q, other[0], other[1], other[2]),
		})
	}
	if len(adversarial) != pairs {
		t.Fatalf("对抗对 %d 组 ≠ 200（yaml min_evidence n:200）", len(adversarial))
	}
	for i, p := range adversarial { // 双向：a 写入后 b 不得命中，反之亦然
		c.Put(p.a, fmt.Sprintf("应答A-%d", i), int64(i))
		if _, ok := c.Get(p.b, int64(i)+1); ok {
			misHits++
			t.Errorf("对抗对 %d：b 误命中 a 的缓存（近形异义串值）", i)
		}
		c.Invalidate(p.a)
		c.Put(p.b, fmt.Sprintf("应答B-%d", i), int64(i)+2)
		if _, ok := c.Get(p.a, int64(i)+3); ok {
			misHits++
			t.Errorf("对抗对 %d：a 误命中 b 的缓存（双向面）", i)
		}
		c.Invalidate(p.b)
	}

	// 自然流 1000 轮：混合 put/get/覆盖/失效——命中 ⇔ 精确键最近值。
	rng := rand.New(rand.NewSource(2026))
	model := map[Key]string{} // 精确键地面真值（无淘汰/过期面的纯键模型）
	pool := []string{"你好呀", "晚安啦", "陪我玩积木", "今天学校很开心", "彩虹有几个颜色",
		"我想养小猫", "讲个睡前故事", "陪我玩", "小狗为什么摇尾巴", "明天去公园"}
	for i := 0; i < 1000; i++ {
		q := pool[rng.Intn(len(pool))] + fmt.Sprintf("编号%d", rng.Intn(8))
		k := gateKey(q, fmt.Sprintf("u%d", rng.Intn(2)), "r0", "e0")
		switch rng.Intn(3) {
		case 0:
			v := fmt.Sprintf("回复%d", i)
			c.Put(k, v, int64(i))
			model[k] = v
		case 1:
			got, ok := c.Get(k, int64(i))
			want, put := model[k]
			if ok != put || (ok && got != want) {
				misHits++
				t.Errorf("自然流 %d：误命中 got=%q ok=%v want=%q put=%v", i, got, ok, want, put)
			}
		default:
			c.Invalidate(k)
			delete(model, k)
		}
	}

	// 安全类 query 永不缓存命中：采样自 T9 词表（不自造词面）。
	cfg := safety.DefaultConfig()
	if len(cfg.CrisisLexicon) < 50 || len(cfg.AttackPatterns) < 50 {
		t.Fatalf("T9 词表覆盖不足（采样面 50+50）")
	}
	probes := append(append([]string{}, cfg.CrisisLexicon[25:50]...), cfg.AttackPatterns[25:50]...)
	safetyHits := 0
	for i, q := range probes {
		k := gateKey(q, "u1", "r0", "e0")
		c.Put(k, "不应写入", int64(i)) // bypass 拒收
		if v, ok := c.Get(k, int64(i)+1); ok {
			safetyHits++
			t.Errorf("安全类 query 缓存命中：%q → %q", q, v)
		}
	}
	if safetyHits != 0 {
		misHits += safetyHits
	}
	if misHits != 0 {
		t.Fatalf("adversarial_cache_hit_count=%d（阈值 ==0，误命中是事故不是省钱）", misHits)
	}
	t.Logf("T15-G0-01：200 组对抗对（文面变异100+上下文跨界100）双向探测+自然流 "+
		"1000 轮+安全类 %d 条（T9 词表采样）全查 0 误命中", len(probes))
}

// ---- T15-G1-01 路由正确率 ----

// TestT15G101RoutingAccuracy T15-G1-01（BI-15.1/G1，真实）：路由正确率
// 500 条意图分层标注 ≥0.92——分层：云端疑问 200（疑问词命中）/云端长话轮
// 150（≥6 rune 非预置域）/端侧预置域 100（词面命中）/安全敏感 50（T9 词表
// 采样——恒云+不可缓存，错误=0 升 G0 行为单独硬断言）。
func TestT15G101RoutingAccuracy(t *testing.T) {
	gaterunner.Mark(t, "T15", "BI-15.1", "T15-G1-01", "G1")
	eng, err := safety.NewEngine(safety.DefaultConfig())
	if err != nil {
		t.Fatalf("safety 引擎组装失败: %v", err)
	}
	r := gateRouter(t, gateBypass(eng))

	type labeled struct {
		q       string
		want    Route
		safety  bool
		stratum string
	}
	var ls []labeled
	cloudQ := []string{"为什么", "怎么", "是什么", "谁", "哪里", "多少", "什么时候"}
	cloudT := []string{"天空", "大海", "恐龙", "月亮", "彩虹", "小猫", "火车"}
	for i := 0; i < 200; i++ { // 云端疑问层
		ls = append(ls, labeled{q: fmt.Sprintf("%s%s是%s的呀", cloudT[i%len(cloudT)],
			cloudQ[i%len(cloudQ)], "那样"), want: RouteCloud, stratum: "cloud_question"})
	}
	cloudLong := []string{"学校门口新开了一家文具店", "我们班来了一位新的体育老师",
		"周末爸爸妈妈带我去郊外野餐", "楼下的小卖部今天没有开门营业",
		"我的书包里装着好多好看的贴纸", "公园里的牡丹花开得特别漂亮"}
	for i := 0; i < 150; i++ { // 云端长话轮层（≥6 rune 非预置域）
		ls = append(ls, labeled{q: cloudLong[i%len(cloudLong)] + fmt.Sprintf("第%d天", i),
			want: RouteCloud, stratum: "cloud_long"})
	}
	edgeQ := []string{"唱首歌吧", "陪我玩积木", "晚安啦", "拜拜下次见", "早上好呀",
		"猜谜语好吗", "抱抱我", "讲故事咯", "我爱你哟", "再见啦"}
	for i := 0; i < 100; i++ { // 端侧预置域层
		ls = append(ls, labeled{q: edgeQ[i%len(edgeQ)] + fmt.Sprintf("第%d遍", i),
			want: RouteEdge, stratum: "edge_pattern"})
	}
	cfg := safety.DefaultConfig()
	if len(cfg.CrisisLexicon) < 50 || len(cfg.AttackPatterns) < 50 {
		t.Fatalf("T9 词表覆盖不足（安全层采样面 50）")
	}
	probes := append(append([]string{}, cfg.CrisisLexicon[25:50]...), cfg.AttackPatterns[25:50]...)
	for _, q := range probes { // 安全敏感层（50 条——恒云+不可缓存）
		ls = append(ls, labeled{q: q, want: RouteCloud, safety: true, stratum: "safety"})
	}
	if len(ls) < 500 {
		t.Fatalf("标注集 %d 条 < 500（yaml min_evidence n:500）", len(ls))
	}
	ls = ls[:500] // 精确 n=500（200+150+100+50 分层恰好足额）
	correct, safetyErrs := 0, 0
	for _, c := range ls {
		d := r.Decide(c.q)
		ok := d.Route == c.want
		if c.safety { // 安全敏感：路由错误=0（G0 行为）+ 恒不可缓存
			if d.Route != RouteCloud || d.Cacheable {
				safetyErrs++
				t.Errorf("安全敏感路由错误：%q → %v cacheable=%v", c.q, d.Route, d.Cacheable)
			}
		}
		if ok {
			correct++
		} else if !c.safety {
			t.Logf("路由分错：%q → %v（期望 %v，层=%s）", c.q, d.Route, c.want, c.stratum)
		}
	}
	acc := float64(correct) / float64(len(ls))
	if safetyErrs != 0 {
		t.Fatalf("safety_route_error=%d（安全敏感类路由错误=0 升 G0 行为——阈值红线）", safetyErrs)
	}
	if acc < 0.92 {
		t.Fatalf("routing_accuracy=%.4f < 0.92（n=%d）", acc, len(ls))
	}
	t.Logf("T15-G1-01：500 条意图分层标注（云疑问200/云长150/端侧100/安全敏感50）"+
		"路由正确率=%.4f ≥ 0.92（安全敏感 0 错误——G0 行为面）", acc)
}

// ---- T15-G1-02 命中率与降本 ----

// gateSessionDay 单用户画像单日会话流：从该画像稳定问题池（跨天重复——
// 儿童行为先验：同样的问题每天问）+ 当日新话题混合驱动缓存。返回当日
// (hits, misses) 增量。
func gateSessionDay(c *Cache, pool []string, novel []string, uid string, dayMs int64) (int, int) {
	hits, misses := 0, 0
	queries := append(append([]string{}, pool...), novel...)
	for j, q := range queries {
		k := gateKey(q, uid, "r0", "e0")
		if _, ok := c.Get(k, dayMs+int64(j)*1000); ok {
			hits++
		} else {
			misses++
			c.Put(k, fmt.Sprintf("day%d-%s-%d", dayMs, uid, j), dayMs+int64(j)*1000)
		}
	}
	return hits, misses
}

// TestT15G102CacheHitRate T15-G1-02（BI-15.2/G1，真实）：命中率与降本——
// 仿真 30 天会话流 ×3 档用户画像（稳定池大小分档：高重复/中/低重复），
// 聚合命中率 ≥0.30（yaml 阈值；TTL 跨天过期+当日新话题=诚实口径——不做
// 全等回放刷分）；成本降本注记=cloud 计数口径（realdriver 同源）。
// 语义缓存 θ 相似命中=嵌入模型面（L5 注记，本测只测精确键面）。
func TestT15G102CacheHitRate(t *testing.T) {
	gaterunner.Mark(t, "T15", "BI-15.2", "T15-G1-02", "G1")
	eng, err := safety.NewEngine(safety.DefaultConfig())
	if err != nil {
		t.Fatalf("safety 引擎组装失败: %v", err)
	}
	// TTL=48h：跨 1 天重复命中、跨 2 天过期重填（诚实过期面在流内自然发生）。
	c, err := NewCache(Config{MaxEntries: 4096, MaxBytes: 1 << 20, TTLMs: 48 * 3600 * 1000,
		SafeBypass: gateBypass(eng)})
	if err != nil {
		t.Fatalf("缓存组装失败: %v", err)
	}
	const days = 30
	dayMs := int64(24 * 3600 * 1000)
	personas := []struct {
		uid  string
		pool []string // 稳定问题池（跨天重复——儿童先验）
	}{
		{uid: "u-high", pool: []string{"彩虹有几个颜色", "小狗为什么摇尾巴", "讲个睡前故事",
			"唱首歌吧", "陪我玩积木", "晚安啦", "你好呀", "明天去公园"}},
		{uid: "u-mid", pool: []string{"彩虹有几个颜色", "小狗为什么摇尾巴", "晚安啦", "你好呀",
			"陪我玩积木"}},
		{uid: "u-low", pool: []string{"晚安啦", "你好呀"}},
	}
	rng := rand.New(rand.NewSource(30))
	totalHits, totalMisses := 0, 0
	for d := 0; d < days; d++ {
		for _, p := range personas {
			novel := []string{fmt.Sprintf("第%d天的新话题%d", d, rng.Intn(3))} // 当日新话题（必 miss）
			h, m := gateSessionDay(c, p.pool, novel, p.uid, int64(d)*dayMs)
			totalHits += h
			totalMisses += m
		}
	}
	total := totalHits + totalMisses
	if total < 300 {
		t.Fatalf("会话流 %d 轮过小（30 天×3 画像×≥4 话轮）", total)
	}
	rate := float64(totalHits) / float64(total)
	if rate < 0.30 {
		t.Fatalf("cache_hit_rate=%.4f < 0.30（30 天×3 画像，hits=%d misses=%d）",
			rate, totalHits, totalMisses)
	}
	s := c.Stats()
	t.Logf("T15-G1-02：30 天×3 档画像命中率=%.4f（hits=%d misses=%d expired=%d "+
		"evicted=%d bypassed=%d；语义 θ 面=L5 注记）",
		rate, totalHits, totalMisses, s.Expired, s.Evicted, s.Bypassed)
}

// ---- T15-G1-03 路由延迟 ----

// TestT15G103RoutingLatency T15-G1-03（BI-15.1/G1，真实）：路由决策 P95≤30ms
// （n=500 墙钟实测——规则面=规范化+词面扫描的构造来源；P95=第 475 位顺序
// 统计量，描述性口径）。真实设备延迟=真机面 L5 注记（CI 宿主≈逻辑面下界）。
func TestT15G103RoutingLatency(t *testing.T) {
	gaterunner.Mark(t, "T15", "BI-15.1", "T15-G1-03", "G1")
	eng, err := safety.NewEngine(safety.DefaultConfig())
	if err != nil {
		t.Fatalf("safety 引擎组装失败: %v", err)
	}
	r := gateRouter(t, gateBypass(eng))
	const n = 500 // yaml min_evidence n:500
	base := []string{"今天学校门口新开了一家文具店特别好看", "我想听第%d个睡前故事",
		"彩虹有几个颜色呀", "唱首歌吧第%d遍", "小狗为什么摇尾巴", "陪我玩第%d次积木",
		"我们班来了一位新的体育老师", "晚安啦第%d天"}
	elapsed := make([]float64, 0, n)
	for i := 0; i < n; i++ {
		q := fmt.Sprintf(base[i%len(base)], i)
		start := time.Now()
		r.Decide(q)
		elapsed = append(elapsed, float64(time.Since(start).Nanoseconds())/1e6)
	}
	sort.Float64s(elapsed)
	p95 := elapsed[(19*len(elapsed))/20-1] // n=500 → 第 475 位
	if p95 > 30 {
		t.Fatalf("routing_decision_p95_ms=%.4f > 30（n=%d max=%.4f）", p95, n, elapsed[n-1])
	}
	t.Logf("T15-G1-03：500 条路由决策 P95=%.4fms（p50=%.4f max=%.4f——CI 宿主逻辑面；"+
		"真机=设备面 L5 注记）", p95, elapsed[249], elapsed[n-1])
}
