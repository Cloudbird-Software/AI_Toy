// router —— 规则路由面（T15-G1-01：500 条意图分层标注，规则路由 ≥0.92；
// 安全敏感类路由错误=0 升 G0 行为）。
//
// 规则面（无模型）：决策=规范化+哈希查表（延迟预算 ≤30ms 的构造来源——
// configs/budgets/latency.yaml cloud_llm 段）。安全敏感 query 恒云+永不缓存
// （SafeBypass 注入面，与 Cache 同源——本包不 import safety，考卷隔离）；
// 复杂问句（疑问词/长推理）→ 云；简单话轮（问候/闲聊/预置脚本域）→ 端侧。
package routecache

import (
	"errors"
	"strings"
)

// Route 路由目标。
type Route int8

const (
	RouteCloud Route = iota // 云端 LLM
	RouteEdge               // 端侧（本地语义兜底/预置脚本）
)

// String 返回路由可读名。
func (r Route) String() string {
	if r == RouteCloud {
		return "cloud"
	}
	return "edge"
}

// Decision 单次路由决策（观测面：目标+可缓存性+依据——决策计数口径供
// T15-G1-02 成本核算：cloud 计数=上游调用成本）。
type Decision struct {
	Route     Route
	Cacheable bool   // 是否可进缓存（安全敏感=false）
	EdgeHit   bool   // 端侧预置/本地命中（零上游成本）
	Reason    string // 规则依据（审计面）
}

// ErrRouter router 配置错误（SafeBypass 缺失——fail-closed）。
var ErrRouter = errors.New("routecache: SafeBypass 须非 nil（安全敏感类路由判定不可缺省）")

// Router 规则路由器：SafeBypass（注入）+ 词面规则。零并发（loop 单线程）。
type Router struct {
	safe func(q string) bool
}

// NewRouter 构造路由器（safe=nil → ErrRouter——安全旁路判定不可缺省）。
func NewRouter(safe func(q string) bool) (*Router, error) {
	if safe == nil {
		return nil, ErrRouter
	}
	return &Router{safe: safe}, nil
}

// questionWords 复杂问句词面（知识/推理域的代理特征）。
var questionWords = [...]string{"为什么", "怎么", "怎么办", "是什么", "什么叫", "谁", "哪里",
	"什么时候", "多少", "几岁", "长什么样", "你知道吗", "讲讲", "解释"}

// edgePatterns 端侧预置/本地域词面（问候/闲聊/情绪安抚/固定玩法）。
var edgePatterns = [...]string{"你好", "早上好", "晚上好", "晚安", "再见", "拜拜",
	"陪我玩", "唱首歌", "讲故事", "猜谜语", "我爱你", "抱抱", "开心", "难过", "无聊"}

// minCloudRunes 云端路由的最短 rune 数（短话轮=闲聊域先验）。
const minCloudRunes = 6

// Decide 规则路由决策（输入=原始 query；内部 Normalize 后判规则）：
//  1. 安全敏感（SafeBypass）→ 云 + 不可缓存（路由错误=0 的 G0 行为面）；
//  2. 疑问词命中或长话轮（≥minCloudRunes 且非预置域）→ 云 + 可缓存；
//  3. 简单话轮（预置域词面/短句）→ 端侧。
func (r *Router) Decide(q string) Decision {
	norm := Normalize(q)
	if r.safe(norm) {
		return Decision{Route: RouteCloud, Cacheable: false, Reason: "safety_sensitive"}
	}
	for _, p := range edgePatterns {
		if strings.Contains(norm, p) {
			return Decision{Route: RouteEdge, Cacheable: true, EdgeHit: true, Reason: "edge_pattern:" + p}
		}
	}
	for _, w := range questionWords {
		if strings.Contains(norm, w) {
			return Decision{Route: RouteCloud, Cacheable: true, Reason: "question_word:" + w}
		}
	}
	if runeLen(norm) >= minCloudRunes {
		return Decision{Route: RouteCloud, Cacheable: true, Reason: "long_utterance"}
	}
	return Decision{Route: RouteEdge, Cacheable: true, EdgeHit: true, Reason: "short_chitchat"}
}

// runeLen rune 数（短句判定的长度口径）。
func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
