// 属性测试（spec §6 三件套之二，testing/quick——AGENTS.md 统计纪律）：
// P1 同文本+同种子音频哈希一致；P2 输出时长随文本长度单调增；P3 不可见控制
// 字符剥离后不影响可听输出；P4 Cancel 幂等+已播不回退。（P5 拦截完备在
// gates_test.go——T13-G0-01 真实测点，对抗句表驱动非 quick。）
package tts

import (
	"hash/fnv"
	"testing"
	"testing/quick"
)

// audioHash 读出全部字节并取 FNV-1a（音频一致性断言面）。
func audioHash(s AudioStream) uint64 {
	h := fnv.New64a()
	for {
		c, err := s.Next()
		if err != nil {
			return h.Sum64()
		}
		h.Write(c.Data)
	}
}

// quickText 非空可打印文本生成器（空文本走 ErrEmptyText 另测）。
func quickText(s []byte) string {
	if len(s) == 0 {
		return "默认话术"
	}
	t := string(s)
	if t == "" {
		return "默认话术"
	}
	return t
}

// PropertyAudioDeterministic P1：同文本+同音色+同种子（deterministicSynth seed）
// 两次合成，音频哈希一致（Router 决策与透传无随机漂移）。
func PropertyAudioDeterministic(text, voice string) bool {
	text = quickText([]byte(text))
	voice = quickText([]byte(voice))
	r, err := NewRouter(RouterConfig{
		PreSpeak:             allowAll,
		Cloud:                newDeterministicSynth(),
		FirstPacketTimeoutMs: 50,
		SilenceCapMs:         100,
	})
	if err != nil {
		return false
	}
	h1, err := hashOnce(r, text, voice)
	if err != nil {
		return false
	}
	h2, err := hashOnce(r, text, voice)
	if err != nil {
		return false
	}
	return h1 == h2
}

func hashOnce(r *Router, text, voice string) (uint64, error) {
	st, err := r.Synthesize(Request{Text: text, Voice: voice, Tier: 0, TurnID: "p1"})
	if err != nil {
		return 0, err
	}
	return audioHash(st), nil
}

// PropertyDurationMonotonic P2：输出总字节随文本长度单调不减（突变=坏输出预警）。
func PropertyDurationMonotonic(a, b string) bool {
	a, b = quickText([]byte(a)), quickText([]byte(b))
	if len(a) > len(b) {
		a, b = b, a // 固定 len(a) <= len(b)
	}
	ra, err := NewRouter(RouterConfig{
		PreSpeak:             allowAll,
		Cloud:                newDeterministicSynth(),
		FirstPacketTimeoutMs: 50,
		SilenceCapMs:         100,
	})
	if err != nil {
		return false
	}
	sa, err := ra.Synthesize(Request{Text: a, Tier: 0, TurnID: "pa"})
	if err != nil {
		return false
	}
	sb, err := ra.Synthesize(Request{Text: b, Tier: 0, TurnID: "pb"})
	if err != nil {
		return false
	}
	return totalBytes(sa) <= totalBytes(sb)
}

func totalBytes(s AudioStream) int64 {
	var n int64
	for {
		c, err := s.Next()
		n += int64(len(c.Data))
		if err != nil {
			return n
		}
	}
}

// PropertyControlCharInaudible P3：不可见控制字符剥离前后，可听输出一致
// （音频哈希相等——拦截面在 PreSpeak 处理原始文本，合成面不受控制字符影响）。
func PropertyControlCharInaudible(text string) bool {
	text = quickText([]byte(text))
	dirty := "​" + text + "​\x00" // 零宽空格包裹 + NUL 夹带
	r, err := NewRouter(RouterConfig{
		PreSpeak:             allowAll, // 拦截桩对控制字符放行（本属性只测合成面）
		Cloud:                newDeterministicSynth(),
		FirstPacketTimeoutMs: 50,
		SilenceCapMs:         100,
	})
	if err != nil {
		return false
	}
	hc, err := hashOnce(r, text, "")
	if err != nil {
		return false
	}
	hd, err := hashOnce(r, dirty, "")
	if err != nil {
		return false
	}
	return hc == hd
}

// PropertyCancelIdempotentNoReplay P4：任意 chunk 序列上任意时刻 Cancel——
// Cancel 恒 nil（幂等）、已交付字节串是完整流前缀（不回退、不重复、不续播）。
func PropertyCancelIdempotentNoReplay(text string, cancelAfter uint8) bool {
	text = quickText([]byte(text))
	synth := newDeterministicSynth()
	r, err := NewRouter(RouterConfig{
		PreSpeak:             allowAll,
		Cloud:                synth,
		FirstPacketTimeoutMs: 50,
		SilenceCapMs:         100,
	})
	if err != nil {
		return false
	}
	full, err := r.Synthesize(Request{Text: text, Tier: 0, TurnID: "full"})
	if err != nil {
		return false
	}
	var want []byte
	for {
		c, err := full.Next()
		if err != nil {
			break
		}
		want = append(want, c.Data...)
	}
	// 重置合成桩状态（同 seed 确定性）
	part, err := r.Synthesize(Request{Text: text, Tier: 0, TurnID: "part"})
	if err != nil {
		return false
	}
	var got []byte
	n := int(cancelAfter)
	for i := 0; i < n; i++ {
		c, err := part.Next()
		if err != nil {
			break // 流已自然终止
		}
		got = append(got, c.Data...)
	}
	for i := 0; i < 3; i++ { // Cancel 幂等 ×3
		if err := r.Cancel("part"); err != nil {
			return false
		}
	}
	if _, err := part.Next(); err == nil {
		return false // 终止后不得续播
	}
	if _, err := part.Next(); err == nil {
		return false
	}
	// 已播前缀不回退：got 是 want 的严格前缀
	return len(got) <= len(want) && string(want[:len(got)]) == string(got)
}

func TestPropertiesT13(t *testing.T) {
	t.Run("P1 同文本同种子音频一致", func(t *testing.T) {
		if err := quick.Check(PropertyAudioDeterministic, nil); err != nil {
			t.Fatalf("P1: %v", err)
		}
	})
	t.Run("P2 时长随文本长度单调", func(t *testing.T) {
		if err := quick.Check(PropertyDurationMonotonic, nil); err != nil {
			t.Fatalf("P2: %v", err)
		}
	})
	t.Run("P3 控制字符不影响可听输出", func(t *testing.T) {
		if err := quick.Check(PropertyControlCharInaudible, nil); err != nil {
			t.Fatalf("P3: %v", err)
		}
	})
	t.Run("P4 Cancel 幂等+已播不回退", func(t *testing.T) {
		if err := quick.Check(PropertyCancelIdempotentNoReplay, nil); err != nil {
			t.Fatalf("P4: %v", err)
		}
	})
}
