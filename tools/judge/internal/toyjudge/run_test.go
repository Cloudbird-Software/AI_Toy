// 契约测试（spec §3.3/§4.2）：pairwise-swap AB/BA 不一致记 tie、报告含 rubric/model
// 哈希、model.yaml（§4.2 schema）缺字段/locked=false exit 2、9a 双 judge、
// 同族 judge exit 2、桩确定性。
package toyjudge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCLI 组装临时 fixture（rubric r1 + model.yaml + targets 目录）执行 run，
// 成功时返回（exit 0, 报告 jsonl 内容, stderr）。
func runCLI(t *testing.T, rubricYAML, modelYAML string, nTargets int, extra ...string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	rubrics := filepath.Join(dir, "rubrics")
	writeFixture(t, filepath.Join(rubrics, "r1.yaml"), rubricYAML)
	model := filepath.Join(dir, "model.yaml")
	writeFixture(t, model, modelYAML)
	targets := filepath.Join(dir, "targets")
	for i := range nTargets {
		writeFixture(t, filepath.Join(targets, fmt.Sprintf("t%d.txt", i)), fmt.Sprintf("被评产物 #%d 内容", i))
	}
	args := append([]string{"run", "--rubric", "r1", "--targets", targets,
		"--mode", ModePairwiseSwap, "--out", filepath.Join(dir, "report.jsonl"),
		"--rubrics-dir", rubrics, "--model", model}, extra...)
	var stdout, stderr bytes.Buffer
	code := Main(args, &stdout, &stderr)
	if code != ExitOK {
		return code, "", stderr.String()
	}
	data, err := os.ReadFile(filepath.Join(dir, "report.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return code, string(data), stderr.String() + stdout.String()
}

const highRiskRubricYAML = `id: r1
high_risk: true
criteria:
  - name: tone
    levels:
      - level: 1
        anchor: 基调错位
      - level: 2
        anchor: 强度失配
      - level: 3
        anchor: 均恰当
`

// sameFamilyModelYAML：高风险双席同族（§4.2 加载期即拒绝）。
const sameFamilyModelYAML = `judge_default: { provider: anthropic, model: claude-sonnet-4-5, temperature: 0.0, locked: true }
judges_high_risk: [claude-sonnet-4-5, claude-opus-4]
policy:
  pairwise_swap: true
  tie_on_disagree: true
  recalibrate: quarterly + on any rubric/judge change
  kappa_gate: { automation: 0.61, ci_autonomous: 0.80 }
gold_dir: configs/judge/gold/
`

// model.yaml judge_default 缺必填字段或 locked=false → exit 2（参数化）。
func TestRunModelConfigInvalidExitsTwo(t *testing.T) {
	for _, tc := range []struct{ field, broken string }{
		{"provider", strings.Replace(testModelYAML, "provider: anthropic, ", "", 1)},
		{"model", strings.Replace(testModelYAML, "model: claude-sonnet-4-5, ", "", 1)},
		{"temperature", strings.Replace(testModelYAML, ", temperature: 0.0", "", 1)},
		{"locked", strings.Replace(testModelYAML, ", locked: true", "", 1)},
		{"locked", strings.Replace(testModelYAML, "locked: true", "locked: false", 1)},
	} {
		t.Run("invalid-"+tc.field, func(t *testing.T) {
			code, _, errMsg := runCLI(t, testRubricYAML, tc.broken, 2)
			if code != ExitInput {
				t.Fatalf("exit=%d want %d", code, ExitInput)
			}
			if !strings.Contains(errMsg, tc.field) {
				t.Errorf("stderr 应指出问题字段 %s: %q", tc.field, errMsg)
			}
		})
	}
}

// 位置偏置：judge 永远给先手更高分 → AB 与 BA 结论相反 → 全 criterion 与整对均记 tie。
func TestPairwiseSwapPositionBiasYieldsTie(t *testing.T) {
	rubric, err := ParseRubric([]byte(testRubricYAML), "r1")
	if err != nil {
		t.Fatal(err)
	}
	judges := []JudgeInfo{JudgeConfig{Model: "stub-1", Temperature: 0}.Info(rubric)}
	targets := []Target{{ID: "x", Content: []byte("内容甲")}, {ID: "y", Content: []byte("内容乙")}}
	biased := func(call PairwiseCall) LevelPair { return LevelPair{First: 3, Second: 1} }
	records := RunPairwiseSwap(rubric, "model-sha", judges, targets, biased)
	if len(records) != 2 { // meta + 单 judge 记录（单席无 consensus）
		t.Fatalf("records=%d want 2", len(records))
	}
	rec, ok := records[1].(JudgementRecord)
	if !ok {
		t.Fatalf("records[1] 类型 %T", records[1])
	}
	for _, cv := range rec.Criteria {
		if cv.Winner != "tie" {
			t.Errorf("%s: winner=%q want tie", cv.Criterion, cv.Winner)
		}
		if cv.AB["x"] != 3 || cv.AB["y"] != 1 {
			t.Errorf("%s: AB=%v want x=3,y=1", cv.Criterion, cv.AB)
		}
		if cv.BA["x"] != 1 || cv.BA["y"] != 3 {
			t.Errorf("%s: BA=%v want x=1,y=3", cv.Criterion, cv.BA)
		}
	}
	if rec.Verdict != "tie" {
		t.Errorf("verdict=%q want tie", rec.Verdict)
	}
}

// 报告含 rubric/model 哈希：meta 行的 rubric_sha256 与 model_config_sha256 等于
// 源文件 sha256（测试端独立复算），judge 记录含三字段 + prompt/config 哈希。
func TestRunReportContainsRubricAndModelHashes(t *testing.T) {
	code, out, errMsg := runCLI(t, testRubricYAML, testModelYAML, 3)
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, errMsg)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 { // meta + 3 对 × 单席
		t.Fatalf("报告行数=%d want 4: %s", len(lines), out)
	}
	var meta MetaRecord
	if err := json.Unmarshal([]byte(lines[0]), &meta); err != nil {
		t.Fatal(err)
	}
	rSHA, mSHA := sha256.Sum256([]byte(testRubricYAML)), sha256.Sum256([]byte(testModelYAML))
	if meta.Type != "meta" || meta.Rubric != "r1" || meta.RubricSHA256 != hex.EncodeToString(rSHA[:]) {
		t.Errorf("meta rubric 哈希不符: %+v", meta)
	}
	if meta.Mode != ModePairwiseSwap || meta.ModelConfigSHA != hex.EncodeToString(mSHA[:]) {
		t.Errorf("meta model 哈希不符: %+v", meta)
	}
	if len(meta.Judges) != 1 || meta.Judges[0].Model != "claude-sonnet-4-5" ||
		meta.Judges[0].PromptSHA256 == "" || meta.Judges[0].ConfigSHA256 == "" {
		t.Errorf("meta judges=%+v", meta.Judges)
	}
	var rec JudgementRecord
	if err := json.Unmarshal([]byte(lines[1]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Type != "judge" || len(rec.Pair) != 2 || len(rec.Criteria) != 2 || rec.Verdict == "" {
		t.Errorf("judge 记录=%+v", rec)
	}
	if rec.Judge.Model != "claude-sonnet-4-5" || rec.Judge.Temperature != 0 ||
		len(rec.Judge.PromptSHA256) != 64 || len(rec.Judge.ConfigSHA256) != 64 {
		t.Errorf("judge 身份=%+v", rec.Judge)
	}
}

// 9a 类高风险 rubric：双 judge 触发（两条 judge 记录 + 一条 consensus 合议行）。
func TestRunHighRiskDoubleJudgeEmitsTwoJudgeRecords(t *testing.T) {
	code, out, errMsg := runCLI(t, highRiskRubricYAML, testModelYAML, 2)
	if code != ExitOK {
		t.Fatalf("exit=%d stderr=%q", code, errMsg)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 { // meta + 2 judge 记录 + 1 consensus
		t.Fatalf("报告行数=%d want 4: %s", len(lines), out)
	}
	var meta MetaRecord
	if err := json.Unmarshal([]byte(lines[0]), &meta); err != nil {
		t.Fatal(err)
	}
	if !meta.HighRisk || len(meta.Judges) != 2 || meta.Judges[0].Model == meta.Judges[1].Model {
		t.Fatalf("meta=%+v", meta)
	}
	judgeCount, consensusCount := 0, 0
	for _, line := range lines[1:] {
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			t.Fatal(err)
		}
		switch probe.Type {
		case "judge":
			judgeCount++
		case "consensus":
			consensusCount++
			var cons ConsensusRecord
			if err := json.Unmarshal([]byte(line), &cons); err != nil {
				t.Fatal(err)
			}
			if len(cons.Verdicts) != 2 || cons.Verdict == "" {
				t.Errorf("consensus=%+v", cons)
			}
		}
	}
	if judgeCount != 2 || consensusCount != 1 {
		t.Errorf("judge 记录=%d consensus=%d want 2/1", judgeCount, consensusCount)
	}
}

// 高风险双 judge 同族 → exit 2（§4.2 加载期即拒绝同族席位）。
func TestRunSameFamilyDoubleJudgeExitsTwo(t *testing.T) {
	code, _, errMsg := runCLI(t, highRiskRubricYAML, sameFamilyModelYAML, 2)
	if code != ExitInput {
		t.Fatalf("exit=%d want %d", code, ExitInput)
	}
	if !strings.Contains(errMsg, "同族") {
		t.Errorf("stderr 应说明同族拒绝: %q", errMsg)
	}
}

// 双 judge 各自 AB/BA 自洽、但互相结论相反 → consensus 记 tie（双 judge 不一致记 tie）。
func TestConsensusDoubleJudgeDisagreementYieldsTie(t *testing.T) {
	rubric, err := ParseRubric([]byte(testRubricYAML), "r1")
	if err != nil {
		t.Fatal(err)
	}
	judges := []JudgeInfo{
		JudgeConfig{Model: "j-one", Temperature: 0}.Info(rubric),
		JudgeConfig{Model: "j-two", Temperature: 0}.Info(rubric),
	}
	targets := []Target{{ID: "x", Content: []byte("甲")}, {ID: "y", Content: []byte("乙")}}
	fn := func(call PairwiseCall) LevelPair { // 位置无关：j-one 判 x 胜，j-two 判 y 胜
		score := func(t Target, model string) int {
			hi, lo := 3, 1
			if model == "j-two" {
				hi, lo = lo, hi
			}
			if t.ID == "x" {
				return hi
			}
			return lo
		}
		return LevelPair{First: score(call.First, call.Judge.Model), Second: score(call.Second, call.Judge.Model)}
	}
	records := RunPairwiseSwap(rubric, "model-sha", judges, targets, fn)
	cons, ok := records[len(records)-1].(ConsensusRecord)
	if !ok {
		t.Fatalf("末行类型 %T 应为 consensus", records[len(records)-1])
	}
	var verdicts []string
	for _, r := range records {
		if rec, ok := r.(JudgementRecord); ok {
			verdicts = append(verdicts, rec.Verdict)
		}
	}
	if len(verdicts) != 2 || verdicts[0] == verdicts[1] || verdicts[0] == "tie" {
		t.Fatalf("双 judge 结论应相反且各自自洽: %v", verdicts)
	}
	if cons.Verdict != "tie" {
		t.Errorf("consensus verdict=%q want tie（双 judge 不一致记 tie）", cons.Verdict)
	}
}

// run 输入校验：不支持的模式、targets 不足两个、缺必选参数。
func TestRunInputValidation(t *testing.T) {
	t.Run("不支持的模式", func(t *testing.T) {
		code, _, errMsg := runCLI(t, testRubricYAML, testModelYAML, 2, "--mode", "direct")
		if code != ExitInput || !strings.Contains(errMsg, "pairwise-swap") {
			t.Fatalf("exit=%d stderr=%q", code, errMsg)
		}
	})
	t.Run("targets 不足两个", func(t *testing.T) {
		if code, _, _ := runCLI(t, testRubricYAML, testModelYAML, 1); code != ExitInput {
			t.Fatalf("单 target 无法配对，应 exit %d", ExitInput)
		}
	})
	for _, argv := range [][]string{nil, {"run"}, {"calibrate"}, {"bogus"}} {
		t.Run("argv:"+fmt.Sprint(argv), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Main(argv, &stdout, &stderr); code != ExitInput {
				t.Fatalf("exit=%d want %d", code, ExitInput)
			}
		})
	}
}

// 属性：DeterministicJudge 同输入同分数；分数与伙伴/位置无关（默认桩无位置偏置）。
func TestDeterministicJudgeProperties(t *testing.T) {
	rubric, err := ParseRubric([]byte(testRubricYAML), "r1")
	if err != nil {
		t.Fatal(err)
	}
	j := JudgeConfig{Model: "claude-sonnet-4-5", Temperature: 0}.Info(rubric)
	x := Target{ID: "x", Content: []byte("同一段被评内容")}
	ab := PairwiseCall{RubricID: "r1", Criterion: "tone", Judge: j,
		First: x, Second: Target{ID: "y", Content: []byte("伙伴一")}}
	first := DeterministicJudge(ab)
	if first != DeterministicJudge(ab) { // 同输入同分数
		t.Fatalf("同输入不同分: %v vs %v", first, DeterministicJudge(ab))
	}
	for _, lv := range []int{first.First, first.Second} {
		if lv < 1 || lv > 3 {
			t.Errorf("级别 %d 越界（三级量表 1..3）", lv)
		}
	}
	wantX := first.First
	for i, partner := range []Target{
		{ID: "p1", Content: []byte("伙伴二")},
		{ID: "p2", Content: []byte("伙伴三 longer")},
	} {
		asFirst := DeterministicJudge(PairwiseCall{RubricID: ab.RubricID, Criterion: ab.Criterion,
			Judge: j, First: x, Second: partner})
		asSecond := DeterministicJudge(PairwiseCall{RubricID: ab.RubricID, Criterion: ab.Criterion,
			Judge: j, First: partner, Second: x})
		if asFirst.First != wantX || asSecond.Second != wantX { // x 的分数不随位置/伙伴变化
			t.Errorf("伙伴 %d: x=%d/%d want %d", i, asFirst.First, asSecond.Second, wantX)
		}
		if asFirst.Second != asSecond.First { // 伙伴的分数也不随位置变化
			t.Errorf("伙伴 %d: 伙伴分数随位置变化", i)
		}
	}
	// AB/BA 对同一目标给出相同级别（无位置偏置，故默认桩不产生假 tie）
	ba := DeterministicJudge(PairwiseCall{RubricID: ab.RubricID, Criterion: ab.Criterion,
		Judge: j, First: ab.Second, Second: x})
	if ba.First != first.Second || ba.Second != first.First {
		t.Errorf("AB/BA 打分应镜像: first=%v ba=%v", first, ba)
	}
}
