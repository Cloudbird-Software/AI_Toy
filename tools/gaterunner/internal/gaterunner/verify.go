// verify —— verify-configs 的 schema 校验 + 六条统计纪律（spec §3.1，违反即 exit 2，
// 每条独立错误信息）。统计下限一律经 evalkit 计算（不得手算）：
//  1. 每资产至少 1 条 G0，且全部 bi 映射 docs/gates/assets/<T>.md 的 BI 编号；
//  2. zero_event 须声明 min_evidence 且满足泊松/二项下限；
//  3. pass_rate 须声明样本量且 n ≥ ZeroFailN(q)（= ln(0.05)/ln(q)）；
//  4. eer 须声明 min_trials ≥ 5000（家庭级）；
//  5. asr 须声明 samples_per_attack: 5 且 report: [mean, best]；
//  6. 阈值来源 src 三选一必填：benchmark | product | noise_band。
package gaterunner

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/Cloudbird-Software/AI_Toy/tools/evalkit/evalkit"
)

var (
	idRe   = regexp.MustCompile(`^(G[012])-\d{2,}$`)
	biRe   = regexp.MustCompile(`^BI-(\d+)\.\d+$`)
	suites = map[string]bool{"ci": true, "nightly": true, "release": true, "holdout": true}
	ops    = map[string]bool{"<=": true, ">=": true, "<": true, ">": true, "==": true}
	rules  = map[string]bool{"zero_event": true, "pass_rate": true, "eer": true, "asr": true, "metric": true}
	srcs   = map[string]bool{"benchmark": true, "product": true, "noise_band": true}
)

// VerifyConfigs 校验 configDir 下全部资产文件；返回逐条错误（已含文件/id 前缀）与
// 门禁总数。docsDir 为 docs/gates/assets（BI 映射校验；资产卡未落盘时按 BI 编号
// 约定校验，见 mapBI）。
func VerifyConfigs(configDir, docsDir string) (violations []string, gateTotal int) {
	seen := map[string]string{}
	for _, path := range ListConfigs(configDir) {
		c, err := LoadAssetConfig(path)
		if err != nil {
			violations = append(violations, err.Error())
			continue
		}
		vs, n := assetViolations(path, c, docsDir, seen)
		violations, gateTotal = append(violations, vs...), gateTotal+n
	}
	return violations, gateTotal
}

// assetViolations 校验单资产文件（schema + 纪律 1–6）。seen 非 nil 时做全仓 id 查重。
func assetViolations(path string, c AssetConfig, docsDir string, seen map[string]string) ([]string, int) {
	var viol []string
	add := func(id, format string, args ...any) {
		viol = append(viol, fmt.Sprintf("%s: %s: %s", path, id, fmt.Sprintf(format, args...)))
	}
	if stem := strings.TrimSuffix(baseName(path), ".yaml"); stem != c.Asset {
		viol = append(viol, fmt.Sprintf("%s: asset %q 与文件名 %s.yaml 不符", path, c.Asset, stem))
	}
	hasG0 := false
	for _, g := range c.Gates {
		if g.Level == "G0" {
			hasG0 = true
		}
		if seen != nil && g.ID != "" {
			if prev, dup := seen[g.ID]; dup {
				add(g.ID, "id 全仓唯一，与 %s 重复", prev)
			} else {
				seen[g.ID] = path
			}
		}
		viol = append(viol, gateViolations(g, c.Asset, docsDir, add)...)
	}
	if !hasG0 {
		viol = append(viol, fmt.Sprintf("%s: 资产 %s 缺少 G0 门禁（纪律 1：每资产至少 1 条 G0）", path, c.Asset))
	}
	return viol, len(c.Gates)
}

// gateViolations 单条门禁的 schema + 纪律 2–6 校验；经 add 附文件/id 前缀。
func gateViolations(g Gate, asset, docsDir string, add func(id, format string, args ...any)) []string {
	var viol []string
	push := func(format string, args ...any) { add(g.ID, format, args...) }
	if m := idRe.FindStringSubmatch(strings.TrimPrefix(g.ID, asset+"-")); m == nil {
		push("id %q 须为 <资产>-<级别>-<序号>（如 %s-G0-01）", g.ID, asset)
	} else if m[1] != g.Level {
		push("id 级别段 %s 与 level %q 不符", m[1], g.Level)
	}
	if !validLevel(g.Level) {
		push("level %q 须为 G0|G1|G2", g.Level)
	}
	if err := mapBI(g, asset, docsDir); err != nil {
		push("bi 映射: %v", err)
	}
	if g.Metric == "" {
		push("metric 缺失")
	}
	if !ops[g.Op] {
		push("op %q 须为 <=、>=、<、>、== 之一", g.Op)
	}
	if !finiteNonNeg(g.Threshold) {
		push("threshold 须为非负有限数，got %v", g.Threshold)
	}
	if !rules[g.Rule] {
		push("rule %q 须为 zero_event|pass_rate|eer|asr|metric 之一", g.Rule)
	}
	if len(g.Suite) == 0 {
		push("suite 缺失（ci|nightly|release|holdout）")
	}
	for _, s := range g.Suite {
		if !suites[s] {
			push("suite 值 %q 无效（须 ci|nightly|release|holdout）", s)
		}
	}
	if !srcs[g.Src] {
		push("阈值来源 src 必填且三选一 benchmark|product|noise_band，got %q（纪律 6）", g.Src)
	}
	switch g.Rule {
	case "zero_event":
		if g.MinEvidence == nil || (g.MinEvidence.hours() == 0 && g.MinEvidence.n() == 0) {
			push("zero_event 断言须声明 min_evidence（hours 或 n，纪律 2）")
			break
		}
		if h := g.MinEvidence.hours(); h > 0 {
			if h < 1 {
				push("zero_event min_evidence.hours 须 ≥ 1")
			} else if up := evalkit.PoissonUpper95(0, h); up > g.Threshold {
				push("zero_event 泊松下限不满足：%dh 零事件 95%%上限 %.4g > threshold %g（纪律 2）", h, up, g.Threshold)
			}
		} else if n := g.MinEvidence.n(); n > 0 {
			if g.Threshold <= 0 || g.Threshold >= 1 {
				push("zero_event 二项下限要求 0<threshold<1，got %g（纪律 2）", g.Threshold)
			} else if need := evalkit.ZeroFailN(1 - g.Threshold); n < need {
				push("zero_event 二项下限不满足：n=%d < ln(0.05)/ln(1-%.4g)=%d（纪律 2）", n, g.Threshold, need)
			}
		}
	case "pass_rate":
		n := 0
		if g.MinEvidence != nil {
			n = g.MinEvidence.n()
		}
		if n == 0 {
			push("pass_rate 断言须声明样本量 min_evidence.n（纪律 3）")
			break
		}
		q := 1 - g.Threshold
		if g.Op == ">=" || g.Op == ">" {
			q = g.Threshold
		}
		if q <= 0 || q >= 1 {
			push("pass_rate 阈值 %g 须使宣称成功率落在 (0,1)（纪律 3）", q)
		} else if need := evalkit.ZeroFailN(q); n < need {
			push("pass_rate 样本量不足：n=%d < ln(0.05)/ln(%.4g)=%d（纪律 3）", n, q, need)
		}
	case "eer":
		trials := 0
		if g.MinEvidence != nil {
			trials = g.MinEvidence.trials()
		}
		if trials == 0 {
			push("eer 断言须声明 min_evidence.min_trials（纪律 4）")
		} else if trials < 5000 {
			push("eer 断言 min_trials=%d 须 ≥ 5000（家庭级，纪律 4）", trials)
		}
	case "asr":
		if g.SamplesPerAttack != 5 {
			push("asr 断言须声明 samples_per_attack: 5，got %d（纪律 5）", g.SamplesPerAttack)
		}
		hasMean, hasBest := false, false
		for _, r := range g.Report {
			hasMean, hasBest = hasMean || r == "mean", hasBest || r == "best"
		}
		if !hasMean || !hasBest {
			push("asr 断言须声明 report: [mean, best]（纪律 5）")
		}
	}
	return viol
}

// mapBI 校验 bi 映射 docs/gates/assets/<T>.md（纪律 1）：资产卡存在 → 须出现该
// BI 编号；未落盘 → 按 BI-<资产号>.m 编号约定校验。
func mapBI(g Gate, asset, docsDir string) error {
	doc := docsDir + "/" + asset + ".md"
	if _, err := os.Stat(doc); err == nil {
		data, err := os.ReadFile(doc)
		if err != nil {
			return fmt.Errorf("读 %s: %w", doc, err)
		}
		if !strings.Contains(string(data), g.BI) {
			return fmt.Errorf("bi %q 未出现在 %s", g.BI, doc)
		}
		return nil
	}
	m := biRe.FindStringSubmatch(g.BI)
	if m == nil {
		return fmt.Errorf("bi %q 须为 BI-<n>.<m> 编号", g.BI)
	}
	if want := strings.TrimPrefix(asset, "T"); m[1] != want {
		return fmt.Errorf("bi %q 未映射 docs/gates/assets/%s.md（须 BI-%s.m）", g.BI, asset, want)
	}
	return nil
}

func baseName(path string) string { return path[strings.LastIndexByte(path, '/')+1:] }
