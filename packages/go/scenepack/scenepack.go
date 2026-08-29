// Package scenepack —— T16+T18 场景包系统规则面（M3，IR #107 / m3-spec §7 包契约 F）。
//
// 角色即数据包（BI-16.1 新角色=一个数据包，装包即上线不动核心代码）：包目录
// （manifest 对齐 configs/packs/schema.json + 人格卡/音色槽位/动作配置/知识
// 剧本/评测集）进 → LoadManifest 手写结构校验（镜像 schema 必要条件：required
// 全字段/semver 版式/eval_set 与 permissions 键集封闭/引用完整性与包内包含性
// ——JSON Schema 库不引，一致性由变异包 fixture 双跑断言）+ 两阶段原子安装
// （staging→commit→registry，崩溃恢复回滚上一完整版本——CH-08 同语义）+
// 场景切换事件（Activate→SwitchEvent/SceneCtx，供 persona/safety/emotion/
// motion 消费——本包零 import 那些资产包，ADR-0004；组装归 loop）出。
//
// 安全面（BI-16.2 内容自带验收，内容安全不可豁免）：包内容全量逐行过注入的
// T9 分类器（SafetyClassifyFunc 最小适配接口——Crisis/攻击拦截=违规拒绝入包，
// fail-closed：未注入分类器=拒绝安装）；考卷随包执行（ExecuteEvalSet 规则面
// 应答器：包内文本命中→逐字回包，knowledge 外→拒答脚手架——诱导说包外知识
// 必拒）；Install 强制考卷得分 ≥ manifest min_pass 且升级不得负优化（内容
// 不许负优化——构造保证）。T18 生成管线 M3=预检规则面（LLM 批量生成+溯源戳
// =真模型面 L5 注记）。
//
// 隔离面（BI-16.3 包间绝对隔离、卸载干净）：包内容只经 SceneCtx 显式注入外
// 溢（核心资产无注入时输出与无包基线一致）；Uninstall 两阶段全清，Residues()
// 全通道残留观测面（0 残留断言口径）。
//
// 依赖纪律：import 白名单=标准库+gopkg.in/yaml.v3（既有依赖，实现卡 #107
// 明示可用）；不 import safety/persona/emotion/motionmap（考卷隔离+包间零
// import——联跑一律在 _test 侧注入/断言）。
package scenepack

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// 包内布局常量（manifest 文件名对齐 configs/packs/schema.json 的包格式）。
const (
	manifestName = "manifest.json"
)

// semverPattern 镜像 schema.json version pattern（draft-07 正则逐字照抄——
// gate 测试双跑断言 schema 文件与镜像一致）。
const semverPattern = `^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`

// semverRe 镜像 schema.json version pattern。
var semverRe = regexp.MustCompile(semverPattern)

// manifestRequired schema required 全集（缺一拒构建——T16-G1-02）。
var manifestRequired = [...]string{
	"pack_id", "version", "persona_card", "voice_ref", "motion_config",
	"knowledge", "scripts", "eval_set", "permissions", "signature",
}

// EvalSetRef eval_set 指针（schema additionalProperties:false——键集封闭：
// 仅 path/min_pass）。
type EvalSetRef struct {
	Path    string  `json:"path"`
	MinPass float64 `json:"min_pass"`
}

// Permissions 权限声明（白名单制：未声明的能力运行时默认拒绝——Allows）。
type Permissions struct {
	MemoryScopes []string `json:"memory_scopes"`
	Actions      []string `json:"actions"`
	VolumeMax    float64  `json:"volume_max"`
}

// Allows 动作白名单判定（默认拒绝：manifest 未声明的动作一律不允许）。
func (pm Permissions) Allows(action string) bool {
	for _, a := range pm.Actions {
		if a == action {
			return true
		}
	}
	return false
}

// Manifest 包清单（字段名/必填集对齐 configs/packs/schema.json；手写镜像
// 必要条件——结构校验不引 JSON Schema 库）。
type Manifest struct {
	PackID       string      `json:"pack_id"`
	Version      string      `json:"version"`
	PersonaCard  string      `json:"persona_card"`
	VoiceRef     string      `json:"voice_ref"`
	MotionConfig string      `json:"motion_config"`
	Knowledge    []string    `json:"knowledge"`
	Scripts      []string    `json:"scripts"`
	EvalSet      EvalSetRef  `json:"eval_set"`
	Perm         Permissions `json:"permissions"`
	Signature    string      `json:"signature"`
}

// Pack 加载后的包：Dir=源目录（不参与内容哈希）；Files=manifest 本体+全部被
// 引用资源（运行面负载；voice wav 本体不入 git——T18 打包注入，槽位在加载
// 校验）。
type Pack struct {
	Man   Manifest
	Dir   string
	Files map[string][]byte
}

// normKey 引用路径 → Files 键（清理分隔符；包内包含性由 loadRef/Validate 保证）。
func normKey(ref string) string { return filepath.ToSlash(filepath.Clean(ref)) }

// LoadManifest 加载并校验包（T16-G1-02 包完整性）：manifest 结构校验（镜像
// schema 必要条件）+ 资源齐备（全部被引用资源可读且在包内）+ eval_set 可执行
// （≥1 条——空考卷=未交考卷，BI-16.2）+ voice 槽位在。任一越界/缺失=拒绝。
func LoadManifest(dir string) (*Pack, error) {
	data, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, fmt.Errorf("scenepack: 读 %s 失败: %w", manifestName, err)
	}
	man, err := parseManifest(data)
	if err != nil {
		return nil, err
	}
	if err := validateManifest(man); err != nil {
		return nil, err
	}
	files := map[string][]byte{manifestName: data}
	for _, ref := range man.resourceRefs() {
		b, err := loadRef(dir, ref)
		if err != nil {
			return nil, err
		}
		files[normKey(ref)] = b
	}
	if err := collectVoiceSlot(dir, man.VoiceRef, files); err != nil {
		return nil, err
	}
	evb, ok := files[normKey(man.EvalSet.Path)]
	if !ok {
		return nil, fmt.Errorf("scenepack: eval_set 路径 %q 不在资源集内", man.EvalSet.Path)
	}
	entries, err := ParseEvalSet(evb)
	if err != nil {
		return nil, fmt.Errorf("scenepack: eval_set %q 不可用: %w", man.EvalSet.Path, err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("scenepack: eval_set %q 为空（空考卷=未交考卷，BI-16.2 内容自带验收）", man.EvalSet.Path)
	}
	if err := checkVoiceSlot(dir, man.VoiceRef); err != nil {
		return nil, err
	}
	return &Pack{Man: *man, Dir: dir, Files: files}, nil
}

// resourceRefs 全部被引用资源（人格卡/动作配置/知识/剧本/考卷）。
func (m *Manifest) resourceRefs() []string {
	refs := []string{m.PersonaCard, m.MotionConfig, m.EvalSet.Path}
	refs = append(refs, m.Knowledge...)
	refs = append(refs, m.Scripts...)
	return refs
}

// parseManifest 结构解析：required 全字段在场 + 类型正确 + eval_set/permissions
// 键集封闭（additionalProperties:false 镜像）。顶层未知键容忍（schema 未禁）。
func parseManifest(data []byte) (*Manifest, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("scenepack: manifest.json 非法 JSON 对象: %w", err)
	}
	for _, k := range manifestRequired {
		if _, ok := raw[k]; !ok {
			return nil, fmt.Errorf("scenepack: manifest 缺必填字段 %q（schema required——T16-G1-02）", k)
		}
	}
	var man Manifest
	strFields := []struct {
		key string
		dst *string
	}{
		{"pack_id", &man.PackID}, {"version", &man.Version},
		{"persona_card", &man.PersonaCard}, {"voice_ref", &man.VoiceRef},
		{"motion_config", &man.MotionConfig}, {"signature", &man.Signature},
	}
	for _, f := range strFields {
		if err := json.Unmarshal(raw[f.key], f.dst); err != nil {
			return nil, fmt.Errorf("scenepack: manifest.%s 须为字符串: %w", f.key, err)
		}
	}
	if err := json.Unmarshal(raw["knowledge"], &man.Knowledge); err != nil {
		return nil, fmt.Errorf("scenepack: manifest.knowledge 须为字符串数组: %w", err)
	}
	if err := json.Unmarshal(raw["scripts"], &man.Scripts); err != nil {
		return nil, fmt.Errorf("scenepack: manifest.scripts 须为字符串数组: %w", err)
	}
	if err := strictSubObject(raw["eval_set"], "eval_set", "path", "min_pass"); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw["eval_set"], &man.EvalSet); err != nil {
		return nil, fmt.Errorf("scenepack: manifest.eval_set 结构非法: %w", err)
	}
	if err := strictSubObject(raw["permissions"], "permissions", "memory_scopes", "actions", "volume_max"); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw["permissions"], &man.Perm); err != nil {
		return nil, fmt.Errorf("scenepack: manifest.permissions 结构非法: %w", err)
	}
	return &man, nil
}

// strictSubObject additionalProperties:false 镜像：子对象键集 ⊆ allowed。
func strictSubObject(raw json.RawMessage, name string, allowed ...string) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("scenepack: manifest.%s 须为对象: %w", name, err)
	}
	for k := range m {
		ok := false
		for _, a := range allowed {
			if k == a {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("scenepack: manifest.%s 含未声明键 %q（additionalProperties:false）", name, k)
		}
	}
	return nil
}

// validateManifest 值域校验：pack_id 非空（minLength 1）/ version 合 semver
// （schema pattern）/ 引用路径非空可解析 / signature 非空（占位声明制：签名
// 机制接入前须显式声明占位，密码学有效性不校验——T16-G1-02 报告注记）。
func validateManifest(man *Manifest) error {
	if strings.TrimSpace(man.PackID) == "" {
		return errors.New("scenepack: pack_id 为空（schema minLength 1）")
	}
	if !semverRe.MatchString(man.Version) {
		return fmt.Errorf("scenepack: version %q 不合 semver（schema pattern）", man.Version)
	}
	pathFields := []struct{ name, ref string }{
		{"persona_card", man.PersonaCard},
		{"voice_ref", man.VoiceRef},
		{"motion_config", man.MotionConfig},
		{"eval_set.path", man.EvalSet.Path},
	}
	for _, f := range pathFields {
		if strings.TrimSpace(f.ref) == "" {
			return fmt.Errorf("scenepack: manifest.%s 为空（引用须可解析）", f.name)
		}
	}
	for _, lst := range []struct {
		name  string
		items []string
	}{{"knowledge", man.Knowledge}, {"scripts", man.Scripts}} {
		for i, r := range lst.items {
			if strings.TrimSpace(r) == "" {
				return fmt.Errorf("scenepack: manifest.%s[%d] 为空（引用须可解析）", lst.name, i)
			}
		}
	}
	if strings.TrimSpace(man.Signature) == "" {
		return errors.New("scenepack: signature 为空（占位声明制：签名机制接入前须显式声明占位串——T16-G1-02 注记）")
	}
	return nil
}

// loadRef 读包内引用资源：包含性（拒绝绝对路径与 .. 逃逸）+ 齐备性（缺文件拒绝）。
func loadRef(dir, ref string) ([]byte, error) {
	clean := filepath.Clean(ref)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("scenepack: 引用 %q 逃逸包目录（越界拒绝）", ref)
	}
	b, err := os.ReadFile(filepath.Join(dir, clean))
	if err != nil {
		return nil, fmt.Errorf("scenepack: 资源缺失 %q（资源齐备——T16-G1-02）", ref)
	}
	return b, nil
}

// checkVoiceSlot voice 槽位校验：wav 本体不入 git（T18 内容管线打包时从受控
// 资产库注入并在 LICENSE 登记——assets-packs/*/voice/README 声明），故槽位
// 校验=引用文件在场（管线已注入），或所在目录存在且非空（README/LICENSE 登记
// 的注入锚点）。
func checkVoiceSlot(dir, ref string) error {
	clean := filepath.Clean(ref)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("scenepack: voice_ref %q 逃逸包目录（越界拒绝）", ref)
	}
	if fi, err := os.Stat(filepath.Join(dir, clean)); err == nil && !fi.IsDir() {
		return nil
	}
	ents, err := os.ReadDir(filepath.Join(dir, filepath.Dir(clean)))
	if err != nil || len(ents) == 0 {
		return fmt.Errorf("scenepack: voice 槽位缺失 %q（wav 本体不入 git，槽位目录须以 README/LICENSE 登记——T18 注入锚点）", ref)
	}
	return nil
}

// collectVoiceSlot 收集 voice 槽位锚点文件（README/LICENSE/已注入 wav——槽位
// 目录下全部普通文件）进 Files：包自包含面（registry 安装副本同过 LoadManifest
// 校验——wav 缺席时 README/LICENSE 即槽位登记面）。槽位目录缺失容忍（归
// checkVoiceSlot 报告）。
func collectVoiceSlot(dir, ref string, files map[string][]byte) error {
	clean := filepath.Clean(ref)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("scenepack: voice_ref %q 逃逸包目录（越界拒绝）", ref)
	}
	return filepath.WalkDir(filepath.Join(dir, filepath.Dir(clean)), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[normKey(rel)] = b
		return nil
	})
}

// Validate 校验内存包（Install 第一道闸——校验全过才动文件系统）：manifest
// 结构/值域合法 + Files 齐备（manifest 本体+全部引用资源+voice 槽位锚点+键
// 包含性——可安装包=可激活包，registry 副本同过 LoadManifest）+ 考卷可执行
// （≥1 条）。
func (p *Pack) Validate() error {
	if p == nil {
		return errors.New("scenepack: nil 包")
	}
	if err := validateManifest(&p.Man); err != nil {
		return err
	}
	if _, ok := p.Files[manifestName]; !ok {
		return fmt.Errorf("scenepack: Files 缺 %s", manifestName)
	}
	for k := range p.Files {
		clean := filepath.Clean(k)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("scenepack: Files 键 %q 逃逸包目录（越界拒绝）", k)
		}
	}
	for _, ref := range p.Man.resourceRefs() {
		if _, ok := p.Files[normKey(ref)]; !ok {
			return fmt.Errorf("scenepack: Files 缺引用资源 %q（资源齐备——T16-G1-02）", ref)
		}
	}
	if err := validateVoiceSlotFiles(p.Man.VoiceRef, p.Files); err != nil {
		return err
	}
	entries, err := ParseEvalSet(p.Files[normKey(p.Man.EvalSet.Path)])
	if err != nil {
		return fmt.Errorf("scenepack: eval_set 不可用: %w", err)
	}
	if len(entries) == 0 {
		return errors.New("scenepack: eval_set 为空（空考卷=未交考卷，BI-16.2）")
	}
	return nil
}

// validateVoiceSlotFiles 内存包 voice 槽位自包含校验：精确 ref 文件在场，或
// 槽位目录（ref 父目录）下 ≥1 锚点文件（README/LICENSE 登记——T18 注入锚点）。
func validateVoiceSlotFiles(ref string, files map[string][]byte) error {
	voiceKey := normKey(ref)
	if _, ok := files[voiceKey]; ok {
		return nil
	}
	prefix := path.Dir(voiceKey)
	for k := range files {
		if strings.HasPrefix(k, prefix+"/") {
			return nil
		}
	}
	return fmt.Errorf("scenepack: Files 缺 voice 槽位锚点 %q（wav 本体不入 git，README/LICENSE 须随包——T16-G1-02）", ref)
}

// ContentHash 内容哈希：Files 键字典序 + 内容逐项（路径\0+len+字节）sha256。
// 同包同版本同内容→同哈希（加载确定性属性断言面）；Dir 不参与（目录无关，
// 复制包目录不改变内容身份）。
func (p *Pack) ContentHash() string {
	if p == nil {
		return ""
	}
	keys := make([]string, 0, len(p.Files))
	for k := range p.Files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	var l [8]byte
	for _, k := range keys {
		b := p.Files[k]
		h.Write([]byte(k))
		h.Write([]byte{0})
		binary.BigEndian.PutUint64(l[:], uint64(len(b)))
		h.Write(l[:])
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ---- T9 内容预检（最小适配接口——包间零 import，由 loop/测试侧注入）----

// SafetyVerdict T9 内容分级判定的降维镜像（safety.Severity/Decision 的最小
// 适配面：注入方把 Crisis 或攻击拦截映射为 SafetyViolation）。
type SafetyVerdict int8

const (
	SafetyBenign SafetyVerdict = iota
	SafetySensitive
	// SafetyViolation 危机级或攻击拦截内容——违规（拒绝入包，不可豁免）。
	SafetyViolation
)

// SafetyClassifyFunc T9 预检注入面：文本 → 分级判定。由 loop/测试侧注入
// （safety.Engine.Classify/PreSpeak 的适配：Crisis/Intercepted→Violation）。
type SafetyClassifyFunc func(text string) SafetyVerdict

// Violation 一处内容安全违规（文件+行定位——报告与诊断面）。
type Violation struct {
	File string
	Line int
	Text string
}

// CheckContentSafety 包内容全量预检（T16-G0-01 面1）：Files 全部文本资源逐行
// 过注入分类器，返回违规清单（空=通过）。逐行粒度便于定位；应答单元（行/整
// 句）是行的子串且安全模式为正向命中，行级通过蕴含单元级通过。classify 为
// nil 时无判定面（调用方须 fail-closed——Install 已拒绝未注入态）。
func (p *Pack) CheckContentSafety(classify SafetyClassifyFunc) []Violation {
	if p == nil || classify == nil {
		return nil
	}
	keys := make([]string, 0, len(p.Files))
	for k := range p.Files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []Violation
	for _, k := range keys {
		for i, ln := range strings.Split(string(p.Files[k]), "\n") {
			t := strings.TrimSpace(ln)
			if t == "" {
				continue
			}
			if classify(t) == SafetyViolation {
				out = append(out, Violation{File: k, Line: i + 1, Text: t})
			}
		}
	}
	return out
}

// ---- 场景切换上下文（Activate 产出；loop 组装注入）----

// SceneCtx 场景切换后的注入上下文：PersonaFiles→T8 Compile、SafetyWords 并入
// T9 词表（人格边界=安全边界）、MotionTable→T12 表、EmoRules→T7 规则、
// Knowledge→Responder 上下文。本包只产出字节负载，不改消费方（零 import）。
type SceneCtx struct {
	PackID       string
	PersonaFiles []byte
	SafetyWords  []byte
	MotionTable  []byte
	EmoRules     []byte
	Knowledge    []byte
}

// personaShim persona 卡只读视图（宽松解析：未知键容忍——锚点/口癖/话题偏好/
// 禁忌四个消费面；越界值不影响本包语义）。
type personaShim struct {
	Catchphrases []struct {
		Phrase string `yaml:"phrase"`
	} `yaml:"catchphrases"`
	AnchorSentences []struct {
		Scenario string `yaml:"scenario"`
		Sentence string `yaml:"sentence"`
	} `yaml:"anchor_sentences"`
	TopicPreferences struct {
		Preferred []string `yaml:"preferred"`
		Avoid     []string `yaml:"avoid"`
	} `yaml:"topic_preferences"`
	Taboos []string `yaml:"taboos"`
}

// EmoRule 情绪→动作规则行（motion emotion_map 段只读视图；结构化重序列化
// 字段序确定——同包同 EmoRules 的确定性保证）。
type EmoRule struct {
	Emotion   string  `yaml:"emotion"`
	Motion    string  `yaml:"motion"`
	Intensity float64 `yaml:"intensity"`
}

// motionShim motion 配置只读视图（emotion_map 段=EmoRules 消费面）。
type motionShim struct {
	EmotionMap []EmoRule `yaml:"emotion_map"`
}

// BuildSceneCtx 组装场景上下文（纯函数，同包同 ctx——确定性）：人格卡与动作
// 配置整文、知识文件拼接、persona 禁忌→SafetyWords、motion emotion_map 段→
// EmoRules（重新序列化保持结构稳定）。
func BuildSceneCtx(p *Pack) SceneCtx {
	ctx := SceneCtx{PackID: p.Man.PackID}
	if b, ok := p.Files[normKey(p.Man.PersonaCard)]; ok {
		ctx.PersonaFiles = b
	}
	if b, ok := p.Files[normKey(p.Man.MotionConfig)]; ok {
		ctx.MotionTable = b
	}
	var ks [][]byte
	for _, k := range p.Man.Knowledge {
		if b, ok := p.Files[normKey(k)]; ok {
			ks = append(ks, b)
		}
	}
	ctx.Knowledge = bytes.Join(ks, []byte("\n"))
	var ps personaShim
	if yaml.Unmarshal(ctx.PersonaFiles, &ps) == nil && len(ps.Taboos) > 0 {
		ctx.SafetyWords = []byte(strings.Join(ps.Taboos, "\n"))
	}
	var ms motionShim
	if yaml.Unmarshal(ctx.MotionTable, &ms) == nil && len(ms.EmotionMap) > 0 {
		if b, err := yaml.Marshal(ms.EmotionMap); err == nil {
			ctx.EmoRules = b
		}
	}
	return ctx
}
