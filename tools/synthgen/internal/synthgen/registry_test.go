package synthgen

// 注册表契约测试（spec §3.7，契约设计沿用 PR #21 Python 版）：测试先行。

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// chdir 进入临时目录（CLI 默认落盘 datasets/synth/**）。
func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Fatal(err)
		}
	})
}

// registerGen 经 CLI 注册 gen-a@1.0.0（默认注册表路径）。
func registerGen(t *testing.T) {
	t.Helper()
	argv := []string{"register", "--id", "gen-a", "--version", "1.0.0",
		"--seed-policy", "fixed", "--outputs-manifest", "m.jsonl"}
	if got := Run(argv, io.Discard, io.Discard); got != ExitOK {
		t.Fatalf("register exit = %d, want %d", got, ExitOK)
	}
}

// 1. 注册恰四字段、可查询；重复 id+version 报错且不落盘；同 id 新版本放行（缺省=最近注册）。
func TestRegisterQueryAndDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.jsonl")
	want := Generator{ID: "gen-a", Version: "1.0.0", SeedPolicy: "fixed", OutputsManifest: "m.jsonl"}
	got, err := RegisterGenerator(path, "gen-a", "1.0.0", "fixed", "m.jsonl")
	if err != nil || got != want {
		t.Fatalf("RegisterGenerator = %+v, %v; want %+v", got, err, want)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != 4 {
		t.Fatalf("注册记录非恰四字段: %s (%v)", raw, err)
	}
	records, err := LoadRegistry(path)
	if err != nil || len(records) != 1 {
		t.Fatalf("LoadRegistry = %+v, %v", records, err)
	}
	if found, _ := FindGenerator(records, "gen-a"); found != want {
		t.Fatalf("FindGenerator = %+v, want %+v", found, want)
	}
	if _, err := RegisterGenerator(path, "gen-a", "1.0.0", "per-sample", "m2.jsonl"); !errors.Is(err, ErrDuplicateGenerator) {
		t.Fatalf("重复注册未报 ErrDuplicateGenerator: %v", err)
	}
	if records, err = LoadRegistry(path); err != nil || len(records) != 1 {
		t.Fatalf("重复注册落盘: %d 行 (%v)", len(records), err)
	}
	if _, err := RegisterGenerator(path, "gen-a", "1.1.0", "fixed", "m.jsonl"); err != nil {
		t.Fatal(err)
	}
	if records, err = LoadRegistry(path); err != nil || len(records) != 2 {
		t.Fatalf("同 id 新版本未放行: %d 行 (%v)", len(records), err)
	}
	if found, _ := FindGenerator(records, "gen-a"); found.Version != "1.1.0" {
		t.Fatalf("缺省版本未取最近注册: %+v", found)
	}
}

// 1. CLI 层：重复注册 / 未注册生成 / 缺失批次 → exit 2。
func TestCLIInputErrors(t *testing.T) {
	chdir(t, t.TempDir())
	argv := []string{"register", "--id", "gen-a", "--version", "1",
		"--seed-policy", "fixed", "--outputs-manifest", "m.jsonl"}
	if got := Run(argv, io.Discard, io.Discard); got != ExitOK {
		t.Fatalf("首次注册 exit = %d, want %d", got, ExitOK)
	}
	if got := Run(argv, io.Discard, io.Discard); got != ExitInput {
		t.Fatalf("重复注册 exit = %d, want %d", got, ExitInput)
	}
	if got := Run([]string{"generate", "--id", "ghost", "--n", "5", "--seed", "1"}, io.Discard, io.Discard); got != ExitInput {
		t.Fatalf("未注册生成 exit = %d, want %d", got, ExitInput)
	}
	if got := Run([]string{"dist-check", "--batch", "nope"}, io.Discard, io.Discard); got != ExitInput {
		t.Fatalf("缺失批次 exit = %d, want %d", got, ExitInput)
	}
}
