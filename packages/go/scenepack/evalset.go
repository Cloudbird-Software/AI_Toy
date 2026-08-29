// evalset —— 包自带考卷（BI-16.2 内容自带验收：装包即跑，作者不能只交内容
// 不交考卷）+ 规则面应答器（m3-spec §7）：考卷随包执行（ExecuteEvalSet），
// 应答=包内语料逐字回包（零编造面）或包外拒答脚手架（诱导说包外知识必拒）。
package scenepack

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// 考卷断言 metric 全集（对齐 tools/journeys 剧本断言契约；越界 metric 拒解析）。
const (
	MetricCompletionRate = "completion_rate"
	MetricLatencyP95Ms   = "latency_p95_ms"
	MetricSafetyEvents   = "safety_events"
	MetricMemoryHitRate  = "memory_hit_rate"
)

// evalMetricOps 断言 op 合法集（点估计口径）。
var evalMetricOps = map[string]bool{">=": true, "<=": true, "==": true, ">": true, "<": true}

// evalMetrics metric 合法集。
var evalMetrics = map[string]bool{
	MetricCompletionRate: true, MetricLatencyP95Ms: true,
	MetricSafetyEvents: true, MetricMemoryHitRate: true,
}

// EvalStep 考卷步骤：Say=用户话语；Expect=期望面（开放键——规则面消费
// reply_within_ms 等，未识别键容忍，schema 由 tools/journeys 演进）。
type EvalStep struct {
	Say    string
	Expect map[string]any
}

// EvalAssertion 单条断言（metric/op/value 点估计）。
type EvalAssertion struct {
	Metric string
	Op     string
	Value  float64
}

// EvalEntry 考卷条目（id/tier/persona/steps/assertions——tools/journeys 契约子集；
// inject 由 T20/chaos 面消费，本包解析容忍不执行）。
type EvalEntry struct {
	ID         string
	Tier       string
	Persona    map[string]any
	Steps      []EvalStep
	Assertions []EvalAssertion
}

// evalEntryRaw/evalStepRaw/evalAssertRaw YAML 只读视图（宽松解析：未知键容忍）。
type evalEntryRaw struct {
	ID         string         `yaml:"id"`
	Tier       string         `yaml:"tier"`
	Persona    map[string]any `yaml:"persona"`
	Steps      []evalStepRaw  `yaml:"steps"`
	Assertions []evalAssert   `yaml:"assertions"`
}

type evalStepRaw struct {
	Say    string         `yaml:"say"`
	Expect map[string]any `yaml:"expect"`
}

type evalAssert struct {
	Metric string  `yaml:"metric"`
	Op     string  `yaml:"op"`
	Value  float64 `yaml:"value"`
}

// ParseEvalSet 解析考卷：结构合法（id 非空/tier∈{core,variant}/≥1 话轮步/
// 断言 metric+op 合法且 value 有限）。steps 列表里的 expect-only 项（无 say、
// 只有 expect——种子包格式：话轮尾部期望块）归一化为末话轮步的 Expect（开放
// 键面，tools/journeys 契约）。空文档返回空表（空考卷=未交考卷的拒绝口径归
// 调用方——LoadManifest/Validate）。
func ParseEvalSet(data []byte) ([]EvalEntry, error) {
	var raw []evalEntryRaw
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("scenepack: eval_set YAML 不可解析: %w", err)
	}
	entries := make([]EvalEntry, 0, len(raw))
	for i, r := range raw {
		if strings.TrimSpace(r.ID) == "" {
			return nil, fmt.Errorf("scenepack: eval_set[%d] 缺 id", i)
		}
		if r.Tier != "core" && r.Tier != "variant" {
			return nil, fmt.Errorf("scenepack: eval_set[%d] tier %q ∉ {core,variant}", i, r.Tier)
		}
		if len(r.Steps) == 0 {
			return nil, fmt.Errorf("scenepack: eval_set[%d] %q 无步骤（空考卷条目）", i, r.ID)
		}
		var steps []EvalStep
		var tailExpect map[string]any
		for j, s := range r.Steps {
			if strings.TrimSpace(s.Say) == "" {
				if len(s.Expect) == 0 {
					return nil, fmt.Errorf("scenepack: eval_set[%d] step[%d] 既无 say 也无 expect（结构非法）", i, j)
				}
				if tailExpect == nil {
					tailExpect = map[string]any{}
				}
				for k, v := range s.Expect { // 期望声明项：并入末话轮步
					tailExpect[k] = v
				}
				continue
			}
			steps = append(steps, EvalStep{Say: s.Say, Expect: s.Expect})
		}
		if len(steps) == 0 {
			return nil, fmt.Errorf("scenepack: eval_set[%d] %q 无话轮步（空考卷条目）", i, r.ID)
		}
		if len(tailExpect) > 0 {
			last := &steps[len(steps)-1]
			if last.Expect == nil {
				last.Expect = map[string]any{}
			}
			for k, v := range tailExpect {
				last.Expect[k] = v
			}
		}
		if len(r.Assertions) == 0 {
			return nil, fmt.Errorf("scenepack: eval_set[%d] %q 无断言（交考卷须带判分口径）", i, r.ID)
		}
		asserts := make([]EvalAssertion, 0, len(r.Assertions))
		for j, a := range r.Assertions {
			if !evalMetrics[a.Metric] {
				return nil, fmt.Errorf("scenepack: eval_set[%d] 断言[%d] metric %q 越界（∈ {completion_rate, latency_p95_ms, safety_events, memory_hit_rate}）", i, j, a.Metric)
			}
			if !evalMetricOps[a.Op] {
				return nil, fmt.Errorf("scenepack: eval_set[%d] 断言[%d] op %q 越界", i, j, a.Op)
			}
			if math.IsNaN(a.Value) || math.IsInf(a.Value, 0) {
				return nil, fmt.Errorf("scenepack: eval_set[%d] 断言[%d] value 非有限值", i, j)
			}
			asserts = append(asserts, EvalAssertion{Metric: a.Metric, Op: a.Op, Value: a.Value})
		}
		entries = append(entries, EvalEntry{ID: r.ID, Tier: r.Tier, Persona: r.Persona, Steps: steps, Assertions: asserts})
	}
	return entries, nil
}

// ---- 包内语料与规则面应答器 ----

// 内容命中双阈值：共享内容 bigram ≥2 且覆盖查询 bigram ≥0.5（单个功能词巧合
// 不构成知识域命中——「小熊，恐龙」≠ 恐龙知识在包内）。
const (
	matchMinShared = 2
	matchMinRatio  = 0.5
)

// contentStopBigrams 泛用功能词 bigram（匹配面剔除：命中不构成知识证据）。
var contentStopBigrams = map[string]bool{
	"我们": true, "你们": true, "他们": true, "她们": true,
	"一个": true, "这个": true, "那个": true, "一些": true,
	"什么": true, "为什": true, "是什": true, "怎么": true,
	"哪个": true, "哪里": true, "哪些": true, "可以": true,
	"知道": true, "告诉": true, "现在": true, "今天": true,
	"明天": true, "昨天": true, "一起": true, "一下": true,
	"谢谢": true, "再见": true, "你好": true, "好吗": true,
	"你说": true, "我说": true, "他说": true, "她说": true,
}

// isContentRune 内容字符：CJK 统一表意或 ASCII 字母数字（标点/空白/其他
// 符号剥离——语气词与分隔符不参与匹配）。
func isContentRune(r rune) bool {
	if r >= 0x4E00 && r <= 0x9FFF {
		return true
	}
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}

// contentBigrams 提取内容 bigram 集：剥离非内容字符后的相邻 rune 对，剔除
// 功能词对（匹配面=知识证据面）。
func contentBigrams(s string) map[string]bool {
	var keep []rune
	for _, r := range s {
		if isContentRune(r) {
			keep = append(keep, r)
		}
	}
	out := make(map[string]bool)
	for i := 0; i+1 < len(keep); i++ {
		if p := string(keep[i]) + string(keep[i+1]); !contentStopBigrams[p] {
			out[p] = true
		}
	}
	return out
}

// corpusEntry 语料条目：match=匹配面（锚点=场景+例句），reply=应答面（锚点=
// 例句；其余=行本体）——应答恒为包内原句（逐字回包）。
type corpusEntry struct {
	match   string
	reply   string
	bigrams map[string]bool
}

// Corpus 包内会话语料（规则面应答器的知识域）：锚点例句/口癖/首选话题/
// knowledge 行/scripts 台词。禁忌与 avoid 话题不入语料（约束面走 SafetyWords，
// 不做应答内容）。topic=拒答脚手架引用的首选话题（空=通用脚手架）。
type Corpus struct {
	entries []corpusEntry
	topic   string
}

// BuildCorpus 组装包内语料（确定性纯函数：同包同语料）。
func BuildCorpus(p *Pack) Corpus {
	var c Corpus
	if p == nil {
		return c
	}
	add := func(match, reply string) {
		m, r := strings.TrimSpace(match), strings.TrimSpace(reply)
		if m == "" || r == "" {
			return
		}
		c.entries = append(c.entries, corpusEntry{match: m, reply: r, bigrams: contentBigrams(m)})
	}
	for _, k := range p.Man.Knowledge {
		if b, ok := p.Files[normKey(k)]; ok {
			for _, ln := range markdownLines(string(b)) {
				add(ln, ln)
			}
		}
	}
	for _, s := range p.Man.Scripts {
		if b, ok := p.Files[normKey(s)]; ok {
			var sh scriptsShim
			if yaml.Unmarshal(b, &sh) == nil {
				for _, st := range sh.Steps {
					add(st.Say, st.Say)
				}
			}
		}
	}
	if b, ok := p.Files[normKey(p.Man.PersonaCard)]; ok {
		var ps personaShim
		if yaml.Unmarshal(b, &ps) == nil {
			for _, cp := range ps.Catchphrases {
				add(cp.Phrase, cp.Phrase)
			}
			for _, a := range ps.AnchorSentences {
				add(a.Scenario+"。"+a.Sentence, a.Sentence) // 场景=匹配面，例句=应答面
			}
			for _, tp := range ps.TopicPreferences.Preferred {
				add(tp, tp)
				if c.topic == "" {
					c.topic = tp
				}
			}
		}
	}
	return c
}

// Lines 语料应答面清单（观测面：应答 ⊆ 语料原句的断言口径）。
func (c Corpus) Lines() []string {
	out := make([]string, 0, len(c.entries))
	for _, e := range c.entries {
		out = append(out, e.reply)
	}
	return out
}

// RefusalText 拒答脚手架（确定性；不引用任何包外知识——诱导说包外知识必拒
// 的应答面）。
func (c Corpus) RefusalText() string {
	if c.topic == "" {
		return "这个我还不太懂呢，我们换一个都懂的话题聊聊吧。"
	}
	return "这个我还不太懂呢。要不要聊聊" + c.topic + "？"
}

// Response 规则面应答。
type Response struct {
	Text    string // 应答文本（语料原句或拒答脚手架——零编造面）
	InPack  bool   // 命中包内语料（逐字回包）
	Refused bool   // 包外拒答（脚手架）
}

// Respond 规则面应答器（确定性纯函数）：query 与语料内容重合达标（共享
// bigram ≥2 且覆盖 ≥0.5）→ 逐字回最优匹配行；否则拒答脚手架。
func (c Corpus) Respond(query string) Response {
	q := contentBigrams(query)
	if len(q) == 0 || len(c.entries) == 0 {
		return Response{Text: c.RefusalText(), Refused: true}
	}
	best, bestShared := "", 0
	for _, e := range c.entries {
		shared := 0
		for b := range q {
			if e.bigrams[b] {
				shared++
			}
		}
		if shared > bestShared {
			best, bestShared = e.reply, shared
		}
	}
	if bestShared >= matchMinShared && float64(bestShared)/float64(len(q)) >= matchMinRatio {
		return Response{Text: best, InPack: true}
	}
	return Response{Text: c.RefusalText(), Refused: true}
}

// markdownLines markdown 行清洗：剥离标题/列表/引用标记与反引号，去空行。
func markdownLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		t := strings.TrimSpace(ln)
		t = strings.TrimLeft(t, "#>-` ")
		t = strings.TrimSpace(t)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// scriptsShim scripts YAML 只读视图（steps[].say=台词面）。
type scriptsShim struct {
	Steps []struct {
		Say string `yaml:"say"`
	} `yaml:"steps"`
}

// ---- 考卷执行（规则面）----

// StepResult 单步执行结果。
type StepResult struct {
	Query     string
	Response  string
	InPack    bool
	Refused   bool
	Safe      bool    // 应答过 T9 分类器（违规=未通过）
	LatencyMs float64 // 规则面应答墙钟（查表面——决策计数口径注记）
}

// AssertionResult 单断言判定（观测 vs 期望）。
type AssertionResult struct {
	Metric   string
	Op       string
	Value    float64
	Observed float64
	Holds    bool
}

// EntryResult 单条目执行结果。
type EntryResult struct {
	EntryID    string
	Steps      []StepResult
	Assertions []AssertionResult
	Passed     bool
}

// EvalReport 整卷执行报告（Entries=执行条目数——执行率口径；Passed/Score=
// 判分口径）。
type EvalReport struct {
	Entries  []EntryResult
	Executed int
	Passed   int
	Score    float64
}

// ExecuteEvalSet 执行包自带考卷（考卷随包执行——安装即跑）：每条目逐步过
// 规则面应答器（in-pack 逐字回包/包外拒答），应答过注入分类器（safety_events
// 口径），断言按观测值判定。classify 为 nil=拒绝执行（fail-closed——内容安全
// 不可豁免，T16-G0-01）。
func ExecuteEvalSet(p *Pack, classify SafetyClassifyFunc) (*EvalReport, error) {
	if p == nil {
		return nil, fmt.Errorf("scenepack: nil 包")
	}
	if classify == nil {
		return nil, fmt.Errorf("scenepack: 须注入 T9 分类器（fail-closed——考卷应答安全面不可缺省）")
	}
	entries, err := ParseEvalSet(p.Files[normKey(p.Man.EvalSet.Path)])
	if err != nil {
		return nil, fmt.Errorf("scenepack: eval_set %q 不可用: %w", p.Man.EvalSet.Path, err)
	}
	corpus := BuildCorpus(p)
	rep := &EvalReport{Entries: make([]EntryResult, 0, len(entries)), Executed: len(entries)}
	for _, ent := range entries {
		er := EntryResult{EntryID: ent.ID}
		lat := make([]float64, 0, len(ent.Steps))
		inPack, safeSteps := 0, 0
		for _, st := range ent.Steps {
			t0 := time.Now()
			r := corpus.Respond(st.Say)
			d := time.Since(t0).Seconds() * 1000
			sr := StepResult{Query: st.Say, Response: r.Text, InPack: r.InPack,
				Refused: r.Refused, Safe: classify(r.Text) != SafetyViolation, LatencyMs: d}
			er.Steps = append(er.Steps, sr)
			lat = append(lat, d)
			if r.InPack {
				inPack++
			}
			if sr.Safe {
				safeSteps++
			}
		}
		n := float64(len(ent.Steps))
		passed := true
		for _, a := range ent.Assertions {
			var obs float64
			switch a.Metric {
			case MetricCompletionRate:
				obs = float64(safeSteps) / n // 完成率=安全应答步占比（应答器恒应答：in-pack 或拒答）
			case MetricSafetyEvents:
				obs = n - float64(safeSteps) // 危机/攻击应答计数
			case MetricLatencyP95Ms:
				obs = p95(lat)
			case MetricMemoryHitRate:
				obs = float64(inPack) / n // 规则面记忆命中=知识域命中（T10 真记忆面联跑归 #105 后）
			}
			ar := AssertionResult{Metric: a.Metric, Op: a.Op, Value: a.Value, Observed: obs,
				Holds: evalCmp(obs, a.Op, a.Value)}
			er.Assertions = append(er.Assertions, ar)
			if !ar.Holds {
				passed = false // 任一断言不过即该条目不过
			}
		}
		er.Passed = passed
		if passed {
			rep.Passed++
		}
		rep.Entries = append(rep.Entries, er)
	}
	if rep.Executed > 0 {
		rep.Score = float64(rep.Passed) / float64(rep.Executed)
	}
	return rep, nil
}

// evalCmp 观测 vs 期望（op 点估计口径）。
func evalCmp(obs float64, op string, want float64) bool {
	switch op {
	case ">=":
		return obs >= want
	case "<=":
		return obs <= want
	case "==":
		return obs == want
	case ">":
		return obs > want
	case "<":
		return obs < want
	}
	return false
}

// p95 分位数（升序取 ceil(0.95n)-1 位；空表=0）。
func p95(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64{}, xs...)
	sort.Float64s(s)
	idx := int(math.Ceil(0.95*float64(len(s)))) - 1
	if idx < 0 {
		idx = 0
	}
	return s[idx]
}
