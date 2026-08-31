// cli —— toyjudge 子命令入口（spec §3.3）：calibrate（κ 门禁）与 run（pairwise-swap）。
package toyjudge

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sync/atomic"

	"github.com/Cloudbird-Software/AI_Toy/tools/llmclient/llmclient"
)

// CLI 退出码契约（CI 与其它工具的依赖面，不得偏离）：
// 0 通过；2 配置/输入错误（rubric 非法、model.yaml 缺字段、同族 judge 等）；20 κ 门禁未达标。
const (
	ExitOK        = 0
	ExitInput     = 2
	ExitKappaGate = 20
)

// Main 是 toyjudge CLI 入口（cmd/toyjudge 为薄壳），返回进程退出码。
func Main(args []string, stdout, stderr io.Writer) int {
	const usage = "用法: toyjudge <calibrate|run> [flags]"
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return ExitInput
	}
	switch args[0] {
	case "calibrate":
		return runCalibrateCmd(args[1:], stdout, stderr)
	case "run":
		return runRunCmd(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "toyjudge: 未知子命令 %q\n%s\n", args[0], usage)
	return ExitInput
}

func runCalibrateCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("calibrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rubricID := fs.String("rubric", "", "rubric id（如 7a）")
	gold := fs.String("gold", "", "人工金标 jsonl（每行 criterion/human/judge）")
	rubricsDir := fs.String("rubrics-dir", "configs/judge/rubrics", "rubric 目录")
	modelPath := fs.String("model", "configs/judge/model.yaml", "judge 模型配置")
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "calibrate: "+format+"\n", a...)
		return ExitInput
	}
	if fs.Parse(args) != nil {
		return ExitInput
	}
	if *rubricID == "" {
		return fail("需要 --rubric")
	}
	if *gold == "" {
		return fail("需要 --gold")
	}
	rubric, err := LoadRubric(*rubricsDir, *rubricID)
	if err != nil {
		return fail("%v", err)
	}
	model, err := LoadModelConfig(*modelPath)
	if err != nil {
		return fail("%v", err)
	}
	rows, err := LoadGold(*gold, rubric)
	if err != nil {
		return fail("%v", err)
	}
	rep := Calibrate(rubric, model.JudgeDefault.Info(rubric), rows, model.Policy.KappaGate.Automation)
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fail("%v", err)
	}
	fmt.Fprintln(stdout, string(data))
	if !rep.Pass {
		fmt.Fprintf(stderr, "calibrate: min κ=%.4f < %.2f，门禁未达\n", rep.MinKappa, rep.KappaGate)
		return ExitKappaGate
	}
	return ExitOK
}

func runRunCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rubricID := fs.String("rubric", "", "rubric id（如 7a）")
	targetsDir := fs.String("targets", "", "被评产物目录（≥2 个文件，两两配对）")
	mode := fs.String("mode", ModePairwiseSwap, "运行模式（当前仅 pairwise-swap）")
	out := fs.String("out", "", "报告 jsonl 输出路径（缺省打到 stdout）")
	rubricsDir := fs.String("rubrics-dir", "configs/judge/rubrics", "rubric 目录")
	modelPath := fs.String("model", "configs/judge/model.yaml", "judge 模型配置")
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "run: "+format+"\n", a...)
		return ExitInput
	}
	if fs.Parse(args) != nil {
		return ExitInput
	}
	if *rubricID == "" {
		return fail("需要 --rubric")
	}
	if *targetsDir == "" {
		return fail("需要 --targets")
	}
	if *mode != ModePairwiseSwap {
		return fail("--mode 仅支持 %s（本卡），got %q", ModePairwiseSwap, *mode)
	}
	rubric, err := LoadRubric(*rubricsDir, *rubricID)
	if err != nil {
		return fail("%v", err)
	}
	model, err := LoadModelConfig(*modelPath)
	if err != nil {
		return fail("%v", err)
	}
	judges, err := model.SelectJudges(rubric)
	if err != nil {
		return fail("%v", err)
	}
	targets, err := LoadTargets(*targetsDir)
	if err != nil {
		return fail("%v", err)
	}
	// 评审后端选择：LLM_JUDGE=1 且 LLM API 已配置 → LLM 后端（pairwise+swap 协议
	// 不变，judge 身份沿用 model.yaml 锁定值）；否则 DeterministicJudge 桩。
	judgeFn := Judge(DeterministicJudge)
	var llmErrs *atomic.Int64
	if os.Getenv("LLM_JUDGE") == "1" {
		cfg, err := llmclient.FromEnv()
		if err != nil {
			return fail("%v（LLM_JUDGE=1 需要已配置 API，模板见 configs/llm/api.env.example）", err)
		}
		judgeFn, llmErrs = NewLLMJudge(llmclient.New(cfg), rubric, stderr)
	}
	records := RunPairwiseSwap(rubric, model.SHA256, judges, targets, judgeFn)
	if err := EmitJSONL(records, *out, stdout); err != nil {
		return fail("%v", err)
	}
	if llmErrs != nil && llmErrs.Load() > 0 {
		return fail("LLM 评审有 %d 次调用失败（报告不完整，已作废——修复 API 后重跑）", llmErrs.Load())
	}
	pairs := len(targets) * (len(targets) - 1) / 2
	dest := "stdout"
	if *out != "" {
		dest = *out
	}
	summary := fmt.Sprintf("run %s（%s）：%d 对 × %d judge → %s\n", rubric.ID, *mode, pairs, len(judges), dest)
	if *out != "" {
		fmt.Fprint(stdout, summary)
	} else {
		fmt.Fprint(stderr, summary)
	}
	return ExitOK
}
