// 运行时镜像，实现落地后替换：CH-05 声纹拒判（spec §8.3，G0）。
//
// 三段：注入（声纹识别失败/拒判，身份绑定失效）→ 系统响应（CI-2：身份通道
// 转只读缓存 + 对孩子明示不确定，绝不冒认、绝不在拒判期写身份记忆）→
// 恢复（识别成功即恢复正常读写）。packages/go/kws+memory（T5/T10，CI-2
// 身份绑定）落地后替换本镜像。
package chaos

import "testing"

// voiceRuntime 身份通道镜像（纯数据）。
type voiceRuntime struct {
	identified     string   // 当前绑定身份；空=不确定
	readOnly       bool     // CI-2 只读模式
	uncertainSay   string   // 明示不确定的对外话术；空=未明示
	identityWrites []string // 身份通道写操作记录（时间序）
	cacheReads     int      // 只读缓存读取次数
}

// newVoiceRuntime 健康状态：说话人已识别。
func newVoiceRuntime(speaker string) voiceRuntime {
	return voiceRuntime{identified: speaker}
}

// injectVoiceReject 注入（三段之一）：声纹拒判——识别失败，身份绑定失效。
func injectVoiceReject(r voiceRuntime) voiceRuntime {
	r.identified = ""
	r.readOnly = true
	r.uncertainSay = ""
	return r
}

// voiceTurn 系统响应（三段之二）：拒判期间的一轮对话——只读缓存仍可读，
// 首轮对外明示不确定（之后保持同一话术，不反复打扰）。
func voiceTurn(r voiceRuntime, atMS int) voiceRuntime {
	if !r.readOnly {
		return r
	}
	r.cacheReads++
	if r.uncertainSay == "" {
		r.uncertainSay = "我不太确定现在是不是你在跟我说话哦，先不记新事情啦。"
	}
	return r
}

// voiceWrite 身份通道写入：只读模式必须拒绝（CI-2）。
func voiceWrite(r voiceRuntime, entry string) voiceRuntime {
	if r.readOnly {
		return r
	}
	r.identityWrites = append(r.identityWrites, entry)
	return r
}

// voiceAttribution 当前输出的身份归属：拒判期必须 unknown（可回溯、不冒认）。
func voiceAttribution(r voiceRuntime) string {
	if r.identified == "" {
		return "unknown"
	}
	return r.identified
}

// voiceIdentifyOK 恢复（三段之三）：识别成功——身份重新绑定，恢复正常读写。
func voiceIdentifyOK(r voiceRuntime, speaker string) voiceRuntime {
	r.identified = speaker
	r.readOnly = false
	r.uncertainSay = ""
	return r
}

func TestVoiceprintReject(t *testing.T) {
	row, ok := Row(RowVoiceprint)
	if !ok {
		t.Fatalf("矩阵行 %s 未登记", RowVoiceprint)
	}
	if row.Gate != GateG0 {
		t.Fatalf("%s 门禁级别=%s，须 G0（发布阻断）", row.ID, row.Gate)
	}

	r := newVoiceRuntime("朵朵")

	// 健康期：写入放行，归属明确。
	r = voiceWrite(r, "朵朵学会了骑自行车")
	if got := len(r.identityWrites); got != 1 {
		t.Fatalf("健康期身份写入=%d 条，须 1", got)
	}
	if got := voiceAttribution(r); got != "朵朵" {
		t.Fatalf("健康期归属=%s，须 朵朵", got)
	}

	// ── 三段之一：注入（声纹拒判）──
	r = injectVoiceReject(r)
	if r.identified != "" || !r.readOnly {
		t.Fatalf("拒判后身份=%q readOnly=%v，须身份清空+只读", r.identified, r.readOnly)
	}
	if got := voiceAttribution(r); got != "unknown" {
		t.Fatalf("拒判期归属=%s，须 unknown（绝不冒认）", got)
	}

	// ── 三段之二：系统响应（CI-2 只读模式 + 明示不确定）──
	r = voiceTurn(r, 100)
	if r.uncertainSay == "" {
		t.Fatal("拒判期未明示不确定")
	}
	if r.cacheReads == 0 {
		t.Fatal("只读缓存须可读（既有记忆可用）")
	}
	// 拒判期身份写入必须被拒。
	r = voiceWrite(r, "来路不明的秘密")
	if got := len(r.identityWrites); got != 1 {
		t.Fatalf("只读模式下身份写入须被拒（写入数=%d，须保持 1）", got)
	}
	// 多轮终端态稳定：话术只说一次且不再变化。
	stable := voiceTurn(voiceTurn(r, 200), 300)
	if stable.uncertainSay != r.uncertainSay {
		t.Fatal("拒判期明示话术须稳定不变")
	}

	// ── 三段之三：恢复（识别成功即恢复）──
	r = voiceIdentifyOK(r, "朵朵")
	if r.readOnly || r.identified == "" {
		t.Fatalf("恢复后 readOnly=%v 身份=%q，须正常绑定", r.readOnly, r.identified)
	}
	r = voiceWrite(r, "朵朵学会了游泳")
	if got := len(r.identityWrites); got != 2 {
		t.Fatalf("恢复后身份写入须放行（写入数=%d，须 2）", got)
	}
	if got := voiceAttribution(r); got != "朵朵" {
		t.Fatalf("恢复后归属=%s，须 朵朵", got)
	}
	r = voiceTurn(r, 1000)
	if r.uncertainSay != "" {
		t.Fatal("恢复后不得再示不确定")
	}
}
