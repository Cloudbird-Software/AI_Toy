// arbiter —— 状态面：loop 组装的档位仲裁器（m3-spec §2 契约 A 状态面）。
//
// 契约真身（Runtime）是纯函数式（状态显式传参）；Arbiter 是其有状态组装面：
// 组件档位表 + 故障/恢复事件进 → 迁移记录（Transition）与全局档（Tier）出，
// loop 组装时 Arbiter.Tier() → Config.Tier（ADR-0004：包间零 import——loop 与
// 本包互不引用，接线在组装层/测试侧）。时钟=显式 atMs 仿真时钟（无墙钟，
// 确定性回放；冷启动 CI 墙钟口径见 gates_test T14-G1-03）。
package runtimefsm

import (
	"sort"

	"github.com/Cloudbird-Software/AI_Toy/tests/properties"
)

// RecoverScope 恢复作用域（OnRecover 的恢复面口径）。
type RecoverScope int

const (
	RecoverNone    RecoverScope = iota // 无恢复（空事件）
	RecoverNetwork                     // 网络恢复：非 Safety 组件有限时间回 L0（活性）
	RecoverAll                         // 全量恢复（冷启动/测试复位面）
)

// String 返回作用域可读名。
func (s RecoverScope) String() string {
	switch s {
	case RecoverNetwork:
		return "network"
	case RecoverAll:
		return "all"
	}
	return "none"
}

// Transition 一次切档迁移记录（事务性切档的观测面：From→To 逐组件原子提交，
// 一次 OnFault/OnRecover 全部生效或全部不生效——无中间态）。
type Transition struct {
	Fault  properties.FailureType   // 触发故障（恢复事件=FailNone）
	Comp   properties.Component     // 迁移组件
	From   properties.Tier          // 迁移前档位
	To     properties.Tier          // 迁移后档位
	Action properties.DegradeAction // 该次故障允许的降级动作（CI-4 预定义集；恢复=ActNone）
	AtMs   int64                    // 仿真时刻
}

// Arbiter 档位仲裁器：组件档位表 + 恢复计时（仿真时钟）。零值不可用——经
// NewArbiter 构造（全组件 L0 起步）。方法非并发安全（loop 单线程组装口径）。
type Arbiter struct {
	r              Runtime                // 契约真身（FailApply 语义同源）
	comps          properties.CompTierMap // 当前组件档位表
	lastMs         int64                  // 最近事件时刻（单调门）
	netDownSinceMs int64                  // 网络故障起始（活性观测：恢复须有限时间回 L0）
	netDown        bool                   // 网络断开中（FailLLMConnect 置位）
}

// NewArbiter 构造仲裁器：全组件 L0（云端全能力起步）。
func NewArbiter() *Arbiter {
	var m properties.CompTierMap
	for i := range m {
		m[i] = properties.L0
	}
	return &Arbiter{comps: m}
}

// OnFault 故障作用：FailApply 应用 + 迁移记录发布（事务性——档位表整体
// 一次提交）。返回本次实际发生的迁移（无变化=空切片，非 nil 安全）。
// atMs 须单调不减（迟到事件钳制为 lastMs——与 loop FSM 单调门对齐）。
func (a *Arbiter) OnFault(f properties.FailureType, atMs int64) []Transition {
	if atMs < a.lastMs {
		atMs = a.lastMs // 迟到事件钳制（仿真时钟单调）
	}
	a.lastMs = atMs
	if f == properties.FailNone {
		return nil
	}
	before := a.comps
	a.comps = a.r.FailApply(a.comps, f)
	if f == properties.FailLLMConnect && !a.netDown {
		a.netDown, a.netDownSinceMs = true, atMs
	}
	return a.transitions(f, before, a.comps, atMs)
}

// OnRecover 恢复作用（活性）：网络恢复→非 Safety 组件回 L0（CompSafety 不动
// ——安全档位只由故障路径约束）；全量恢复→含 Safety 全组件回 L0。恢复可
// 重复调用（幂等——已恢复组件无迁移产出；「别把恢复写成一次性事件」）。
// 网络恢复后再次断网 → netDown 重新计时（有限时间回 L0 的活性上限观测面）。
func (a *Arbiter) OnRecover(scope RecoverScope, atMs int64) []Transition {
	if atMs < a.lastMs {
		atMs = a.lastMs
	}
	a.lastMs = atMs
	if scope == RecoverNone {
		return nil
	}
	before := a.comps
	for i := range a.comps {
		c := properties.Component(i)
		if scope == RecoverNetwork && c == properties.CompSafety {
			continue // 网络恢复不碰 Safety 档（非网络域组件）
		}
		a.comps[i] = properties.L0
	}
	if scope == RecoverNetwork {
		a.netDown = false
	}
	return a.transitions(properties.FailNone, before, a.comps, atMs)
}

// NetDownMs 网络断开持续时长（活性观测面：恢复延时=atMs−netDownSinceMs，
// 有限时间回 L0 断言用；未断网=0）。
func (a *Arbiter) NetDownMs(atMs int64) int64 {
	if !a.netDown {
		return 0
	}
	if atMs < a.netDownSinceMs {
		return 0
	}
	return atMs - a.netDownSinceMs
}

// Tier 全局档（注入 loop.Config.Tier）：组件档位最大值（最保守档决定全局
// ——任一组件深降级，全局按其档执行）。
func (a *Arbiter) Tier() int {
	t := a.comps[0]
	for _, v := range a.comps[1:] {
		if v > t {
			t = v
		}
	}
	return int(t)
}

// CompTiers 返回组件档位表快照（值拷贝——外部只读）。
func (a *Arbiter) CompTiers() properties.CompTierMap {
	return a.comps
}

// transitions 生成 before→after 差分迁移记录（逐组件；仅档位实际变化者），
// 按组件序稳定排序（确定性：同事件序列同记录）。
func (a *Arbiter) transitions(f properties.FailureType, before, after properties.CompTierMap, atMs int64) []Transition {
	var act properties.DegradeAction
	if f != properties.FailNone {
		act = primaryAction(f)
	}
	out := make([]Transition, 0, int(properties.NumComponents))
	for i := 0; i < int(properties.NumComponents); i++ {
		if before[i] == after[i] {
			continue
		}
		out = append(out, Transition{Fault: f, Comp: properties.Component(i),
			From: before[i], To: after[i], Action: act, AtMs: atMs})
	}
	sort.Slice(out, func(x, y int) bool { return out[x].Comp < out[y].Comp })
	return out
}

// primaryAction 故障→主降级动作（DegradeMap 允许集的确定性代表：CI-4 允许
// 集非空；按降档深浅偏好序取第一个允许动作；空集兜底 ActApology——诚实告知）。
func primaryAction(f properties.FailureType) properties.DegradeAction {
	m := degradeMap(f)
	for _, a := range []properties.DegradeAction{properties.ActDropToL1, properties.ActDropToL2,
		properties.ActDropToL3, properties.ActMemoryReadOnly, properties.ActVoiceprintRetry,
		properties.ActIMUThrottle, properties.ActTimeFreeze, properties.ActRollback, properties.ActApology} {
		if m[a] {
			return a
		}
	}
	return properties.ActApology
}
