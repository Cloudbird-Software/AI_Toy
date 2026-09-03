// melo 测试：端侧引擎接入面（会话/前端注入、确定性、voice 参数化白名单、
// 超长防御）与 Router 装配集成。ONNX 真模型数值面归 reports/eval/T13/ 的
// Python 对拍（本包零外部依赖纪律——不引 onnxruntime）。
package tts

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

// ---- 测试注入桩 ----

// tablePhonemizer 确定性测试前端：每 rune 派生 token（控制字符剥离），序列
// 带 pad 交替——与 ChinesePhonemizer 同为 pad 交替形态但无语义（属性测试用）。
type tablePhonemizer struct{ scale int }

func (p tablePhonemizer) Phonemize(text string) (MeloPhonemes, error) {
	t := stripControls(text)
	if t == "" {
		return MeloPhonemes{}, nil
	}
	n := len(t) * p.scale
	tk := make([]int64, 0, 2*n+1)
	tn := make([]int64, 0, 2*n+1)
	lg := make([]int64, 0, 2*n+1)
	tk, tn, lg = append(tk, meloPadID), append(tn, 0), append(lg, meloLangIDZHMixEn)
	for i, r := range t {
		id := int64(r%97+1) + int64(i%3)*97
		tk = append(tk, id%1000, meloPadID)
		tn = append(tn, int64(r%5+1), 0)
		lg = append(lg, meloLangIDZHMixEn, meloLangIDZHMixEn)
	}
	return MeloPhonemes{Tokens: tk, Tones: tn, LangIDs: lg}, nil
}

// seqSession 确定性会话桩：波形=fnv(全部输入张量+标量) 派生 LCG——输入任一
// 字段变化即波形变化（Session 只透传图输入的语义面）。
type seqSession struct {
	mu    sync.Mutex
	calls int
	fail  bool
}

func (s *seqSession) Run(in MeloIO) ([]float32, error) {
	s.mu.Lock()
	s.calls++
	fail := s.fail
	s.mu.Unlock()
	if fail {
		return nil, errors.New("seqSession: injected failure")
	}
	n := 0
	for _, v := range in.Tokens {
		n += int(v)
	}
	n = n%4096 + int(in.LengthScale*1000) + int(in.NoiseScale*100) + int(in.SdpRatio*100)
	audio := make([]float32, n+1024)
	state := uint64(len(in.Tokens)) ^ uint64(n)
	for i := range audio {
		state = state*6364136223846793005 + 1442695040888963407
		audio[i] = float32(int32(state>>33)%2000) / 20000 // [-0.1,0.1)
	}
	return audio, nil
}

func (s *seqSession) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// newTestMelo 默认装配（tablePhonemizer × seqSession）。
func newTestMelo(seed uint64) *MeloSynthesizer {
	m, err := NewMeloSynthesizer(tablePhonemizer{scale: 2}, &seqSession{},
		MeloConfig{Seed: seed, ChunkSamples: 64})
	if err != nil {
		panic(err)
	}
	return m
}

// drainBytesAll 排干流收集全部字节（含错误返回）。
func drainBytesAll(s AudioStream) []byte {
	var out []byte
	for {
		c, err := s.Next()
		out = append(out, c.Data...)
		if err != nil {
			return out
		}
	}
}

// ---- Synthesizer 行为 ----

func TestMeloDeterministicSameInput(t *testing.T) {
	m := newTestMelo(42)
	a, err := m.Synthesize(Request{Text: "你好小云", Voice: "ZH", Tier: 2, TurnID: "t1"})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	b, err := m.Synthesize(Request{Text: "你好小云", Voice: "ZH", Tier: 2, TurnID: "t2"})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if !bytes.Equal(drainBytesAll(a), drainBytesAll(b)) {
		t.Fatal("同文本+同音色+同种子两次合成字节不一致（P1 破损）")
	}
}

func TestMeloDivergentAcrossTextAndVoice(t *testing.T) {
	m := newTestMelo(42)
	h := func(text, voice string) []byte {
		s, err := m.Synthesize(Request{Text: text, Voice: voice, Tier: 2})
		if err != nil {
			t.Fatalf("Synthesize(%q,%q): %v", text, voice, err)
		}
		return drainBytesAll(s)
	}
	if bytes.Equal(h("你好", "ZH"), h("再见", "ZH")) {
		t.Fatal("不同文本产出相同波形（噪声未随文本发散）")
	}
	if bytes.Equal(h("你好", "ZH"), h("你好", "ZH@rate=1.5")) {
		t.Fatal("不同语速产出相同波形（length_scale 未进会话输入）")
	}
}

func TestMeloStreamChunking(t *testing.T) {
	m := newTestMelo(7)
	st, err := m.Synthesize(Request{Text: "流式分块", Tier: 2})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	seq := 0
	finals := 0
	for {
		c, err := st.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		seq++
		if c.Seq != seq {
			t.Fatalf("Seq 不单调：got %d want %d", c.Seq, seq)
		}
		if len(c.Data)%2 != 0 {
			t.Fatalf("PCM s16le 块字节数须为偶数：got %d", len(c.Data))
		}
		if c.Final {
			finals++
		}
	}
	if seq == 0 {
		t.Fatal("零块（波形丢失）")
	}
	if finals != 1 {
		t.Fatalf("Final 块数=%d，须恰 1（末块）", finals)
	}
	// 终止态固化：EOF 后续读恒 EOF
	if _, err := st.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("EOF 后续读=%v，须固化 EOF", err)
	}
}

func TestMeloCancelIdempotent(t *testing.T) {
	m := newTestMelo(7)
	st, err := m.Synthesize(Request{Text: "取消语义", Tier: 2})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if _, err := st.Next(); err != nil {
		t.Fatalf("首块: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := st.Cancel(); err != nil {
			t.Fatalf("Cancel #%d: %v", i, err)
		}
	}
	if _, err := st.Next(); !errors.Is(err, ErrCanceled) {
		t.Fatalf("Cancel 后 Next=%v，须 ErrCanceled", err)
	}
}

func TestMeloGuards(t *testing.T) {
	m := newTestMelo(1)
	if _, err := m.Synthesize(Request{Text: "", Tier: 2}); !errors.Is(err, ErrEmptyText) {
		t.Fatalf("空文本=%v，须 ErrEmptyText", err)
	}
	if _, err := m.Synthesize(Request{Text: "正常", Voice: "KID_CLONE_01", Tier: 2}); !errors.Is(err, ErrVoiceUnsupported) {
		t.Fatalf("非官方音色=%v，须 ErrVoiceUnsupported（克隆面拒绝）", err)
	}
	if _, err := m.Synthesize(Request{Text: "正常", Voice: "ZH@pitch=0.5", Tier: 2}); !errors.Is(err, ErrVoiceUnsupported) {
		t.Fatalf("pitch 参数=%v，须显式拒绝（DSP 面未落地）", err)
	}
	if _, err := m.Synthesize(Request{Text: "正常", Voice: "ZH@rate=9", Tier: 2}); !errors.Is(err, ErrVoiceUnsupported) {
		t.Fatalf("rate 越界=%v，须拒绝", err)
	}
	if _, err := m.Synthesize(Request{Text: "正常", Voice: "ZH@rate=1.5", Tier: 2}); err != nil {
		t.Fatalf("rate 合法入参=%v，须放行", err)
	}
	// 控制字符剥离后为空 → 空文本
	if _, err := m.Synthesize(Request{Text: "​\x00", Tier: 2}); !errors.Is(err, ErrEmptyText) {
		t.Fatalf("纯控制字符=%v，须 ErrEmptyText", err)
	}
}

func TestMeloTextTooLongCap(t *testing.T) {
	m, err := NewMeloSynthesizer(tablePhonemizer{scale: 1}, &seqSession{},
		MeloConfig{Seed: 1, MaxTokens: 16, ChunkSamples: 64})
	if err != nil {
		t.Fatalf("NewMeloSynthesizer: %v", err)
	}
	if _, err := m.Synthesize(Request{Text: strings.Repeat("长", 64), Tier: 2}); !errors.Is(err, ErrTextTooLong) {
		t.Fatalf("超长=%v，须 ErrTextTooLong", err)
	}
}

func TestMeloSessionErrorPropagates(t *testing.T) {
	sess := &seqSession{fail: true}
	m, err := NewMeloSynthesizer(tablePhonemizer{scale: 1}, sess,
		MeloConfig{Seed: 1, ChunkSamples: 64})
	if err != nil {
		t.Fatalf("NewMeloSynthesizer: %v", err)
	}
	if _, err := m.Synthesize(Request{Text: "会话故障", Tier: 2}); err == nil {
		t.Fatal("会话 err 须上抛（Router 降级表依赖）")
	}
}

func TestMeloRequiresInjection(t *testing.T) {
	if _, err := NewMeloSynthesizer(nil, &seqSession{}, MeloConfig{}); err == nil {
		t.Fatal("缺 Phonemizer 须拒绝构造（不内置桩）")
	}
	if _, err := NewMeloSynthesizer(tablePhonemizer{}, nil, MeloConfig{}); err == nil {
		t.Fatal("缺 MeloSession 须拒绝构造")
	}
}

// ---- ChinesePhonemizer（真表）----

func TestChinesePhonemizerShape(t *testing.T) {
	p := NewChinesePhonemizer()
	ph, err := p.Phonemize("你好呀。")
	if err != nil {
		t.Fatalf("Phonemize: %v", err)
	}
	// 上游结构（chinese_mix.g2p 首尾边界符 + commons.intersperse(0)）：
	// [pad,pad,pad, c1,pad, c2,pad, …, cn,pad,pad,pad]——pad 占位在首 3 位、
	// 尾 3 位与内容间偶数位。
	if len(ph.Tokens) < 12 {
		t.Fatalf("音素序列过短：%d（表查失灵？）", len(ph.Tokens))
	}
	if len(ph.Tokens) != len(ph.Tones) || len(ph.Tokens) != len(ph.LangIDs) {
		t.Fatalf("序列不等长：%d/%d/%d", len(ph.Tokens), len(ph.Tones), len(ph.LangIDs))
	}
	vocab := int64(len(meloSymbols))
	for i, id := range ph.Tokens {
		if id < 0 || id >= vocab {
			t.Fatalf("token %d 越界 %d（vocab=%d）", i, id, vocab)
		}
		if ph.Tones[i] < 0 || ph.Tones[i] > 5 {
			t.Fatalf("tone %d 越界：%d", i, ph.Tones[i])
		}
		if id != meloPadID && ph.LangIDs[i] != meloLangIDZHMixEn {
			t.Fatalf("非 pad %d 位 lang=%d，须 ZH_MIX_EN(%d)", i, ph.LangIDs[i], meloLangIDZHMixEn)
		}
		if ph.LangIDs[i] != 0 && ph.LangIDs[i] != meloLangIDZHMixEn {
			t.Fatalf("lang %d=%d 越域（0 或 ZH_MIX_EN）", i, ph.LangIDs[i])
		}
		inContent := i >= 3 && i <= len(ph.Tokens)-4
		if (!inContent || i%2 == 0) && id != meloPadID {
			t.Fatalf("pad 位含非 pad：%d 位=%d", i, id)
		}
	}
	// 首尾 pad（边界符+intersperse 语义）
	if ph.Tokens[0] != meloPadID || ph.Tokens[1] != meloPadID ||
		ph.Tokens[len(ph.Tokens)-1] != meloPadID || ph.Tokens[len(ph.Tokens)-2] != meloPadID {
		t.Fatal("首尾须 pad 包夹（含上游边界符）")
	}
}

func TestChinesePhonemizerKnownReading(t *testing.T) {
	p := NewChinesePhonemizer()
	ph, err := p.Phonemize("你好")
	if err != nil {
		t.Fatalf("Phonemize: %v", err)
	}
	// 你=ni3 → [n, i]，好=hao3 → [h, ao]，全部 tone3。上游结构：首 3 pad +
	// 音素/pad 交替 + 尾 3 pad（边界符+intersperse）。
	want := []int64{
		meloPadID, meloPadID, meloPadID,
		meloSymbolIDMust("n"), meloPadID,
		meloSymbolIDMust("i"), meloPadID,
		meloSymbolIDMust("h"), meloPadID,
		meloSymbolIDMust("ao"), meloPadID, meloPadID, meloPadID,
	}
	if len(ph.Tokens) != len(want) {
		t.Fatalf("token 序列=%v，want %v", ph.Tokens, want)
	}
	for i := range want {
		if ph.Tokens[i] != want[i] {
			t.Fatalf("token[%d]=%d want %d（全序列 %v）", i, ph.Tokens[i], want[i], ph.Tokens)
		}
	}
	for _, i := range []int{3, 5, 7, 9} {
		if ph.Tones[i] != 3 {
			t.Fatalf("音素 %d 声调=%d，须 3（三声本调）", i, ph.Tones[i])
		}
	}
}

func TestChinesePhonemizerFallbacks(t *testing.T) {
	p := NewChinesePhonemizer()
	cases := []struct {
		name, in string
		wantNil  bool
	}{
		{"空文本", "", true},
		{"纯空白（空格/Tab）", "  \t ", true},
		{"纯控制字符", "​\x00\x1b", true},
	}
	for _, c := range cases {
		ph, err := p.Phonemize(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if c.wantNil && len(ph.Tokens) != 0 {
			t.Fatalf("%s：tokens=%v，须空", c.name, ph.Tokens)
		}
	}
	// 数字逐位读 + 表外字符 UNK + 标点符号面
	ph, err := p.Phonemize("1x!")
	if err != nil {
		t.Fatalf("Phonemize: %v", err)
	}
	// 1→一(yi1: y i) x→UNK !→!；首 3 pad + 内容/pad 交替 + 尾 3 pad
	want := []int64{
		meloPadID, meloPadID, meloPadID,
		meloSymbolIDMust("y"), meloPadID,
		meloSymbolIDMust("i"), meloPadID,
		meloSymbolIDMust("UNK"), meloPadID,
		meloSymbolIDMust("!"), meloPadID, meloPadID, meloPadID,
	}
	if len(ph.Tokens) != len(want) {
		t.Fatalf("混合序列=%v，want %v", ph.Tokens, want)
	}
	for i := range want {
		if ph.Tokens[i] != want[i] {
			t.Fatalf("token[%d]=%d want %d", i, ph.Tokens[i], want[i])
		}
	}
	if ph.Tones[3] != 1 || ph.Tones[5] != 1 {
		t.Fatalf("一 声调须 1（yi1）：got %v", ph.Tones)
	}
}

// ---- Router 集成：两真引擎形状插入既有决策序 ----

func TestMeloRouterIntegrationEdgeAndDegrade(t *testing.T) {
	melo := newTestMelo(9)
	r, err := NewRouter(RouterConfig{
		PreSpeak:             allowAll,
		Edge:                 melo,
		FirstPacketTimeoutMs: 50,
		SilenceCapMs:         200,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	// L2：端侧直走
	st, err := r.Synthesize(Request{Text: "端侧直走", Tier: 2, TurnID: "e1"})
	if err != nil {
		t.Fatalf("L2 Synthesize: %v", err)
	}
	ms := r.Metrics()
	if len(ms) == 0 || ms[len(ms)-1].Channel != "edge" {
		t.Fatalf("L2 通道=%v，须 edge", ms[len(ms)-1].Channel)
	}
	if n := len(drainBytesAll(st)); n == 0 {
		t.Fatal("端侧流零字节")
	}
	// L3：仅缓存，未命中→ErrNoChannel
	if _, err := r.Synthesize(Request{Text: "无缓存", Tier: 3, TurnID: "l3"}); !errors.Is(err, ErrNoChannel) {
		t.Fatalf("L3 未命中=%v，须 ErrNoChannel", err)
	}
}

func TestRouterCloudIndexTTSWithEdgeDegrade(t *testing.T) {
	// 云=httptest IndexTTS 形状；端=MeloSynthesizer 桩会话。
	var mu sync.Mutex
	var gotVoice string
	srv := newIndexTTSServer(t, 8, 0, func(voice string) {
		mu.Lock()
		gotVoice = voice
		mu.Unlock()
	})
	cloud, err := NewIndexTTSClient(IndexTTSConfig{Endpoint: srv.URL, ChunkBytes: 32})
	if err != nil {
		t.Fatalf("NewIndexTTSClient: %v", err)
	}
	melo := newTestMelo(3)
	r, err := NewRouter(RouterConfig{
		PreSpeak: allowAll, Cloud: cloud, Edge: melo,
		FirstPacketTimeoutMs: 2000, SilenceCapMs: 500,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	st, err := r.Synthesize(Request{Text: "云端优先", Voice: "ZH", Tier: 0, TurnID: "c1"})
	if err != nil {
		t.Fatalf("L0 Synthesize: %v", err)
	}
	ms := r.Metrics()
	if ms[len(ms)-1].Channel != "cloud" {
		t.Fatalf("L0 通道=%s，须 cloud", ms[len(ms)-1].Channel)
	}
	data := drainBytesAll(st)
	if len(data) == 0 {
		t.Fatal("云流零字节")
	}
	mu.Lock()
	v := gotVoice
	mu.Unlock()
	if v != "ZH" {
		t.Fatalf("服务端收到 voice=%q，须默认官方音色 ZH", v)
	}
	_ = melo
}

func TestRouterIndexTTSTimeoutDegradesToEdge(t *testing.T) {
	// 云首包慢于 FirstPacketTimeoutMs → 静默占位 → Edge 补偿（降级表真身面）。
	srv := newIndexTTSServer(t, 8, 800, nil)
	cloud, err := NewIndexTTSClient(IndexTTSConfig{Endpoint: srv.URL})
	if err != nil {
		t.Fatalf("NewIndexTTSClient: %v", err)
	}
	melo := newTestMelo(5)
	r, err := NewRouter(RouterConfig{
		PreSpeak: allowAll, Cloud: cloud, Edge: melo,
		FirstPacketTimeoutMs: 50, SilenceCapMs: 2000,
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	st, err := r.Synthesize(Request{Text: "超时降级", Tier: 0, TurnID: "d1"})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	ms := r.Metrics()
	if ms[len(ms)-1].Channel != "degraded" {
		t.Fatalf("通道=%s，须 degraded", ms[len(ms)-1].Channel)
	}
	c0, err := st.Next()
	if err != nil {
		t.Fatalf("首块（静默占位）: %v", err)
	}
	if len(c0.Data) != 0 {
		t.Fatalf("静默占位须 0 字节，got %d", len(c0.Data))
	}
	if n := len(drainBytesAll(st)); n == 0 {
		t.Fatal("Edge 补偿流零字节")
	}
}
