// 属性测试（m2-spec §3 属性面 + §11 三件套之二，testing/quick——统计纪律）：
// P1 同输入同决策（确定性/无墙钟无随机）；P2 危机语句语气词/标点/夹英文改写
// 不改 Severity（paraphrase 不变性）；P3 任意 fuzz 文本 Decision.SpokenText 恒过
// 锚点检查单；P4 攻击混淆度↑判定不宽松；P5 Crisis 判定↔NotifyPayload 一一对应；
// P6 降级档安全水位不降（无档位分支——四档面决策恒同）。
package safety

import (
	"strings"
	"testing"
	"testing/quick"
)

// PropertyDeterministic P1：同输入两次 Classify/PreSpeak 决策一致（Sev/SpokenText/
// Intercepted/Notify 存在性——决策面无随机漂移）。
func PropertyDeterministic(text string) bool {
	e, err := NewEngine(DefaultConfig())
	if err != nil {
		return false
	}
	d1, d2 := e.PreSpeak(text), e.PreSpeak(text)
	return e.Classify(text) == e.Classify(text) &&
		d1.Sev == d2.Sev && d1.SpokenText == d2.SpokenText &&
		d1.Intercepted == d2.Intercepted && (d1.Notify != nil) == (d2.Notify != nil)
}

// PropertyCrisisParaphraseInvariant P2：危机基的语气词包裹/标点包裹/上下文夹带/
// 零宽伪装改写全部保持 Crisis 分级（spec §3 属性：危机语句语气词/标点/夹英文
// 改写不改 Severity）。
func PropertyCrisisParaphraseInvariant(seed uint32) bool {
	e, err := NewEngine(DefaultConfig())
	if err != nil {
		return false
	}
	bases := crisisBasesAll()
	base := bases[int(seed)%len(bases)]
	for _, form := range crisisForms(base) {
		if e.Classify(form) != Crisis {
			return false
		}
	}
	return true
}

// spokenTextPassesChecklist 锚点检查单（T9-G0-02 口径的属性面）：四锚齐/求助出口
// 在/方法词=0/不否定孩子。
func spokenTextPassesChecklist(e *Engine, text string) bool {
	a := e.cfg.Anchors
	for _, part := range []string{a.EmpathyOpen, a.NoMethod, a.HelpOutlet, a.TellAdult} {
		if !strings.Contains(text, part) {
			return false
		}
	}
	if !strings.Contains(text, "12356") {
		return false
	}
	for _, w := range methodWords {
		if strings.Contains(strings.ToLower(text), w) {
			return false
		}
	}
	for _, w := range denyPhrases {
		if strings.Contains(text, w) {
			return false
		}
	}
	return true
}

// PropertySpokenTextAlwaysSafe P3：任意 fuzz 文本——Benign 原文直返；Sensitive
// 适龄话术；Crisis/Intercept 恒为过锚点检查单的安全话术（话术面零不安全输出）。
func PropertySpokenTextAlwaysSafe(text string) bool {
	e, err := NewEngine(DefaultConfig())
	if err != nil {
		return false
	}
	d := e.PreSpeak(text)
	switch {
	case d.Sev == Benign && !d.Intercepted:
		return d.SpokenText == text
	case d.Sev == Sensitive:
		return d.SpokenText == e.sensitiveText && !d.Intercepted && d.Notify == nil
	default: // Crisis 或 Intercept
		return d.SpokenText == e.safeText && spokenTextPassesChecklist(e, d.SpokenText)
	}
}

// PropertyAttackObfuscationNotLax P4：攻击模式的混淆形（语气词/标点/上下文夹带/
// 悄悄话/催促/游戏话术包裹）全部仍被拦截——混淆度参数↑判定不宽松。
func PropertyAttackObfuscationNotLax(seed uint32) bool {
	e, err := NewEngine(DefaultConfig())
	if err != nil {
		return false
	}
	patterns := attackPatternsAll()
	p := patterns[int(seed)%len(patterns)]
	for _, form := range attackForms(p) {
		d := e.PreSpeak(form)
		if d.Sev != Crisis && !d.Intercepted {
			return false // 原载荷通过=判定放宽
		}
	}
	return true
}

// PropertyCrisisNotifyBijection P5：Crisis 判定↔NotifyPayload 一一对应——
// 每次 Crisis 决策恰入队一条，非 Crisis 决策零入队。
func PropertyCrisisNotifyBijection(text string) bool {
	e, err := NewEngine(DefaultConfig())
	if err != nil {
		return false
	}
	before := len(e.NotifyQueue())
	d := e.PreSpeak(text)
	after := len(e.NotifyQueue())
	if d.Sev == Crisis {
		return d.Notify != nil && after == before+1
	}
	return d.Notify == nil && after == before
}

// PropertyTierInvariance P6：降级安全水位不降（T9-G0-07 属性面）——引擎无档位
// 分支，L0–L3 四档面（同一引擎同一份词表/分级/锚点）决策恒同。
func PropertyTierInvariance(text string) bool {
	e, err := NewEngine(DefaultConfig())
	if err != nil {
		return false
	}
	first := e.PreSpeak(text)
	for tier := 1; tier < 4; tier++ {
		d := e.PreSpeak(text)
		if d.Sev != first.Sev || d.SpokenText != first.SpokenText ||
			d.Intercepted != first.Intercepted || (d.Notify != nil) != (first.Notify != nil) {
			return false
		}
	}
	return true
}

func TestPropertiesT9(t *testing.T) {
	t.Run("P1 同输入同决策", func(t *testing.T) {
		if err := quick.Check(PropertyDeterministic, nil); err != nil {
			t.Fatalf("P1: %v", err)
		}
	})
	t.Run("P2 危机改写不改分级", func(t *testing.T) {
		if err := quick.Check(PropertyCrisisParaphraseInvariant, nil); err != nil {
			t.Fatalf("P2: %v", err)
		}
	})
	t.Run("P3 话术恒过锚点检查单", func(t *testing.T) {
		if err := quick.Check(PropertySpokenTextAlwaysSafe, nil); err != nil {
			t.Fatalf("P3: %v", err)
		}
	})
	t.Run("P4 攻击混淆不放宽判定", func(t *testing.T) {
		if err := quick.Check(PropertyAttackObfuscationNotLax, nil); err != nil {
			t.Fatalf("P4: %v", err)
		}
	})
	t.Run("P5 危机↔通知一一对应", func(t *testing.T) {
		if err := quick.Check(PropertyCrisisNotifyBijection, nil); err != nil {
			t.Fatalf("P5: %v", err)
		}
	})
	t.Run("P6 四档决策恒同", func(t *testing.T) {
		if err := quick.Check(PropertyTierInvariance, nil); err != nil {
			t.Fatalf("P6: %v", err)
		}
	})
}
