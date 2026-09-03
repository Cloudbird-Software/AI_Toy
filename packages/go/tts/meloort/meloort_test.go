// meloort 测试（非门禁）：ORT 会话对拍（Python 前端张量+固定噪声 → 参考波形
// 逐样本比对）、运行确定性、形状契约防御、Go 全链闭环（查表 g2p→确定性噪声→
// ORT 合成→wav）与 RTF 实测（T13_RTF_OUT 门控）。模型/库缺失时 Skip（基础
// 设施面，非数据面，惯例照 turntaking/vap）。实测数字入 reports/eval/T13/。
package meloort

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/Cloudbird-Software/AI_Toy/packages/go/tts"
)

func newTestSession(t *testing.T) *Session {
	t.Helper()
	p := DefaultModelPath()
	if _, err := os.Stat(p); err != nil {
		t.Skipf("模型未就位（基础设施面，缺 %s）: %v", p, err)
	}
	s, err := New(Config{ModelPath: p, LibraryPath: DefaultLibraryPath()})
	if err != nil {
		t.Skipf("会话初始化失败（基础设施面）: %v", err)
	}
	t.Cleanup(func() { _ = s.Destroy() })
	return s
}

// ---- fixtures 加载（gen_melo_ort_fixtures.py 产物，小端原始二进制） ----

type fixture struct {
	Text          string `json:"text"`
	T             int    `json:"t"`
	Samples       int    `json:"samples"`
	Tokens, Tones []int64
	Langs         []int64
	JaBert        []float32 // mBERT 韵律特征（会话输入一部分；生产路径恒零=债务）
	Sdp, Z, Ref   []float32
}

func loadI64(t *testing.T, path string) []int64 {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("fixtures 缺失（先跑 tools/tts/gen_melo_ort_fixtures.py）: %v", err)
	}
	out := make([]int64, len(raw)/8)
	for i := range out {
		out[i] = int64(binary.LittleEndian.Uint64(raw[i*8:]))
	}
	return out
}

func loadF32(t *testing.T, path string) []float32 {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("fixtures 缺失（先跑 tools/tts/gen_melo_ort_fixtures.py）: %v", err)
	}
	out := make([]float32, len(raw)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	return out
}

func loadFixtures(t *testing.T) []fixture {
	t.Helper()
	var meta struct {
		Sentences []struct {
			I       int    `json:"i"`
			Text    string `json:"text"`
			T       int    `json:"t"`
			Samples int    `json:"samples"`
		} `json:"sentences"`
	}
	mb, err := os.ReadFile("testdata/melo-ort-parity/meta.json")
	if err != nil {
		t.Skipf("fixtures 缺失（先跑 tools/tts/gen_melo_ort_fixtures.py）: %v", err)
	}
	if err := json.Unmarshal(mb, &meta); err != nil {
		t.Fatalf("meta.json 解析: %v", err)
	}
	var out []fixture
	for _, m := range meta.Sentences {
		stem := filepath.Join("testdata/melo-ort-parity", fmt.Sprintf("%02d", m.I))
		out = append(out, fixture{
			Text: m.Text, T: m.T, Samples: m.Samples,
			Tokens: loadI64(t, stem+"-tokens.i64"),
			Tones:  loadI64(t, stem+"-tones.i64"),
			Langs:  loadI64(t, stem+"-langs.i64"),
			JaBert: loadF32(t, stem+"-jabert.f32"),
			Sdp:    loadF32(t, stem+"-sdp.f32"),
			Z:      loadF32(t, stem+"-z.f32"),
			Ref:    loadF32(t, stem+"-ref.f32"),
		})
	}
	return out
}

func (f fixture) meloIO() tts.MeloIO {
	return tts.MeloIO{
		Tokens: f.Tokens, Tones: f.Tones, LangIDs: f.Langs,
		Bert:       make([]float32, 1024*f.T), // ZH_MIX_EN 契约恒零槽
		JaBert:     f.JaBert,
		SdpNoise:   f.Sdp,
		ZNoise:     f.Z,
		NoiseScale: 0.6, NoiseScaleW: 0.8, LengthScale: 1, SdpRatio: 0.2,
	}
}

// audioMetrics 波形比对口径（与 tools/tts/export_melotts_onnx.py audio_metrics
// 一致：max_abs/mean_abs/rmse/snr_db/pearson_r）。
func audioMetrics(a, b []float32) (maxAbs, meanAbs, rmse, snr, r float64) {
	if len(a) != len(b) {
		return 0, 0, 0, 0, math.NaN()
	}
	var sd, sa, va, vb float64
	for i := range a {
		d := float64(a[i]) - float64(b[i])
		sd += d * d
		sa += math.Abs(d)
		va += float64(a[i]) * float64(a[i])
		vb += float64(b[i]) * float64(b[i])
	}
	n := float64(len(a))
	rmse = math.Sqrt(sd / n)
	meanAbs = sa / n
	maxAbs = 0
	for i := range a {
		if d := math.Abs(float64(a[i]) - float64(b[i])); d > maxAbs {
			maxAbs = d
		}
	}
	sigRms := math.Sqrt(va / n)
	if rmse > 0 {
		snr = 20 * math.Log10(sigRms/rmse)
	} else {
		snr = math.Inf(1)
	}
	if va > 0 && vb > 0 {
		var cov float64
		for i := range a {
			cov += float64(a[i]) * float64(b[i])
		}
		r = cov / math.Sqrt(va*vb)
	} else {
		r = math.NaN()
	}
	return maxAbs, meanAbs, rmse, snr, r
}

// TestMeloORTGoldenParity 会话级对拍：同（前端张量+噪声）下 Go ORT vs Python
// ORT 参考波形。阈值来自导出对拍同源证据（torch↔ort SNR≈88dB r=1.0）放宽容纳
// ORT 版本差（Python 1.19 参考生成 vs Go 1.29 运行时）。
func TestMeloORTGoldenParity(t *testing.T) {
	s := newTestSession(t)
	for _, f := range loadFixtures(t) {
		audio, err := s.Run(f.meloIO())
		if err != nil {
			t.Errorf("%q: Run: %v", f.Text, err)
			continue
		}
		if len(audio) != f.Samples {
			t.Errorf("%q: 样本数 %d ≠ 参考 %d", f.Text, len(audio), f.Samples)
			continue
		}
		maxAbs, meanAbs, _, snr, r := audioMetrics(audio, f.Ref)
		t.Logf("%q: samples=%d max_abs=%.3e mean_abs=%.3e snr=%.2f dB r=%.9f",
			f.Text, len(audio), maxAbs, meanAbs, snr, r)
		if r < 0.999 {
			t.Errorf("%q: pearson r=%.9f < 0.999", f.Text, r)
		}
		if snr < 40 {
			t.Errorf("%q: snr=%.2f dB < 40（对拍破损）", f.Text, snr)
		}
	}
}

// TestMeloORTDeterministic 同输入两次 Run 字节一致（ORT 同进程确定性）。
func TestMeloORTDeterministic(t *testing.T) {
	s := newTestSession(t)
	f := loadFixtures(t)[0]
	a, err := s.Run(f.meloIO())
	if err != nil {
		t.Fatalf("Run#1: %v", err)
	}
	b, err := s.Run(f.meloIO())
	if err != nil {
		t.Fatalf("Run#2: %v", err)
	}
	if !bytes.Equal(float32Bytes(a), float32Bytes(b)) {
		t.Fatal("同输入两次 Run 字节不一致（会话确定性破损）")
	}
}

func float32Bytes(v []float32) []byte {
	out := make([]byte, len(v)*4)
	for i, x := range v {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(x))
	}
	return out
}

// TestMeloORTShapeContract 形状契约防御：错误形状在 Run 前被拒（不进 ORT）。
func TestMeloORTShapeContract(t *testing.T) {
	s := newTestSession(t)
	io := tts.MeloIO{
		Tokens: make([]int64, 5), Tones: make([]int64, 5), LangIDs: make([]int64, 5),
		Bert: make([]float32, 1024*5), JaBert: make([]float32, 768*5),
		SdpNoise: make([]float32, 2*5),
		ZNoise:   make([]float32, 192*5), // 缺 8 倍时间预留——导出契约外
		NoiseScale: 0.6, NoiseScaleW: 0.8, LengthScale: 1, SdpRatio: 0.2,
	}
	if _, err := s.Run(io); err == nil {
		t.Fatal("z_noise 缺 8T 预留竟被接受（形状契约失效）")
	}
	io.ZNoise = make([]float32, 192*8*5)
	io.SdpNoise = make([]float32, 2*4) // sdp 与 tokens 不等长
	if _, err := s.Run(io); err == nil {
		t.Fatal("sdp_noise 长度失配竟被接受（形状契约失效）")
	}
}

// TestMeloFrontendStructuralParity 前端结构对拍：Go 查表 g2p 输出 vs fixtures
// 里的 Python 前端张量。符号面须逐位一致（查表与 pypinyin/opencpop 同源）；
// 声调面如实计数分歧（一/不/三声 sandhi 未移植=ADR-0008 债务③，不在此断言）。
func TestMeloFrontendStructuralParity(t *testing.T) {
	symbols := tts.SymbolsForDump()
	ph := tts.NewChinesePhonemizer()
	for _, f := range loadFixtures(t) {
		got, err := ph.Phonemize(f.Text)
		if err != nil {
			t.Fatalf("Phonemize(%q): %v", f.Text, err)
		}
		if len(got.Tokens) != len(f.Tokens) {
			t.Errorf("%q: token 数 %d ≠ Python %d", f.Text, len(got.Tokens), len(f.Tokens))
			continue
		}
		symAgree, toneAgree := 0, 0
		diverge := []string{}
		for i := range f.Tokens {
			gs, rs := symbols[got.Tokens[i]], symbols[f.Tokens[i]]
			if gs == rs {
				symAgree++
				if got.Tones[i] == f.Tones[i] {
					toneAgree++
				} else if len(diverge) < 4 {
					diverge = append(diverge, fmt.Sprintf("%d:%s tone%d≠%d", i, gs, got.Tones[i], f.Tones[i]))
				}
			}
		}
		n := float64(len(f.Tokens))
		t.Logf("%q: 符号一致率 %.3f 声调一致率 %.3f（分歧例 %v）",
			f.Text, float64(symAgree)/n, float64(toneAgree)/n, diverge)
		if symAgree != len(f.Tokens) {
			t.Errorf("%q: 符号面有分歧（%d/%d）——查表与上游 symbols 失配", f.Text, symAgree, len(f.Tokens))
		}
	}
}

// ---- Go 全链闭环：查表 g2p → 确定性噪声 → ORT 合成 → PCM → wav ----

// pcmDecode 流字节还原 float32 波形（pcmChunks 的逆，仅测试用）。
func pcmDecode(chunks [][]byte) []float32 {
	var raw []byte
	for _, c := range chunks {
		raw = append(raw, c...)
	}
	out := make([]float32, len(raw)/2)
	for i := range out {
		s := int16(binary.LittleEndian.Uint16(raw[i*2:]))
		out[i] = float32(s) / 32767
	}
	return out
}

// writeWav PCM s16le 单声道 wav（44 字节标准头；返回写入字节数）。
func writeWav(path string, samples []float32, sr int) (int, error) {
	data := make([]byte, 44+len(samples)*2)
	copy(data[0:], "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(36+len(samples)*2))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1) // PCM
	binary.LittleEndian.PutUint16(data[22:], 1) // mono
	binary.LittleEndian.PutUint32(data[24:], uint32(sr))
	binary.LittleEndian.PutUint32(data[28:], uint32(sr*2))
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], uint32(len(samples)*2))
	for i, v := range samples {
		if v > 1 {
			v = 1
		} else if v < -1 {
			v = -1
		}
		binary.LittleEndian.PutUint16(data[44+i*2:], uint16(int16(v*32767)))
	}
	return len(data), os.WriteFile(path, data, 0o644)
}

// TestMeloFullChainClosedLoop Go 全链闭环：MeloSynthesizer(ChinesePhonemizer,
// 真 ORT 会话) → 流 → wav。断言：可写文件、时长随句长单调合理、峰值非零、
// 同文本两次合成字节一致（P1 全链口径）。样本 wav 经 T13_SAMPLE_DIR 留档。
func TestMeloFullChainClosedLoop(t *testing.T) {
	sess := newTestSession(t)
	synth, err := tts.NewMeloSynthesizer(tts.NewChinesePhonemizer(), sess, tts.MeloConfig{})
	if err != nil {
		t.Fatalf("NewMeloSynthesizer: %v", err)
	}
	sampleDir := os.Getenv("T13_SAMPLE_DIR")

	texts := []string{"你好呀。", "今天天气真好，我们一起去公园玩吧。"}
	prevDur := -1.0
	for i, text := range texts {
		a, err := synth.Synthesize(tts.Request{Text: text, Voice: "ZH", Tier: 2})
		if err != nil {
			t.Fatalf("Synthesize(%q): %v", text, err)
		}
		b, err := synth.Synthesize(tts.Request{Text: text, Voice: "ZH", Tier: 2})
		if err != nil {
			t.Fatalf("Synthesize#2(%q): %v", text, err)
		}
		var ba, bb [][]byte
		for {
			c, e := a.Next()
			ba = append(ba, c.Data)
			if e != nil {
				break
			}
		}
		for {
			c, e := b.Next()
			bb = append(bb, c.Data)
			if e != nil {
				break
			}
		}
		pcmA, pcmB := flatten(ba), flatten(bb)
		if !bytes.Equal(pcmA, pcmB) {
			t.Errorf("%q: 全链两次合成字节不一致（P1 破损）", text)
		}
		audio := pcmDecode(ba)
		dur := float64(len(audio)) / float64(synth.SampleRate())
		peak := 0.0
		for _, v := range audio {
			if p := math.Abs(float64(v)); p > peak {
				peak = p
			}
		}
		t.Logf("%q: samples=%d dur=%.2fs peak=%.4f", text, len(audio), dur, peak)
		if peak < 0.01 {
			t.Errorf("%q: 峰值 %.4f 近零（非可听波形）", text, peak)
		}
		if dur < 0.3 || dur > 30 {
			t.Errorf("%q: 时长 %.2fs 不合理", text, dur)
		}
		if prevDur >= 0 && dur <= prevDur {
			t.Errorf("时长未随句长单调增长：%.2f → %.2f", prevDur, dur)
		}
		prevDur = dur

		wavPath := filepath.Join(t.TempDir(), fmt.Sprintf("goort-%d.wav", i))
		if n, err := writeWav(wavPath, audio, synth.SampleRate()); err != nil {
			t.Errorf("写 wav %s: %v", wavPath, err)
		} else if n <= 44 {
			t.Errorf("wav 空文件：%s", wavPath)
		}
		if sampleDir != "" {
			p := filepath.Join(sampleDir, fmt.Sprintf("melotts-zh-goort-%d.wav", i))
			if _, err := writeWav(p, audio, synth.SampleRate()); err != nil {
				t.Errorf("留档 wav %s: %v", p, err)
			}
		}
	}
}

func flatten(cs [][]byte) []byte {
	var out []byte
	for _, c := range cs {
		out = append(out, c...)
	}
	return out
}

// ---- RTF 实测（端侧口径；T13_RTF_OUT=<path> 门控，产物续 melotts-rtf.json） ----

// pct 最近邻百分位（与 export_melotts_onnx.py pct 同口径）。
func pct(sorted []float64, p float64) float64 {
	idx := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx > len(sorted)-1 {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// rtfRow RTF 实测单行（JSON 报告行）。
type rtfRow struct {
	Tier       string  `json:"tier"`
	Text       string  `json:"text"`
	Phonemes   int     `json:"phonemes"`
	AudioS     float64 `json:"audio_s"`
	FrontendMs float64 `json:"frontend_ms"`
	InferMs    float64 `json:"infer_ms"`
	RTF        float64 `json:"rtf"`
}

// TestMeloORTRTFBenchmark 三档句长各 10 次 RTF 实测（P50/P95），JSON 续写
// reports/eval/T13/melotts-rtf.json 口径（Go 全链：phonemize+noise+ORT）。
// 仅在设 T13_RTF_OUT 时运行（重活；CI 与日常测试不跑）。
func TestMeloORTRTFBenchmark(t *testing.T) {
	out := os.Getenv("T13_RTF_OUT")
	if out == "" {
		t.Skip("RTF 实测为重活：设 T13_RTF_OUT=<path> 启用（产物续 reports/eval/T13/ 口径）")
	}
	sess := newTestSession(t)
	ph := tts.NewChinesePhonemizer()
	synth, err := tts.NewMeloSynthesizer(ph, sess, tts.MeloConfig{})
	if err != nil {
		t.Fatalf("NewMeloSynthesizer: %v", err)
	}
	tiers := []struct {
		name  string
		texts []string
	}{
		{"short", []string{"你好呀。", "晚安哦。", "好耶！"}},
		{"medium", []string{"今天天气真好，我们一起去公园玩吧。", "你想听故事吗？我来给你讲一个小兔子的故事。"}},
		{"long", []string{
			"从前有一只小兔子，它住在森林里，最爱吃胡萝卜，每天都蹦蹦跳跳地去找小伙伴玩。",
			"今天我们一起搭了一个很高很高的积木城堡，还给它画了漂亮的旗子，明天再继续好不好？",
		}},
	}
	const runs = 10
	var rows []rtfRow
	// 计时口径与 export_melotts_onnx.py rtf 对齐：frontend=前端单独计时；
	// infer=会话 Run（噪声张量的生成/分配双方都排除在 infer 之外）；RTF=
	// infer / 音频时长。链路组装（Synthesize 整链）的闭环正确性归上测。
	timing := func(tier, text string) (r rtfRow, err error) {
		r.Tier, r.Text = tier, text
		t0 := now()
		p, e := ph.Phonemize(text)
		if e != nil {
			return r, e
		}
		r.FrontendMs = sinceMs(t0)
		r.Phonemes = len(p.Tokens)
		t := len(p.Tokens)
		io := tts.MeloIO{
			Tokens: p.Tokens, Tones: p.Tones, LangIDs: p.LangIDs,
			Bert:       make([]float32, 1024*t),
			JaBert:     make([]float32, 768*t),
			SdpNoise:   make([]float32, 2*t),
			ZNoise:     make([]float32, 192*8*t),
			NoiseScale: 0.6, NoiseScaleW: 0.8, LengthScale: 1, SdpRatio: 0.2,
		}
		t1 := now()
		audio, e := sess.Run(io)
		if e != nil {
			return r, e
		}
		r.InferMs = sinceMs(t1)
		r.AudioS = float64(len(audio)) / float64(synth.SampleRate())
		r.RTF = r.InferMs / 1000 / r.AudioS
		return r, nil
	}
	for _, tier := range tiers {
		if _, err := timing(tier.name, tier.texts[0]); err != nil { // 预热（会话/页缓存）
			t.Fatalf("预热: %v", err)
		}
		for k := 0; k < runs; k++ {
			r, err := timing(tier.name, tier.texts[k%len(tier.texts)])
			if err != nil {
				t.Fatalf("%s run%d: %v", tier.name, k, err)
			}
			rows = append(rows, r)
			t.Logf("[%s %02d] phonemes=%d audio=%.2fs infer=%.0fms rtf=%.4f",
				tier.name, k, r.Phonemes, r.AudioS, r.InferMs, r.RTF)
		}
	}
	stats := func(rs []rtfRow) (rtfs, infers []float64) {
		for _, r := range rs {
			rtfs = append(rtfs, r.RTF)
			infers = append(infers, r.InferMs)
		}
		sort.Float64s(rtfs)
		sort.Float64s(infers)
		return rtfs, infers
	}
	allR, allI := stats(rows)
	mean := func(v []float64) float64 {
		s := 0.0
		for _, x := range v {
			s += x
		}
		return s / float64(len(v))
	}
	tierStats := map[string]map[string]float64{}
	for _, tier := range tiers {
		var rs []rtfRow
		for _, r := range rows {
			if r.Tier == tier.name {
				rs = append(rs, r)
			}
		}
		tr, ti := stats(rs)
		tierStats[tier.name] = map[string]float64{
			"n": float64(len(rs)), "rtf_mean": mean(tr),
			"rtf_p50": pct(tr, 50), "rtf_p95": pct(tr, 95),
			"infer_ms_p50": pct(ti, 50), "infer_ms_p95": pct(ti, 95),
		}
	}
	report := map[string]any{
		"onnx":          DefaultModelPath(),
		"chain":         "go-full: 查表g2p+确定性噪声+ORT(yalue/onnxruntime_go)会话",
		"device":        "CPU",
		"threads":       defaultIntraOpThreads,
		"sampling_rate": synth.SampleRate(),
		"n":             len(rows),
		"rtf_mean":      r4(mean(allR)), "rtf_p50": r4(pct(allR, 50)),
		"rtf_p95": r4(pct(allR, 95)), "rtf_max": r4(allR[len(allR)-1]),
		"infer_ms_p50":  r1(pct(allI, 50)),
		"infer_ms_p95":  r1(pct(allI, 95)),
		"infer_ms_max":  r1(allI[len(allI)-1]),
		"frontend_ms_mean": r1(mean(mapRows(rows, func(r rtfRow) float64 { return r.FrontendMs }))),
		"tiers":         tierStats,
		"rows":          rows,
		"first_chunk_note": "非流式整段出——first_packet≈infer_ms（流式导出仍为 ADR-0008 债务）",
	}
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("报告序列化: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatalf("报告目录: %v", err)
	}
	if err := os.WriteFile(out, b, 0o644); err != nil {
		t.Fatalf("写报告 %s: %v", out, err)
	}
	t.Logf("RTF P50=%.4f P95=%.4f（n=%d）→ %s", pct(allR, 50), pct(allR, 95), len(rows), out)
}

func mapRows(rs []rtfRow, f func(rtfRow) float64) []float64 {
	out := make([]float64, 0, len(rs))
	for _, r := range rs {
		out = append(out, f(r))
	}
	return out
}

func r4(v float64) float64 { return math.Round(v*10000) / 10000 }
func r1(v float64) float64 { return math.Round(v*10) / 10 }

func now() int64 { return int64(time.Now().UnixNano()) }

func sinceMs(start int64) float64 { return float64(now()-start) / 1e6 }
