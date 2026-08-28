// config —— configs/gates/<asset>.yaml 的 schema 与加载（spec §4.1）。
// yaml.v3 严格模式（KnownFields）＝schema 校验：未知字段即配置错误。
package gaterunner

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// 退出码（spec §3.1，CI 与 justfile 的依赖面，不得偏离）。
const (
	ExitOK     = 0
	ExitConfig = 2
	ExitG0     = 10
	ExitG1     = 20
	ExitG2     = 30
)

// InputError 表示配置/输入不可读或不符合 schema（CLI exit 2）。
type InputError struct{ msg string }

func (e *InputError) Error() string { return e.msg }

func inputErrorf(format string, args ...any) error {
	return &InputError{msg: fmt.Sprintf(format, args...)}
}

// MinEvidence 统计证据声明：hours=泊松负样本小时 / n=样本量 / min_trials=EER 配对数。
type MinEvidence struct {
	Hours     *int `yaml:"hours"`
	N         *int `yaml:"n"`
	MinTrials *int `yaml:"min_trials"`
}

func (m *MinEvidence) hours() int  { return deref(m.Hours) }
func (m *MinEvidence) n() int      { return deref(m.N) }
func (m *MinEvidence) trials() int { return deref(m.MinTrials) }

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// Gate 单条门禁断言（id/bi/level/metric/op/threshold/src/rule + 口径声明）。
type Gate struct {
	ID               string       `yaml:"id"`
	BI               string       `yaml:"bi"`
	Level            string       `yaml:"level"`
	Metric           string       `yaml:"metric"`
	Op               string       `yaml:"op"`
	Threshold        float64      `yaml:"threshold"`
	Src              string       `yaml:"src"`
	Rule             string       `yaml:"rule"`
	MinEvidence      *MinEvidence `yaml:"min_evidence"`
	SamplesPerAttack int          `yaml:"samples_per_attack"`
	Report           []string     `yaml:"report"`
	Suite            []string     `yaml:"suite"`
}

// Band 噪声带 (均值,σ)，由 calibrate 建议、founder PR 回填。
type Band struct {
	Mean  float64 `yaml:"mean"`
	Sigma float64 `yaml:"sigma"`
}

// AssetConfig 单资产阈值文件（spec §4.1 固定 schema）。
type AssetConfig struct {
	Asset     string          `yaml:"asset"`
	Name      string          `yaml:"name"`
	Updated   string          `yaml:"updated"`
	NoiseBand map[string]Band `yaml:"noise_band"`
	Gates     []Gate          `yaml:"gates"`
}

// newStrictDecoder 严格 yaml 解码（未知字段即错误，＝schema 校验）。
func newStrictDecoder(data []byte) *yaml.Decoder {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	return dec
}

// LoadAssetConfig 严格解析单个资产阈值文件。
func LoadAssetConfig(path string) (AssetConfig, error) {
	var c AssetConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return c, inputErrorf("门禁配置不存在或不可读: %s", path)
	}
	if err := newStrictDecoder(data).Decode(&c); err != nil {
		return c, inputErrorf("门禁配置不可解析: %s: %v", path, err)
	}
	if c.Asset == "" {
		return c, inputErrorf("%s: 缺 asset 字段", path)
	}
	return c, nil
}

// ListConfigs 返回目录下排序后的全部 *.yaml 路径（目录缺失视为空——configs/gates
// 由 W3 卡落盘，缺失不构成错误）。
func ListConfigs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var paths []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".yaml" {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(paths)
	return paths
}

func finiteNonNeg(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }
