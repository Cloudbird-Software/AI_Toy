package synthgen

// cli —— synthgen 子命令入口（spec §3.7）。

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// CLI 退出码（CI 与 justfile 的依赖面，不得偏离）。
const (
	ExitOK        = 0  // 通过
	ExitViolation = 20 // 单源占比 >30%（多样性门槛）
	ExitInput     = 2  // 输入错：重复注册 / 未注册 / 批次缺失等
)

// 默认落盘位置（repo 布局：datasets/synth/**，相对 CWD）。
const (
	RegistryPath = "datasets/synth/registry.jsonl"
	BatchesDir   = "datasets/synth/batches"
)

// Run 执行 synthgen CLI（argv 不含程序名），返回进程退出码。
func Run(argv []string, stdout, stderr io.Writer) int {
	if len(argv) == 0 {
		fmt.Fprintln(stderr, "usage: synthgen <register|generate|generate-neg|dist-check> [flags]")
		return ExitInput
	}
	switch argv[0] {
	case "register":
		return runRegister(argv[1:], stdout, stderr)
	case "generate":
		return runGenerate(argv[1:], stdout, stderr)
	case "generate-neg":
		return runGenerateNeg(argv[1:], stdout, stderr)
	case "dist-check":
		return runDistCheck(argv[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "error: 未知子命令 %q（可用：register、generate、generate-neg、dist-check）\n", argv[0])
		return ExitInput
	}
}

func runRegister(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("register", flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("id", "", "生成器 id")
	version := fs.String("version", "", "生成器版本")
	seedPolicy := fs.String("seed-policy", "", "种子策略（如 fixed）")
	outputsManifest := fs.String("outputs-manifest", "", "输出清单路径")
	if fs.Parse(args) != nil {
		return ExitInput
	}
	if *id == "" || *version == "" || *seedPolicy == "" || *outputsManifest == "" {
		fmt.Fprintln(stderr, "error: register 需要 --id、--version、--seed-policy、--outputs-manifest")
		return ExitInput
	}
	g, err := RegisterGenerator(RegistryPath, *id, *version, *seedPolicy, *outputsManifest)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitInput
	}
	fmt.Fprintf(stdout, "registered %s@%s -> %s\n", g.ID, g.Version, RegistryPath)
	return ExitOK
}

func runGenerate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("id", "", "生成器 id（须已注册，缺省取最近注册版本）")
	n := fs.Int("n", 0, "生成条数")
	seed := fs.Int64("seed", 0, "随机种子（同 seed 完全复现）")
	if fs.Parse(args) != nil {
		return ExitInput
	}
	if *id == "" || *n < 1 {
		fmt.Fprintln(stderr, "error: generate 需要 --id 与 --n ≥ 1")
		return ExitInput
	}
	records, err := LoadRegistry(RegistryPath)
	var b Batch
	var dir string
	if err == nil {
		var g Generator
		if g, err = FindGenerator(records, *id); err == nil {
			b, dir, err = GenerateBatch(g, *n, *seed)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitInput
	}
	fmt.Fprintf(stdout, "batch %s: n=%d train=%d holdout=%d -> %s\n", b.ID, b.N, b.TrainN, b.HoldoutN, dir)
	return ExitOK
}

// runGenerateNeg 生成负样本批（m2-spec §2，IR #90）：gen-tneg 家庭音景 / gen-kwsadv
// 对抗同音节，参数化时长（6h=--duration-ms 21600000 即 360min）；批不切
// synth-holdout（TrainN=0/HoldoutN=0、全量入 eval 池）；--seed-label 走 FNV-1a 64
// 全仓种子约定（门禁 canonical 批与测试侧同标签同种子）。
func runGenerateNeg(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("generate-neg", flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("id", "", "负样本生成器 id（须已注册：gen-tneg / gen-kwsadv）")
	durationMs := fs.Int("duration-ms", 0, "流时长 ms（≥30；6h=21600000 即 360min）")
	seed := fs.Int64("seed", 0, "随机种子（同 seed 完全复现；与 --seed-label 二选一）")
	seedLabel := fs.String("seed-label", "", "种子标签（FNV-1a 64 对齐全仓约定；与 --seed 二选一）")
	if fs.Parse(args) != nil {
		return ExitInput
	}
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	if *id == "" || *durationMs < NegFrameMs {
		fmt.Fprintf(stderr, "error: generate-neg 需要 --id 与 --duration-ms ≥ %d\n", NegFrameMs)
		return ExitInput
	}
	if explicit["seed"] && explicit["seed-label"] {
		fmt.Fprintln(stderr, "error: --seed 与 --seed-label 二选一")
		return ExitInput
	}
	if explicit["seed-label"] {
		*seed = NegSeed(*seedLabel)
	}
	records, err := LoadRegistry(RegistryPath)
	var b NegBatch
	var dir string
	if err == nil {
		var g Generator
		if g, err = FindGenerator(records, *id); err == nil {
			b, dir, err = GenerateBatchNeg(g, BatchesDir, *durationMs, *seed)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitInput
	}
	fmt.Fprintf(stdout, "neg-batch %s: n=%d train=%d holdout=%d eval=%d purpose=%s duration=%dms -> %s\n",
		b.ID, b.N, b.TrainN, b.HoldoutN, b.EvalN, b.Purpose, b.DurationMs, dir)
	return ExitOK
}

func runDistCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dist-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	batch := fs.String("batch", "", "批次 id（datasets/synth/batches 下目录名）")
	reference := fs.String("reference", "", "真实参考集 jsonl（可选）")
	if fs.Parse(args) != nil {
		return ExitInput
	}
	if *batch == "" {
		fmt.Fprintln(stderr, "error: dist-check 需要 --batch")
		return ExitInput
	}
	samples, err := readJSONL(filepath.Join(BatchesDir, *batch, "samples.jsonl"))
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return ExitInput
	}
	if len(samples) == 0 {
		fmt.Fprintf(stderr, "error: 空批次: %s\n", *batch)
		return ExitInput
	}
	var ref []map[string]any
	if *reference != "" {
		if ref, err = readJSONL(*reference); err != nil { // 空 jsonl → 非 nil 空集，仍参与 JS 距离
			fmt.Fprintf(stderr, "error: %v\n", err)
			return ExitInput
		}
	}
	report := Evaluate(samples, ref)
	fmt.Fprintf(stdout, "batch %s n=%d\n", *batch, report.N)
	for _, field := range DiversityFields {
		entry := report.Fields[field]
		js := ""
		if entry.JSDistanceBits != nil {
			js = fmt.Sprintf(" js_ref=%.3f", *entry.JSDistanceBits)
		}
		fmt.Fprintf(stdout, "%-8s entropy=%.3fbit cats=%d%s\n", field, entry.EntropyBits, entry.Categories, js)
	}
	status := "VIOLATION"
	if report.OK {
		status = "OK"
	}
	fmt.Fprintf(stdout, "single-source share=%.2f (limit %.2f) %s\n", report.SingleSourceShare, SingleSourceLimit, status)
	if report.OK {
		return ExitOK
	}
	return ExitViolation
}

// readJSONL 读 jsonl 记录（跳过空行，返回非 nil 切片）；文件缺失或行非 JSON 报错。
func readJSONL(path string) ([]map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	records := []map[string]any{}
	for i, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r map[string]any
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("%s 第 %d 行非法 JSON: %w", path, i+1, err)
		}
		records = append(records, r)
	}
	return records, nil
}
