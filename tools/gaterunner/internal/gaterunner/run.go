// run —— 门禁执行面（spec §3.1，IR #64 真实调度）：登记表来自 collect 源码扫描
// （ScanMarks），门禁 id 命中注册测试 → 实跑 `go test -count=1 -run ^<Test>$ <pkg>`
// （cwd=Root），verdict 按退出码（0=pass、非 0=fail），Evidence=实际命令串；未命中 →
// verdict=not_implemented（实现未开始：不计 pass 不计 fail，summary 单列 not_impl_ids，
// exit 0）。统计判定 judge（evalkit upper95）保留给 benchmark/holdout 数据面接入后
// 复用（观测来源换成真实评测产物即可；伪造观测的旧桩已随 IR #64 删除）。
package gaterunner

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
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
	Verdict         string  `json:"verdict"` // pass | fail | warn(G2) | exempt(G1 豁免) | not_implemented(未注册)
	StatisticalRule string  `json:"statistical_rule"`
	Evidence        string  `json:"evidence"`
}

// Summary 分级汇总：g0/g1 pass|fail、g2 pass|warn、not_implemented（该级门禁全部
// 未注册）、n/a（该级无门禁）；fail_ids 含全部红/warn（不含豁免）；
// not_impl_ids 单列未实现门禁（不计 pass 不计 fail）。
type Summary struct {
	G0      string   `json:"g0"`
	G1      string   `json:"g1"`
	G2      string   `json:"g2"`
	FailIDs []string `json:"fail_ids"`
	NotImpl []string `json:"not_impl_ids"`
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

// observation 观测值：k=计数类（zero_event/pass_rate），value=直接观测（eer/asr/metric）。
// benchmark/holdout 数据面接入后由真实评测产物填充，经 judge 统计判定。
type observation struct {
	k     int
	value float64
}

// dispatchGate 实跑注册测试：`go test -count=1 -run ^<Test>$ <pkg>`（cwd=root，
// pkg 由注册源文件目录推导）。返回证据命令串、是否通过（退出码 0）与合并输出
// （红灯时供诊断打印）。
func dispatchGate(root string, m RegEntry) (evidence string, passed bool, output string) {
	pkg := "./" + filepath.ToSlash(filepath.Dir(m.Source))
	if pkg == "./." {
		pkg = "."
	}
	evidence = fmt.Sprintf("go test -count=1 -run ^%s$ %s", m.Test, pkg)
	cmd := exec.Command("go", "test", "-count=1", "-run", "^"+m.Test+"$", pkg)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	return evidence, err == nil, string(out)
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

// ExecuteRun 跑单资产门禁：校验配置 → 过滤 level/suite → 逐门禁真实调度（登记表
// 命中即实跑注册测试，未命中 not_implemented）→ 报告+退出码。配置错误返回
// InputError（CLI exit 2）。not_implemented 不计 pass 不计 fail（exit 0，显式单列）。
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
		res := Result{ID: g.ID, BI: g.BI, Level: g.Level, Metric: g.Metric, Threshold: round4(g.Threshold)}
		if m, ok := byID[g.ID]; ok && m.Test != "" && m.Source != "" {
			evidence, passed, output := dispatchGate(opts.Root, m)
			res.StatisticalRule, res.Evidence, res.Verdict = "go_test_exit_code", evidence, "fail"
			if passed {
				res.Verdict = "pass"
			} else {
				fmt.Fprintf(os.Stderr, "门禁 %s 实跑红（%s）：\n%s", g.ID, evidence, output)
			}
		} else {
			res.Verdict, res.StatisticalRule, res.Evidence = "not_implemented", "not_implemented", ""
		}
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

// summarize 分级汇总。级别状态优先级：任一红 → fail（G2 为 warn）；该级全部
// not_implemented → not_implemented；该级无门禁 → n/a；否则 pass。not_implemented
// 不算 pass 也不算 fail，not_impl_ids 单列（fail/warn/豁免均不进入该列表）。
func summarize(results []Result) Summary {
	s := Summary{G0: "n/a", G1: "n/a", G2: "n/a", FailIDs: []string{}, NotImpl: []string{}}
	total, impl, red := map[string]int{}, map[string]int{}, map[string]bool{}
	for _, r := range results {
		total[r.Level]++
		if r.Verdict == "not_implemented" {
			s.NotImpl = append(s.NotImpl, r.ID)
			continue
		}
		impl[r.Level]++
		if r.Verdict != "fail" && r.Verdict != "warn" {
			continue
		}
		red[r.Level] = true
		s.FailIDs = append(s.FailIDs, r.ID)
	}
	for _, lvl := range [3]string{"G0", "G1", "G2"} {
		st := "pass"
		switch {
		case total[lvl] == 0:
			st = "n/a"
		case red[lvl]:
			st = "fail"
			if lvl == "G2" {
				st = "warn"
			}
		case impl[lvl] == 0:
			st = "not_implemented"
		}
		switch lvl {
		case "G0":
			s.G0 = st
		case "G1":
			s.G1 = st
		case "G2":
			s.G2 = st
		}
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
