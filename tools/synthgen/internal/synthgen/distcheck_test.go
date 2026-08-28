package synthgen

// dist-check 多样性契约测试（spec §3.7）：分布熵 / 参考集 JS 距离 / 单源占比门槛。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// rec 构造 dist-check fixture 样本。
func rec(i int, model, speaker string) map[string]any {
	return map[string]any{
		"sample_id":  fmt.Sprintf("s%04d", i),
		"provenance": map[string]any{"generator_id": "g", "generator_version": "1", "seed": 0, "upstream_model": model},
		"payload":    map[string]any{"speaker": speaker, "speed": "normal", "topic": "bedtime"},
	}
}

// writeBatch 写 100 条批次 fixture：dom 条来自 m-dom，其余分散在 7 个模型。
func writeBatch(t *testing.T, dom int) {
	t.Helper()
	samples := make([]map[string]any, 100)
	for i := range samples {
		if i < dom {
			samples[i] = rec(i, "m-dom", "spk-1")
		} else {
			samples[i] = rec(i, fmt.Sprintf("m-%d", i%7), "spk-1")
		}
	}
	var buf bytes.Buffer
	for _, s := range samples {
		data, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}
	dir := filepath.Join(BatchesDir, "batch-x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "samples.jsonl"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 4. 均匀分布熵=log2(k)、单点熵 0；JS 同分布 0 / 不相交 1 / 对称。
func TestEntropyExtremesAndJSDistance(t *testing.T) {
	if got := ShannonEntropy(Distribution([]string{"a", "b", "c", "d"})); math.Abs(got-2) > 1e-9 {
		t.Fatalf("四类均匀熵 = %v, want 2", got)
	}
	if got := ShannonEntropy(Distribution([]string{"a", "a", "a"})); got != 0 {
		t.Fatalf("单点熵 = %v, want 0", got)
	}
	a, b := Distribution([]string{"x", "y"}), Distribution([]string{"z", "w"})
	if got := JSDistance(a, a); got > 1e-9 {
		t.Fatalf("同分布 JS = %v, want 0", got)
	}
	if got := JSDistance(a, b); math.Abs(got-1) > 1e-9 {
		t.Fatalf("不相交 JS = %v, want 1", got)
	}
	if got, back := JSDistance(a, b), JSDistance(b, a); math.Abs(got-back) > 1e-12 {
		t.Fatalf("JS 不对称: %v vs %v", got, back)
	}
}

// 4. 与真实参考集的 JS 距离：同分布 → 0；远分布 → >0.5；报告恰覆盖三字段。
func TestEvaluateJSDistanceToReference(t *testing.T) {
	samples := make([]map[string]any, 40)
	same := make([]map[string]any, 40)
	far := make([]map[string]any, 40)
	for i := range samples {
		samples[i] = rec(i, "m-a", fmt.Sprintf("spk-%d", i%4))
		same[i] = map[string]any{"speaker": fmt.Sprintf("spk-%d", i%4), "speed": "normal", "topic": "bedtime"}
		far[i] = map[string]any{"speaker": "spk-x", "speed": "slow", "topic": "play"}
	}
	report := Evaluate(samples, same)
	if len(report.Fields) != 3 || report.Fields["speaker"].JSDistanceBits == nil ||
		math.Abs(*report.Fields["speaker"].JSDistanceBits) > 1e-9 {
		t.Fatalf("同分布参考集 JS 距离异常: %+v", report.Fields)
	}
	if js := Evaluate(samples, far).Fields["speaker"].JSDistanceBits; js == nil || *js <= 0.5 {
		t.Fatalf("远分布参考集 JS 距离未 >0.5: %+v", Evaluate(samples, far).Fields["speaker"])
	}
}

// 4. 单源占比门槛：31% → exit 20（输出占比 0.31）；30% → exit 0。
func TestDistCheckSingleSourceGate(t *testing.T) {
	for _, tc := range []struct {
		dom int
		ok  bool
	}{{31, false}, {30, true}} {
		t.Run(fmt.Sprintf("dom=%d", tc.dom), func(t *testing.T) {
			chdir(t, t.TempDir())
			writeBatch(t, tc.dom)
			var out bytes.Buffer
			got := Run([]string{"dist-check", "--batch", "batch-x"}, &out, io.Discard)
			if (got == ExitOK) != tc.ok {
				t.Fatalf("exit = %d, 输出: %q", got, out.String())
			}
			if !tc.ok && !strings.Contains(out.String(), "0.31") {
				t.Fatalf("输出未含占比 0.31: %q", out.String())
			}
		})
	}
}

// 4. --reference 输出 js_ref。
func TestCLIReferenceOutput(t *testing.T) {
	chdir(t, t.TempDir())
	writeBatch(t, 10)
	ref := "real-ref.jsonl"
	if err := os.WriteFile(ref, []byte(`{"speaker":"spk-1","speed":"normal","topic":"bedtime"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	argv := []string{"dist-check", "--batch", "batch-x", "--reference", ref}
	if got := Run(argv, &out, io.Discard); got != ExitOK {
		t.Fatalf("exit = %d, want %d", got, ExitOK)
	}
	if !strings.Contains(out.String(), "js_ref=") {
		t.Fatalf("输出未含 js_ref=: %q", out.String())
	}
}

// 5. 属性：分布熵对字段值置换不变（双射重命名 / 逆序重排）。
func TestEntropyInvariantUnderValuePermutation(t *testing.T) {
	relabel := map[string]string{"0": "3", "1": "4", "2": "5", "3": "0", "4": "1", "5": "2"}
	rng := rand.New(rand.NewSource(2)) // 固定种子可复现
	for iter := 0; iter < 100; iter++ {
		values := make([]string, 1+rng.Intn(60))
		renamed := make([]string, len(values))
		for i := range values {
			values[i] = strconv.Itoa(rng.Intn(6))
			renamed[i] = relabel[values[i]]
		}
		base := ShannonEntropy(Distribution(values))
		if got := ShannonEntropy(Distribution(renamed)); math.Abs(got-base) > 1e-12 {
			t.Fatalf("iter %d: 双射重命名改变熵: %v vs %v", iter, got, base)
		}
		reordered := make([]string, len(values))
		for i, v := range values {
			reordered[len(values)-1-i] = v
		}
		if got := ShannonEntropy(Distribution(reordered)); math.Abs(got-base) > 1e-12 {
			t.Fatalf("iter %d: 逆序重排改变熵: %v vs %v", iter, got, base)
		}
	}
}
