package synthgen

// 溯源戳 + 8:2 切分 + manifest 契约测试（spec §3.7）：测试先行。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// readIDs 读 jsonl 样本并取 sample_id 列表。
func readIDs(t *testing.T, path string) []string {
	t.Helper()
	records, err := readJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(records))
	for i, r := range records {
		ids[i], _ = r["sample_id"].(string)
	}
	return ids
}

// 2. 每条样本带溯源戳：恰 {generator_id, generator_version, seed, upstream_model} 四字段。
func TestEverySampleCarriesProvenanceStamp(t *testing.T) {
	chdir(t, t.TempDir())
	registerGen(t)
	argv := []string{"generate", "--id", "gen-a", "--n", "25", "--seed", "7"}
	if got := Run(argv, io.Discard, io.Discard); got != ExitOK {
		t.Fatalf("generate exit = %d, want %d", got, ExitOK)
	}
	samples, err := readJSONL(filepath.Join(BatchesDir, "gen-a-1.0.0-seed7-n25", "samples.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 25 {
		t.Fatalf("样本数 = %d, want 25", len(samples))
	}
	for i, s := range samples {
		prov, ok := s["provenance"].(map[string]any)
		if !ok || len(prov) != 4 {
			t.Fatalf("第 %d 条溯源戳非恰四字段: %v", i, prov)
		}
		if prov["generator_id"] != "gen-a" || prov["generator_version"] != "1.0.0" || prov["seed"] != float64(7) {
			t.Errorf("第 %d 条溯源字段值不符: %v", i, prov)
		}
		if model, _ := prov["upstream_model"].(string); model == "" {
			t.Errorf("第 %d 条 upstream_model 为空", i)
		}
		if _, ok := s["payload"].(map[string]any); !ok {
			t.Errorf("第 %d 条缺 payload 桩", i)
		}
	}
}

// 3. N=100 → 80/20（train/holdout 不相交、并集=全量）；manifest 记录切分；同 seed 逐字节复现。
func TestGenerateSplits8020ManifestReproducible(t *testing.T) {
	chdir(t, t.TempDir())
	registerGen(t)
	argv := []string{"generate", "--id", "gen-a", "--n", "100", "--seed", "42"}
	if got := Run(argv, io.Discard, io.Discard); got != ExitOK {
		t.Fatalf("generate exit = %d, want %d", got, ExitOK)
	}
	dir := filepath.Join(BatchesDir, "gen-a-1.0.0-seed42-n100")
	train, holdout := readIDs(t, filepath.Join(dir, "synth-train.jsonl")),
		readIDs(t, filepath.Join(dir, "synth-holdout.jsonl"))
	if len(train) != 80 || len(holdout) != 20 {
		t.Fatalf("切分 = %d/%d, want 80/20", len(train), len(holdout))
	}
	ids := readIDs(t, filepath.Join(dir, "samples.jsonl"))
	merged := append(append([]string(nil), train...), holdout...)
	sort.Strings(merged)
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	if len(ids) != 100 || !reflect.DeepEqual(merged, sorted) {
		t.Fatalf("切分有重或漏: 并集 %d != 全量 %d", len(merged), len(sorted))
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["train_n"] != float64(80) || manifest["holdout_n"] != float64(20) ||
		manifest["seed"] != float64(42) || manifest["n"] != float64(100) {
		t.Fatalf("manifest 切分记录不符: %v", manifest)
	}
	first, err := os.ReadFile(filepath.Join(dir, "samples.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if got := Run(argv, io.Discard, io.Discard); got != ExitOK {
		t.Fatalf("二次 generate exit = %d, want %d", got, ExitOK)
	}
	second, err := os.ReadFile(filepath.Join(dir, "samples.jsonl"))
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("同 seed 未逐字节复现")
	}
}

// 3. 属性（50 轮随机种子）：同 seed 切分完全一致、无重无漏、holdout=floor(0.2n)。
func TestSplitReproducibleAnySeed(t *testing.T) {
	rng := rand.New(rand.NewSource(1)) // 固定种子可复现
	for iter := 0; iter < 50; iter++ {
		seed := rng.Int63()
		ids := make([]string, 97)
		for i := range ids {
			ids[i] = fmt.Sprintf("s%04d", i)
		}
		train1, hold1 := SplitHoldout(ids, seed)
		train2, hold2 := SplitHoldout(ids, seed)
		if !reflect.DeepEqual(train1, train2) || !reflect.DeepEqual(hold1, hold2) {
			t.Fatalf("iter %d: 同 seed 切分不一致", iter)
		}
		if len(train1) != 78 || len(hold1) != 19 { // floor(97×0.2)=19
			t.Fatalf("iter %d: 切分 = %d/%d, want 78/19", iter, len(train1), len(hold1))
		}
		merged := append(append([]string(nil), train1...), hold1...)
		sort.Strings(merged)
		want := append([]string(nil), ids...)
		sort.Strings(want)
		if !reflect.DeepEqual(merged, want) {
			t.Fatalf("iter %d: 切分有重或漏", iter)
		}
	}
}
