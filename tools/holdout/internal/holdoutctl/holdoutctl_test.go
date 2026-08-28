package holdoutctl

// 契约测试（spec §3.4，契约设计沿用 PR #18）：测试先行。

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const testSealKey = "test-only-seal-key"

// sealManifest 测试端独立实现 HMAC 签名，避免与实现端自洽循环。
func sealManifest(m map[string]any) map[string]any {
	payload := make(map[string]any, len(m))
	for k, v := range m {
		if k != "signature" {
			payload[k] = v
		}
	}
	canonical, _ := json.Marshal(payload)
	mac := hmac.New(sha256.New, []byte(testSealKey))
	mac.Write(canonical)
	m["signature"] = hex.EncodeToString(mac.Sum(nil))
	return m
}

func makeManifest(n int, badCount bool) map[string]any {
	objects := make([]any, n)
	for i := range objects {
		objects[i] = map[string]any{"path": fmt.Sprintf("datasets/holdout/shard-%03d.jsonl", i)}
	}
	if badCount {
		n++
	}
	return map[string]any{"suite": "real-t4", "objects": objects, "object_count": n}
}

func writeManifest(t *testing.T, m map[string]any) string {
	t.Helper()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sealed-manifest.json")
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// setHoldoutEnv 清空全部 HOLDOUT_*（空值视为缺失）后按 env 设置。
func setHoldoutEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, name := range append(requiredHoldoutEnv[:], "HOLDOUT_ACTOR") {
		t.Setenv(name, "")
	}
	for name, value := range env {
		t.Setenv(name, value)
	}
}

func readAuditRows(t *testing.T, path string) (rows []map[string]any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("审计行非法 JSON: %v", err)
		}
		rows = append(rows, row)
	}
	return rows
}

// 1. eval 凭据门禁：缺任一 HOLDOUT_* 或非 holdout 环境 → exit 2；seal 缺失 → exit 1（非 2）。
func TestEvalCredentialGate(t *testing.T) {
	full := map[string]string{"HOLDOUT_ENVIRONMENT": "holdout", "HOLDOUT_RUNNER_TOKEN": "t",
		"HOLDOUT_STORAGE_URL": "blob://x", "HOLDOUT_SEAL_KEY": testSealKey}
	for _, name := range requiredHoldoutEnv {
		env := maps.Clone(full)
		delete(env, name)
		t.Run("missing-"+strings.ToLower(strings.TrimPrefix(name, "HOLDOUT_")), func(t *testing.T) {
			setHoldoutEnv(t, env)
			if got := Run([]string{"eval", "--suite", "s", "--out", t.TempDir()}); got != ExitNoCredentials {
				t.Fatalf("exit = %d, want %d", got, ExitNoCredentials)
			}
		})
	}
	notHoldout := maps.Clone(full)
	notHoldout["HOLDOUT_ENVIRONMENT"] = "dev"
	t.Run("not-holdout-environment", func(t *testing.T) {
		setHoldoutEnv(t, notHoldout)
		if got := Run([]string{"eval", "--suite", "s", "--out", t.TempDir()}); got != ExitNoCredentials {
			t.Fatalf("exit = %d, want %d", got, ExitNoCredentials)
		}
	})
	t.Run("seal-missing-fails-not-2", func(t *testing.T) {
		setHoldoutEnv(t, full)
		got := Run([]string{"eval", "--suite", "s", "--out", t.TempDir(),
			"--manifest", filepath.Join(t.TempDir(), "nope.json"), "--audit-log", "unused.jsonl"})
		if got != ExitFail {
			t.Fatalf("exit = %d, want %d", got, ExitFail)
		}
	})
}

// 2. verify-seal：合法 fixture（测试密钥构造）→ 0；篡改/计数不符/不存在/无密钥 → 非零。
func TestVerifySeal(t *testing.T) {
	t.Setenv("HOLDOUT_SEAL_KEY", testSealKey)
	if got := Run([]string{"verify-seal", "--manifest", writeManifest(t, sealManifest(makeManifest(3, false)))}); got != ExitOK {
		t.Fatalf("合法 fixture exit = %d, want %d", got, ExitOK)
	}
	tampered := sealManifest(makeManifest(3, false))
	tampered["objects"].([]any)[0].(map[string]any)["path"] = "datasets/holdout/evil.jsonl" // 篡改，不动 signature
	for name, m := range map[string]map[string]any{
		"tampered-object": tampered,
		"count-mismatch":  sealManifest(makeManifest(3, true)), // 签名合法但声明数不符
	} {
		t.Run(name, func(t *testing.T) {
			if got := Run([]string{"verify-seal", "--manifest", writeManifest(t, m)}); got == ExitOK {
				t.Fatal("exit = 0, want 非零")
			}
		})
	}
	t.Run("manifest-missing", func(t *testing.T) {
		if got := Run([]string{"verify-seal", "--manifest", filepath.Join(t.TempDir(), "nope.json")}); got == ExitOK {
			t.Fatal("exit = 0, want 非零")
		}
	})
	t.Run("no-key", func(t *testing.T) {
		setHoldoutEnv(t, nil)
		if got := Run([]string{"verify-seal", "--manifest", writeManifest(t, sealManifest(makeManifest(3, false)))}); got == ExitOK {
			t.Fatal("exit = 0, want 非零")
		}
	})
}

// 3. audit：追加不覆盖（两行），行含 timestamp/actor/event/suite/sha256 且哈希=artifact 摘要。
func TestAuditAppendsNotOverwrites(t *testing.T) {
	setHoldoutEnv(t, map[string]string{"HOLDOUT_ACTOR": "tester"})
	dir := t.TempDir()
	artifact, log := filepath.Join(dir, "metrics.json"), filepath.Join(dir, "holdout-audit.jsonl")
	var wantSums []string
	for _, payload := range []string{`{"suite": "real-t4"}`, `{"suite": "real-t4", "v": 2}`} {
		if err := os.WriteFile(artifact, []byte(payload), 0o644); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte(payload))
		wantSums = append(wantSums, hex.EncodeToString(digest[:]))
		if got := Run([]string{"audit", "--suite", "real-t4", "--artifact", artifact, "--audit-log", log}); got != ExitOK {
			t.Fatalf("exit = %d, want %d", got, ExitOK)
		}
	}
	rows := readAuditRows(t, log)
	if len(rows) != 2 { // 追加不覆盖，首行原样保留
		t.Fatalf("审计行数 = %d, want 2", len(rows))
	}
	for i, row := range rows {
		if row["timestamp"] == nil || row["actor"] != "tester" || row["event"] == nil ||
			row["suite"] != "real-t4" || row["sha256"] != wantSums[i] {
			t.Errorf("第 %d 行字段不符: %v", i, row)
		}
	}
}

// 4. k-匿名单测（表驱动，n=4 抑制、n=5 保留）+ 属性（表随机）：任意分片中 n<5 键绝不出现在输出。
func TestKAnonymity(t *testing.T) {
	if MinSliceN != 5 {
		t.Fatalf("MinSliceN = %d, want 5", MinSliceN)
	}
	for _, tc := range []struct{ in, want map[string]float64 }{
		{map[string]float64{"a": 4}, map[string]float64{}},
		{map[string]float64{"a": 5}, map[string]float64{"a": 5}},
		{map[string]float64{"a": 4, "b": 5, "slice": 3, "other": 10}, map[string]float64{"b": 5, "other": 10}},
	} {
		if got := ApplyKAnonymity(tc.in, MinSliceN); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ApplyKAnonymity(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
	rng := rand.New(rand.NewSource(1)) // 固定种子可复现
	for iter := 0; iter < 100; iter++ {
		slices := make(map[string]float64)
		for i, n := 0, rng.Intn(25); i < n; i++ {
			slices[fmt.Sprintf("slice-%d", rng.Intn(1000))] = float64(rng.Intn(51))
		}
		out := ApplyKAnonymity(slices, MinSliceN)
		for key, n := range slices {
			if _, leaked := out[key]; n < MinSliceN && leaked {
				t.Fatalf("iter %d: n=%v 的键 %q 泄漏进输出", iter, n, key)
			}
		}
	}
}

// 5. eval 集成：凭据齐全 + 合法 seal + 分片 JSON → 聚合输出无原始路径、审计哈希=输出 sha256。
func TestEvalIntegrationAggregatesAndAudits(t *testing.T) {
	dir := t.TempDir()
	shard := filepath.Join(dir, "shards.json")
	if err := os.WriteFile(shard, []byte(`{"slice": 3, "other": 10}`), 0o644); err != nil {
		t.Fatal(err)
	}
	setHoldoutEnv(t, map[string]string{"HOLDOUT_ENVIRONMENT": "holdout", "HOLDOUT_RUNNER_TOKEN": "t",
		"HOLDOUT_STORAGE_URL": "blob://holdout", "HOLDOUT_SEAL_KEY": testSealKey})
	outDir, log := filepath.Join(dir, "out"), filepath.Join(dir, "holdout-audit.jsonl")
	argv := []string{"eval", "--suite", "real-t4", "--out", outDir,
		"--manifest", writeManifest(t, sealManifest(makeManifest(3, false))), "--audit-log", log, "--shards", shard}
	if got := Run(argv); got != ExitOK {
		t.Fatalf("exit = %d, want %d", got, ExitOK)
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "holdout-real-t4-metrics.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "datasets/holdout") { // 原始样本路径不出受控存储
		t.Fatalf("输出泄漏原始路径: %s", raw)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	metrics := payload["metrics"].(map[string]any)
	if _, leaked := metrics["slice"]; leaked {
		t.Error("n=3 分片未被抑制")
	}
	if metrics["other"] != float64(10) || payload["suppressed_slices"] != float64(1) || payload["suite"] != "real-t4" {
		t.Errorf("聚合结果异常: %v", payload)
	}
	digest := sha256.Sum256(raw)
	if rows := readAuditRows(t, log); len(rows) != 1 || rows[0]["event"] != "eval" ||
		rows[0]["sha256"] != hex.EncodeToString(digest[:]) {
		t.Errorf("审计行异常: %v", rows)
	}
}
