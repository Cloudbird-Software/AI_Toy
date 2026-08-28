// 运行时镜像，实现落地后替换：CH-03 输出超长/死循环文本（spec §8.3，G1）。
//
// 三段：注入（内容源产出死循环文本）→ 系统响应（到达硬上限即截断 + 自然
// 收尾，不把无限流喂给下游 TTS/动作）→ 恢复（「—」无自动恢复：断言终端态
// 稳定——截断幂等，重复渲染不变）。packages/go/content-pipeline（T18）落地
// 后替换本镜像。
package chaos

import (
	"strings"
	"testing"
)

// overrunBudgets CH-03 的 G1 界。
const (
	overrunHardCapRunes = 200               // 硬截断上限（rune 数）
	overrunClosing      = "……好啦，今天就先说到这儿吧。" // 自然收尾锚
)

// overrunGenerator 死循环文本源（纯数据）：前缀 + 无限重复段。
type overrunGenerator struct {
	seed string
}

// emit 生成一份远超上限的死循环文本（重复段 × 足够多次）。
func (g overrunGenerator) emit() string {
	var b strings.Builder
	b.WriteString(g.seed)
	for i := 0; i < overrunHardCapRunes; i++ {
		b.WriteString("然后小熊又吃了一个蜂蜜饼干，")
	}
	return b.String()
}

// hardTruncate 系统响应（三段之二）：硬截断 + 自然收尾。
// 未超限的文本原样放行（正常输出不受影响）；超限文本截至上限内并拼收尾。
func hardTruncate(s string) string {
	runes := []rune(s)
	if len(runes) <= overrunHardCapRunes {
		return s
	}
	body := string(runes[:overrunHardCapRunes-len([]rune(overrunClosing))])
	return body + overrunClosing
}

func TestOutputOverrun(t *testing.T) {
	row, ok := Row(RowOverrun)
	if !ok {
		t.Fatalf("矩阵行 %s 未登记", RowOverrun)
	}
	if row.Gate != GateG1 {
		t.Fatalf("%s 门禁级别=%s，须 G1（合并阻断）", row.ID, row.Gate)
	}
	if row.Recover != NoRecovery {
		t.Fatalf("%s 恢复列=%q，须为「—」（无自动恢复，断言终端态稳定）", row.ID, row.Recover)
	}

	// ── 三段之一：注入（死循环文本，长度远超硬上限）──
	gen := overrunGenerator{seed: "从前有一只小熊，它特别特别爱吃蜂蜜。"}
	raw := gen.emit()
	if got := len([]rune(raw)); got <= overrunHardCapRunes {
		t.Fatalf("注入文本长度=%d，须 >%d（注入须成立）", got, overrunHardCapRunes)
	}

	// ── 三段之二：系统响应（硬截断 + 自然收尾）──
	out := hardTruncate(raw)
	if got := len([]rune(out)); got > overrunHardCapRunes {
		t.Fatalf("截断后长度=%d，须 ≤%d", got, overrunHardCapRunes)
	}
	if !strings.HasSuffix(out, overrunClosing) {
		t.Fatalf("截断输出未以自然收结尾（得到 %q）", tail(out, len(overrunClosing)+4))
	}

	// ── 三段之三：恢复列「—」——无自动恢复，断言终端态稳定 ──
	if again := hardTruncate(raw); again != out {
		t.Fatal("截断须幂等：同一输入重复截断结果不一致（终端态不稳定）")
	}
	if twice := hardTruncate(out); twice != out {
		t.Fatal("已截断文本再过截断器必须原样通过（终端态不稳定）")
	}

	// 边界：恰好等于上限的文本不触发截断；超 1 rune 即截断。
	atCap := strings.Repeat("熊", overrunHardCapRunes)
	if got := hardTruncate(atCap); got != atCap {
		t.Fatal("恰好等于上限的文本须原样放行")
	}
	overCap := strings.Repeat("熊", overrunHardCapRunes+1)
	if got := hardTruncate(overCap); len([]rune(got)) != overrunHardCapRunes {
		t.Fatalf("超限 1 rune 须截回上限，得 %d", len([]rune(got)))
	}
}

// tail 取字符串末 n 个 rune（错误信息用）。
func tail(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[len(runes)-n:])
}
