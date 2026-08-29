package synthgen

// 负样本帧流生成器契约测试（m2-spec §2，IR #90）：确定性逐字节复现 / 时长参数化
// / 帧能量落声明谱参数带（防静音流冒充）/ 版本冻结纪律 / eval-only 批语义 /
// CLI generate-neg / NegSeed FNV-1a 约定。属性用 testing/quick（spec §11.2）。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/quick"
)

// drainStream 消费整条帧流并深拷贝 PCM（流缓冲复用契约下须当场复制）。
func drainStream(st *NegStream) []NegFrame {
	var out []NegFrame
	for {
		f, ok := st.Next()
		if !ok {
			return out
		}
		out = append(out, NegFrame{TS: f.TS, Source: f.Source, PCM: slices.Clone(f.PCM)})
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// 1. NegSeed 约定：FNV-1a 64 标准测试向量锚定、同标签同种子、异标签异种子。
func TestNegSeedFNVConvention(t *testing.T) {
	// FNV-1a 64 标准向量："a" = 0xaf63dc4c8601ec8c、"b" = 0xaf63df6c8601f1a5
	//（uint64 溢出语义转 int64 须经变量，常量转换须可表示）。
	fnvOf := func(s string) int64 {
		var u uint64
		switch s {
		case "a":
			u = 0xaf63dc4c8601ec8c
		default:
			u = 0xaf63df4c8601f1a5
		}
		return int64(u)
	}
	if NegSeed("a") != fnvOf("a") || NegSeed("b") != fnvOf("b") {
		t.Fatalf("NegSeed FNV-1a 向量锚定失效: a=%#x b=%#x", NegSeed("a"), NegSeed("b"))
	}
	if NegSeed("T4-G0-01") != NegSeed("T4-G0-01") {
		t.Fatal("同标签种子不稳定")
	}
	if NegSeed("T4-G0-01") == NegSeed("T4-G0-02") {
		t.Fatal("异标签种子冲突（label 唯一 → 种子唯一被破坏）")
	}
}

// 2. 属性（quick）：同 (generator, duration, seed) 帧流逐字节复现；异 seed 分歧。
func TestNegStreamQuickDeterministicReplay(t *testing.T) {
	f := func(seed int64, frames int) bool {
		dur := (abs(frames)%180+1)*NegFrameMs + NegFrameMs // 60–5430ms
		a, errA := NewTNegStream(dur, seed)
		b, errB := NewTNegStream(dur, seed)
		if errA != nil || errB != nil {
			return errA == nil && errB == nil
		}
		fa, fb := drainStream(a), drainStream(b)
		if len(fa) != len(fb) {
			return false
		}
		for i := range fa {
			if fa[i].TS != fb[i].TS || fa[i].Source != fb[i].Source || !slices.Equal(fa[i].PCM, fb[i].PCM) {
				return false
			}
		}
		return true
	}
	if err := quick.Check(f, nil); err != nil {
		t.Fatal(err)
	}
	// 异 seed 至少一帧分歧（确定性 ≠ 常量流）。
	a, _ := NewTNegStream(30000, 101)
	b, _ := NewTNegStream(30000, 202)
	da, db := drainStream(a), drainStream(b)
	diff := false
	for i := range da {
		if !slices.Equal(da[i].PCM, db[i].PCM) {
			diff = true
			break
		}
	}
	if !diff {
		t.Fatal("异 seed 帧流完全相同（熵不足）")
	}
}

// 3. 属性（quick）：帧能量分布落声明谱参数带内（防静音流冒充负样本）——任何帧
// RMS ∈ [NegRMSFloor, NegRMSCeiling]，双生成器都验。
func TestNegStreamQuickEnergyBand(t *testing.T) {
	for name, mk := range map[string]func(int, int64) (*NegStream, error){
		"tneg":   NewTNegStream,
		"kwsadv": NewKWSAdvStream,
	} {
		f := func(seed int64, frames int) bool {
			dur := (abs(frames)%90+10)*NegFrameMs + NegFrameMs // 330–3000ms
			st, err := mk(dur, seed)
			if err != nil {
				return false
			}
			for {
				fr, ok := st.Next()
				if !ok {
					return true
				}
				rms := frameRMS(fr.PCM)
				if rms < NegRMSFloor || rms > NegRMSCeiling {
					return false
				}
			}
		}
		if err := quick.Check(f, nil); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// 4. 时长参数化：帧数=duration/NegFrameMs（6h=21600000ms → 720000 帧，即 360min
// 参数组装，无特殊 6h 模式）；帧 TS 按音频时钟逐帧推进；短时长/版本不符拒绝。
func TestNegStreamDurationParameterized(t *testing.T) {
	const sixHoursMs = 6 * 3600 * 1000
	st, err := NewTNegStream(sixHoursMs, NegSeed("duration-check"))
	if err != nil {
		t.Fatal(err)
	}
	if want := sixHoursMs / NegFrameMs; st.Frames() != want {
		t.Fatalf("6h 帧数 = %d, want %d（360min 参数组装）", st.Frames(), want)
	}
	// TS 音频时钟：前 5 帧 TS = 0/30/60/90/120ms（墙钟无关）。
	for i := 0; i < 5; i++ {
		f, ok := st.Next()
		if !ok || f.TS != int64(i)*NegFrameMs {
			t.Fatalf("帧 %d TS = %d, want %d", i, f.TS, int64(i)*NegFrameMs)
		}
		if len(f.PCM) != NegFrameMs*NegSampleRate/1000 {
			t.Fatalf("帧 %d PCM 长度 = %d, want 480", i, len(f.PCM))
		}
	}
	// 时长下限：< NegFrameMs 拒绝。
	if _, err := NewTNegStream(NegFrameMs-1, 0); err == nil {
		t.Fatal("时长 < 帧长未被拒绝")
	}
	// 版本冻结纪律：注册版本 ≠ 实现冻结版本 → 拒绝；未知生成器 → 拒绝。
	if _, err := NewNegStream(Generator{ID: TNegGeneratorID, Version: "0.9.0"}, 30000, 0); err == nil {
		t.Fatal("gen-tneg 版本漂移未被拒绝（改参数=新 version 重新注册）")
	}
	if _, err := NewNegStream(Generator{ID: KWSAdvGeneratorID, Version: "0.9.0"}, 30000, 0); err == nil {
		t.Fatal("gen-kwsadv 版本漂移未被拒绝")
	}
	if _, err := NewNegStream(Generator{ID: "gen-unknown", Version: "1.0.0"}, 30000, 0); err == nil {
		t.Fatal("未知负样本生成器未被拒绝")
	}
}

// 5. 源类型覆盖与等额轮转：≥4 源类型全出现、单源占比 ≤0.30（T2-G1-01 门槛的
// 生成器侧前提）；突发块含 ≥近讲声级峰值（远场诚实性口径的反向面）。
func TestNegStreamSourceCoverageAndRotation(t *testing.T) {
	const dur = 10 * 60 * 1000
	for name, tc := range map[string]struct {
		mk   func(int, int64) (*NegStream, error)
		want []string
	}{
		"tneg":   {NewTNegStream, tnegSourceTypes[:]},
		"kwsadv": {NewKWSAdvStream, kwsAdvSourceTypes[:]},
	} {
		st, err := tc.mk(dur, NegSeed("rotation:"+name))
		if err != nil {
			t.Fatal(err)
		}
		counts := map[string]int{}
		maxAbs := 0
		n := 0
		for {
			f, ok := st.Next()
			if !ok {
				break
			}
			counts[f.Source]++
			for _, v := range f.PCM {
				if a := abs(int(v)); a > maxAbs {
					maxAbs = a
				}
			}
			n++
		}
		if n != dur/NegFrameMs {
			t.Fatalf("%s 帧数 = %d, want %d", name, n, dur/NegFrameMs)
		}
		for _, src := range tc.want {
			if counts[src] == 0 {
				t.Fatalf("%s 源类型 %s 未出现（冻结表 ≥4 类）", name, src)
			}
		}
		if len(counts) != len(tc.want) {
			t.Fatalf("%s 源类型数 = %d, want %d", name, len(counts), len(tc.want))
		}
		for src, c := range counts {
			if share := float64(c) / float64(n); share > 0.30 {
				t.Fatalf("%s 单源占比 %s = %.3f > 0.30", name, src, share)
			}
		}
		if float64(maxAbs) < NegBurstPeakMin {
			t.Fatalf("%s 全流峰值 %d < %.0f（无 ≥近讲声级能量事件，非真实家庭音景）", name, maxAbs, NegBurstPeakMin)
		}
	}
}

// 6. 批语义：GenerateBatchNeg 落 manifest+frames.jsonl、eval-only 拓扑（TrainN=0/
// HoldoutN=0/EvalN=N、无 synth-train/synth-holdout 文件）、溯源戳四字段齐、PCM
// 本体不落盘、ReadNegBatch 读回一致。
func TestGenerateBatchNegEvalOnlySemantics(t *testing.T) {
	dir := t.TempDir()
	const dur = 60000
	seed := NegSeed("batch-semantics")
	for _, g := range []Generator{TNegGen(), KWSAdvGen()} {
		b, batchDir, err := GenerateBatchNeg(g, dir, dur, seed)
		if err != nil {
			t.Fatal(err)
		}
		if b.TrainN != 0 || b.HoldoutN != 0 || b.EvalN != b.N || b.N != dur/NegFrameMs {
			t.Fatalf("%s 批切分记录 = train %d/holdout %d/eval %d/n %d（eval-only 须全量入池）",
				g.ID, b.TrainN, b.HoldoutN, b.EvalN, b.N)
		}
		if b.Purpose != NegPurpose || b.Note == "" {
			t.Fatalf("%s 批 purpose/note = %q/%q（不切分理由须写进 manifest）", g.ID, b.Purpose, b.Note)
		}
		if b.GeneratorID != g.ID || b.GeneratorVersion != g.Version || b.Seed != seed || b.DurationMs != dur {
			t.Fatalf("%s 批重建参数记录不符: %+v", g.ID, b)
		}
		for _, forbidden := range []string{"synth-train.jsonl", "synth-holdout.jsonl"} {
			if _, err := os.Stat(filepath.Join(batchDir, forbidden)); !os.IsNotExist(err) {
				t.Fatalf("%s 批含 %s（eval-only 拓扑：负样本永不进训练管道）", g.ID, forbidden)
			}
		}
		// 批目录恰两文件：manifest.json + frames.jsonl（PCM 不落盘——确定性重建契约）。
		entries, err := os.ReadDir(batchDir)
		if err != nil {
			t.Fatal(err)
		}
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		if !slices.Equal(names, []string{"frames.jsonl", "manifest.json"}) {
			t.Fatalf("%s 批目录文件 = %v（PCM 本体不得落盘）", g.ID, names)
		}
		// 帧记录：sample_id 唯一 + 溯源戳恰四字段 + ts/source/rms 齐。
		raw, err := os.ReadFile(filepath.Join(batchDir, "frames.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		n := 0
		for _, line := range splitLines(raw) {
			if line == "" {
				continue
			}
			var rec NegFrameRecord
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("%s frames.jsonl 非法 JSON: %v", g.ID, err)
			}
			if seen[rec.SampleID] {
				t.Fatalf("%s sample_id 重复: %s", g.ID, rec.SampleID)
			}
			seen[rec.SampleID] = true
			if rec.Provenance.GeneratorID != g.ID || rec.Provenance.GeneratorVersion != g.Version ||
				rec.Provenance.Seed != seed || rec.Provenance.UpstreamModel == "" {
				t.Fatalf("%s 帧溯源戳四字段不符: %+v", g.ID, rec.Provenance)
			}
			if rec.RMS < NegRMSFloor || rec.RMS > NegRMSCeiling {
				t.Fatalf("%s 帧 RMS %.1f 越声明谱带", g.ID, rec.RMS)
			}
			n++
		}
		if n != b.N {
			t.Fatalf("%s frames.jsonl 行数 = %d, want %d", g.ID, n, b.N)
		}
		// ReadNegBatch 读回一致。
		got, gotDir, err := ReadNegBatch(dir, g.ID)
		if err != nil || got.ID != b.ID || gotDir != batchDir {
			t.Fatalf("ReadNegBatch = (%+v, %s, %v)", got, gotDir, err)
		}
	}
	// 目录缺失/无匹配批 → 报错。
	if _, _, err := ReadNegBatch(t.TempDir(), TNegGeneratorID); err == nil {
		t.Fatal("无负样本批时 ReadNegBatch 未报错")
	}
}

// splitLines 按行切（含尾空行）。
func splitLines(raw []byte) []string {
	var out []string
	start := 0
	for i, b := range raw {
		if b == '\n' {
			out = append(out, string(raw[start:i]))
			start = i + 1
		}
	}
	out = append(out, string(raw[start:]))
	return out
}

// 7. CLI generate-neg：注册→生成（--seed-label FNV 约定）→ manifest 落盘；同参
// 重跑逐字节复现；未注册 id / 短时长 / 版本漂移 / seed 双填 → ExitInput。
func TestCLIGenerateNeg(t *testing.T) {
	chdir(t, t.TempDir())
	for _, gid := range []string{TNegGeneratorID, KWSAdvGeneratorID} {
		argv := []string{"register", "--id", gid, "--version", "1.0.0",
			"--seed-policy", "fixed-fnv64a", "--outputs-manifest", "frames.jsonl"}
		if got := Run(argv, io.Discard, io.Discard); got != ExitOK {
			t.Fatalf("register %s exit = %d", gid, got)
		}
	}
	var out bytes.Buffer
	argv := []string{"generate-neg", "--id", TNegGeneratorID, "--duration-ms", "60000", "--seed-label", "T4-G0-01"}
	if got := Run(argv, &out, io.Discard); got != ExitOK {
		t.Fatalf("generate-neg exit = %d, 输出 %q", got, out.String())
	}
	batchDir := filepath.Join(BatchesDir, fmt.Sprintf("%s-%s-seed%d-d%d",
		TNegGeneratorID, TNegImplVersion, NegSeed("T4-G0-01"), 60000))
	first, err := os.ReadFile(filepath.Join(batchDir, "manifest.json"))
	if err != nil {
		t.Fatalf("canonical 批 manifest 未落盘: %v", err)
	}
	if got := Run(argv, io.Discard, io.Discard); got != ExitOK {
		t.Fatalf("二次 generate-neg exit = %d", got)
	}
	second, _ := os.ReadFile(filepath.Join(batchDir, "manifest.json"))
	if string(first) != string(second) {
		t.Fatal("同参 generate-neg manifest 未复现")
	}
	frames1, _ := os.ReadFile(filepath.Join(batchDir, "frames.jsonl"))
	_ = Run(argv, io.Discard, io.Discard)
	frames2, _ := os.ReadFile(filepath.Join(batchDir, "frames.jsonl"))
	if string(frames1) != string(frames2) {
		t.Fatal("同参 generate-neg frames.jsonl 未复现")
	}
	// 参数错误面：未注册 / 短时长 / seed 双填 / 版本漂移。
	bad := [][]string{
		{"generate-neg", "--id", "gen-nope", "--duration-ms", "60000"},
		{"generate-neg", "--id", TNegGeneratorID, "--duration-ms", "10"},
		{"generate-neg", "--id", TNegGeneratorID, "--duration-ms", "60000", "--seed", "1", "--seed-label", "x"},
		{"generate-neg", "--id", TNegGeneratorID},
	}
	for _, argv := range bad {
		if got := Run(argv, io.Discard, io.Discard); got != ExitInput {
			t.Fatalf("参数错误面 exit = %d, argv %v", got, argv)
		}
	}
	// 版本漂移：注册旧版本后 generate-neg 须拒绝（防「参数已改、版本未跟」）。
	if got := Run([]string{"register", "--id", TNegGeneratorID, "--version", "0.9.0",
		"--seed-policy", "fixed-fnv64a", "--outputs-manifest", "frames.jsonl"}, io.Discard, io.Discard); got != ExitOK {
		t.Fatalf("注册 0.9.0 exit = %d", got)
	}
	// FindGenerator 取最近注册（0.9.0）→ NewNegStream 版本纪律拒绝。
	if got := Run([]string{"generate-neg", "--id", TNegGeneratorID, "--duration-ms", "60000"}, io.Discard, io.Discard); got != ExitInput {
		t.Fatalf("版本漂移未拒绝: exit = %d", got)
	}
}
