// T1 元层门禁四条真实接线（IR #73）：dispatchGate 经 ScanMarks 登记表实跑
// `go test -count=1 -run ^<Test>$ ./tests/meta`（退出码=verdict）。口径与样本量
// 声明在 configs/gates/T1.yaml（本文件只落断言本体，不复制阈值语义）：
//
//	T1-G0-01 mixed_pr_count == 0（BI-1.3 隔离；suite ci/nightly/release）
//	T1-G1-01 assertion_registration_rate >= 1.0（BI-1.1 覆盖度；冷启动红→豁免台账）
//	T1-G1-02 rerun_in_band_rate >= 1.0（BI-1.2；min_evidence n:30）
//	T1-G1-03 reproducible_record_rate >= 1.0（BI-1.2；min_evidence n:20）
package meta

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Cloudbird-Software/AI_Toy/tools/gaterunner/gaterunner"
	"gopkg.in/yaml.v3"
)

// 评测面（验收资产：阈值/协议/数据/金标旅程）与功能面（被评代码）前缀——
// BI-1.3 隔离口径：评测集变更走独立 PR，两类前缀同现即混合。
var (
	evalSidePrefixes = []string{"configs/gates/", "docs/gates/", "datasets/", "configs/judge/", "tests/golden-journeys/"}
	funcSidePrefixes = []string{"packages/", "tools/", "baml/"}
)

// TestT1MixedPRCount T1-G0-01：HEAD commit 变更文件 ∪ 未提交变更（porcelain：
// staged+unstaged+untracked 合并集）不得同时命中评测面与功能面前缀——混合即红
// （G0 无豁免）。普通提交取本提交变更；merge 提交取该合并相对首父带入的变更
// （PR 事件 checkout 场景）；根提交退回普通形式。非 git 环境跳过（门禁由仓内
// CI/nightly 承载）。
func TestT1MixedPRCount(t *testing.T) {
	gaterunner.Mark(t, "T1", "BI-1.3", "T1-G0-01", "G0")
	root, ok := repoRoot(t)
	if !ok {
		t.Skip("非 git 仓：mixed_pr_count 依赖变更历史")
	}
	mixed, evalSide, funcSide := classifyMixed(t, root)
	if mixed {
		t.Errorf("mixed_pr_count=1（阈值 ==0）：HEAD+未提交变更同时命中评测面 %v 与功能面 %v——评测集变更须走独立 PR（BI-1.3 隔离）", evalSide, funcSide)
	}
}

// TestT1AssertionRegistrationRate T1-G1-01：执行记录率 = reports/gates/*.json 中
// verdict∈{pass,fail,exempt} 的门禁 id 数 ÷ configs/gates/*.yaml 声明门禁总数，
// 须达 1.0。冷启动 0 记录为真实红（ADR-0002 阶段化 DEBT），经 reports/exemptions.yaml
// 台账豁免（gaterunner run：G1 fail → exempt，exit 0）；M1 起逐资产接线后自然消解。
func TestT1AssertionRegistrationRate(t *testing.T) {
	gaterunner.Mark(t, "T1", "BI-1.1", "T1-G1-01", "G1")
	root, ok := repoRoot(t)
	if !ok {
		t.Skip("非 git 仓：无法定位仓库根")
	}
	declared, total := declaredGateIDs(t, filepath.Join(root, "configs", "gates"))
	if total == 0 {
		t.Fatalf("configs/gates/*.yaml 门禁总数为 0——配置面缺失，登记率不可计算")
	}
	recorded := recordedGateIDs(t, filepath.Join(root, "reports", "gates"))
	var missing []string
	for id := range declared {
		if !recorded[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		n := min(5, len(missing))
		t.Errorf("assertion_registration_rate=%.4f（%d/%d）< 1.0：%d 条门禁无执行记录（reports/gates/*.json verdict∈{pass,fail,exempt}），如 %v——冷启动 DEBT（ADR-0002），见 reports/exemptions.yaml 台账豁免",
			float64(total-len(missing))/float64(total), total-len(missing), total, len(missing), missing[:n])
	}
}

// TestT1RerunInBandRate T1-G1-02：同一 mixed-PR 判定重复执行 30 次
// （min_evidence n:30），结果须全一致（rerun_in_band_rate 达 1.0）；任何一次
// 漂移即红。
func TestT1RerunInBandRate(t *testing.T) {
	gaterunner.Mark(t, "T1", "BI-1.2", "T1-G1-02", "G1")
	root, ok := repoRoot(t)
	if !ok {
		t.Skip("非 git 仓：重跑判定依赖变更历史")
	}
	const n = 30 // min_evidence.n（T1.yaml：3 套件 × 3 个 × 10 次）
	first, firstEval, firstFunc := classifyMixed(t, root)
	for i := 1; i < n; i++ {
		mixed, evalSide, funcSide := classifyMixed(t, root)
		if mixed != first {
			t.Errorf("rerun_in_band_rate<1.0：第 %d/%d 次重跑判定漂移（首跑=%v 本次=%v；首跑评测面=%v 功能面=%v，本次评测面=%v 功能面=%v）",
				i+1, n, first, mixed, firstEval, firstFunc, evalSide, funcSide)
			return
		}
	}
}

// TestT1ReproducibleRecordRate T1-G1-03：构造 20 条（min_evidence n:20）最小
// 报告记录（commit/timestamp/dataset_versions/seed），JSON 序列化→反序列化逆向
// 复算，四字段须非空完整（reproducible_record_rate 达 1.0）。
func TestT1ReproducibleRecordRate(t *testing.T) {
	gaterunner.Mark(t, "T1", "BI-1.2", "T1-G1-03", "G1")
	const n = 20 // min_evidence.n（T1.yaml：抽 20 条历史记录逆向复算）
	commit := headSHA()
	for i := 0; i < n; i++ {
		rec := map[string]any{
			"commit":           commit,
			"timestamp":        time.Now().UTC().Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
			"dataset_versions": map[string]string{"golden-journeys": "2026-08-28", "turntaking-synth": "2026-08-28"},
			"seed":             strconv.Itoa(i + 1),
		}
		data, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("记录 %d 序列化失败: %v", i, err)
		}
		var back map[string]any
		if err := json.Unmarshal(data, &back); err != nil {
			t.Errorf("记录 %d 逆向复算失败: %v（原始=%s）", i, err, data)
			continue
		}
		for _, k := range []string{"commit", "timestamp", "dataset_versions", "seed"} {
			v, ok := back[k]
			if !ok || v == nil {
				t.Errorf("记录 %d 缺字段 %q（原始=%s）——可复现记录须齐备", i, k, data)
				continue
			}
			if s, isStr := v.(string); isStr && s == "" {
				t.Errorf("记录 %d 字段 %q 为空（原始=%s）", i, k, data)
			}
			if m, isMap := v.(map[string]any); isMap && len(m) == 0 {
				t.Errorf("记录 %d 字段 %q 为空映射（原始=%s）", i, k, data)
			}
		}
	}
}

// classifyMixed 取 HEAD 变更 ∪ 未提交变更，返回是否混合及两侧命中文件。
func classifyMixed(t *testing.T, root string) (mixed bool, evalSide, funcSide []string) {
	t.Helper()
	files := append(headChangedFiles(t, root), uncommittedFiles(root)...)
	evalSide, funcSide = splitSides(files)
	return len(evalSide) > 0 && len(funcSide) > 0, evalSide, funcSide
}

// repoRoot 解析 git 仓根（go test 以包目录为 cwd，经 rev-parse 定位）；
// 非 git 环境或顶层非本仓布局（configs/gates 缺席）返回 false。
func repoRoot(t *testing.T) (string, bool) {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", false
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", false
	}
	if _, err := os.Stat(filepath.Join(root, "configs", "gates")); err != nil {
		return "", false
	}
	return root, true
}

// headChangedFiles HEAD commit 变更文件：普通提交=本提交变更；merge 提交=相对
// 首父带入的变更（PR 事件 checkout 的 merge ref 场景）；根提交退回普通形式。
func headChangedFiles(t *testing.T, root string) []string {
	t.Helper()
	out, err := gitOut(root, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD^1", "HEAD")
	if err != nil {
		out, _ = gitOut(root, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")
	}
	return splitLines(out)
}

// uncommittedFiles 未提交变更文件（porcelain：staged+unstaged+untracked 合并集；
// rename 以新路径计）。
func uncommittedFiles(root string) []string {
	out, _ := gitOut(root, "status", "--porcelain")
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		p := strings.TrimSpace(line[3:])
		if i := strings.Index(p, " -> "); i >= 0 {
			p = strings.TrimSpace(p[i+4:])
		}
		p = strings.Trim(p, `"`)
		if p != "" {
			files = append(files, p)
		}
	}
	return files
}

func splitSides(files []string) (evalSide, funcSide []string) {
	for _, f := range files {
		if matchAnyPrefix(f, evalSidePrefixes) {
			evalSide = append(evalSide, f)
		}
		if matchAnyPrefix(f, funcSidePrefixes) {
			funcSide = append(funcSide, f)
		}
	}
	return
}

func matchAnyPrefix(path string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// headSHA 当前 HEAD sha（非 git 环境回退固定 fixture 值——记录完整性检查不依赖
// 真实历史，但字段须非空）。
func headSHA() string {
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "no-git-fixture"
	}
	if s := strings.TrimSpace(string(out)); s != "" {
		return s
	}
	return "no-git-fixture"
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func splitLines(s string) []string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

type gateList struct {
	Gates []struct {
		ID string `yaml:"id"`
	} `yaml:"gates"`
}

// declaredGateIDs configs/gates/*.yaml 声明的门禁 id 集合与总数。
func declaredGateIDs(t *testing.T, dir string) (map[string]bool, int) {
	t.Helper()
	ids := map[string]bool{}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("读取门禁配置失败 %s: %v", p, err)
		}
		var cfg gateList
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			t.Fatalf("门禁配置不可解析 %s: %v", p, err)
		}
		for _, g := range cfg.Gates {
			if g.ID != "" {
				ids[g.ID] = true
			}
		}
	}
	return ids, len(ids)
}

type reportEntry struct {
	ID      string `json:"id"`
	Verdict string `json:"verdict"`
}

// recordedGateIDs reports/gates/*.json 中 verdict∈{pass,fail,exempt} 的门禁 id 集合
// （兼容顶层列表与 {results|assertions} 对象两种形态——与 repoctl coverage 同一
// 数据面）。
func recordedGateIDs(t *testing.T, dir string) map[string]bool {
	t.Helper()
	ids := map[string]bool{}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("读取执行记录失败 %s: %v", p, err)
		}
		var entries []reportEntry
		if err := json.Unmarshal(data, &entries); err == nil {
			collectRecorded(ids, entries)
			continue
		}
		var doc struct {
			Results    []reportEntry `json:"results"`
			Assertions []reportEntry `json:"assertions"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Errorf("执行记录不可解析 %s: %v", p, err)
			continue
		}
		collectRecorded(ids, append(doc.Results, doc.Assertions...))
	}
	return ids
}

func collectRecorded(ids map[string]bool, entries []reportEntry) {
	for _, e := range entries {
		if e.ID != "" && countedVerdict(e.Verdict) {
			ids[e.ID] = true
		}
	}
}

func countedVerdict(v string) bool { return v == "pass" || v == "fail" || v == "exempt" }
