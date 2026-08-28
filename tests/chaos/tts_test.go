// 运行时镜像，实现落地后替换：CH-02 TTS 超时/首包失败（spec §8.3，G1）。
//
// 三段：注入（云 TTS 首包失败/流中超时，静默开始）→ 系统响应（静默 ≤2s 内
// 端侧补偿出声；已出口半句不重播）→ 恢复（下一轮回云档）。
// packages/go/tts（T13）落地后替换本镜像。
package chaos

import (
	"strings"
	"testing"
)

// ttsSilenceBudgetMS CH-02 的 G1 界：静默 ≤2s（端侧补偿必须在此之前出声）。
const ttsSilenceBudgetMS = 2000

// ttsFault 注入形态：首包失败（无音频出口）/ 流中超时（半句已出口）。
type ttsFault struct {
	mode         string // first_packet | mid_stream
	spokenPrefix string // mid_stream：超时前已出口的半句
	atMS         int    // 故障（静默开始）时刻
}

// ttsTurn 一次云 TTS 输出轮的镜像（纯数据）。
type ttsTurn struct {
	silenceStart int      // 静默起点（注入后毫秒）
	compensateAt int      // 端侧补偿出声时刻；-1=未补偿
	audio        []string // 出口音频文本流（按播放序）
}

// injectTTSFault 注入（三段之一）：云 TTS 超时/首包失败，静默开始。
// mid_stream 超时时半句已经出口（记入音频流）；first_packet 失败则无任何音频。
func injectTTSFault(f ttsFault) ttsTurn {
	turn := ttsTurn{silenceStart: f.atMS, compensateAt: -1}
	if f.mode == "mid_stream" && f.spokenPrefix != "" {
		turn.audio = append(turn.audio, f.spokenPrefix)
	}
	return turn
}

// ttsCompensate 系统响应（三段之二）：端侧补偿出声——全新完整话术，
// 不重播已出口的半句；至多补偿一次。
func ttsCompensate(turn ttsTurn, decideAtMS int) ttsTurn {
	if turn.compensateAt < 0 {
		turn.compensateAt = decideAtMS
		turn.audio = append(turn.audio, "（端侧补偿）哎呀，刚才卡了一下，我们接着说——")
	}
	return turn
}

// ttsSilenceMS 静默时长（注入 → 补偿出声）。
func ttsSilenceMS(turn ttsTurn) int {
	return turn.compensateAt - turn.silenceStart
}

// ttsNextRoute 恢复（三段之三）：故障轮结束后下一轮的路由——云恢复即回云档。
func ttsNextRoute(cloudRestored bool) string {
	if cloudRestored {
		return "cloud"
	}
	return "edge"
}

// occurrences 统计片段在音频流中的出现次数（元素级包含计数）。
func occurrences(audio []string, frag string) int {
	n := 0
	for _, a := range audio {
		n += strings.Count(a, frag)
	}
	return n
}

func TestTTSTimeout(t *testing.T) {
	row, ok := Row(RowTTS)
	if !ok {
		t.Fatalf("矩阵行 %s 未登记", RowTTS)
	}
	if row.Gate != GateG1 {
		t.Fatalf("%s 门禁级别=%s，须 G1（合并阻断）", row.ID, row.Gate)
	}

	const half = "今天我给你讲一个小熊的故事，它住在森林深处"
	faults := []ttsFault{
		{mode: "first_packet", atMS: 600},
		{mode: "mid_stream", spokenPrefix: half, atMS: 1200},
	}
	for _, f := range faults {
		t.Run(f.mode, func(t *testing.T) {
			// ── 三段之一：注入 ──
			turn := injectTTSFault(f)
			if turn.compensateAt != -1 {
				t.Fatal("注入后补偿未就绪（compensateAt 须为 -1）")
			}

			// ── 三段之二：系统响应 ──
			// 边界：静默恰好 2s 时补偿仍合规。
			turn = ttsCompensate(turn, f.atMS+ttsSilenceBudgetMS)
			if got := ttsSilenceMS(turn); got < 0 || got > ttsSilenceBudgetMS {
				t.Fatalf("%s：静默 %dms，须 ∈ [0,%d]", f.mode, got, ttsSilenceBudgetMS)
			}
			if n := occurrences(turn.audio, half); n > 1 {
				t.Fatalf("%s：半句重播（出现 %d 次，须 ≤1）", f.mode, n)
			}
			// 补偿是全新完整话术，不拼半句。
			if last := turn.audio[len(turn.audio)-1]; strings.Contains(last, half) {
				t.Fatalf("%s：补偿话术含半句残段：%q", f.mode, last)
			}
			// 故障未清除：下一轮仍走端侧。
			if route := ttsNextRoute(false); route != "edge" {
				t.Fatalf("%s：故障期下一轮路由=%s，须 edge", f.mode, route)
			}

			// ── 三段之三：恢复（下轮回云档）──
			if route := ttsNextRoute(true); route != "cloud" {
				t.Fatalf("%s：云恢复后下一轮路由=%s，须 cloud", f.mode, route)
			}

			// 负例（门禁有牙）：静默 2001ms 才补偿即违规，须被捕获。
			late := ttsCompensate(injectTTSFault(f), f.atMS+ttsSilenceBudgetMS+1)
			if ttsSilenceMS(late) <= ttsSilenceBudgetMS {
				t.Fatalf("%s：静默 %dms 超 %dms 须判违规", f.mode, ttsSilenceMS(late), ttsSilenceBudgetMS)
			}
		})
	}
}
