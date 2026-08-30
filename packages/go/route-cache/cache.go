// Package routecache 实现 T15 路由与缓存 go 侧规则面（m3-spec §3，实现卡 #104）。
//
// 命中/穿透/失效语义（spec §3）：命中=键四元组全等（规范化后）→零上游延迟；
// 穿透=未命中/过期→走 Responder 并回填；失效=TTL 过期/Invalidate/安全旁路。
// 误命中=0 由精确键构造保证（对抗对 200 组实测）——语义近邻缓存（θ 相似度）
// 需嵌入模型=真模型面（L5 注记），本包不接、不宣称。
//
// import 纪律（ADR-0004）：白名单=标准库。安全类 query 永不缓存：SafeBypass
// 由 loop 组装注入（safety 判定 Crisis/Attack→旁路）——本包不 import safety
// （考卷隔离）；Get 恒 miss+Put 拒收（T15-G0-01）。
package routecache

import (
	"container/list"
	"errors"
	"strings"
)

// Key 缓存键：规范化 query+身份+角色+情绪上下文（四元组全等才命中——
// 误命中=0 的精确键构造面）。
type Key struct {
	NormQuery string // 规范化后 query（Normalize 产物）
	UserID    string // 身份域（T5 绑定；未验证=空串）
	Role      string // 人格角色（T8 编译面）
	EmoLabel  string // 情绪上下文标签（T7）
}

// Config 缓存配置（fail-closed：上限>0、TTLMs>0、SafeBypass 非 nil——
// 缺省即配置错误，不留「悄悄无限缓存」的缝）。
type Config struct {
	MaxEntries int                 // 条目数硬上限（LRU 淘汰）
	MaxBytes   int64               // 字节预算硬上限（T14 预算联动面）
	TTLMs      int64               // 条目 TTL（毫秒，仿真时钟）
	SafeBypass func(q string) bool // 安全旁路判定（注入面——loop 组装）
}

// Stats 命中/穿透/失效观测面（T15-G1-02 报告口径）。
type Stats struct {
	Hits     int // 命中（零上游延迟）
	Misses   int // 穿透（未命中/过期→上游+回填）
	Expired  int // TTL 过期失效
	Evicted  int // LRU/字节预算淘汰
	Bypassed int // 安全旁路（Get miss + Put 拒收合计）
}

// ErrConfig 配置错误（NewCache fail-closed）。
var ErrConfig = errors.New("routecache: 非法配置（MaxEntries/MaxBytes/TTLMs 须 >0 且 SafeBypass 非 nil）")

// entry 单条缓存（LRU 链表节点载荷）。
type entry struct {
	key      Key
	resp     string
	storedMs int64 // 写入时刻（TTL 判定）
	bytes    int64 // 条目字节占用（resp 本体，键不计入——决策成本口径）
}

// Cache 精确键语义缓存：container/list 双向链表（front=最近使用）+ 键索引。
// 方法非并发安全（loop 单线程组装口径）。
type Cache struct {
	cfg   Config
	order *list.List            // LRU 序（element.Value=*entry）
	index map[Key]*list.Element // 键→节点
	bytes int64                 // 当前字节占用
	stats Stats
}

// NewCache 构造缓存：fail-closed 校验（上限>0/TTLMs>0/SafeBypass 非 nil）。
func NewCache(cfg Config) (*Cache, error) {
	if cfg.MaxEntries <= 0 || cfg.MaxBytes <= 0 || cfg.TTLMs <= 0 || cfg.SafeBypass == nil {
		return nil, ErrConfig
	}
	return &Cache{cfg: cfg, order: list.New(), index: make(map[Key]*list.Element, cfg.MaxEntries)}, nil
}

// Get 键全等命中（TTL 过期=穿透并移除条目；bypass 恒 miss）。命中刷新 LRU
// 最近使用位。返回 (resp, true)=命中零上游延迟。
func (c *Cache) Get(k Key, nowMs int64) (string, bool) {
	if c.cfg.SafeBypass(k.NormQuery) {
		c.stats.Bypassed++
		return "", false // 安全类 query 永不缓存命中（T15-G0-01）
	}
	el, ok := c.index[k]
	if !ok {
		c.stats.Misses++
		return "", false
	}
	e := el.Value.(*entry)
	if nowMs-e.storedMs >= c.cfg.TTLMs { // 过期=失效移除（穿透回填由调用方）
		c.remove(el)
		c.stats.Expired++
		c.stats.Misses++
		return "", false
	}
	c.order.MoveToFront(el) // 命中刷新最近使用
	c.stats.Hits++
	return e.resp, true
}

// Put 回填：bypass 拒收（安全类 query 永不写入）；同键覆盖（旧条目字节先
// 扣回）；容量超限逐最旧未用（LRU）——先条目数后字节预算双门。
func (c *Cache) Put(k Key, resp string, nowMs int64) {
	if c.cfg.SafeBypass(k.NormQuery) {
		c.stats.Bypassed++
		return // 拒收（不落任何形态的存储）
	}
	if el, ok := c.index[k]; ok { // 覆盖旧值（键不变——LRU 位刷新）
		e := el.Value.(*entry)
		c.bytes -= e.bytes
		e.resp, e.storedMs, e.bytes = resp, nowMs, int64(len(resp))
		c.bytes += e.bytes
		c.order.MoveToFront(el)
	} else {
		e := &entry{key: k, resp: resp, storedMs: nowMs, bytes: int64(len(resp))}
		c.index[k] = c.order.PushFront(e)
		c.bytes += e.bytes
	}
	c.evict() // 容量有界：entries≤MaxEntries 且 bytes≤MaxBytes
}

// Invalidate 显式失效单键（不存在=无操作）。
func (c *Cache) Invalidate(k Key) {
	if el, ok := c.index[k]; ok {
		c.remove(el)
	}
}

// Stats 返回观测面快照（T15-G1-02 命中率/降本口径）。
func (c *Cache) Stats() Stats { return c.stats }

// Len 当前条目数（容量有界断言面）。
func (c *Cache) Len() int { return len(c.index) }

// Bytes 当前字节占用（预算硬上限断言面）。
func (c *Cache) Bytes() int64 { return c.bytes }

// remove 摘除节点并回收字节。
func (c *Cache) remove(el *list.Element) {
	e := el.Value.(*entry)
	c.order.Remove(el)
	delete(c.index, e.key)
	c.bytes -= e.bytes
}

// evict 容量有界：条目数>MaxEntries 或 bytes>MaxBytes → 从链表尾（最旧
// 未用）逐条淘汰至双门内（新写入条目至少保留——单条超预算也逐最旧先行，
// 含新条目自身超限时的最终自逐，保证不变量恒成立）。
func (c *Cache) evict() {
	for c.order.Len() > 0 &&
		(c.order.Len() > c.cfg.MaxEntries || c.bytes > c.cfg.MaxBytes) {
		c.remove(c.order.Back())
		c.stats.Evicted++
	}
}

// Normalize query 规范化：去语气词（嘛/呀/啦——口语尾缀，全串剥离）+
// 空白折叠（连续空白→单空格）+ 去首尾空白 + ASCII 小写。语气词改写键
// 不变（「你好呀」≡「你好嘛」≡「你好」）——消抖动属性面。
func Normalize(q string) string {
	if q == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(q))
	for _, r := range q {
		switch r {
		case '嘛', '呀', '啦':
			continue // 语气词剥离
		case ' ', '\t', '\n', '\r', '\v', '\f', '　':
			if b.Len() > 0 && !lastIsSpace(&b) {
				b.WriteByte(' ') // 空白折叠
			}
		default:
			b.WriteRune(toLower(r))
		}
	}
	out := strings.TrimRight(b.String(), " ")
	return out
}

// lastIsSpace 构造器尾字符是否为折叠空格。
func lastIsSpace(b *strings.Builder) bool {
	s := b.String()
	return len(s) > 0 && s[len(s)-1] == ' '
}

// toLower ASCII 小写（非 ASCII 原样——大小写口径限拉丁字母面）。
func toLower(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + ('a' - 'A')
	}
	return r
}
