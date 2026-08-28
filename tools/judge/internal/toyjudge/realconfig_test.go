// 集成测试（spec §4.2/IR #37）：calibrate/run 子命令读仓库真实
// configs/judge/model.yaml 与 configs/judge/rubrics/ 下真实 rubric；
// configs/judge/gold/ 尚未落金标（仅 .gitkeep），金标 fixture 写临时目录
// 经 --gold 注入（不改动 configs/）。
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

// repoRoot 定位仓库根（本包位于 <root>/tools/judge/internal/toyjudge）。
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("未定位到仓库根: %v", err)
	}
	return root
}

// 真实 model.yaml（§4.2）解析：字段与规格一致，哈希等于文件内容 sha256。
func TestLoadRealModelConfig(t *testing.T) {
	path := filepath.Join(repoRoot(t), "configs", "judge", "model.yaml")
	m, err := LoadModelConfig(path)
	if err != nil {
		t.Fatalf("真实 model.yaml 应加载成功: %v", err)
	}
	if m.JudgeDefault.Provider != "anthropic" || m.JudgeDefault.Model != "claude-sonnet-4-5" ||
		m.JudgeDefault.Temperature != 0 {
		t.Errorf("judge_default=%+v", m.JudgeDefault)
	}
	if m.JudgesHighRisk != [2]string{"claude-sonnet-4-5", "gpt-4o"} {
		t.Errorf("judges_high_risk=%v", m.JudgesHighRisk)
	}
	if m.Policy.KappaGate.Automation != 0.61 || m.Policy.KappaGate.CIAutonomous != 0.80 ||
		!m.Policy.PairwiseSwap || !m.Policy.TieOnDisagree {
		t.Errorf("policy=%+v", m.Policy)
	}
	if m.GoldDir != "configs/judge/gold/" {
		t.Errorf("gold_dir=%q", m.GoldDir)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if m.SHA256 != hex.EncodeToString(sum[:]) {
		t.Error("配置哈希应等于文件内容 sha256")
	}
}

// calibrate 读真实 model.yaml + 真实 rubric 7a（金标临时注入）：
// 完全一致金标 → exit 0；已知分歧金标 → exit 20（门禁 0.61 来自真实配置）。
func TestCalibrateWithRealModelConfig(t *testing.T) {
	root := repoRoot(t)
	rubricsDir := filepath.Join(root, "configs", "judge", "rubrics")
	modelPath := filepath.Join(root, "configs", "judge", "model.yaml")
	var agree, disagree strings.Builder
	for i := 0; i < 9; i++ {
		h := i%3 + 1
		fmt.Fprintf(&agree, "{\"criterion\": \"appropriateness\", \"human\": %d, \"judge\": %d}\n", h, h)
		if i < 6 {
			fmt.Fprintf(&disagree, "{\"criterion\": \"appropriateness\", \"human\": %d, \"judge\": %d}\n", h, h%3+1)
		}
	}
	run := func(gold string) (int, CalibrateReport) {
		goldPath := filepath.Join(t.TempDir(), "7a.jsonl")
		if err := os.WriteFile(goldPath, []byte(gold), 0o644); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		code := Main([]string{"calibrate", "--rubric", "7a", "--gold", goldPath,
			"--rubrics-dir", rubricsDir, "--model", modelPath}, &stdout, &stderr)
		var rep CalibrateReport
		if code == ExitOK || code == ExitKappaGate {
			if err := json.Unmarshal([]byte(stdout.String()), &rep); err != nil {
				t.Fatalf("stdout 非完整 JSON: %v\n%s", err, stdout.String())
			}
		}
		return code, rep
	}
	t.Run("完全一致金标", func(t *testing.T) {
		code, rep := run(agree.String())
		if code != ExitOK {
			t.Fatalf("exit=%d（κ=1 应过门禁）", code)
		}
		if !rep.Pass || rep.KappaGate != 0.61 || rep.Rubric != "7a" ||
			rep.Judge.Model != "claude-sonnet-4-5" || rep.Judge.PromptSHA256 == "" {
			t.Errorf("report=%+v", rep)
		}
		if len(rep.Criteria) != 1 || rep.Criteria[0].Criterion != "appropriateness" || rep.Criteria[0].N != 9 {
			t.Errorf("criteria=%+v", rep.Criteria)
		}
	})
	t.Run("已知分歧金标", func(t *testing.T) {
		code, rep := run(disagree.String())
		if code != ExitKappaGate {
			t.Fatalf("exit=%d want %d（κ<0.61，门禁来自真实配置）", code, ExitKappaGate)
		}
		if rep.Pass || rep.KappaGate != 0.61 {
			t.Errorf("report=%+v", rep)
		}
	})
}

// run 读真实 model.yaml + 真实 rubric：常规 rubric 7a → judge_default 单席；
// 高风险 rubric 9a → judges_high_risk 双席（claude + gpt）+ consensus。
func TestRunWithRealModelConfig(t *testing.T) {
	root := repoRoot(t)
	rubricsDir := filepath.Join(root, "configs", "judge", "rubrics")
	modelPath := filepath.Join(root, "configs", "judge", "model.yaml")
	data, err := os.ReadFile(modelPath)
	if err != nil {
		t.Fatal(err)
	}
	modelSum := sha256.Sum256(data)
	modelSHA := hex.EncodeToString(modelSum[:])
	targets := t.TempDir()
	for i, name := range []string{"candidate-a.txt", "candidate-b.txt"} {
		if err := os.WriteFile(filepath.Join(targets, name), []byte(fmt.Sprintf("被评产物 %d", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(rubricID string) (string, MetaRecord, string) {
		out := filepath.Join(t.TempDir(), "report.jsonl")
		var stdout, stderr bytes.Buffer
		code := Main([]string{"run", "--rubric", rubricID, "--targets", targets,
			"--mode", ModePairwiseSwap, "--out", out,
			"--rubrics-dir", rubricsDir, "--model", modelPath}, &stdout, &stderr)
		if code != ExitOK {
			t.Fatalf("run %s 应成功，exit=%d stderr=%q", rubricID, code, stderr.String())
		}
		rep, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(rep)), "\n")
		var meta MetaRecord
		if err := json.Unmarshal([]byte(lines[0]), &meta); err != nil {
			t.Fatal(err)
		}
		return string(rep), meta, stderr.String()
	}
	t.Run("常规 rubric 7a 单席", func(t *testing.T) {
		report, meta, _ := run("7a")
		if len(meta.Judges) != 1 || meta.Judges[0].Model != "claude-sonnet-4-5" ||
			meta.HighRisk || meta.ModelConfigSHA != modelSHA || meta.Rubric != "7a" ||
			meta.Mode != ModePairwiseSwap {
			t.Errorf("meta=%+v", meta)
		}
		if strings.Contains(report, "consensus") {
			t.Error("单席不应有 consensus 行")
		}
	})
	t.Run("高风险 rubric 9a 双席", func(t *testing.T) {
		report, meta, _ := run("9a")
		if !meta.HighRisk || len(meta.Judges) != 2 ||
			meta.Judges[0].Model != "claude-sonnet-4-5" || meta.Judges[1].Model != "gpt-4o" ||
			meta.ModelConfigSHA != modelSHA {
			t.Errorf("meta=%+v", meta)
		}
		if meta.Judges[0].PromptSHA256 != meta.Judges[1].PromptSHA256 {
			t.Error("双席 prompt 哈希应同源（同一 rubric 派生）")
		}
		if meta.Judges[0].ConfigSHA256 == meta.Judges[1].ConfigSHA256 {
			t.Error("双席 config 哈希应不同（模型不同）")
		}
		judge, consensus := 0, 0
		for _, line := range strings.Split(strings.TrimSpace(report), "\n") {
			var probe struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal([]byte(line), &probe); err != nil {
				t.Fatal(err)
			}
			switch probe.Type {
			case "judge":
				judge++
			case "consensus":
				consensus++
			}
		}
		if judge != 2 || consensus != 1 { // 1 对 × 2 judge + 1 合议
			t.Errorf("judge=%d consensus=%d want 2/1", judge, consensus)
		}
	})
}
