// calibrate 测试（spec §3.1）：噪声带 (均值,σ) 由 evalkit.NoiseBand 实算（测试用
// 同种子重放复算比对）、建议 YAML 可解析回填、--runs/资产非法 → exit 2。
package gaterunner

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cloudbird-Software/AI_Toy/tools/evalkit/evalkit"
	"gopkg.in/yaml.v3"
)

func TestCalibrateNoiseBand(t *testing.T) {
	root := newRunFixture(t)
	out := filepath.Join(root, "reports", "cal-T4.yaml")
	var stdout, stderr bytes.Buffer
	code := Main([]string{"calibrate", "--asset", "T4", "--runs", "10",
		"--config-dir", filepath.Join(root, "configs", "gates"),
		"--commit", "abc1234", "--out", out}, &stdout, &stderr)
	if code != ExitOK || stderr.Len() > 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), out) {
		t.Fatalf("stdout 未提及输出路径: %q", stdout.String())
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		NoiseBand map[string]Band `yaml:"noise_band"`
	}
	if err := yaml.Unmarshal(data, &s); err != nil {
		t.Fatalf("建议 YAML 不可解析: %v\n%s", err, data)
	}
	if len(s.NoiseBand) != 3 {
		t.Fatalf("噪声带 metric 数=%d, want 3: %v", len(s.NoiseBand), s.NoiseBand)
	}
	for metric, band := range s.NoiseBand {
		wantMean, wantSigma := evalkit.NoiseBand(calibrateValues("abc1234", metric, 10))
		// 建议文件经 round4 落盘，期望值同样取整后比对。
		if math.Abs(band.Mean-round4(wantMean)) > 1e-9 || math.Abs(band.Sigma-round4(wantSigma)) > 1e-9 {
			t.Errorf("%s: (mean,σ)=(%v,%v), want (%v,%v)", metric, band.Mean, band.Sigma, round4(wantMean), round4(wantSigma))
		}
	}
}

func TestCalibrateInputErrors(t *testing.T) {
	root := newRunFixture(t)
	cfg := filepath.Join(root, "configs", "gates")
	cases := []struct {
		name string
		args []string
	}{
		{"runs<2", []string{"calibrate", "--asset", "T4", "--runs", "1", "--config-dir", cfg}},
		{"资产配置缺失", []string{"calibrate", "--asset", "T9", "--runs", "10", "--config-dir", cfg}},
		{"asset 缺省", []string{"calibrate", "--runs", "10", "--config-dir", cfg}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Main(c.args, &stdout, &stderr); code != ExitConfig || stderr.Len() == 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr.String())
			}
		})
	}
}
