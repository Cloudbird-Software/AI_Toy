// config —— rubric 三级量表与 model.yaml judge 配置（§4.2 schema）的加载与校验
// （spec §3.3/§4.2）。
package toyjudge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// 三级量表纪律（spec §3.3）：levels 恰为 [1,2,3] 且每级锚定，禁 1–5 打分量表。
var wantLevels = [...]int{1, 2, 3}

// Level 是量表的一档：级别 + 锚定样例。
type Level struct {
	Level  int
	Anchor string
}

// Criterion 是 rubric 的一个评分维度（独立打三级分）。
type Criterion struct {
	Name   string
	Levels []Level
}

// Rubric 是通过校验的 rubric；SHA256 为源文件内容哈希（随报告落盘）。
type Rubric struct {
	ID       string
	HighRisk bool
	Criteria []Criterion
	SHA256   string
}

type levelFile struct {
	Level  *int    `yaml:"level"`
	Anchor *string `yaml:"anchor"`
}

type criterionFile struct {
	Name   *string     `yaml:"name"`
	Levels []levelFile `yaml:"levels"`
}

type rubricFile struct {
	ID       *string         `yaml:"id"`
	HighRisk bool            `yaml:"high_risk"`
	Criteria []criterionFile `yaml:"criteria"`
}

// ParseRubric 解析并校验 rubric：id 非空且等于 wantID；criteria ≥1 且名字唯一；
// 每 criterion 的 levels 恰为 [1,2,3] 且每级 anchor 非空（缺任一即校验失败）。
func ParseRubric(data []byte, wantID string) (*Rubric, error) {
	var f rubricFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("rubric %s: YAML 解析失败: %w", wantID, err)
	}
	if f.ID == nil || strings.TrimSpace(*f.ID) == "" {
		return nil, fmt.Errorf("rubric %s: id 缺失", wantID)
	}
	if *f.ID != wantID {
		return nil, fmt.Errorf("rubric 文件 id %q 与请求的 %q 不符", *f.ID, wantID)
	}
	if len(f.Criteria) == 0 {
		return nil, fmt.Errorf("rubric %s: criteria 不能为空", wantID)
	}
	r := &Rubric{ID: *f.ID, HighRisk: f.HighRisk}
	for i, c := range f.Criteria {
		if c.Name == nil || strings.TrimSpace(*c.Name) == "" {
			return nil, fmt.Errorf("rubric %s: criterion[%d] 名字缺失", wantID, i)
		}
		for j := range i {
			if f.Criteria[j].Name != nil && *f.Criteria[j].Name == *c.Name {
				return nil, fmt.Errorf("rubric %s: criterion 名字重复: %s", wantID, *c.Name)
			}
		}
		for _, l := range c.Levels {
			if l.Level == nil {
				return nil, fmt.Errorf("rubric %s/%s: 存在缺 level 的档位", wantID, *c.Name)
			}
			if l.Anchor == nil || strings.TrimSpace(*l.Anchor) == "" {
				return nil, fmt.Errorf("rubric %s/%s: level %d 缺 anchor（每级锚定样例）", wantID, *c.Name, *l.Level)
			}
		}
		if len(c.Levels) != len(wantLevels) {
			return nil, fmt.Errorf("rubric %s/%s: 必须恰为三级量表 [1,2,3]（禁 1–5 量表），实际 %d 级",
				wantID, *c.Name, len(c.Levels))
		}
		slices.SortFunc(c.Levels, func(a, b levelFile) int { return *a.Level - *b.Level })
		got := [len(wantLevels)]int{}
		for j, l := range c.Levels {
			got[j] = *l.Level
		}
		if got != wantLevels {
			return nil, fmt.Errorf("rubric %s/%s: levels 须恰为 [1,2,3]，实际 %v", wantID, *c.Name, got)
		}
		r.Criteria = append(r.Criteria, Criterion{Name: *c.Name, Levels: []Level{
			{Level: *c.Levels[0].Level, Anchor: *c.Levels[0].Anchor},
			{Level: *c.Levels[1].Level, Anchor: *c.Levels[1].Anchor},
			{Level: *c.Levels[2].Level, Anchor: *c.Levels[2].Anchor},
		}})
	}
	return r, nil
}

// LoadRubric 从 dir/<id>.yaml 读取并校验 rubric，携带文件内容哈希。
func LoadRubric(dir, id string) (*Rubric, error) {
	data, err := os.ReadFile(filepath.Join(dir, id+".yaml"))
	if err != nil {
		return nil, fmt.Errorf("rubric %s 不可读: %w", id, err)
	}
	r, err := ParseRubric(data, id)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	r.SHA256 = hex.EncodeToString(sum[:])
	return r, nil
}

// JudgeConfig 是 model.yaml 的一个 judge 席位（§4.2）：provider/model/temperature
// 锁定读取；prompt 不再来自 model.yaml（schema 无 prompt 字段），由 rubric 派生
// （见 JudgeInfo）。
type JudgeConfig struct {
	Provider    string
	Model       string
	Temperature float64
}

// JudgeInfo 是进报告的 judge 身份：model/temperature + prompt 与配置的哈希
// （prompt 文本不落报告，只落哈希，BAML-1）。
type JudgeInfo struct {
	Model        string  `json:"model"`
	Temperature  float64 `json:"temperature"`
	PromptSHA256 string  `json:"prompt_sha256"`
	ConfigSHA256 string  `json:"config_sha256"`
}

// judgePrompt 从 rubric 派生 judge prompt 文本（§4.2 schema 无 prompt 字段；
// BAML-1 客户端接入前以 rubric id + criteria 序列化为替身，rubric 变更即 prompt 变更）。
func judgePrompt(rubric *Rubric) string {
	if rubric == nil {
		return ""
	}
	data, _ := json.Marshal(struct {
		ID       string      `json:"id"`
		Criteria []Criterion `json:"criteria"`
	}{rubric.ID, rubric.Criteria})
	return string(data)
}

// Info 返回带哈希的 judge 身份；prompt 哈希取 rubric 派生文本的 sha256，
// config 哈希取 provider/model/temperature/prompt canonical JSON 的 sha256。
func (c JudgeConfig) Info(rubric *Rubric) JudgeInfo {
	prompt := judgePrompt(rubric)
	canonical, _ := json.Marshal(struct {
		Provider    string  `json:"provider"`
		Model       string  `json:"model"`
		Temperature float64 `json:"temperature"`
		Prompt      string  `json:"prompt"`
	}{c.Provider, c.Model, c.Temperature, prompt})
	cfgSum, promptSum := sha256.Sum256(canonical), sha256.Sum256([]byte(prompt))
	return JudgeInfo{Model: c.Model, Temperature: c.Temperature,
		PromptSHA256: hex.EncodeToString(promptSum[:]), ConfigSHA256: hex.EncodeToString(cfgSum[:])}
}

// KappaGate 是 κ 门禁阈值（§4.2 policy.kappa_gate）：automation 供自动化门禁，
// ci_autonomous 供 CI 自主合入门禁。
type KappaGate struct {
	Automation   float64
	CIAutonomous float64
}

// Policy 是评审策略（§4.2 policy）。
type Policy struct {
	PairwiseSwap  bool
	TieOnDisagree bool
	Recalibrate   string
	KappaGate     KappaGate
}

// ModelConfig 是 configs/judge/model.yaml（§4.2 schema）：默认 judge、高风险双席、
// 策略、金标目录 + 文件内容哈希。
type ModelConfig struct {
	JudgeDefault   JudgeConfig
	JudgesHighRisk [2]string
	Policy         Policy
	GoldDir        string
	SHA256         string
}

type judgeDefaultFile struct {
	Provider    *string  `yaml:"provider"`
	Model       *string  `yaml:"model"`
	Temperature *float64 `yaml:"temperature"`
	Locked      *bool    `yaml:"locked"`
}

type kappaGateFile struct {
	Automation   *float64 `yaml:"automation"`
	CIAutonomous *float64 `yaml:"ci_autonomous"`
}

type policyFile struct {
	PairwiseSwap  *bool          `yaml:"pairwise_swap"`
	TieOnDisagree *bool          `yaml:"tie_on_disagree"`
	Recalibrate   string         `yaml:"recalibrate"`
	KappaGate     *kappaGateFile `yaml:"kappa_gate"`
}

type modelFile struct {
	JudgeDefault   *judgeDefaultFile `yaml:"judge_default"`
	JudgesHighRisk []string          `yaml:"judges_high_risk"`
	Policy         *policyFile       `yaml:"policy"`
	GoldDir        *string           `yaml:"gold_dir"`
}

// LoadModelConfig 读取 model.yaml（§4.2 schema）：judge_default 的
// provider/model/temperature/locked 四字段必填且 locked 须为 true（锁定纪律）；
// judges_high_risk 恰 2 条且异族；policy.kappa_gate 两阈值必填且落在 (0,1]；
// pairwise_swap/tie_on_disagree 须为 true（spec §3.3 强制行为，本卡唯一支持）；
// gold_dir 非空。任一不满足即配置错误（CLI 映射 exit 2）。
func LoadModelConfig(path string) (*ModelConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("model 配置不可读: %w", err)
	}
	var f modelFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("model 配置 YAML 解析失败: %w", err)
	}
	if f.JudgeDefault == nil {
		return nil, fmt.Errorf("model.yaml: judge_default 缺失（§4.2 schema）")
	}
	switch {
	case f.JudgeDefault.Provider == nil || strings.TrimSpace(*f.JudgeDefault.Provider) == "":
		return nil, fmt.Errorf("model.yaml judge_default: provider 字段缺失")
	case f.JudgeDefault.Model == nil || strings.TrimSpace(*f.JudgeDefault.Model) == "":
		return nil, fmt.Errorf("model.yaml judge_default: model 字段缺失")
	case f.JudgeDefault.Temperature == nil:
		return nil, fmt.Errorf("model.yaml judge_default: temperature 字段缺失")
	case f.JudgeDefault.Locked == nil:
		return nil, fmt.Errorf("model.yaml judge_default: locked 字段缺失")
	}
	if !*f.JudgeDefault.Locked {
		return nil, fmt.Errorf("model.yaml judge_default: locked=false 违反锁定纪律（judge 配置须锁定）")
	}
	if len(f.JudgesHighRisk) != 2 {
		return nil, fmt.Errorf("model.yaml judges_high_risk: 须恰 2 条（高风险双 judge），实际 %d 条", len(f.JudgesHighRisk))
	}
	for i, model := range f.JudgesHighRisk {
		if strings.TrimSpace(model) == "" {
			return nil, fmt.Errorf("model.yaml judges_high_risk[%d]: 模型名为空", i)
		}
	}
	if family := modelFamily(f.JudgesHighRisk[0]); family == modelFamily(f.JudgesHighRisk[1]) {
		return nil, fmt.Errorf("model.yaml judges_high_risk 同族（%s 与 %s 均为 %s 族），须不同厂商",
			f.JudgesHighRisk[0], f.JudgesHighRisk[1], family)
	}
	if f.Policy == nil || f.Policy.KappaGate == nil {
		return nil, fmt.Errorf("model.yaml: policy.kappa_gate 缺失")
	}
	kg := f.Policy.KappaGate
	switch {
	case kg.Automation == nil:
		return nil, fmt.Errorf("model.yaml policy.kappa_gate: automation 字段缺失")
	case kg.CIAutonomous == nil:
		return nil, fmt.Errorf("model.yaml policy.kappa_gate: ci_autonomous 字段缺失")
	case *kg.Automation <= 0 || *kg.Automation > 1:
		return nil, fmt.Errorf("model.yaml policy.kappa_gate.automation=%v 越界（须 (0,1]）", *kg.Automation)
	case *kg.CIAutonomous <= 0 || *kg.CIAutonomous > 1:
		return nil, fmt.Errorf("model.yaml policy.kappa_gate.ci_autonomous=%v 越界（须 (0,1]）", *kg.CIAutonomous)
	}
	switch {
	case f.Policy.PairwiseSwap == nil:
		return nil, fmt.Errorf("model.yaml policy: pairwise_swap 字段缺失")
	case !*f.Policy.PairwiseSwap:
		return nil, fmt.Errorf("model.yaml policy: pairwise_swap=false 与 spec §3.3 强制行为冲突（AB/BA 各评一次）")
	case f.Policy.TieOnDisagree == nil:
		return nil, fmt.Errorf("model.yaml policy: tie_on_disagree 字段缺失")
	case !*f.Policy.TieOnDisagree:
		return nil, fmt.Errorf("model.yaml policy: tie_on_disagree=false 与 spec §3.3 强制行为冲突（不一致记 tie）")
	}
	if f.GoldDir == nil || strings.TrimSpace(*f.GoldDir) == "" {
		return nil, fmt.Errorf("model.yaml: gold_dir 字段缺失")
	}
	m := &ModelConfig{
		JudgeDefault: JudgeConfig{Provider: *f.JudgeDefault.Provider,
			Model: *f.JudgeDefault.Model, Temperature: *f.JudgeDefault.Temperature},
		JudgesHighRisk: [2]string{f.JudgesHighRisk[0], f.JudgesHighRisk[1]},
		Policy: Policy{PairwiseSwap: true, TieOnDisagree: true, Recalibrate: f.Policy.Recalibrate,
			KappaGate: KappaGate{Automation: *kg.Automation, CIAutonomous: *kg.CIAutonomous}},
		GoldDir: *f.GoldDir,
	}
	sum := sha256.Sum256(data)
	m.SHA256 = hex.EncodeToString(sum[:])
	return m, nil
}

// modelFamily 取模型族：路径末段首个 '-' 之前的小写前缀
// （claude-sonnet-4-5 → claude；gpt-4o → gpt；anthropic/claude-3 → claude）。
func modelFamily(model string) string {
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	if i := strings.Index(model, "-"); i >= 0 {
		model = model[:i]
	}
	return strings.ToLower(model)
}

// SelectJudges 选定评审席位（§4.2）：常规 rubric → judge_default 单席；
// 高风险 rubric（9a 类）→ judges_high_risk 双席且必须异族——「双 judge 不同厂商，
// 同族 judge 拒绝」（spec §3.3/§11.12）。§4.2 只给双席模型名，温度沿用
// judge_default；prompt 哈希由 rubric 派生（见 JudgeInfo）。
func (m *ModelConfig) SelectJudges(rubric *Rubric) ([]JudgeInfo, error) {
	if rubric == nil {
		return nil, fmt.Errorf("SelectJudges 需要 rubric（席位与 prompt 哈希均随 rubric 走）")
	}
	if !rubric.HighRisk {
		return []JudgeInfo{m.JudgeDefault.Info(rubric)}, nil
	}
	seats := make([]JudgeInfo, len(m.JudgesHighRisk))
	for i, model := range m.JudgesHighRisk {
		seats[i] = JudgeConfig{Model: model, Temperature: m.JudgeDefault.Temperature}.Info(rubric)
	}
	if family := modelFamily(seats[0].Model); family == modelFamily(seats[1].Model) {
		return nil, fmt.Errorf("高风险双 judge 同族（%s 与 %s 均为 %s 族），须不同厂商",
			seats[0].Model, seats[1].Model, family)
	}
	return seats, nil
}
