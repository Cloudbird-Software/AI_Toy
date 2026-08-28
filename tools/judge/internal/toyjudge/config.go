// config —— rubric 三级量表与 model.yaml judge 配置的加载与校验（spec §3.3）。
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

// JudgeConfig 是 model.yaml 中一条 judge 配置（model/temperature/prompt 三字段锁定）。
type JudgeConfig struct {
	Model       string
	Temperature float64
	Prompt      string
}

// JudgeInfo 是进报告的 judge 身份：model/temperature 三字段 + prompt 与配置的哈希
// （prompt 文本不落报告，只落哈希，BAML-1）。
type JudgeInfo struct {
	Model        string  `json:"model"`
	Temperature  float64 `json:"temperature"`
	PromptSHA256 string  `json:"prompt_sha256"`
	ConfigSHA256 string  `json:"config_sha256"`
}

// Info 返回带哈希的 judge 身份；config 哈希取三字段 canonical JSON 的 sha256。
func (c JudgeConfig) Info() JudgeInfo {
	canonical, _ := json.Marshal(struct {
		Model       string  `json:"model"`
		Temperature float64 `json:"temperature"`
		Prompt      string  `json:"prompt"`
	}{c.Model, c.Temperature, c.Prompt})
	cfgSum, promptSum := sha256.Sum256(canonical), sha256.Sum256([]byte(c.Prompt))
	return JudgeInfo{Model: c.Model, Temperature: c.Temperature,
		PromptSHA256: hex.EncodeToString(promptSum[:]), ConfigSHA256: hex.EncodeToString(cfgSum[:])}
}

// ModelConfig 是 configs/judge/model.yaml：judge 配置表 + 文件内容哈希。
type ModelConfig struct {
	Judges []JudgeConfig
	SHA256 string
}

type judgeFile struct {
	Model       *string  `yaml:"model"`
	Temperature *float64 `yaml:"temperature"`
	Prompt      *string  `yaml:"prompt"`
}

type modelFile struct {
	Judges []judgeFile `yaml:"judges"`
}

// LoadModelConfig 读取 model.yaml fixture：每条 judge 必须同时具备
// model/temperature/prompt 三字段，缺任一即配置错误（CLI 映射 exit 2）。
func LoadModelConfig(path string) (*ModelConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("model 配置不可读: %w", err)
	}
	var f modelFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("model 配置 YAML 解析失败: %w", err)
	}
	if len(f.Judges) == 0 {
		return nil, fmt.Errorf("model 配置至少需要一条 judge（judges 列表为空）")
	}
	m := &ModelConfig{Judges: make([]JudgeConfig, len(f.Judges))}
	for i, j := range f.Judges {
		switch {
		case j.Model == nil || strings.TrimSpace(*j.Model) == "":
			return nil, fmt.Errorf("model.yaml judges[%d]: model 字段缺失", i)
		case j.Temperature == nil:
			return nil, fmt.Errorf("model.yaml judges[%d]: temperature 字段缺失", i)
		case j.Prompt == nil || strings.TrimSpace(*j.Prompt) == "":
			return nil, fmt.Errorf("model.yaml judges[%d]: prompt 字段缺失", i)
		}
		m.Judges[i] = JudgeConfig{Model: *j.Model, Temperature: *j.Temperature, Prompt: *j.Prompt}
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

// SelectJudges 选定评审席位：常规 rubric 取首席；高风险 rubric（9a 类）取前两席
// 且必须异族——「双 judge 不同厂商，同族 judge 拒绝」（spec §3.3/§11.12）。
func (m *ModelConfig) SelectJudges(highRisk bool) ([]JudgeInfo, error) {
	first := m.Judges[0].Info()
	if !highRisk {
		return []JudgeInfo{first}, nil
	}
	if len(m.Judges) < 2 {
		return nil, fmt.Errorf("高风险 rubric 需要双 judge，model.yaml 只有 %d 条 judge 配置", len(m.Judges))
	}
	second := m.Judges[1].Info()
	if family := modelFamily(first.Model); family == modelFamily(second.Model) {
		return nil, fmt.Errorf("高风险双 judge 同族（%s 与 %s 均为 %s 族），须不同厂商",
			first.Model, second.Model, family)
	}
	return []JudgeInfo{first, second}, nil
}
