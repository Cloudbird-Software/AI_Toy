// run —— 门禁执行面（spec §3.1）。当前为桩执行：登记表来自 collect 源码扫描，
// observed 由确定性模拟生成（rand 以 commit sha+id 为种，红/绿约 1:9——真实 go test
// 调度接入后替换 simulate 即可）；统计判定真实走 evalkit（upper95 计算）。
package gaterunner

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Cloudbird-Software/AI_Toy/tools/evalkit/evalkit"
)

// Result 单条门禁判定结果（报告 schema 字段照抄规格 §3.1 JSON 块）。
type Result struct {
	ID              string  `json:"id"`
	BI              string  `json:"bi"`
	Level           string  `json:"level"`
	Metric          string  `json:"metric"`
	Observed        float64 `json:"observed"`
	EvidenceHours   int     `json:"evidence_hours"`
	Upper95         float64 `json:"upper95"`
	Threshold       float64 `json:"threshold"`
	Verdict         string  `json:"verdict"` // pass | fail | warn(G2) | exempt(G1 豁免)
	StatisticalRule string  `json:"statistical_rule"`
	Evidence        string  `json:"evidence"`
}

// Summary 分级汇总：g0/g1 pass|fail、g2 pass|warn；fail_ids 含全部红/warn（不含豁免）。
type Summary struct {
	G0      string   `json:"g0"`
	G1      string   `json:"g1"`
	G2      string   `json:"g2"`
	FailIDs []string `json:"fail_ids"`
}

// Report 单资产门禁报告（committed，可逆向复算）。
type Report struct {
	Asset             string            `json:"asset"`
	Suite             string            `json:"suite"`
	Commit            string            `json:"commit"`
	DatasetVersions   map[string]string `json:"dataset_versions"`
	JudgeModel        *string           `json:"judge_model"`
	Timestamp         string            `json:"timestamp"`
	Results           []Result          `json:"results"`
	Summary           Summary           `json:"summary"`
	ExemptionsApplied []string          `json:"exemptions_applied"`
}

// RunOpts run 子命令执行参数（路径相对 cwd，Root 为扫描/git 根）。
type RunOpts struct {
	Asset, Level, Suite string
	Commit, Root        string
	ConfigDir, DocsDir  string
	ExemptionsPath      string
	ReportPath          string
}

// observation 模拟观测：k=计数类（zero_event/pass_rate），value=直接观测（eer/asr/metric）。
type observation struct {
	k     int
	value float64
}

// simulate 确定性桩执行面：以 (commit,id) 为种决定红绿（~10% 红）并生成观测。
func simulate(g Gate, rng *rand.Rand) observation {
	red := rng.Float64() < 0.1
	if g.Rule == "zero_event" {
		if red {
			return observation{k: 1}
		}
		return observation{k: 0}
	}
	if g.Rule == "pass_rate" {
		n := 1
		if g.MinEvidence != nil {
			n = max(g.MinEvidence.n(), 1)
		}
		t := g.Threshold * 0.8
		if g.Op == ">=" || g.Op == ">" {
			t = min(g.Threshold+0.03, 1)
			if red {
				t = g.Threshold - 0.03
			}
		} else if red {
			t = min(g.Threshold*1.25, 1)
		}
		return observation{k: int(math.Round(t * float64(n)))}
	}
	// eer / asr / metric：直接观测值。
	v := g.Threshold * 0.9
	if g.Op == ">=" || g.Op == ">" {
		v = g.Threshold + 0.02
		if red {
			v = max(g.Threshold*0.9, 0)
		}
	} else if red {
		v = g.Threshold*1.15 + 0.001
	}
	return observation{value: round4(v)}
}

// judge 统计判定（真实走 evalkit）：零事件看 95% 上界（泊松 Garwood / 二项 CP），
// 通过率报 Wilson 上界做审计、点估计定判，其余报点估计。判据 op 一律作用于所注统计量。
func judge(g Gate, o observation) Result {
	r := Result{ID: g.ID, BI: g.BI, Level: g.Level, Metric: g.Metric, Threshold: round4(g.Threshold)}
	switch g.Rule {
	case "zero_event":
		h, n := 0, 0
		if g.MinEvidence != nil {
			h, n = g.MinEvidence.hours(), g.MinEvidence.n()
		}
		if h > 0 {
			r.StatisticalRule, r.EvidenceHours = "poisson_zero_upper95", h
			r.Observed = round4(float64(o.k) / float64(h))
			r.Upper95 = round4(evalkit.PoissonUpper95(o.k, h))
		} else {
			r.StatisticalRule = "binom_zero_upper95"
			r.Observed = round4(float64(o.k) / float64(n))
			r.Upper95 = round4(evalkit.BinomUpper95(o.k, n))
		}
		r.Verdict = verdictOf(g.Op, r.Upper95, g.Threshold)
	case "pass_rate":
		n := 1
		if g.MinEvidence != nil {
			n = max(g.MinEvidence.n(), 1)
		}
		r.StatisticalRule = "wilson_upper95"
		r.Observed = round4(float64(o.k) / float64(n))
		_, hi := evalkit.Wilson(o.k, n)
		r.Upper95 = round4(hi)
		r.Verdict = verdictOf(g.Op, r.Observed, g.Threshold)
	case "eer":
		r.StatisticalRule = "eer_point_est"
		r.Observed, r.Upper95, r.Verdict = round4(o.value), round4(o.value), verdictOf(g.Op, o.value, g.Threshold)
	case "asr":
		r.StatisticalRule = "asr_mean_best"
		r.Observed, r.Upper95, r.Verdict = round4(o.value), round4(o.value), verdictOf(g.Op, o.value, g.Threshold)
	default: // metric
		r.StatisticalRule = "metric_point"
		r.Observed, r.Upper95, r.Verdict = round4(o.value), round4(o.value), verdictOf(g.Op, o.value, g.Threshold)
	}
	return r
}

func verdictOf(op string, stat, threshold float64) string {
	switch op {
	case "<=":
		if stat <= threshold {
			return "pass"
		}
	case ">=":
		if stat >= threshold {
			return "pass"
		}
	case "<":
		if stat < threshold {
			return "pass"
		}
	case ">":
		if stat > threshold {
			return "pass"
		}
	case "==":
		if stat == threshold {
			return "pass"
		}
	}
	return "fail"
}

func round4(x float64) float64 { return math.Round(x*1e4) / 1e4 }

// seedSource 把 commit+id 经 fnv64a 哈希成 int64 随机源（同 journeys 约定）。
func seed(commit, id string) int64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s:%s", commit, id)
	return int64(h.Sum64())
}

// ExecuteRun 跑单资产门禁：校验配置 → 过滤 level/suite → 逐门禁模拟+判定 →
// 报告+退出码。配置错误返回 InputError（CLI exit 2）。
func ExecuteRun(opts RunOpts) (*Report, int, error) {
	path := filepath.Join(opts.ConfigDir, opts.Asset+".yaml")
	cfg, err := LoadAssetConfig(path)
	if err != nil {
		return nil, ExitConfig, err
	}
	if viol, _ := assetViolations(path, cfg, opts.DocsDir, nil); len(viol) > 0 {
		return nil, ExitConfig, &InputError{msg: strings.Join(viol, "\n")}
	}
	gates := filterGates(cfg.Gates, opts.Level, opts.Suite)
	if len(gates) == 0 {
		return nil, ExitConfig, inputErrorf("资产 %s 无门禁匹配 level=%s suite=%s", opts.Asset, opts.Level, opts.Suite)
	}
	marks, _ := ScanMarks(opts.Root)
	byID := map[string]RegEntry{}
	for _, m := range marks {
		byID[m.ID] = m
	}
	exemptions, err := LoadExemptions(opts.ExemptionsPath)
	if err != nil {
		return nil, ExitConfig, err
	}
	commit := resolveCommit(opts.Root, opts.Commit)
	rep := &Report{Asset: opts.Asset, Suite: opts.Suite, Commit: commit,
		DatasetVersions: map[string]string{}, Timestamp: time.Now().UTC().Format(time.RFC3339),
		Results: make([]Result, 0, len(gates)), ExemptionsApplied: []string{}}
	today := time.Now().UTC().Format("2006-01-02")
	for _, g := range gates {
		o := simulate(g, rand.New(rand.NewSource(seed(commit, g.ID))))
		res := judge(g, o)
		res.Evidence = evidenceCmd(cfg.Asset, byID[g.ID])
		if res.Verdict == "fail" && g.Level == "G1" { // G1 可豁免（≤30 天，过期自动红）；G0 无豁免
			if ex, ok := exemptionFor(exemptions, g.ID, today); ok {
				res.Verdict = "exempt"
				rep.ExemptionsApplied = append(rep.ExemptionsApplied, g.ID+"@"+ex.Expires)
			}
		}
		if g.Level == "G2" && res.Verdict == "fail" {
			res.Verdict = "warn" // G2=趋势警告（进看板，不阻断）
		}
		rep.Results = append(rep.Results, res)
	}
	rep.Summary = summarize(rep.Results)
	return rep, exitOf(rep.Summary), nil
}

func filterGates(gates []Gate, level, suite string) []Gate {
	var out []Gate
	for _, g := range gates {
		if level != "all" && !strings.EqualFold(level, g.Level) {
			continue
		}
		if !suiteMatches(g.Suite, suite) {
			continue
		}
		out = append(out, g)
	}
	return out
}

// suiteMatches：门禁 suite 列表须含所选 suite；holdout（密封评测）额外放行
// 标注 release 的门禁（configs 只用 ci/nightly/release，holdout 不单独标注）。
func suiteMatches(gateSuites []string, suite string) bool {
	for _, s := range gateSuites {
		if s == suite || (suite == "holdout" && s == "release") {
			return true
		}
	}
	return false
}

func summarize(results []Result) Summary {
	s := Summary{G0: "pass", G1: "pass", G2: "pass", FailIDs: []string{}}
	for _, r := range results {
		red := r.Verdict == "fail" || r.Verdict == "warn"
		if !red {
			continue
		}
		switch r.Level {
		case "G0":
			s.G0 = "fail"
		case "G1":
			s.G1 = "fail"
		case "G2":
			s.G2 = "warn"
		}
		s.FailIDs = append(s.FailIDs, r.ID)
	}
	return s
}

func exitOf(s Summary) int {
	switch {
	case s.G0 == "fail":
		return ExitG0
	case s.G1 == "fail":
		return ExitG1
	case s.G2 == "warn":
		return ExitG2
	}
	return ExitOK
}

// evidenceCmd 最小复现命令：登记表命中 → go test 到注册测试；否则 just gate 兜底。
func evidenceCmd(asset string, m RegEntry) string {
	if m.Test != "" && m.Source != "" {
		dir := filepath.ToSlash(filepath.Dir(m.Source))
		if dir == "." {
			dir = ""
		}
		return fmt.Sprintf("go test ./%s -run ^%s$ -count=1", dir, m.Test)
	}
	return "just gate " + asset
}

// resolveCommit：--commit 优先；缺省取 root 的 git HEAD；均无 → "unknown"。
func resolveCommit(root, flagCommit string) string {
	if flagCommit != "" {
		return flagCommit
	}
	if out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	return "unknown"
}

// EmitReport 序列化报告：ReportPath 非空写入该文件（目录自动创建），否则打印 stdout。
func EmitReport(rep *Report, reportPath string, stdout io.Writer) error {
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if reportPath != "" {
		if dir := filepath.Dir(reportPath); dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
		return os.WriteFile(reportPath, data, 0o644)
	}
	_, err = stdout.Write(data)
	return err
}

// Exemption —— reports/exemptions.yaml 条目（schema：id/reason/owner/expires/linked_pr）。
type Exemption struct {
	ID       string `yaml:"id"`
	Reason   string `yaml:"reason"`
	Owner    string `yaml:"owner"`
	Expires  string `yaml:"expires"`
	LinkedPR string `yaml:"linked_pr"`
}

// LoadExemptions 读 G1 豁免台账；文件缺失视为无豁免（repoctl exemption audit 负责台账纪律）。
func LoadExemptions(path string) ([]Exemption, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	var list []Exemption
	dec := newStrictDecoder(data)
	if err := dec.Decode(&list); err != nil {
		return nil, inputErrorf("豁免台账不可解析: %s: %v", path, err)
	}
	return list, nil
}

// exemptionFor 返回该断言未过期（expires ≥ today）的豁免。
func exemptionFor(list []Exemption, id, today string) (Exemption, bool) {
	for _, ex := range list {
		if ex.ID == id && ex.Expires >= today {
			return ex, true
		}
	}
	return Exemption{}, false
}
