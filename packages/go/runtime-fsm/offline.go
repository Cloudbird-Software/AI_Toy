// offline —— L2/L3 离线应答规则面（m3-spec §2 / T14-G0-01 降级诚实性）。
//
// 技术路径 C「功能收缩式离线」（AGENTS.md 路径表）：检索式（本地知识精确
// 匹配）+ 能力边界声明（未命中→诚实拒绝，不编造）。降级诚实性的构造保证：
// 应答只可能来自本地知识集（命中）或边界声明话术（未命中）——不存在第三
// 种产出通道，编造=0 由构造封闭 + 门禁实测双保险（真模型接入后同测复测）。
//
// 拒绝话术须过 T9 词表（门禁测试侧 import safety 断言——本包不 import safety，
// 考卷隔离红线不破）；话术本体为预定义常量（变更须同步 T14-G0-01 复测）。
package runtimefsm

import (
	"fmt"
	"strings"
)

// 边界声明话术（诚实拒绝——不编造、不冒充云端能力；儿童亲和口径）。
const (
	// RefusalTemplate 边界声明模板：%s=能力面说明（按档位注入）。
	RefusalTemplate = "这个问题我现在离线答不了，%s。等我连上网再告诉你，好吗？"

	// BoundaryL2 L2 档能力边界（端侧对话模型面）。
	BoundaryL2 = "离线的时候我只会讲学过的东西"

	// BoundaryL3 L3 档能力边界（仅预置脚本面）。
	BoundaryL3 = "离线的时候我只能陪你聊固定的话题"
)

// Offline L2/L3 检索式应答器：本地知识集（规范化问题→答案）+ 档位能力
// 约束。零值不可用——经 NewOffline 构造。
type Offline struct {
	known map[string]string // 规范化问题 → 答案（只读视图）
}

// NewOffline 构造离线应答器（known 为 nil 时=空知识集——全部诚实拒绝）。
// 键须为规范化形式（NormalizeQuery 产物；构造期不改写——fail-closed，
// 键形错误=永远不命中=诚实拒绝，不会误答）。
func NewOffline(known map[string]string) *Offline {
	m := make(map[string]string, len(known))
	for k, v := range known {
		m[k] = v
	}
	return &Offline{known: m}
}

// NormalizeQuery 问题规范化：空白折叠（连续空白→单空格）+ 去首尾空白 +
// ASCII 小写。检索键的唯一入口（同义语气改写共享一键的语义面归 T15
// routecache.Normalize——本函数只做检索面最小规范化，不复制语气词规则）。
func NormalizeQuery(q string) string {
	fields := strings.Fields(strings.ToLower(q))
	return strings.Join(fields, " ")
}

// Answer 检索式应答：本地知识命中→返回答案（refused=false）；未命中→
// 按档位的边界声明话术（tier=2→BoundaryL2，其余→BoundaryL3——L0/L1 云端
// 面不走本应答器，保守按 L3 话术）。永不编造（产出通道封闭：known ∪
// {refusal}）。
func (o *Offline) Answer(q string, tier int) (resp string, refused bool) {
	if ans, ok := o.known[NormalizeQuery(q)]; ok {
		return ans, false
	}
	boundary := BoundaryL3
	if tier == 2 {
		boundary = BoundaryL2
	}
	return fmt.Sprintf(RefusalTemplate, boundary), true
}

// Known 查询某规范化问题是否在本地知识集（观测面——门禁/属性测试用）。
func (o *Offline) Known(normQ string) bool {
	_, ok := o.known[normQ]
	return ok
}

// Size 本地知识集条目数（观测面）。
func (o *Offline) Size() int {
	return len(o.known)
}
