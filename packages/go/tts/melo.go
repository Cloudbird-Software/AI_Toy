// melo —— MeloTTS-Chinese（MIT）端侧离线合成路径（issue #132 / ADR-0008）。
//
// 官方音色 ZH（spk id 1）整段合成 → PCM s16le 流式分块。红线合规面：只用
// 官方音色，儿童音色偏好经语速参数（voice ID @rate=）过渡，不克隆任何真实
// 儿童声音（AGENTS.md G0 红线）。
//
// 依赖纪律（镜像 kws.Inferencer 模式，ADR-0004）：ONNX 会话与文本前端一律
// 接口化注入——MeloSession/MeloPhonemizer。包本体零外部依赖；onnxruntime
// 绑定由运行时装配层注入（M2 待办，见 ADR-0008 债务表）。
//
// 确定性（P1 属性实现面）：采样噪声由 (seed, text, voice) 确定性派生
// （splitmix64 + Box-Muller）——同文本+同音色+同种子两次合成音频字节一致。
package tts

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
)

// 导出图的 BERT 特征通道数与噪声形状（tools/tts/export_melotts_onnx.py 一致）。
const (
	meloBertChannels   = 1024 // ZH_MIX_EN 路径恒零槽（上游 api.py 路由）
	meloJaBertChannels = 768  // mBERT 特征槽
	meloSdpNoiseCh     = 2    // SDP reverse 噪声通道
	meloZChannels      = 192  // inter_channels（z 噪声通道）
	meloZTimeHeadroom  = 8    // z 噪声时间维预留倍数（mel 长度数据相关，图内切片）
	meloSpeakerZH      = 1    // 官方音色 ZH（config.json spk2id）
)

// 上游 api.py tts_to_file 默认采样参数。
const (
	meloDefaultNoiseScale  = 0.6
	meloDefaultNoiseScaleW = 0.8
	meloDefaultSdpRatio    = 0.2
	meloMinRate            = 0.5
	meloMaxRate            = 2.0
)

// MeloPhonemes 前端产物（与 ONNX 图输入对齐）。
type MeloPhonemes struct {
	Tokens  []int64   // 音素 token id（含 add_blank 间隔，须含首尾 pad）
	Tones   []int64   // 声调 id（与 Tokens 同长）
	LangIDs []int64   // 语言 id（与 Tokens 同长；ZH_MIX_EN=3）
	JaBert  []float32 // [768×T] 韵律特征；nil→全零（韵律降质可听，M2 债务）
}

// Phonemizer 文本→音素前端。生产注入 ChinesePhonemizer（melophone.go）。
type Phonemizer interface {
	Phonemize(text string) (MeloPhonemes, error)
}

// MeloIO ONNX 会话输入/输出张量包（字段与导出图输入一一对应）。
type MeloIO struct {
	Tokens, Tones, LangIDs []int64
	Bert                   []float32 // [1024×T]（ZH_MIX_EN 恒零）
	JaBert                 []float32 // [768×T]
	SdpNoise               []float32 // [2×T]
	ZNoise                 []float32 // [192×8T]（时间维预留，图内切到 mel 长度）
	// NoiseScale 扩散噪声尺度 / NoiseScaleW SDP 噪声尺度 / LengthScale
	// 时长缩放（=1/语速）/ SdpRatio SDP 混合比（上游默认 0.6/0.8/1/0.2）。
	NoiseScale, NoiseScaleW, LengthScale, SdpRatio float32
}

// MeloSession ONNX 会话执行器（onnxruntime 绑定由装配层注入）。实现契约：
// 同输入同输出（P1）；audio 为 [-1,1] float32 单声道波形。
type MeloSession interface {
	Run(io MeloIO) (audio []float32, err error)
}

// MeloConfig 端侧引擎配置。零值字段取 NewMeloSynthesizer 默认。
type MeloConfig struct {
	SampleRate   int    // 导出图 44100
	Seed         uint64 // 确定性噪声种子基
	MaxTokens    int    // 单段 token 上限（超长防御；默认 4096）
	ChunkSamples int    // 流分块样本数（默认 4096 ≈ 93ms @44.1k）
}

// MeloSynthesizer 端侧合成引擎（Synthesizer 接口的 L2/L3 通道实现）。
// 整段合成后分块重放（导出图非流式——首包≈全段推理时长，见 ADR-0008 债务）。
type MeloSynthesizer struct {
	ph           Phonemizer
	sess         MeloSession
	sampleRate   int
	seed         uint64
	maxTokens    int
	chunkSamples int
}

// NewMeloSynthesizer 构造端侧引擎：ph/sess 非 nil 必填（零依赖纪律：缺注入
// 即拒绝构造，不静默降级到桩）。
func NewMeloSynthesizer(ph Phonemizer, sess MeloSession, cfg MeloConfig) (*MeloSynthesizer, error) {
	if ph == nil || sess == nil {
		return nil, errors.New("tts: MeloSynthesizer 须注入 Phonemizer 与 MeloSession（接口化，不内置桩）")
	}
	if cfg.SampleRate == 0 {
		cfg.SampleRate = 44100
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = 4096
	}
	if cfg.ChunkSamples == 0 {
		cfg.ChunkSamples = 4096
	}
	return &MeloSynthesizer{
		ph: ph, sess: sess,
		sampleRate: cfg.SampleRate, seed: cfg.Seed,
		maxTokens: cfg.MaxTokens, chunkSamples: cfg.ChunkSamples,
	}, nil
}

// ErrTextTooLong 单段 token 超限（超长注入防御；分句归上层话术面）。
var ErrTextTooLong = errors.New("tts: melo tokens exceed per-request cap")

// ErrVoiceUnsupported 音色不合规/参数未知（官方音色白名单面）。
var ErrVoiceUnsupported = errors.New("tts: voice not supported (official voices only)")

// Synthesize 实现 Synthesizer：前端 → 确定性噪声 → 会话 → PCM 分块流。
func (m *MeloSynthesizer) Synthesize(req Request) (AudioStream, error) {
	if req.Text == "" {
		return nil, ErrEmptyText
	}
	rate, err := parseMeloVoice(req.Voice)
	if err != nil {
		return nil, err
	}
	ph, err := m.ph.Phonemize(req.Text)
	if err != nil {
		return nil, fmt.Errorf("tts: phonemize: %w", err)
	}
	t := len(ph.Tokens)
	if t == 0 {
		return nil, ErrEmptyText
	}
	if t > m.maxTokens {
		return nil, fmt.Errorf("%w: %d > %d", ErrTextTooLong, t, m.maxTokens)
	}
	if len(ph.Tones) != t || len(ph.LangIDs) != t {
		return nil, fmt.Errorf("tts: phonemize 前端序列不等长（tokens=%d tones=%d langs=%d）",
			t, len(ph.Tones), len(ph.LangIDs))
	}
	in := MeloIO{
		Tokens: ph.Tokens, Tones: ph.Tones, LangIDs: ph.LangIDs,
		Bert:        make([]float32, meloBertChannels*t), // ZH_MIX_EN 恒零槽
		JaBert:      jaBertOrZeros(ph.JaBert, t),
		SdpNoise:    make([]float32, meloSdpNoiseCh*t),
		ZNoise:      make([]float32, meloZChannels*t),
		NoiseScale:  meloDefaultNoiseScale,
		NoiseScaleW: meloDefaultNoiseScaleW,
		LengthScale: float32(1 / rate),
		SdpRatio:    meloDefaultSdpRatio,
	}
	meloNoise(m.seed, req.Text, req.Voice, t, in.SdpNoise, in.ZNoise)
	audio, err := m.sess.Run(in)
	if err != nil {
		return nil, fmt.Errorf("tts: melo session: %w", err)
	}
	chunks := pcmChunks(audio, m.chunkSamples)
	return &chunkStream{chunks: chunks}, nil
}

// SampleRate 输出采样率（播放面/预算面读取）。
func (m *MeloSynthesizer) SampleRate() int { return m.sampleRate }

// parseMeloVoice 音色 ID 语法（ADR-0008）：""|"ZH"=官方音色；可带参数后缀
// "ZH@rate=1.1"（语速 0.5..2.0）。pitch 参数显式拒绝（端侧 DSP 面未落地，
// 静默忽略=不诚实）。其余音色 ID 一律拒绝——不提供克隆面。
func parseMeloVoice(v string) (rate float64, err error) {
	rate = 1.0
	base := v
	if i := strings.IndexByte(v, '@'); i >= 0 {
		base, v = v[:i], v[i+1:]
		for _, kv := range strings.Split(v, "@") {
			if kv == "" {
				continue
			}
			k, val, ok := strings.Cut(kv, "=")
			if !ok {
				return 0, fmt.Errorf("%w: 参数须为 key=value 形式：%q", ErrVoiceUnsupported, kv)
			}
			switch k {
			case "rate":
				f, e := strconv.ParseFloat(val, 64)
				if e != nil || f < meloMinRate || f > meloMaxRate {
					return 0, fmt.Errorf("%w: rate 须 ∈ [%g, %g]，got %q",
						ErrVoiceUnsupported, meloMinRate, meloMaxRate, val)
				}
				rate = f
			case "pitch":
				return 0, fmt.Errorf("%w: pitch 参数化待端侧 DSP 面（ADR-0008 债务，不静默忽略）", ErrVoiceUnsupported)
			default:
				return 0, fmt.Errorf("%w: 未知音色参数 %q", ErrVoiceUnsupported, k)
			}
		}
	}
	if base != "" && base != "ZH" {
		return 0, fmt.Errorf("%w: %q（仅官方音色 ZH）", ErrVoiceUnsupported, base)
	}
	return rate, nil
}

// jaBertOrZeros 前端未供特征→全零（形状诚实：图输入不可缺位）。
func jaBertOrZeros(jb []float32, t int) []float32 {
	if len(jb) == meloJaBertChannels*t {
		return jb
	}
	return make([]float32, meloJaBertChannels*t)
}

// meloNoise 确定性噪声生成：h=fnv64(text∥voice)；splitmix64(seed^h) 驱动
// Box-Muller 正态对。同 (seed,text,voice) 恒同序列（P1），跨文本发散（非恒定
// 输出——P1 伴侣断言）。
func meloNoise(seed uint64, text, voice string, t int, sdp, z []float32) {
	h := fnv.New64a()
	h.Write([]byte(text))
	h.Write([]byte{0})
	h.Write([]byte(voice))
	g := gaussRNG{s: splitmix(seed ^ h.Sum64())}
	for i := range sdp {
		sdp[i] = float32(g.next())
	}
	for i := range z {
		z[i] = float32(g.next())
	}
}

func splitmix(x uint64) uint64 {
	// 种子预混（splitmix64 一轮），避免低熵种子（0/1）退化为线性序列。
	x += 0x9E3779B97F4A7C15
	x = (x ^ (x >> 30)) * 0xBF58476D1CE4E5B9
	x = (x ^ (x >> 27)) * 0x94D049BB133111EB
	return x ^ (x >> 31)
}

// gaussRNG splitmix64 + Box-Muller（确定性正态源；math.Log/Sqrt 平台一致——
// Go 数字库对基本函数全平台同实现）。
type gaussRNG struct {
	s     uint64
	cache float64
	has   bool
}

func (g *gaussRNG) u64() uint64 {
	g.s += 0x9E3779B97F4A7C15
	x := g.s
	x = (x ^ (x >> 30)) * 0xBF58476D1CE4E5B9
	x = (x ^ (x >> 27)) * 0x94D049BB133111EB
	return x ^ (x >> 31)
}

func (g *gaussRNG) u01() float64 {
	return float64(g.u64()>>11) * (1.0 / 9007199254740992.0) // [0,1)
}

func (g *gaussRNG) next() float64 {
	if g.has {
		g.has = false
		return g.cache
	}
	u1 := g.u01()
	for u1 <= 0 { // log(0) 防线（概率 2^-53 级，仍防御）
		u1 = g.u01()
	}
	u2 := g.u01()
	r := math.Sqrt(-2 * math.Log(u1))
	g.cache = r * math.Sin(2*math.Pi*u2)
	g.has = true
	return r * math.Cos(2*math.Pi*u2)
}

// pcmChunks float32 波形 → PCM s16le 小端字节，按 chunkSamples 分块（Seq 从 1
// 起，末块 Final；静音样本天然为零字节非 nil——chunk 语义与 Router 一致）。
func pcmChunks(audio []float32, n int) []Chunk {
	if len(audio) == 0 {
		return []Chunk{{Seq: 1, Final: true}} // 空波形：单空块收口（流仍终止）
	}
	var out []Chunk
	buf := make([]byte, 0, n*2)
	flush := func(final bool) {
		c := Chunk{Data: append([]byte(nil), buf...), Final: final}
		out = append(out, c)
		buf = buf[:0]
	}
	for i, v := range audio {
		if v > 1 {
			v = 1
		} else if v < -1 {
			v = -1
		}
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], uint16(int16(v*32767)))
		buf = append(buf, b[0], b[1])
		if len(buf) >= n*2 {
			flush(false)
		}
		_ = i
	}
	if len(buf) > 0 {
		flush(true)
	} else if len(out) > 0 {
		out[len(out)-1].Final = true
	}
	for i := range out {
		out[i].Seq = i + 1
	}
	return out
}

// chunkStream 预合成块重放流（终止态固化+Cancel 幂等——与 Router 流契约一致）。
type chunkStream struct {
	mu       sync.Mutex
	chunks   []Chunk
	i        int
	canceled bool
}

func (s *chunkStream) Next() (Chunk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.canceled {
		return Chunk{}, ErrCanceled
	}
	if s.i >= len(s.chunks) {
		return Chunk{}, io.EOF
	}
	c := s.chunks[s.i]
	s.i++
	return c, nil
}

func (s *chunkStream) Cancel() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.canceled = true
	return nil
}
