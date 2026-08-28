// calibrate —— 噪声带建议（spec §3.1）：对资产全部 metric 连跑 runs 次基线观测，
// 用 evalkit.NoiseBand 出 (均值,σ)，产出可回填 configs/gates/<T>.yaml noise_band 的
// 建议（回填须 founder PR——AGENTS.md 禁改阈值）。观测当前为确定性桩（seed=commit+
// metric），真实评测链路接入后替换 calibrateValues 即可。
package gaterunner

import (
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"

	"github.com/Cloudbird-Software/AI_Toy/tools/evalkit/evalkit"
	"gopkg.in/yaml.v3"
)

// calibrateValues 确定性桩：runs 个基线观测（值域 [0,1)）。
func calibrateValues(commit, metric string, runs int) []float64 {
	rng := rand.New(rand.NewSource(seed(commit, "cal:"+metric)))
	values := make([]float64, runs)
	for i := range values {
		values[i] = round4(rng.Float64())
	}
	return values
}

// suggestionYAML 渲染噪声带建议文件内容。
func suggestionYAML(asset, commit string, runs int, bands map[string]Band) ([]byte, error) {
	header := fmt.Sprintf("# gaterunner calibrate 噪声带建议（asset=%s，runs=%d，commit=%s）\n"+
		"# 回填 configs/gates/%s.yaml 的 noise_band 须 founder PR（AGENTS.md：禁改阈值）\n", asset, runs, commit, asset)
	body, err := yaml.Marshal(map[string]map[string]Band{"noise_band": bands})
	if err != nil {
		return nil, err
	}
	return append([]byte(header), body...), nil
}

// ExecuteCalibrate 连跑出噪声带建议；返回写入路径提示（outPath 空 = stdout）。
func ExecuteCalibrate(asset string, runs int, configDir, commit, outPath string, stdout io.Writer) (string, error) {
	if runs < 2 {
		return "", inputErrorf("--runs 须 ≥ 2（噪声带取样本σ），got %d", runs)
	}
	cfg, err := LoadAssetConfig(filepath.Join(configDir, asset+".yaml"))
	if err != nil {
		return "", err
	}
	metrics := map[string]bool{}
	for _, g := range cfg.Gates {
		metrics[g.Metric] = true
	}
	names := make([]string, 0, len(metrics))
	for m := range metrics {
		names = append(names, m)
	}
	sort.Strings(names)
	bands := make(map[string]Band, len(names))
	for _, m := range names {
		mean, sigma := evalkit.NoiseBand(calibrateValues(commit, m, runs))
		bands[m] = Band{Mean: round4(mean), Sigma: round4(sigma)}
	}
	data, err := suggestionYAML(asset, commit, runs, bands)
	if err != nil {
		return "", err
	}
	if outPath == "" {
		_, err = stdout.Write(data)
		return "stdout", err
	}
	if dir := filepath.Dir(outPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	return outPath, os.WriteFile(outPath, data, 0o644)
}
