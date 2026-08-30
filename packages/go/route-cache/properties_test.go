// T15 属性测试（m3-spec §3 属性行 + docs/gates/assets/T15.md，testing/quick）：
//
//	P1 容量有界：任意操作流下 Len ≤ MaxEntries 且 Bytes ≤ MaxBytes 恒成立
//	   （LRU/字节预算淘汰让位——含单条超预算最终自逐，无越界无残留）。
//	P2 精确键命中：全等四元组才命中，且命中值=该键最近一次 Put 的值
//	   （误命中=0 的构造面：任意查询串 stress 下近形键不串值）。
//	P3 确定性回放：同操作序列两缓存产生同逐操作命中观测与同终态计数
//	   （同 query+同缓存态+同阈值同决策——消抖动）。
//	P4 语气词消抖：query 追加任意语气词（嘛/呀/啦）不改规范化键与路由目标
//	   （T15 资产卡属性行）。
package routecache

import (
	"testing"
	"testing/quick"
)

// propOp quick 生成的抽象操作（字段任意值——选择器经取模落合法域）。
type propOp struct {
	Kind int8   // 操作选择器（mod 4：Put/Get/Invalidate/bypass 探测）
	Q    string // 任意查询串（stress：空/控制字符/任意 unicode——键面 fuzz）
	U    int8   // 用户选择器（mod 3）
	R    int8   // 角色选择器（mod 2）
	E    int8   // 情绪选择器（mod 2）
	Resp string // 任意载荷
	At   int64  // 仿真时刻（stress：负值钳 0）
}

// propMod 非负取模（quick 可生成负数）。
func propMod(v, m int64) int {
	r := v % m
	if r < 0 {
		r += m
	}
	return int(r)
}

// propKey 抽象操作 → 缓存键。
func (o propOp) key() Key {
	return Key{NormQuery: Normalize(o.Q), UserID: "u" + string(rune('0'+propMod(int64(o.U), 3))),
		Role:     "r" + string(rune('0'+propMod(int64(o.R), 2))),
		EmoLabel: "e" + string(rune('0'+propMod(int64(o.E), 2)))}
}

// propAt 仿真时刻（负值钳 0——TTL 判定的合法输入域）。
func (o propOp) at() int64 {
	if o.At < 0 {
		return 0
	}
	return o.At
}

// propConfig 属性测试缓存配置（小容量——淘汰路径自然激活；bypass 命中固定词面）。
func propConfig(maxEntries int, maxBytes int64, ttl int64) Config {
	return Config{MaxEntries: maxEntries, MaxBytes: maxBytes, TTLMs: ttl,
		SafeBypass: func(q string) bool { return q == "秘密" }}
}

// TestP1CapacityBounded quick：任意操作流容量有界（条目数+字节双门，恒成立）。
func TestP1CapacityBounded(t *testing.T) {
	c, err := NewCache(propConfig(8, 400, 1000))
	if err != nil {
		t.Fatal(err)
	}
	prop := func(ops []propOp) bool {
		for _, o := range ops {
			switch propMod(int64(o.Kind), 4) {
			case 0:
				c.Put(o.key(), o.Resp, o.at())
			case 1:
				c.Get(o.key(), o.at())
			case 2:
				c.Invalidate(o.key())
			default:
				c.Put(Key{NormQuery: "秘密"}, o.Resp, o.at()) // bypass 拒收（容量不增）
			}
			if c.Len() > 8 || c.Bytes() > 400 {
				return false // 容量越界（含单条超预算自逐后仍越界=淘汰失效）
			}
		}
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 300}); err != nil {
		t.Errorf("P1 容量有界 失效: %v", err)
	}
}

// TestP2ExactKeyHit quick：命中 ⇔ 键全等且曾写入且未过期（大容量消除淘汰
// 干扰——纯键面）；命中值=该键最近 Put 值（近形键不串值）。模型 TTL-aware：
// quick 任意 At（含极值）下「Put 于 t0、Get 于 t1≥t0+TTL」=过期 miss 是真实
// 语义非误命中（quick 时间种子非确定——模型不过期面=flaky 根因，修）。
func TestP2ExactKeyHit(t *testing.T) {
	const ttl = int64(1 << 30)
	c, err := NewCache(propConfig(4096, 1<<20, ttl))
	if err != nil {
		t.Fatal(err)
	}
	type p2entry struct {
		val string
		at  int64
	}
	last := map[Key]p2entry{} // 键→最近 Put 值与时刻（无淘汰面的 TTL-aware 键模型）
	prop := func(o propOp) bool {
		k := o.key()
		switch propMod(int64(o.Kind), 4) {
		case 0:
			c.Put(k, o.Resp, o.at())
			last[k] = p2entry{o.Resp, o.at()}
		case 1:
			got, ok := c.Get(k, o.at())
			e, put := last[k]
			expired := put && o.at() >= e.at && o.at()-e.at >= ttl // 时刻倒流（at()<e.at）=未过期（cache 同口径）
			if expired {
				delete(last, k) // 过期=缓存已移除条目（Get 穿透面）
			}
			if ok != (put && !expired) || (ok && got != e.val) {
				return false // 命中≠全等键未过期（或串值）——误命中面
			}
		case 2:
			c.Invalidate(k)
			delete(last, k)
		default:
			c.Put(Key{NormQuery: "秘密"}, o.Resp, o.at())
			if _, ok := c.Get(Key{NormQuery: "秘密"}, o.at()); ok {
				return false // bypass 恒 miss
			}
		}
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 500}); err != nil {
		t.Errorf("P2 精确键命中 失效: %v", err)
	}
}

// TestP3DeterministicReplay quick：同操作序列两缓存——逐操作命中观测、终态
// 计数（Hits/Misses/Evicted/Expired/Bypassed/Len/Bytes）逐位一致。
func TestP3DeterministicReplay(t *testing.T) {
	run := func(ops []propOp) ([]bool, Stats, int, int64) {
		c, err := NewCache(propConfig(8, 400, 1000))
		if err != nil {
			t.Fatal(err)
		}
		var hits []bool
		for _, o := range ops {
			switch propMod(int64(o.Kind), 4) {
			case 0:
				c.Put(o.key(), o.Resp, o.at())
				hits = append(hits, false)
			case 1:
				_, ok := c.Get(o.key(), o.at())
				hits = append(hits, ok)
			case 2:
				c.Invalidate(o.key())
				hits = append(hits, false)
			default:
				c.Put(Key{NormQuery: "秘密"}, o.Resp, o.at())
				hits = append(hits, false)
			}
		}
		return hits, c.Stats(), c.Len(), c.Bytes()
	}
	prop := func(ops []propOp) bool {
		h1, s1, l1, b1 := run(ops)
		h2, s2, l2, b2 := run(ops)
		if s1 != s2 || l1 != l2 || b1 != b2 || len(h1) != len(h2) {
			return false
		}
		for i := range h1 {
			if h1[i] != h2[i] {
				return false
			}
		}
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("P3 确定性回放 失效: %v", err)
	}
}

// TestP4ModalParticleStability quick：query 追加任意语气词——规范化键不变、
// 路由决策不变（消抖动属性：同 query 同缓存态同决策）。
func TestP4ModalParticleStability(t *testing.T) {
	r, err := NewRouter(func(string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	particles := []string{"嘛", "呀", "啦", "呀嘛", "啦呀嘛"}
	prop := func(q string) bool {
		base := Normalize(q)
		if base != Normalize(base) { // 规范化幂等（同阈值同键的前提）
			return false
		}
		d0 := r.Decide(q)
		for _, p := range particles {
			if Normalize(q+p) != base {
				return false // 语气词改写键（消抖动破坏）
			}
			d := r.Decide(q + p)
			if d.Route != d0.Route || d.Cacheable != d0.Cacheable {
				return false // 语气词改路由目标
			}
		}
		return true
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 300}); err != nil {
		t.Errorf("P4 语气词消抖 失效: %v", err)
	}
}
