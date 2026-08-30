// T15 表驱动单测（m3-spec §3 包契约 B：命中/穿透/失效语义、容量有界、
// 安全旁路、规范化）。
package routecache

import (
	"strings"
	"testing"
)

// newTestCache 固定配置测试缓存（SafeBypass：含「秘密」字样的 query 视为安全类）。
func newTestCache(t *testing.T) *Cache {
	t.Helper()
	c, err := NewCache(Config{MaxEntries: 3, MaxBytes: 100, TTLMs: 1000,
		SafeBypass: func(q string) bool { return strings.Contains(q, "秘密") }})
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	return c
}

func TestNewCacheFailClosed(t *testing.T) {
	bypass := func(string) bool { return false }
	cases := []struct {
		name string
		cfg  Config
	}{
		{"MaxEntries≤0", Config{MaxEntries: 0, MaxBytes: 10, TTLMs: 10, SafeBypass: bypass}},
		{"MaxBytes≤0", Config{MaxEntries: 1, MaxBytes: 0, TTLMs: 10, SafeBypass: bypass}},
		{"TTLMs≤0", Config{MaxEntries: 1, MaxBytes: 10, TTLMs: 0, SafeBypass: bypass}},
		{"SafeBypass=nil", Config{MaxEntries: 1, MaxBytes: 10, TTLMs: 10}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewCache(c.cfg); err == nil {
				t.Fatal("应 fail-closed 拒绝（不留悄悄无限缓存的缝）")
			}
		})
	}
}

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"你好", "你好"},
		{"你好呀", "你好"},
		{"你好嘛", "你好"},
		{"你好啦", "你好"},
		{"  你 好 呀  ", "你 好"},
		{"A B  C\tD\nE", "a b c d e"},
		{"Hello WORLD", "hello world"},
		{"晚安啦呀嘛", "晚安"},
		{"你\t好", "你 好"},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Fatalf("Normalize(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestCacheHitMissTTL(t *testing.T) {
	c := newTestCache(t)
	k := Key{NormQuery: "晚安", UserID: "child", Role: "bear", EmoLabel: "happy"}
	if _, ok := c.Get(k, 100); ok {
		t.Fatal("空缓存不应命中")
	}
	c.Put(k, "晚安，做个好梦", 100)
	if s := c.Stats(); s.Misses != 1 || s.Hits != 0 {
		t.Fatalf("首查应计穿透: %+v", s)
	}
	got, ok := c.Get(k, 200)
	if !ok || got != "晚安，做个好梦" {
		t.Fatalf("应命中 got=%q ok=%v", got, ok)
	}
	// TTL 过期=穿透（now-stored ≥ TTLMs）。
	if _, ok := c.Get(k, 1100); ok {
		t.Fatal("TTL 过期不应命中")
	}
	s := c.Stats()
	if s.Expired != 1 || s.Hits != 1 || s.Misses != 2 {
		t.Fatalf("Stats 口径错误: %+v", s)
	}
	if c.Len() != 0 {
		t.Fatalf("过期条目应移除，Len=%d", c.Len())
	}
}

func TestCacheKeyFourTupleExact(t *testing.T) {
	c := newTestCache(t)
	base := Key{NormQuery: "q", UserID: "u", Role: "r", EmoLabel: "e"}
	c.Put(base, "v", 0)
	variants := []Key{
		{NormQuery: "q2", UserID: "u", Role: "r", EmoLabel: "e"},
		{NormQuery: "q", UserID: "u2", Role: "r", EmoLabel: "e"},
		{NormQuery: "q", UserID: "u", Role: "r2", EmoLabel: "e"},
		{NormQuery: "q", UserID: "u", Role: "r", EmoLabel: "e2"},
	}
	for i, k := range variants {
		if _, ok := c.Get(k, 10); ok {
			t.Fatalf("变体 %d 不应命中（四元组须全等）", i)
		}
	}
	if _, ok := c.Get(base, 10); !ok {
		t.Fatal("原键应命中")
	}
}

func TestCacheSafeBypassNeverHitNeverWrite(t *testing.T) {
	c := newTestCache(t)
	k := Key{NormQuery: "这是秘密问题", UserID: "u"}
	c.Put(k, "x", 0) // 拒收
	if c.Len() != 0 {
		t.Fatal("安全类 query 应拒写")
	}
	c.Put(Key{NormQuery: "晚安", UserID: "u"}, "v", 0)
	if _, ok := c.Get(k, 10); ok {
		t.Fatal("安全类 query Get 恒 miss")
	}
	s := c.Stats()
	if s.Bypassed != 2 {
		t.Fatalf("旁路计数=%d want 2（Get+Put 各一）", s.Bypassed)
	}
}

func TestCacheLRUEviction(t *testing.T) {
	c := newTestCache(t) // MaxEntries=3
	put := func(q, v string) { c.Put(Key{NormQuery: q}, v, 0) }
	put("a", "1")
	put("b", "2")
	put("c", "3")
	if c.Len() != 3 {
		t.Fatalf("Len=%d want 3", c.Len())
	}
	if _, ok := c.Get(Key{NormQuery: "a"}, 10); !ok { // a 刷新为最近使用
		t.Fatal("a 应命中（LRU 位刷新）")
	}
	put("d", "4") // 淘汰最旧未用=b
	if c.Len() != 3 {
		t.Fatalf("容量有界破坏 Len=%d", c.Len())
	}
	if _, ok := c.Get(Key{NormQuery: "b"}, 20); ok {
		t.Fatal("b 应已被 LRU 淘汰")
	}
	for _, q := range []string{"a", "c", "d"} {
		if _, ok := c.Get(Key{NormQuery: q}, 20); !ok {
			t.Fatalf("%s 不应被淘汰", q)
		}
	}
	if s := c.Stats(); s.Evicted != 1 {
		t.Fatalf("Evicted=%d want 1", s.Evicted)
	}
}

func TestCacheBytesBudget(t *testing.T) {
	c, err := NewCache(Config{MaxEntries: 10, MaxBytes: 10, TTLMs: 1000,
		SafeBypass: func(string) bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	c.Put(Key{NormQuery: "a"}, strings.Repeat("x", 6), 0)
	c.Put(Key{NormQuery: "b"}, strings.Repeat("y", 6), 0) // 12>10 → 淘汰 a
	if c.Len() != 1 {
		t.Fatalf("字节预算淘汰失效 Len=%d", c.Len())
	}
	if _, ok := c.Get(Key{NormQuery: "a"}, 10); ok {
		t.Fatal("a 应因字节预算被淘汰")
	}
	// 单条超预算：逐最旧先行后新条目最终自逐——不变量 bytes≤MaxBytes 恒成立
	// （超预算响应=干脆不缓存，fail-closed：不留悄悄超预算的缝）。
	big := strings.Repeat("z", 50)
	c.Put(Key{NormQuery: "big"}, big, 0)
	if c.Len() != 0 || c.Bytes() != 0 {
		t.Fatalf("单条超预算应自逐（不缓存）: Len=%d Bytes=%d", c.Len(), c.Bytes())
	}
	if _, ok := c.Get(Key{NormQuery: "big"}, 10); ok {
		t.Fatal("超预算条目不应可命中")
	}
}

func TestCacheOverwriteSameKey(t *testing.T) {
	c := newTestCache(t)
	k := Key{NormQuery: "q"}
	c.Put(k, "old", 0)
	c.Put(k, "new", 10)
	if got, ok := c.Get(k, 20); !ok || got != "new" {
		t.Fatalf("同键覆盖失效 got=%q ok=%v", got, ok)
	}
	if c.Len() != 1 {
		t.Fatalf("覆盖不应增条目 Len=%d", c.Len())
	}
}

func TestCacheInvalidate(t *testing.T) {
	c := newTestCache(t)
	k := Key{NormQuery: "q"}
	c.Put(k, "v", 0)
	c.Invalidate(k)
	if _, ok := c.Get(k, 10); ok {
		t.Fatal("Invalidate 后不应命中")
	}
	c.Invalidate(Key{NormQuery: "不存在"}) // 幂等
	if c.Len() != 0 || c.Bytes() != 0 {
		t.Fatal("失效后缓存应空")
	}
}

func TestRouterDecide(t *testing.T) {
	safe := func(q string) bool { return strings.Contains(q, "不想活") }
	r, err := NewRouter(safe)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRouter(nil); err == nil {
		t.Fatal("SafeBypass=nil 应 fail-closed")
	}
	cases := []struct {
		name      string
		q         string
		wantRoute Route
		wantCache bool
	}{
		{"安全敏感→云+禁缓存", "我不想活了", RouteCloud, false},
		{"疑问词→云", "恐龙为什么会灭绝", RouteCloud, true},
		{"长话轮→云", "给我讲一个关于太空飞船的故事吧", RouteCloud, true},
		{"预置域→端", "晚安啦", RouteEdge, true},
		{"短句→端", "你好", RouteEdge, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := r.Decide(c.q)
			if d.Route != c.wantRoute {
				t.Fatalf("route=%v want %v（reason=%s）", d.Route, c.wantRoute, d.Reason)
			}
			if d.Cacheable != c.wantCache {
				t.Fatalf("cacheable=%v want %v", d.Cacheable, c.wantCache)
			}
		})
	}
}
