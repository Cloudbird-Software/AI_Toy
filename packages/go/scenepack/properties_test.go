// 属性测试（m3-spec §7 属性清单，三件套之二，testing/quick）：
// P1 同包同版本同内容哈希（加载确定性：目录无关+键序无关——复制包目录与任意
// Files 键插入序不改内容身份）；P2 包升级内置评测得分不降（任意升级尝试序列
// 下注册表登记分单调不降——负优化由 Install 拒绝构造保证）；P3 应答零编造+
// 确定性（任意 fuzz 串的应答恒 ∈ 语料原句 ∪ {拒答脚手架}，且同查询同应答）。
// 隔离属性（任意包组合下核心资产与基线一致）归门禁面 T16-G1-01（gates_test）。
package scenepack

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"
	"testing/quick"
)

// TestPropertyContentHashStable P1 同包同版本同内容哈希：quick 种子驱动两条通道——
// ①同源包复制到不同目录再加载（Dir 不参与哈希）；②Files 以随机键序重建（map
// 迭代序不确定性隔离——哈希本身按键字典序）。两条通道哈希均与基准全等。
func TestPropertyContentHashStable(t *testing.T) {
	base := mustLoad(t, seedPackDir)
	f := func(seed int64) bool {
		// 通道 ①：复制目录后加载（不同 Dir、同内容）。
		copied := mustLoad(t, fixturePack(t, "copy"))
		if copied.ContentHash() != base.ContentHash() {
			t.Logf("复制目录改变内容哈希：%s vs %s", copied.ContentHash(), base.ContentHash())
			return false
		}
		// 通道 ②：Files 键随机插入序重建。
		keys := make([]string, 0, len(base.Files))
		for k := range base.Files {
			keys = append(keys, k)
		}
		r := rand.New(rand.NewSource(seed))
		r.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
		rebuilt := &Pack{Man: base.Man, Dir: "/elsewhere", Files: make(map[string][]byte, len(keys))}
		for _, k := range keys {
			rebuilt.Files[k] = base.Files[k]
		}
		if rebuilt.ContentHash() != base.ContentHash() {
			t.Logf("键插入序改变内容哈希（seed=%d）", seed)
			return false
		}
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 100}); err != nil {
		t.Errorf("P1 同包同版本同内容哈希被违反：%v", err)
	}
}

// propEvalYAML 按 bits 合成考卷（bit=true→恒过条目/false→恒不过条目）：得分
// =Σbits/n（规则面判分确定性）。
func propEvalYAML(bits []bool) string {
	var b strings.Builder
	for i, ok := range bits {
		id := fmt.Sprintf("prop-eval-%02d", i)
		if ok {
			fmt.Fprintf(&b, "- id: %s\n  tier: core\n  persona: { age: 5, patience: medium }\n  steps:\n    - say: \"小熊，我要睡觉了\"\n  assertions:\n    - { metric: completion_rate, op: \">=\", value: 0.9 }\n", id)
		} else {
			fmt.Fprintf(&b, "- id: %s\n  tier: core\n  persona: { age: 5, patience: medium }\n  steps:\n    - say: \"小熊，我要睡觉了\"\n  assertions:\n    - { metric: completion_rate, op: \"<=\", value: 0.5 }\n", id)
		}
	}
	return b.String()
}

// TestPropertyUpgradeScoreNonDecreasing P2 包升级内置评测得分不降（内容不许
// 负优化）：quick 生成随机升级尝试序列（每轮考卷组成随机）；断言①任一尝试后
// 注册表登记分 ≥ 已装分（负优化尝试被 Install 拒绝且注册表保持旧版）；②成功
// 升级后登记分单调不降。
func TestPropertyUpgradeScoreNonDecreasing(t *testing.T) {
	f := func(seed int64, b1, b2, b3 []bool) bool {
		if len(b1) == 0 || len(b2) == 0 || len(b3) == 0 {
			return true // quick 可产空表——空考卷非法，构造面跳过
		}
		root := t.TempDir()
		m := newManager(t, root)
		const id = "prop-up"
		ver := func(v string, bits []bool) *Pack {
			return mustLoad(t, fixturePack(t, v, setPackID(id), setVersion(v),
				overwriteFile("eval/eval_set.yaml", propEvalYAML(bits)), setMinPass(0.1)))
		}
		installed, registered := 0, -1.0
		for _, round := range []struct {
			v    string
			bits []bool
		}{{"0.1.0", b1}, {"0.2.0", b2}, {"0.3.0", b3}} {
			err := m.Install(ver(round.v, round.bits), stubClassify)
			score := float64(0)
			for _, ok := range round.bits {
				if ok {
					score++
				}
			}
			score /= float64(len(round.bits))
			switch {
			case err == nil:
				installed++
				if score < registered-1e-12 {
					t.Logf("负优化升级被放行：%.4f < %.4f（%s）", score, registered, round.v)
					return false
				}
				registered = score
			default:
				// 拒绝合法（min_pass/负优化）；注册表须保持旧版旧分。
				if installed > 0 {
					got := mustLoad(t, filepath.Join(root, "registry", id))
					if got.Man.Version >= round.v {
						t.Logf("拒绝后注册表版本异常：%s（尝试 %s）", got.Man.Version, round.v)
						return false
					}
				}
			}
		}
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 60}); err != nil {
		t.Errorf("P2 包升级内置评测得分不降被违反：%v", err)
	}
}

// TestPropertyResponderZeroFabrication P3 应答零编造+确定性：任意 fuzz 串
// （quick 的 string 生成任意字节——含非 UTF-8/标点/超长）过规则面应答器：
// ①应答 ∈ 语料原句 ∪ {拒答脚手架}（诱导说包外知识必拒的构造面——应答面
// 无第三种来源）；②InPack 与 Refused 恰一为真；③同查询双跑同应答（确定性）。
func TestPropertyResponderZeroFabrication(t *testing.T) {
	for _, dir := range []string{seedPackDir, "../../../assets-packs/_template"} {
		p := mustLoad(t, dir)
		c := BuildCorpus(p)
		lines := map[string]bool{}
		for _, ln := range c.Lines() {
			lines[ln] = true
		}
		scaffold := c.RefusalText()
		f := func(query string) bool {
			r := c.Respond(query)
			if r.InPack == r.Refused {
				t.Logf("InPack/Refused 须恰一为真：%+v（query=%q）", r, query)
				return false
			}
			if r.InPack {
				if !lines[r.Text] {
					t.Logf("应答非语料原句（零编造面破坏）：%q（query=%q）", r.Text, query)
					return false
				}
			} else if r.Text != scaffold {
				t.Logf("拒答文本非脚手架：%q（query=%q）", r.Text, query)
				return false
			}
			r2 := c.Respond(query)
			return r == r2
		}
		if err := quick.Check(f, &quick.Config{MaxCount: 200}); err != nil {
			t.Errorf("P3 应答零编造/确定性被违反（%s）：%v", dir, err)
		}
	}
}
