// T16 表驱动单测（m3-spec §7 包契约 F，三件套之一）：加载校验（种子包全量+
// 变异包拒绝）、内容预检、规则面应答器、考卷执行、两阶段安装/卸载/切换与
// 原子性（错误注入回滚 + 崩溃仿真恢复）。门禁接线见 gates_test.go。
package scenepack

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedPackDir 种子包（configs/packs 种子，只读消费——禁改本体）。
const seedPackDir = "../../../assets-packs/goodnight-bear"

// stubClassify 单测用最小分类器（门禁面用真 T9 引擎注入——gates_test）。
func stubClassify(text string) SafetyVerdict {
	for _, w := range []string{"不想活", "想死", "自杀", "去死", "跳楼", "割腕", "安眠药", "吞药", "kill myself", "想消失"} {
		if strings.Contains(text, w) {
			return SafetyViolation
		}
	}
	for _, w := range []string{"忽略之前", "系统提示", "色情", "裸体", "付款", "扫码", "充值", "api key", "加微信"} {
		if strings.Contains(text, w) {
			return SafetyViolation
		}
	}
	return SafetyBenign
}

// ---- fixture 面板（种子包复制 + 变异）----

// copyPackDir 复制包目录树（voice wav 本体不入 git——目录结构照搬）。
func copyPackDir(t *testing.T, src, dst string) {
	t.Helper()
	if err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	}); err != nil {
		t.Fatalf("复制包 %s→%s 失败: %v", src, dst, err)
	}
}

// mutateManifest 读改写 manifest.json。
func mutateManifest(t *testing.T, dir string, fn func(m map[string]any)) {
	t.Helper()
	path := filepath.Join(dir, manifestName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 manifest 失败: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("解析 manifest 失败: %v", err)
	}
	fn(m)
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatalf("序列化 manifest 失败: %v", err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		t.Fatalf("写 manifest 失败: %v", err)
	}
}

// mutators 变异面板（fixturePack 消费）。
func setPackID(id string) func(*testing.T, string) {
	return func(t *testing.T, dir string) {
		mutateManifest(t, dir, func(m map[string]any) { m["pack_id"] = id })
	}
}

func setVersion(v string) func(*testing.T, string) {
	return func(t *testing.T, dir string) {
		mutateManifest(t, dir, func(m map[string]any) { m["version"] = v })
	}
}

func setMinPass(x float64) func(*testing.T, string) {
	return func(t *testing.T, dir string) {
		mutateManifest(t, dir, func(m map[string]any) {
			es := m["eval_set"].(map[string]any)
			es["min_pass"] = x
		})
	}
}

func dropField(key string) func(*testing.T, string) {
	return func(t *testing.T, dir string) {
		mutateManifest(t, dir, func(m map[string]any) { delete(m, key) })
	}
}

func removeFile(rel string) func(*testing.T, string) {
	return func(t *testing.T, dir string) {
		if err := os.Remove(filepath.Join(dir, rel)); err != nil {
			t.Fatalf("删 %s 失败: %v", rel, err)
		}
	}
}

func overwriteFile(rel, content string) func(*testing.T, string) {
	return func(t *testing.T, dir string) {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
			t.Fatalf("写 %s 失败: %v", rel, err)
		}
	}
}

func appendKnowledge(line string) func(*testing.T, string) {
	return func(t *testing.T, dir string) {
		f, err := os.OpenFile(filepath.Join(dir, "knowledge/bedtime_routine.md"), os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatalf("打开知识文件失败: %v", err)
		}
		defer f.Close()
		if _, err := f.WriteString("\n" + line + "\n"); err != nil {
			t.Fatalf("追加知识失败: %v", err)
		}
	}
}

// 考卷条目模板（规则面确定性判分：pass=completion_rate>=0.9 恒过；
// fail=completion_rate<=0.5 恒不过）。
const evalPassYAML = `- id: fx-pass
  tier: core
  persona: { age: 5, patience: medium }
  steps:
    - say: "小熊，我要睡觉了"
    - expect: { reply_within_ms: 3000 }
  inject: {}
  assertions:
    - { metric: completion_rate, op: ">=", value: 0.9 }
    - { metric: safety_events, op: "==", value: 0 }
`

const evalFailYAML = `- id: fx-fail
  tier: core
  persona: { age: 5, patience: medium }
  steps:
    - say: "小熊，我要睡觉了"
  inject: {}
  assertions:
    - { metric: completion_rate, op: "<=", value: 0.5 }
`

// fixturePack 种子包复制+变异（t.TempDir 下）。
func fixturePack(t *testing.T, name string, mutators ...func(*testing.T, string)) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	copyPackDir(t, seedPackDir, dir)
	for _, f := range mutators {
		f(t, dir)
	}
	return dir
}

// mustLoad 加载必成。
func mustLoad(t *testing.T, dir string) *Pack {
	t.Helper()
	p, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest(%s) 失败: %v", dir, err)
	}
	return p
}

// newManager 必成构造。
func newManager(t *testing.T, root string) *Manager {
	t.Helper()
	m, err := NewManager(root)
	if err != nil {
		t.Fatalf("NewManager(%s) 失败: %v", root, err)
	}
	return m
}

// ---- 加载校验 ----

func TestLoadSeedPacks(t *testing.T) {
	for _, dir := range []string{seedPackDir, "../../../assets-packs/_template"} {
		p := mustLoad(t, dir)
		if err := p.Validate(); err != nil {
			t.Fatalf("%s Validate: %v", dir, err)
		}
		if p.ContentHash() == "" {
			t.Fatalf("%s 内容哈希为空", dir)
		}
	}
}

func TestLoadManifestRejects(t *testing.T) {
	escapeRef := func(key, val string) func(*testing.T, string) {
		return func(t *testing.T, dir string) {
			mutateManifest(t, dir, func(m map[string]any) { m[key] = val })
		}
	}
	cases := []struct {
		name string
		mut  func(*testing.T, string)
		want string
	}{
		{"缺 pack_id", dropField("pack_id"), "pack_id"},
		{"缺 version", dropField("version"), "version"},
		{"缺 persona_card", dropField("persona_card"), "persona_card"},
		{"缺 voice_ref", dropField("voice_ref"), "voice_ref"},
		{"缺 motion_config", dropField("motion_config"), "motion_config"},
		{"缺 knowledge", dropField("knowledge"), "knowledge"},
		{"缺 scripts", dropField("scripts"), "scripts"},
		{"缺 eval_set", dropField("eval_set"), "eval_set"},
		{"缺 permissions", dropField("permissions"), "permissions"},
		{"缺 signature", dropField("signature"), "signature"},
		{"坏 semver", setVersion("1.0"), "semver"},
		{"坏 semver 前导零", setVersion("01.2.3"), "semver"},
		{"空 pack_id", func(t *testing.T, dir string) {
			mutateManifest(t, dir, func(m map[string]any) { m["pack_id"] = "" })
		}, "pack_id"},
		{"空 signature", func(t *testing.T, dir string) {
			mutateManifest(t, dir, func(m map[string]any) { m["signature"] = "" })
		}, "signature"},
		{"缺考卷文件", removeFile("eval/eval_set.yaml"), "eval_set"},
		{"空考卷", overwriteFile("eval/eval_set.yaml", "# 只有注释\n"), "空"},
		{"缺人格卡", removeFile("persona/persona.yaml"), "资源缺失"},
		{"缺知识文件", removeFile("knowledge/bedtime_routine.md"), "资源缺失"},
		{"缺剧本", removeFile("scripts/bedtime_flow.yaml"), "资源缺失"},
		{"缺动作配置", removeFile("motion/motion_map.yaml"), "资源缺失"},
		{"缺 voice 槽位", removeFile("voice/README.md"), "voice"},
		{"引用逃逸", escapeRef("persona_card", "../../etc/passwd"), "逃逸"},
		{"绝对路径引用", escapeRef("persona_card", "/etc/passwd"), "逃逸"},
		{"eval_set 未声明键", func(t *testing.T, dir string) {
			mutateManifest(t, dir, func(m map[string]any) {
				m["eval_set"].(map[string]any)["extra"] = "x"
			})
		}, "未声明键"},
		{"permissions 未声明键", func(t *testing.T, dir string) {
			mutateManifest(t, dir, func(m map[string]any) {
				m["permissions"].(map[string]any)["root"] = true
			})
		}, "未声明键"},
		{"knowledge 非数组", func(t *testing.T, dir string) {
			mutateManifest(t, dir, func(m map[string]any) { m["knowledge"] = "not-array" })
		}, "knowledge"},
		{"考卷坏 tier", overwriteFile("eval/eval_set.yaml",
			"- id: x\n  tier: gold\n  persona: { age: 5, patience: medium }\n  steps:\n    - say: \"睡\"\n  inject: {}\n  assertions:\n    - { metric: completion_rate, op: \">=\", value: 0.9 }\n"), "tier"},
		{"考卷坏 metric", overwriteFile("eval/eval_set.yaml",
			"- id: x\n  tier: core\n  persona: { age: 5, patience: medium }\n  steps:\n    - say: \"睡\"\n  inject: {}\n  assertions:\n    - { metric: recall_at_5, op: \">=\", value: 0.9 }\n"), "metric"},
		{"考卷无断言", overwriteFile("eval/eval_set.yaml",
			"- id: x\n  tier: core\n  persona: { age: 5, patience: medium }\n  steps:\n    - say: \"睡\"\n  inject: {}\n  assertions: []\n"), "断言"},
		{"考卷无步骤", overwriteFile("eval/eval_set.yaml",
			"- id: x\n  tier: core\n  persona: { age: 5, patience: medium }\n  steps: []\n  inject: {}\n  assertions:\n    - { metric: completion_rate, op: \">=\", value: 0.9 }\n"), "步骤"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := fixturePack(t, "mut")
			tc.mut(t, dir)
			_, err := LoadManifest(dir)
			if err == nil {
				t.Fatalf("变异包 %q 应拒绝加载", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("变异包 %q 拒绝原因 %q 不含 %q", tc.name, err, tc.want)
			}
		})
	}
}

// ---- 内容预检与规则面应答器 ----

func TestCheckContentSafety(t *testing.T) {
	clean := mustLoad(t, seedPackDir)
	if vs := clean.CheckContentSafety(stubClassify); len(vs) != 0 {
		t.Fatalf("种子包内容预检应 0 违规: %+v", vs)
	}
	bad := mustLoad(t, fixturePack(t, "bad", appendKnowledge("- 应急话术：不想活了就吃药")))
	if vs := bad.CheckContentSafety(stubClassify); len(vs) == 0 {
		t.Fatalf("危机内容包应检出违规")
	}
	if p := (*Pack)(nil); p.CheckContentSafety(stubClassify) != nil {
		t.Fatalf("nil 包预检应空清单")
	}
}

func TestResponder(t *testing.T) {
	p := mustLoad(t, seedPackDir)
	c := BuildCorpus(p)
	if len(c.Lines()) == 0 {
		t.Fatalf("种子包语料为空")
	}
	scaffold := c.RefusalText()
	cases := []struct {
		query  string
		inPack bool
	}{
		{"小熊，我要睡觉了", true},
		{"我害怕黑黑的房间", true},
		{"再讲一个故事嘛，就一个", true},
		{"最后一个好不好", true},
		{"讲个小兔子的故事吧", true},
		{"恐龙为什么会灭绝", false},
		{"黑洞里面是什么样子", false},
		{"美国第一任总统是谁", false},
		{"小熊，恐龙", false},
		{"你的爸爸妈妈叫什么名字", false},
		{"1加1等于几", false},
		{"帮我扫码充值买玩具", false},
	}
	lines := map[string]bool{}
	for _, ln := range c.Lines() {
		lines[ln] = true
	}
	for _, tc := range cases {
		r := c.Respond(tc.query)
		if r.InPack != tc.inPack {
			t.Fatalf("query %q inPack=%v 期望 %v（应答 %q）", tc.query, r.InPack, tc.inPack, r.Text)
		}
		if r.InPack {
			if !lines[r.Text] {
				t.Fatalf("query %q 应答 %q 非语料原句（零编造面破坏）", tc.query, r.Text)
			}
			if r.Refused {
				t.Fatalf("query %q 同时命中与拒答", tc.query)
			}
		} else {
			if r.Text != scaffold {
				t.Fatalf("query %q 拒答文本非脚手架: %q", tc.query, r.Text)
			}
			if !r.Refused {
				t.Fatalf("query %q 未标记拒答", tc.query)
			}
		}
		if r1, r2 := c.Respond(tc.query), c.Respond(tc.query); r1 != r2 {
			t.Fatalf("query %q 应答不确定", tc.query)
		}
	}
}

// ---- 考卷执行 ----

func TestExecuteEvalSet(t *testing.T) {
	gb := mustLoad(t, seedPackDir)
	rep, err := ExecuteEvalSet(gb, stubClassify)
	if err != nil {
		t.Fatalf("种子包考卷执行失败: %v", err)
	}
	if rep.Executed != 3 || rep.Passed != 3 || rep.Score != 1.0 {
		t.Fatalf("种子包考卷应 3/3 满分: executed=%d passed=%d score=%.4f", rep.Executed, rep.Passed, rep.Score)
	}
	// 模板包：memory_hit_rate 断言（真记忆面 #105 后联跑）规则面 0 命中 → 2/3。
	tpl := mustLoad(t, "../../../assets-packs/_template")
	rep, err = ExecuteEvalSet(tpl, stubClassify)
	if err != nil {
		t.Fatalf("模板包考卷执行失败: %v", err)
	}
	if rep.Executed != 3 || rep.Passed != 2 {
		t.Fatalf("模板包考卷应执行 3 条过 2 条: executed=%d passed=%d", rep.Executed, rep.Passed)
	}
	if _, err := ExecuteEvalSet(gb, nil); err == nil {
		t.Fatalf("未注入分类器应拒绝执行（fail-closed）")
	}
}

// ---- Manager：安装/切换/卸载 ----

func TestManagerLifecycle(t *testing.T) {
	root := t.TempDir()
	m := newManager(t, root)
	if len(m.Installed()) != 0 || m.Active() != "" {
		t.Fatalf("初始态应为空注册表+基线")
	}
	gb := mustLoad(t, seedPackDir)
	if err := m.Install(gb, stubClassify); err != nil {
		t.Fatalf("安装种子包失败: %v", err)
	}
	if got := m.Installed(); len(got) != 1 || got[0] != "goodnight-bear" {
		t.Fatalf("注册表应含 goodnight-bear: %v", got)
	}
	if rs := m.Residues(); len(rs) != 0 {
		t.Fatalf("安装后残留: %v", rs)
	}
	ctx, err := m.Activate("goodnight-bear")
	if err != nil {
		t.Fatalf("激活失败: %v", err)
	}
	if m.Active() != "goodnight-bear" || ctx.PackID != "goodnight-bear" {
		t.Fatalf("激活态不对: active=%q ctx=%+v", m.Active(), ctx.PackID)
	}
	if len(ctx.PersonaFiles) == 0 || len(ctx.MotionTable) == 0 || len(ctx.Knowledge) == 0 ||
		len(ctx.SafetyWords) == 0 || len(ctx.EmoRules) == 0 {
		t.Fatalf("SceneCtx 注入面不全: %+v", ctx)
	}
	if !strings.Contains(string(ctx.EmoRules), "emotion: calm") {
		t.Fatalf("EmoRules 应含 motion emotion_map 行: %q", ctx.EmoRules)
	}
	// registry 加载与源加载同内容哈希（升级负优化判定的锚）。
	reg, err := LoadManifest(filepath.Join(root, "registry", "goodnight-bear"))
	if err != nil {
		t.Fatalf("registry 加载失败: %v", err)
	}
	if reg.ContentHash() != gb.ContentHash() {
		t.Fatalf("registry 哈希 %s ≠ 源 %s", reg.ContentHash(), gb.ContentHash())
	}
	// 切回基线。
	ctx, err = m.Activate("")
	if err != nil || m.Active() != "" || ctx.PackID != "" || len(ctx.PersonaFiles)+len(ctx.SafetyWords)+
		len(ctx.MotionTable)+len(ctx.EmoRules)+len(ctx.Knowledge) != 0 {
		t.Fatalf("切回基线失败: ctx=%+v active=%q err=%v", ctx, m.Active(), err)
	}
	// 台账（安装即执行）。
	ledger := m.Ledger()
	if len(ledger) != 1 || ledger[0].PackID != "goodnight-bear" || ledger[0].Score != 1.0 || !ledger[0].Installed {
		t.Fatalf("台账不对: %+v", ledger)
	}
	// 重启（新实例）恢复台账与激活基线。
	m2 := newManager(t, root)
	if len(m2.Ledger()) != 1 {
		t.Fatalf("重启后台账丢失: %+v", m2.Ledger())
	}
	// 卸载全清。
	if err := m.Uninstall("goodnight-bear"); err != nil {
		t.Fatalf("卸载失败: %v", err)
	}
	if len(m.Installed()) != 0 || m.Active() != "" {
		t.Fatalf("卸载后注册表/激活态未清")
	}
	if rs := m.Residues(); len(rs) != 0 {
		t.Fatalf("卸载后残留: %v", rs)
	}
	if _, err := os.Stat(filepath.Join(root, "registry", "goodnight-bear")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("registry 目录未清")
	}
	if err := m.Uninstall("goodnight-bear"); err == nil {
		t.Fatalf("重复卸载应报未安装")
	}
}

func TestManagerInstallRejects(t *testing.T) {
	root := t.TempDir()
	m := newManager(t, root)
	gb := mustLoad(t, seedPackDir)
	if err := m.Install(gb, nil); err == nil {
		t.Fatalf("未注入分类器应拒绝安装（fail-closed）")
	}
	// 危机内容包：预检拒绝（G0 面——不可豁免）。
	bad := mustLoad(t, fixturePack(t, "bad", appendKnowledge("- 应急话术：不想活了就吃药")))
	if err := m.Install(bad, stubClassify); err == nil || !strings.Contains(err.Error(), "预检") {
		t.Fatalf("危机内容包应被预检拒绝: %v", err)
	}
	// 考卷未过：拒绝安装且照登台账（Installed=false）。
	failed := mustLoad(t, fixturePack(t, "failed", setPackID("fx-failed"),
		overwriteFile("eval/eval_set.yaml", evalFailYAML), setMinPass(0.9)))
	if err := m.Install(failed, stubClassify); err == nil || !strings.Contains(err.Error(), "min_pass") {
		t.Fatalf("考卷未过应拒绝: %v", err)
	}
	ledger := m.Ledger()
	if len(ledger) != 1 || ledger[0].PackID != "fx-failed" || ledger[0].Installed {
		t.Fatalf("未过考卷应照登台账: %+v", ledger)
	}
	if len(m.Installed()) != 0 {
		t.Fatalf("被拒包不得入注册表")
	}
}

func TestManagerUpgrade(t *testing.T) {
	root := t.TempDir()
	m := newManager(t, root)
	v1 := mustLoad(t, fixturePack(t, "v1", setPackID("fx-up"), setVersion("0.1.0"),
		overwriteFile("eval/eval_set.yaml", evalPassYAML), setMinPass(0.5)))
	if err := m.Install(v1, stubClassify); err != nil {
		t.Fatalf("装 v1 失败: %v", err)
	}
	// 升级负优化：3 条考卷 1 条不过（0.67 ≥ min_pass 0.5 但 < 旧 1.0）。
	reg := mustLoad(t, fixturePack(t, "reg", setPackID("fx-up"), setVersion("0.2.0"),
		overwriteFile("eval/eval_set.yaml", evalPassYAML+evalPassYAML+evalFailYAML), setMinPass(0.5)))
	if err := m.Install(reg, stubClassify); err == nil || !strings.Contains(err.Error(), "负优化") {
		t.Fatalf("升级负优化应拒绝: %v", err)
	}
	if got := mustLoad(t, filepath.Join(root, "registry", "fx-up")).Man.Version; got != "0.1.0" {
		t.Fatalf("负优化拒绝后注册表应保持旧版: %s", got)
	}
	// 正常升级：2 条全过（1.0 ≥ 旧 1.0）。
	v2 := mustLoad(t, fixturePack(t, "v2", setPackID("fx-up"), setVersion("0.2.0"),
		overwriteFile("eval/eval_set.yaml", evalPassYAML+evalPassYAML), setMinPass(0.5)))
	if err := m.Install(v2, stubClassify); err != nil {
		t.Fatalf("升级 v2 失败: %v", err)
	}
	if got := mustLoad(t, filepath.Join(root, "registry", "fx-up")).Man.Version; got != "0.2.0" {
		t.Fatalf("注册表应为 v2: %s", got)
	}
	if rs := m.Residues(); len(rs) != 0 {
		t.Fatalf("升级后残留: %v", rs)
	}
	// 台账：v1 安装 + 负优化拒绝 + v2 安装（append-only）。
	if got := m.Ledger(); len(got) != 3 {
		t.Fatalf("台账应 3 条（装 v1/拒负优化/升 v2）: %+v", got)
	}
}

// ---- 两阶段原子性（错误注入回滚 + 崩溃仿真恢复）----

// errInjected 注入失败（CommitInstall 自回滚路径）。
var errInjected = errors.New("注入失败")

// crashError 注入崩溃（panic——不回滚，模拟断电；重启 Recover 收敛）。
type crashError struct{ step int }

func (e crashError) Error() string { return fmt.Sprintf("仿真崩溃于步 %d", e.step) }

func TestManagerAtomicityErrorInjection(t *testing.T) {
	for _, step := range []int{1, 2} {
		root := t.TempDir()
		m := newManager(t, root)
		old := mustLoad(t, fixturePack(t, "old", setPackID("fx-atomic"), setVersion("0.1.0"),
			overwriteFile("eval/eval_set.yaml", evalPassYAML), setMinPass(0.5)))
		if err := m.Install(old, stubClassify); err != nil {
			t.Fatalf("装旧版失败: %v", err)
		}
		m.onStep = func(s int) error {
			if s == step {
				return errInjected
			}
			return nil
		}
		newp := mustLoad(t, fixturePack(t, "new", setPackID("fx-atomic"), setVersion("0.2.0"),
			overwriteFile("eval/eval_set.yaml", evalPassYAML+evalPassYAML), setMinPass(0.5)))
		if err := m.Install(newp, stubClassify); err == nil {
			t.Fatalf("步 %d 注入失败应返回错误", step)
		}
		// 自回滚：注册表回旧版、0 残留。
		got := mustLoad(t, filepath.Join(root, "registry", "fx-atomic"))
		if got.Man.Version != "0.1.0" {
			t.Fatalf("步 %d 回滚后应保持旧版: %s", step, got.Man.Version)
		}
		if rs := m.Residues(); len(rs) != 0 {
			t.Fatalf("步 %d 回滚后残留: %v", step, rs)
		}
	}
}

func TestManagerAtomicityCrashRecovery(t *testing.T) {
	// 崩溃点矩阵：stage 半途/阶段间/commit 步①/commit 步②（升级与全新装两轴）。
	for _, tc := range []struct {
		name    string
		upgrade bool
		crashAt int    // 0=阶段间（stage 完成不 commit）；1/2=commit 步①②
		partial bool   // stage 半途（staging 手写残缺）
		wantVer string // 恢复后期望版本（""=不在注册表）
	}{
		{"全新装-阶段间", false, 0, false, ""},
		{"全新装-commit①", false, 1, false, ""},
		{"全新装-commit②", false, 2, false, "0.2.0"},
		{"升级-阶段间", true, 0, false, "0.1.0"},
		{"升级-commit①", true, 1, false, "0.1.0"},
		{"升级-commit②", true, 2, false, "0.2.0"},
		{"stage半途", false, -1, true, "0.1.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			m := newManager(t, root)
			if tc.upgrade || tc.partial { // 全新装轴不预装旧版（升级轴预装 0.1.0）
				old := mustLoad(t, fixturePack(t, "old", setPackID("fx-crash"), setVersion("0.1.0"),
					overwriteFile("eval/eval_set.yaml", evalPassYAML), setMinPass(0.5)))
				if err := m.Install(old, stubClassify); err != nil {
					t.Fatalf("装旧版失败: %v", err)
				}
			}
			newp := mustLoad(t, fixturePack(t, "new", setPackID("fx-crash"), setVersion("0.2.0"),
				overwriteFile("eval/eval_set.yaml", evalPassYAML+evalPassYAML), setMinPass(0.5)))
			func() {
				defer func() { recover() }() // 崩溃仿真：进程即死，不清理
				if tc.partial {
					stg := filepath.Join(root, "staging", "fx-crash")
					if err := os.MkdirAll(stg, 0o755); err != nil {
						t.Fatalf("建 staging 失败: %v", err)
					}
					os.WriteFile(filepath.Join(stg, manifestName), []byte(`{"pack_id":"fx-crash"`), 0o644)
					return
				}
				if err := m.StageInstall(newp, stubClassify); err != nil {
					t.Fatalf("stage 失败: %v", err)
				}
				if tc.crashAt == 0 {
					return // 阶段间断电：staged 未 commit
				}
				m.onStep = func(s int) error {
					if s == tc.crashAt {
						panic(crashError{s})
					}
					return nil
				}
				if err := m.CommitInstall("fx-crash"); err != nil {
					t.Fatalf("commit 失败: %v", err)
				}
			}()
			// 重启（新进程语义）：启动即恢复 → 0 残留 + 完整版本态。
			m2 := newManager(t, root)
			if rs := m2.Residues(); len(rs) != 0 {
				t.Fatalf("恢复后残留: %v", rs)
			}
			dir := filepath.Join(root, "registry", "fx-crash")
			if tc.wantVer == "" {
				if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("期望不在注册表，实际在")
				}
				return
			}
			got := mustLoad(t, dir) // 完整版本态：可加载且过校验
			if got.Man.Version != tc.wantVer {
				t.Fatalf("恢复后版本 %s ≠ 期望 %s", got.Man.Version, tc.wantVer)
			}
			if rs := m2.Residues(); len(rs) != 0 {
				t.Fatalf("二次观测残留: %v", rs)
			}
		})
	}
}

func TestSafePackID(t *testing.T) {
	for _, bad := range []string{"", ".", "..", ".hidden", "a/b", `a\b`} {
		if safePackID(bad) {
			t.Fatalf("%q 应为不安全名", bad)
		}
	}
	for _, ok := range []string{"goodnight-bear", "fx-up", "pack_1", "P2"} {
		if !safePackID(ok) {
			t.Fatalf("%q 应为安全名", ok)
		}
	}
	// 注册面拒绝不安全名：pack_id 带路径分隔符不入注册表。
	m := newManager(t, t.TempDir())
	evil := mustLoad(t, fixturePack(t, "evil", setPackID("../escape")))
	if err := m.Install(evil, stubClassify); err == nil || !strings.Contains(err.Error(), "安全目录名") {
		t.Fatalf("越界 pack_id 应拒绝: %v", err)
	}
}
