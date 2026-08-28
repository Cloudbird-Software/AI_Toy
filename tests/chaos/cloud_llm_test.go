// 运行时镜像，实现落地后替换：CH-01 云 LLM 断连/5xx/限流（spec §8.3，G0）。
//
// 三段：注入（云不可达，在途半截回复必须丢弃）→ 系统响应（≤2 档内 3s
// 恢复对话 + 诚实告知受限）→ 恢复（≤30s 回 L0，全程无脏输出）。
// packages/go/router（T15 路由缓存）落地后替换本镜像。
package chaos

import (
	"strings"
	"testing"
)

// cloudTier 路由档位：L0 云档 / L1 混合 / L2 端侧。
type cloudTier int

const (
	tierL0 cloudTier = iota
	tierL1
	tierL2
)

// cloudBudgets CH-01 的 G0 界（spec §8.3 期望/恢复列）。
const (
	cloudFailoverBudgetMS = 3000  // ≤2 档内 3s 恢复对话
	cloudTierDropMax      = 2     // 降档步数上限
	cloudRejoinBudgetMS   = 30000 // ≤30s 回 L0
)

// cloudFault 注入形态与检出耗时（注入后毫秒）。
type cloudFault struct {
	kind     string // disconnect | http_5xx | rate_limit
	detectMS int
}

// cloudReply 一次接话的对外表现（可观测面）。
type cloudReply struct {
	answered bool
	notice   string // 诚实告知受限文案；空=未告知
	text     string // 对外输出文本
}

// cloudRuntime 云链路运行时镜像（纯数据）。
type cloudRuntime struct {
	tier       cloudTier
	cloudDown  bool
	failoverAt int  // 降档完成、对话恢复时刻；-1=未注入
	noticed    bool // 是否已诚实告知受限
	rejoinedAt int  // 回 L0 时刻；-1=未回
}

// newCloudRuntime 注入前的健康运行时（L0 云档）。
func newCloudRuntime() cloudRuntime {
	return cloudRuntime{tier: tierL0, failoverAt: -1, rejoinedAt: -1}
}

// cloudSwitchCostMS 域内策略：各故障形态的档位切换成本（毫秒）。
func cloudSwitchCostMS(kind string) int {
	switch kind {
	case "http_5xx":
		return 800 // 一次快速重试后改路由
	case "rate_limit":
		return 1500 // 退避窗口后降档
	default:
		return 1200 // 断连：全链路重路由
	}
}

// injectCloudFault 注入（三段之一）：云不可达。failover = 检出耗时 + 切换成本；
// 限流直落 L2（混合档同样吃紧），断连/5xx 落 L1——均 ≤2 档。
func injectCloudFault(r cloudRuntime, f cloudFault) cloudRuntime {
	r.cloudDown = true
	r.failoverAt = f.detectMS + cloudSwitchCostMS(f.kind)
	if f.kind == "rate_limit" {
		r.tier = tierL2
	} else {
		r.tier = tierL1
	}
	return r
}

// cloudTurn 系统响应（三段之二）：用户在 nowMS 说话。
// 检出/降档完成前静默等待（不出半截输出）；完成后在降档上接话并诚实告知一次。
func cloudTurn(r cloudRuntime, nowMS int) (cloudRuntime, cloudReply) {
	rep := cloudReply{}
	if !r.cloudDown && r.tier == tierL0 {
		rep.answered = true
		rep.text = "（云档完整回复）"
		return r, rep
	}
	if r.failoverAt < 0 || nowMS < r.failoverAt {
		return r, rep
	}
	rep.answered = true
	rep.text = "我在呢，先用口袋里的脑子陪你聊一会儿。"
	if !r.noticed {
		r.noticed = true
		rep.notice = "（小提示：我有点连不上网，可能记不住新事情哦。）"
	}
	return r, rep
}

// cloudHeal 恢复（三段之三）：云侧 healedAtMS 恢复，探针成本 rejoinCostMS 后回 L0。
func cloudHeal(r cloudRuntime, healedAtMS, rejoinCostMS int) cloudRuntime {
	r.cloudDown = false
	r.rejoinedAt = healedAtMS + rejoinCostMS
	r.tier = tierL0
	return r
}

// rejoinCompliant 回 L0 是否落在 30s 预算内（门禁判定）。
func rejoinCompliant(r cloudRuntime, healedAtMS int) bool {
	return r.rejoinedAt >= 0 && r.rejoinedAt-healedAtMS <= cloudRejoinBudgetMS
}

func TestLLMOutage(t *testing.T) {
	row, ok := Row(RowCloudLLM)
	if !ok {
		t.Fatalf("矩阵行 %s 未登记", RowCloudLLM)
	}
	if row.Gate != GateG0 {
		t.Fatalf("%s 门禁级别=%s，须 G0（发布阻断）", row.ID, row.Gate)
	}

	// 注入形态 × 检出耗时：断连 / 5xx / 限流。
	faults := []cloudFault{
		{kind: "disconnect", detectMS: 400},
		{kind: "http_5xx", detectMS: 200},
		{kind: "rate_limit", detectMS: 900},
	}
	const inFlight = "从前有一座" // 注入瞬间的在途半截回复

	for _, f := range faults {
		t.Run(f.kind, func(t *testing.T) {
			healthy := newCloudRuntime()
			if healthy.tier != tierL0 {
				t.Fatalf("注入前档位=%d，须 L0", healthy.tier)
			}

			// ── 三段之一：注入 ──
			r := injectCloudFault(healthy, f)
			if !r.cloudDown {
				t.Fatal("注入后云侧须标记不可达")
			}
			drop := int(r.tier) - int(healthy.tier)
			if drop < 1 || drop > cloudTierDropMax {
				t.Fatalf("%s：降档步数=%d，须 ∈ [1,%d]", f.kind, drop, cloudTierDropMax)
			}
			if r.failoverAt > cloudFailoverBudgetMS {
				t.Fatalf("%s：对话恢复=%dms，须 ≤%dms", f.kind, r.failoverAt, cloudFailoverBudgetMS)
			}

			// ── 三段之二：系统响应 ──
			var texts []string
			notices, firstAnswered := 0, -1
			var rep cloudReply
			for atMS := 0; atMS <= cloudFailoverBudgetMS; atMS += 250 {
				r, rep = cloudTurn(r, atMS)
				if rep.answered && firstAnswered < 0 {
					firstAnswered = atMS
				}
				if rep.notice != "" {
					notices++
				}
				if rep.text != "" {
					texts = append(texts, rep.text)
				}
			}
			if firstAnswered < 0 || firstAnswered > cloudFailoverBudgetMS {
				t.Fatalf("%s：首个完整回复时刻=%dms，须 ≤%dms", f.kind, firstAnswered, cloudFailoverBudgetMS)
			}
			if notices != 1 {
				t.Fatalf("%s：诚实告知须恰好一次，得 %d 次", f.kind, notices)
			}
			if strings.Contains(strings.Join(texts, "\n"), inFlight) {
				t.Fatalf("%s：在途半截输出泄漏（无脏输出是 G0 断言）", f.kind)
			}

			// ── 三段之三：恢复（≤30s 回 L0）──
			const healedAt = 60_000
			for _, rejoinCost := range []int{2000, 10_000, 29_000} {
				healed := cloudHeal(r, healedAt, rejoinCost)
				if !rejoinCompliant(healed, healedAt) {
					t.Fatalf("%s：回 L0 耗时 %dms 须 ≤%dms", f.kind, healed.rejoinedAt-healedAt, cloudRejoinBudgetMS)
				}
				if healed.tier != tierL0 {
					t.Fatalf("%s：恢复后档位=%d，须回 L0", f.kind, healed.tier)
				}
				_, rep := cloudTurn(healed, healed.rejoinedAt+500)
				if !rep.answered || rep.notice != "" {
					t.Fatalf("%s：恢复后接话 answered=%v notice=%q，须无受限告知", f.kind, rep.answered, rep.notice)
				}
			}
			// 负例（门禁有牙）：重连成本超出 30s 预算必须判违规。
			over := cloudHeal(r, healedAt, 31_000)
			if rejoinCompliant(over, healedAt) {
				t.Fatalf("%s：重连 %dms 超预算 %dms 须判违规", f.kind, over.rejoinedAt-healedAt, cloudRejoinBudgetMS)
			}
		})
	}
}
