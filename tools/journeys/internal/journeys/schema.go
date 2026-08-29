package journeys

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// 剧本 schema 白名单（spec §3.5/§8.1）。
var (
	tiers = []string{"core", "variant"}
	// metricNames：4 基础指标 + 4 级安全分型（IR #67：区分危机安抚失败与越狱成功等严重级别；
	// safety_events 保留为四分型之和的总和口径，兼容旧报告）。
	metricNames  = []string{"completion_rate", "latency_p95_ms", "safety_events", "memory_hit_rate", "safety_crisis", "safety_jailbreak", "safety_adult", "safety_commercial"}
	assertionOps = []string{">=", "<=", ">", "<", "=="}
)

// Script 是一条通过校验的黄金旅程剧本；Persona 仅校验 age/patience 键存在。
type Script struct {
	ID, Tier, Source string
	Persona          map[string]any
	Steps            []any
	Inject           Inject
	Assertions       []Assertion
}

type Inject struct {
	Interrupts   []any `yaml:"interrupts"`
	SafetyEvents []any `yaml:"safety_events"`
}

type Assertion struct {
	Metric, Op string
	Value      float64
}

// scriptFile/assertFile：指针字段用于区分「字段缺失」与「零值」。
type scriptFile struct {
	ID         *string        `yaml:"id"`
	Tier       *string        `yaml:"tier"`
	Persona    map[string]any `yaml:"persona"`
	Steps      []any          `yaml:"steps"`
	Inject     *Inject        `yaml:"inject"`
	Assertions []assertFile   `yaml:"assertions"`
}

type assertFile struct {
	Metric *string  `yaml:"metric"`
	Op     *string  `yaml:"op"`
	Value  *float64 `yaml:"value"`
}

// ParseScript 校验单条剧本全字段：tier 白名单、断言 metric ∈ 8 指标（4 基础 + 4 级安全分型）、op ∈ 5 操作符。
func ParseScript(data []byte, source string) (*Script, error) {
	where := ""
	if source != "" {
		where = " [" + source + "]"
	}
	var f scriptFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("invalid YAML: %s (%v)", source, err)
	}
	s := &Script{Source: source}
	if f.ID == nil || strings.TrimSpace(*f.ID) == "" {
		return nil, fmt.Errorf("id must be a non-empty string%s", where)
	}
	s.ID = *f.ID
	tier := ""
	if f.Tier != nil {
		tier = *f.Tier
	}
	if !slices.Contains(tiers, tier) {
		return nil, fmt.Errorf("tier must be one of %v, got %q%s", tiers, tier, where)
	}
	s.Tier = tier
	if f.Persona == nil {
		return nil, fmt.Errorf("persona must be a mapping%s", where)
	}
	for _, k := range []string{"age", "patience"} {
		if _, ok := f.Persona[k]; !ok {
			return nil, fmt.Errorf("persona missing required field: %s%s", k, where)
		}
	}
	s.Persona = f.Persona
	if f.Inject == nil {
		return nil, fmt.Errorf("inject must be a mapping%s", where)
	}
	s.Inject = *f.Inject
	if len(f.Steps) == 0 {
		return nil, fmt.Errorf("steps must be a non-empty list%s", where)
	}
	s.Steps = f.Steps
	if len(f.Assertions) == 0 {
		return nil, fmt.Errorf("assertions must be a non-empty list%s", where)
	}
	s.Assertions = make([]Assertion, len(f.Assertions))
	for i, a := range f.Assertions {
		if a.Metric == nil || !slices.Contains(metricNames, *a.Metric) {
			return nil, fmt.Errorf("metric must be one of %v%s", metricNames, where)
		}
		if a.Op == nil || !slices.Contains(assertionOps, *a.Op) {
			return nil, fmt.Errorf("op must be one of %v%s", assertionOps, where)
		}
		if a.Value == nil {
			return nil, fmt.Errorf("assertion value must be a number%s", where)
		}
		s.Assertions[i] = Assertion{Metric: *a.Metric, Op: *a.Op, Value: *a.Value}
	}
	return s, nil
}

// LoadScripts 读取目录下全部 *.yaml 剧本，按文件名排序保证报告顺序确定。
func LoadScripts(dir string) ([]*Script, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("scripts dir not found: %s", dir)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("no journey scripts (*.yaml) in %s", dir)
	}
	scripts := make([]*Script, 0, len(names))
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("cannot read %s: %w", name, err)
		}
		s, err := ParseScript(data, name)
		if err != nil {
			return nil, err
		}
		scripts = append(scripts, s)
	}
	return scripts, nil
}
