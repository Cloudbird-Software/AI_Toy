// run —— pairwise-swap 评审：AB/BA 各评一次，不一致记 tie；
// 高风险 rubric 双 judge，双 judge 不一致也记 tie（spec §3.3/§11.12）。
package toyjudge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ModePairwiseSwap 是本卡唯一支持的运行模式。
const ModePairwiseSwap = "pairwise-swap"

// MetaRecord 是报告首行：rubric/model 哈希与 judge 席位（锁定可追溯）。
type MetaRecord struct {
	Type           string      `json:"type"`
	Rubric         string      `json:"rubric"`
	RubricSHA256   string      `json:"rubric_sha256"`
	Mode           string      `json:"mode"`
	HighRisk       bool        `json:"high_risk"`
	ModelConfigSHA string      `json:"model_config_sha256"`
	Judges         []JudgeInfo `json:"judges"`
	Timestamp      string      `json:"timestamp"`
}

// CriterionVerdict 是单 criterion 的 AB/BA 两次打分（按 target id）与合成胜负；
// winner ∈ {a, b, tie}，a/b 指 pair[0]/pair[1]。
type CriterionVerdict struct {
	Criterion string         `json:"criterion"`
	AB        map[string]int `json:"ab"`
	BA        map[string]int `json:"ba"`
	Winner    string         `json:"winner"`
}

// JudgementRecord 是一条 judge 记录（每 judge × pair 一行）。
type JudgementRecord struct {
	Type     string             `json:"type"`
	Judge    JudgeInfo          `json:"judge"`
	Pair     []string           `json:"pair"`
	Criteria []CriterionVerdict `json:"criteria"`
	Verdict  string             `json:"verdict"`
}

// JudgeVerdict 是 consensus 中一位 judge 的结论。
type JudgeVerdict struct {
	Model   string `json:"model"`
	Verdict string `json:"verdict"`
}

// ConsensusRecord 是高风险双 judge 的合议行：双 judge 结论不一致时记 tie。
type ConsensusRecord struct {
	Type     string         `json:"type"`
	Pair     []string       `json:"pair"`
	Verdicts []JudgeVerdict `json:"verdicts"`
	Verdict  string         `json:"verdict"`
}

// LoadTargets 读取 targets 目录（跳过子目录与点开头文件，按文件名排序），
// id 为去扩展名文件名；pairwise 需要至少 2 个 target。
func LoadTargets(dir string) ([]Target, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("targets 目录不可读: %w", err)
	}
	seen := map[string]bool{}
	var targets []Target
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("target 不可读: %w", err)
		}
		id := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		if seen[id] {
			return nil, fmt.Errorf("target id 重复: %s", id)
		}
		seen[id] = true
		targets = append(targets, Target{ID: id, Content: data})
	}
	if len(targets) < 2 {
		return nil, fmt.Errorf("pairwise-swap 需要至少 2 个 target，实际 %d", len(targets))
	}
	return targets, nil
}

// abWinner 是 AB 调用（先 pair[0]）的胜负；baWinner 把 BA 调用（先 pair[1]）
// 的胜负映射回原对。均返回 a|b|tie。
func abWinner(first, second int) string {
	switch {
	case first > second:
		return "a"
	case second > first:
		return "b"
	}
	return "tie"
}

func baWinner(first, second int) string {
	switch {
	case first > second: // pair[1] 在先手位胜
		return "b"
	case second > first:
		return "a"
	}
	return "tie"
}

// overall：全部 criterion 结论一致才记该结论，否则 tie。
func overall(winners []string) string {
	w := winners[0]
	for _, x := range winners[1:] {
		if x != w {
			return "tie"
		}
	}
	return w
}

// judgePair 对一个有序对 (a, b) 按 AB/BA 各评一次：单 criterion 内
// 两次结论不一致记 tie，再跨 criterion 合成整对结论。
func judgePair(rubric *Rubric, jc JudgeInfo, a, b Target, judgeFn Judge) JudgementRecord {
	rec := JudgementRecord{Type: "judge", Judge: jc, Pair: []string{a.ID, b.ID}}
	var winners []string
	for _, c := range rubric.Criteria {
		ab := judgeFn(PairwiseCall{RubricID: rubric.ID, Criterion: c.Name, Judge: jc, First: a, Second: b})
		ba := judgeFn(PairwiseCall{RubricID: rubric.ID, Criterion: c.Name, Judge: jc, First: b, Second: a})
		w := abWinner(ab.First, ab.Second)
		if baWinner(ba.First, ba.Second) != w { // AB/BA 不一致记 tie
			w = "tie"
		}
		rec.Criteria = append(rec.Criteria, CriterionVerdict{Criterion: c.Name,
			AB:     map[string]int{a.ID: ab.First, b.ID: ab.Second},
			BA:     map[string]int{a.ID: ba.Second, b.ID: ba.First},
			Winner: w})
		winners = append(winners, w)
	}
	rec.Verdict = overall(winners)
	return rec
}

// RunPairwiseSwap 生成 pairwise-swap 报告记录：meta + 每 (pair, judge) 一条 judge
// 记录 + 高风险（双 judge）每 pair 一条 consensus。judgeFn 为 nil 时用默认桩。
func RunPairwiseSwap(rubric *Rubric, modelSHA string, judges []JudgeInfo, targets []Target, judgeFn Judge) []any {
	if judgeFn == nil {
		judgeFn = DeterministicJudge
	}
	meta := MetaRecord{Type: "meta", Rubric: rubric.ID, RubricSHA256: rubric.SHA256,
		Mode: ModePairwiseSwap, HighRisk: rubric.HighRisk, ModelConfigSHA: modelSHA,
		Judges: judges, Timestamp: time.Now().UTC().Format(time.RFC3339)}
	records := []any{meta}
	for i := range targets {
		for j := i + 1; j < len(targets); j++ {
			a, b := targets[i], targets[j]
			verdicts := make([]string, len(judges))
			for k, jc := range judges {
				rec := judgePair(rubric, jc, a, b, judgeFn)
				records = append(records, rec)
				verdicts[k] = rec.Verdict
			}
			if len(judges) > 1 { // 双 judge 不一致记 tie
				vs := make([]JudgeVerdict, len(judges))
				for k := range judges {
					vs[k] = JudgeVerdict{Model: judges[k].Model, Verdict: verdicts[k]}
				}
				records = append(records, ConsensusRecord{Type: "consensus",
					Pair: []string{a.ID, b.ID}, Verdicts: vs, Verdict: overall(verdicts)})
			}
		}
	}
	return records
}

// EmitJSONL 逐行序列化记录：outPath 非空写文件（自动建父目录），否则打到 stdout。
func EmitJSONL(records []any, outPath string, stdout io.Writer) error {
	var buf bytes.Buffer
	for _, r := range records {
		data, err := json.Marshal(r)
		if err != nil {
			return err
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}
	if outPath == "" {
		_, err := stdout.Write(buf.Bytes())
		return err
	}
	if dir := filepath.Dir(outPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(outPath, buf.Bytes(), 0o644)
}
