package holdoutctl

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 退出码：0 成功；1 失败（seal 破损、文件不可读等）；2 凭据缺失或非 holdout 环境。
const (
	ExitOK            = 0
	ExitFail          = 1
	ExitNoCredentials = 2

	MinSliceN       = 5 // k-匿名：任何分片 n<5 的切片一律抑制输出
	ManifestRelPath = "datasets/holdout/sealed-manifest.json"
	AuditRelPath    = "reports/holdout-audit.jsonl"
)

var requiredHoldoutEnv = [...]string{"HOLDOUT_ENVIRONMENT", "HOLDOUT_RUNNER_TOKEN", "HOLDOUT_STORAGE_URL", "HOLDOUT_SEAL_KEY"}

// Run 分发子命令并返回退出码；main 只做薄包装。
func Run(args []string) int {
	const usage = "用法: holdoutctl <verify-seal|eval|audit> [flags]"
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return ExitFail
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "verify-seal":
		return runVerifySeal(rest)
	case "eval":
		return runEval(rest)
	case "audit":
		return runAudit(rest)
	}
	fmt.Fprintf(os.Stderr, "holdoutctl: 未知子命令 %q\n%s\n", cmd, usage)
	return ExitFail
}

func utcNowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

func fail(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	return ExitFail
}

// verifySeal 校验 sealed-manifest：对去掉 signature 后的 canonical JSON
// （键排序、紧凑分隔符）做 HMAC-SHA256，并核对声明对象数，返回对象数。
func verifySeal(path, key string) (int, error) {
	if key == "" {
		return 0, errors.New("HOLDOUT_SEAL_KEY 未设置，无法校验签名")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("sealed manifest 不存在或不可读: %w", err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return 0, fmt.Errorf("sealed manifest 不可解析: %w", err)
	}
	payload := make(map[string]any, len(manifest))
	for k, v := range manifest {
		if k != "signature" {
			payload[k] = v
		}
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(canonical)
	signature, _ := manifest["signature"].(string)
	want := hex.EncodeToString(mac.Sum(nil))
	if len(signature) != 64 || !hmac.Equal([]byte(want), []byte(signature)) {
		return 0, errors.New("signature 缺失、格式非法或不匹配（篡改）")
	}
	objects, ok := manifest["objects"].([]any)
	if !ok || len(objects) == 0 {
		return 0, errors.New("objects 缺失或为空")
	}
	if declared, _ := manifest["object_count"].(float64); int(declared) != len(objects) || declared != float64(int(declared)) {
		return 0, fmt.Errorf("对象数不符: 声明 %v != 实际 %d", declared, len(objects))
	}
	return len(objects), nil
}

// ApplyKAnonymity 抑制任何 n<k 的切片（k-匿名），其余原样保留。
func ApplyKAnonymity(slices map[string]float64, k int) map[string]float64 {
	out := make(map[string]float64, len(slices))
	for key, n := range slices {
		if n >= float64(k) {
			out[key] = n
		}
	}
	return out
}

// redactRawPaths 原始样本路径不出受控存储：序列化前剥除疑似路径字符串。
func redactRawPaths(v any) any {
	if m, ok := v.(map[string]any); ok {
		for k, val := range m {
			m[k] = redactRawPaths(val)
		}
		return m
	}
	if s, ok := v.(string); ok && strings.Contains(s, "datasets/holdout/") {
		return "[redacted]"
	}
	return v
}

func fileSHA256(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

// appendAudit 追加一行审计记录（谁/何时/哪个 suite/输出摘要哈希），禁改写。
func appendAudit(log, suite, sha, actor, event string) (map[string]string, error) {
	if actor == "" {
		for _, name := range [...]string{"HOLDOUT_ACTOR", "USER"} {
			if actor = os.Getenv(name); actor != "" {
				break
			}
		}
		if actor == "" {
			actor = "unknown"
		}
	}
	row := map[string]string{"actor": actor, "event": event, "sha256": sha,
		"suite": suite, "timestamp": utcNowISO()}
	if err := os.MkdirAll(filepath.Dir(log), 0o755); err != nil {
		return row, err
	}
	data, _ := json.Marshal(row) // 全 string 键值，无失败路径；map 键排序输出
	f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return row, err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return row, err
}

func runVerifySeal(args []string) int {
	fs := flag.NewFlagSet("verify-seal", flag.ExitOnError) // 解析失败 exit 2（同 PR #18 argparse 语义）
	manifest := fs.String("manifest", ManifestRelPath, "sealed manifest 路径")
	_ = fs.Parse(args)
	n, err := verifySeal(*manifest, os.Getenv("HOLDOUT_SEAL_KEY"))
	if err != nil {
		return fail("verify-seal FAILED: %v", err)
	}
	fmt.Printf("OK: seal verified: %d objects (%s)\n", n, *manifest)
	return ExitOK
}

func runEval(args []string) int {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	suite := fs.String("suite", "", "suite 名，例如 real-t4")
	out := fs.String("out", "", "输出目录，例如 reports/nightly/")
	manifest := fs.String("manifest", ManifestRelPath, "sealed manifest 路径")
	log := fs.String("audit-log", AuditRelPath, "审计日志路径")
	shards := fs.String("shards", "", "分片计数 JSON 路径（{slice: n}，逗号分隔可多个）")
	_ = fs.Parse(args)
	if *suite == "" || *out == "" {
		return fail("eval: 需要 --suite 与 --out")
	}
	var missing []string
	for _, name := range requiredHoldoutEnv {
		if os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 || os.Getenv("HOLDOUT_ENVIRONMENT") != "holdout" {
		fmt.Fprintf(os.Stderr, "eval 只能在 environment=holdout 的 runner 上运行（凭据缺失: %s）\n",
			strings.Join(missing, ","))
		return ExitNoCredentials
	}
	if err := evalRun(*suite, *out, *manifest, *log, *shards); err != nil {
		return fail("eval 中止: %v", err)
	}
	return ExitOK
}

// evalRun 只输出聚合指标：分片求和 → k-匿名抑制 → 剥除原始路径 → 写盘 → 审计。
func evalRun(suite, outDir, manifest, log, shards string) error {
	if _, err := verifySeal(manifest, os.Getenv("HOLDOUT_SEAL_KEY")); err != nil {
		return err
	}
	merged := make(map[string]float64)
	for _, path := range strings.Split(shards, ",") {
		if path == "" {
			continue
		}
		var shard map[string]float64
		raw, err := os.ReadFile(path)
		if err == nil {
			err = json.Unmarshal(raw, &shard)
		}
		if err != nil {
			return fmt.Errorf("分片不可读: %w", err)
		}
		for key, n := range shard {
			merged[key] += n
		}
	}
	kept := ApplyKAnonymity(merged, MinSliceN)
	payload := redactRawPaths(map[string]any{
		"generated_at": utcNowISO(), "k_anonymity_min_n": MinSliceN, "metrics": kept,
		"source": "sealed-holdout", "suite": suite, "suppressed_slices": len(merged) - len(kept),
	}).(map[string]any)
	data, _ := json.MarshalIndent(payload, "", "  ")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	outFile := filepath.Join(outDir, fmt.Sprintf("holdout-%s-metrics.json", suite))
	if err := os.WriteFile(outFile, append(data, '\n'), 0o644); err != nil {
		return err
	}
	sum, err := fileSHA256(outFile)
	if err != nil {
		return err
	}
	if _, err := appendAudit(log, suite, sum, "", "eval"); err != nil {
		return err
	}
	fmt.Printf("eval %s: %d 项聚合指标（抑制 %d 个小分片）→ %s\n", suite, len(kept), len(merged)-len(kept), outFile)
	return nil
}

func runAudit(args []string) int {
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	suite := fs.String("suite", "unspecified", "suite 名")
	artifact := fs.String("artifact", "", "要记录摘要哈希的输出文件")
	log := fs.String("audit-log", AuditRelPath, "审计日志路径")
	_ = fs.Parse(args)
	var sum string
	if *artifact != "" {
		got, err := fileSHA256(*artifact)
		if err != nil {
			return fail("audit FAILED: %v", err)
		}
		sum = got
	} else {
		seed, _ := json.Marshal(map[string]string{"suite": *suite, "ts": utcNowISO()})
		digest := sha256.Sum256(seed)
		sum = hex.EncodeToString(digest[:])
	}
	row, err := appendAudit(*log, *suite, sum, "", "audit")
	if err != nil {
		return fail("audit FAILED: %v", err)
	}
	out, _ := json.Marshal(row)
	fmt.Println(string(out))
	return ExitOK
}
