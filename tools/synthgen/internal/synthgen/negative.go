package synthgen

// negative —— 负样本帧流生成器与批语义（m2-spec §2，IR #90）。
//
// 两个冻结生成器（源类型参数集随版本冻结——改参数=新 version 重新注册，注册表
// 可审计；NewNegStream 对注册版本≠实现版本报错，防「参数已改、版本未跟」的
// 静默漂移）：
//
//	gen-tneg@1.0.0   家庭音景：speech_like（远场语音状谐波列车）/ tv_noise（宽带
//	                 平稳噪声，声级相对近讲参考 SNR −20~0dB 扰动）/ burst（门响/
//	                 掉落/笑声状突发尖峰 ≥近讲声级）/ mixed（三源叠加）——源类型
//	                 4 类，等额轮转调度保证单源占比 0.25（≤0.30 门槛）。
//	gen-kwsadv@1.0.0 对抗同音节：他牌唤醒词音节模式（xiaoai/tianmao/xiaodu）+
//	                 本词音节高混淆近邻（nearconf，f0 贴本词占位 200Hz）。
//
// 声学口径（远场诚实性，同步写进批 manifest note）：所有语音状内容叠加宽带底
// 噪、底噪幅度 ≥ 语音幅度 2.6 倍——家庭远场人声埋在电视/环境底噪之下，负样本
// 须是真实家庭声学而非干净近讲录音；M3 真模型接入后同批重测。PCM 不随批落盘
// （对齐 .gitignore 大文件约定），由 (generator@version, seed, duration_ms)
// 确定性重建：种子 FNV-1a 64 对齐全仓约定，块/帧子种子经 splitmix64 终结器混合。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// 冻结生成器标识与版本（registry 侧同 id 注册；版本=参数集指纹）。
const (
	TNegGeneratorID   = "gen-tneg"
	KWSAdvGeneratorID = "gen-kwsadv"
	TNegImplVersion   = "1.0.0"
	KWSAdvImplVersion = "1.0.0"

	// NegPurpose 负样本批用途声明：全量入 eval 池、永不进训练管道。
	NegPurpose = "eval-only"

	// 帧参数：16kHz mono int16，帧长与 kws 门禁帧（gateFrameMs=30）一致。
	NegFrameMs    = 30
	NegSampleRate = 16000
	negFrameLen   = NegFrameMs * NegSampleRate / 1000 // 480 样本/帧

	// 近讲声级参考（kws 测试口径 p1Amp=4000 峰值 → RMS 2828）。
	negNearRMS = 2828.42712474619

	// 声明能量带（属性断言面：防静音流冒充负样本）。
	NegRMSFloor     = 120.0   // 任何帧 RMS 下限（含 burst 块间隙底噪）
	NegRMSCeiling   = 16000.0 // 帧 RMS 上限（防 int16 顶格失真）
	NegBurstPeakMin = 4000.0  // 突发事件峰值下限（≥近讲声级 p1Amp）
)

// tnegSourceTypes gen-tneg 源类型表（冻结，≥4 类）。
var tnegSourceTypes = [...]string{"speech_like", "tv_noise", "burst", "mixed"}

// kwsAdvSourceTypes gen-kwsadv 对抗音节类表（冻结：他牌 3 + 高混淆近邻 1）。
var kwsAdvSourceTypes = [...]string{"xiaoai", "tianmao", "xiaodu", "nearconf"}

// negUpstreamModel 溯源戳上游模型标识（负样本=声学参数集谱系，非 LLM）。
func negUpstreamModel(g Generator) string {
	if g.ID == KWSAdvGeneratorID {
		return "adv-syllable-1.0.0"
	}
	return "household-acoustic-1.0.0"
}

// ── 确定性随机基元 ─────────────────────────────────────────────────────

// xrand xorshift64*（0 种子退化由 xrandFrom 防护）。
type xrand uint64

// xrandFrom 构造确定性随机流（0 种子退化防护）。
func xrandFrom(seed uint64) *xrand {
	x := xrand(seed)
	if x == 0 {
		x = 0x9E3779B97F4A7C15
	}
	return &x
}

func (x *xrand) next() uint64 {
	*x ^= *x >> 12
	*x ^= *x << 25
	*x ^= *x >> 27
	return uint64(*x) * 0x2545F4914F6CDD1D
}

// u01 返回 [0,1)。
func (x *xrand) u01() float64 { return float64(x.next()>>11) / (1 << 53) }

// sym 返回 [−1,1)（方差 1/3——宽带噪声以 σ·√3 缩放匹配目标 RMS）。
func (x *xrand) sym() float64 { return 2*x.u01() - 1 }

// mix64 确定性混合（splitmix64 终结器）：(a,b,c) → 均匀比特流。
func mix64(a, b, c uint64) uint64 {
	x := a ^ b*0x9E3779B97F4A7C15 ^ c*0xC2B2AE3D27D4EB4F
	x ^= x >> 30
	x *= 0xBF58476D1CE4E5B9
	x ^= x >> 27
	x *= 0x94D049BB133111EB
	x ^= x >> 31
	return x
}

// fnv64a FNV-1a 64（全仓种子约定：字符串标签 → 64 位种子）。
func fnv64a(s string) uint64 {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

// NegSeed 门禁/调用方种子：FNV-1a 64 对齐全仓约定（label 唯一 → 种子唯一）。
func NegSeed(label string) int64 { return int64(fnv64a(label)) }

const negSqrt3 = 1.7320508075688772

// ── 调度（等额轮转：单源占比 0.25 ≤0.30） ─────────────────────────────

// negBlock 调度块：源类型 + 帧区间（块内参数/表由 (seed, blockIdx) 冻结派生）。
type negBlock struct {
	source     string
	startFrame int
	frames     int
}

// scheduleBlocks 等额轮转调度：每轮 1200–4800ms 均分给全部源类型（源序确定性
// 洗牌），单源累计占比恒 1/len(types)（±尾轮截断 ≤1 块），≤0.30 门槛有余量。
func scheduleBlocks(types []string, frames int, seed int64) []negBlock {
	n := len(types)
	var blocks []negBlock
	r := xrandFrom(mix64(uint64(seed), fnv64a("neg-schedule"), 1))
	pos := 0
	for pos < frames {
		roundFrames := (1200 + int(r.u01()*3600)) / NegFrameMs // 40–160 帧
		if roundFrames < n {
			roundFrames = n
		}
		sub := roundFrames / n
		order := make([]int, n)
		for i := range order {
			order[i] = i
		}
		for i := n - 1; i > 0; i-- { // Fisher–Yates（确定性）
			j := int(r.u01() * float64(i+1))
			order[i], order[j] = order[j], order[i]
		}
		for _, t := range order {
			if pos >= frames {
				break
			}
			f := sub
			if f > frames-pos {
				f = frames - pos
			}
			blocks = append(blocks, negBlock{source: types[t], startFrame: pos, frames: f})
			pos += f
		}
	}
	return blocks
}

// ── 帧流 ──────────────────────────────────────────────────────────────

// NegFrame 负样本帧：16kHz mono int16 PCM + 源类型标注。
type NegFrame struct {
	TS     int64   // 帧起始 ms（音频时钟单调推进）
	Source string  // 源类型（tneg: speech_like/tv_noise/burst/mixed；kwsadv: 音节类）
	PCM    []int16 // 480 样本（复用缓冲：仅在本帧与下一次 Next 前有效）
}

// NegStream 负样本帧流（单向顺序消费；同 (generator, duration, seed) 逐字节复现）。
type NegStream struct {
	g        Generator
	seed     int64
	frames   int
	blocks   []negBlock
	next     int
	blockIdx int
	ctx      *negBlockCtx
	pcm      []int16
}

// Frames 帧总数（= durationMs/NegFrameMs）。
func (s *NegStream) Frames() int { return s.frames }

// Next 返回下一帧（流尽返回 false）。PCM 缓冲逐帧复用——消费方不得跨 Next 保留
// 引用（kws.Detector 逐帧同步消费即契约内用法）。
func (s *NegStream) Next() (NegFrame, bool) {
	if s.next >= s.frames {
		return NegFrame{}, false
	}
	i := s.next
	s.next++
	if s.ctx == nil || i >= s.blocks[s.blockIdx].startFrame+s.blocks[s.blockIdx].frames {
		for s.blockIdx < len(s.blocks)-1 && i >= s.blocks[s.blockIdx].startFrame+s.blocks[s.blockIdx].frames {
			s.blockIdx++
		}
		s.ctx = s.buildBlock(s.blockIdx)
	}
	s.fillFrame(i)
	return NegFrame{TS: int64(i) * NegFrameMs, Source: s.ctx.source, PCM: s.pcm}, true
}

// TNegGen / KWSAdvGen 冻结生成器记录（免注册直用面——门禁测试侧取冻结参数集）。
func TNegGen() Generator {
	return Generator{ID: TNegGeneratorID, Version: TNegImplVersion,
		SeedPolicy: "fixed-fnv64a", OutputsManifest: "frames.jsonl"}
}

// KWSAdvGen 对抗生成器冻结记录。
func KWSAdvGen() Generator {
	return Generator{ID: KWSAdvGeneratorID, Version: KWSAdvImplVersion,
		SeedPolicy: "fixed-fnv64a", OutputsManifest: "frames.jsonl"}
}

// NewTNegStream 家庭音景负样本帧流（gen-tneg 冻结参数集）。
func NewTNegStream(durationMs int, seed int64) (*NegStream, error) {
	return NewNegStream(TNegGen(), durationMs, seed)
}

// NewKWSAdvStream 对抗同音节负样本帧流（gen-kwsadv 冻结参数集）。
func NewKWSAdvStream(durationMs int, seed int64) (*NegStream, error) {
	return NewNegStream(KWSAdvGen(), durationMs, seed)
}

// NewNegStream 按注册生成器构造负样本帧流（gen-tneg → 家庭音景；gen-kwsadv →
// 对抗同音节）。版本纪律：注册版本须等于实现冻结版本。
func NewNegStream(g Generator, durationMs int, seed int64) (*NegStream, error) {
	if durationMs < NegFrameMs {
		return nil, fmt.Errorf("负样本时长须 ≥%dms（got %dms）", NegFrameMs, durationMs)
	}
	var types []string
	switch g.ID {
	case TNegGeneratorID:
		if g.Version != TNegImplVersion {
			return nil, fmt.Errorf("%s 参数集冻结于 %s，注册版本 %s 不符（改参数=新 version 重新注册）",
				TNegGeneratorID, TNegImplVersion, g.Version)
		}
		types = tnegSourceTypes[:]
	case KWSAdvGeneratorID:
		if g.Version != KWSAdvImplVersion {
			return nil, fmt.Errorf("%s 参数集冻结于 %s，注册版本 %s 不符（改参数=新 version 重新注册）",
				KWSAdvGeneratorID, KWSAdvImplVersion, g.Version)
		}
		types = kwsAdvSourceTypes[:]
	default:
		return nil, fmt.Errorf("未知负样本生成器 %q（仅 %s/%s）", g.ID, TNegGeneratorID, KWSAdvGeneratorID)
	}
	frames := durationMs / NegFrameMs
	return &NegStream{
		g: g, seed: seed, frames: frames,
		blocks: scheduleBlocks(types, frames, seed),
		pcm:    make([]int16, negFrameLen),
	}, nil
}

// ── 块级合成（声学参数冻结带） ────────────────────────────────────────

// negEvent 块内预合成事件（音节/突发）：相对块首样本偏移 + 波形。
type negEvent struct {
	startSample int
	wave        []float64
}

// negBlockCtx 块级上下文：冻结参数与预合成表（buildBlock 一次构建、逐帧消费）。
type negBlockCtx struct {
	source      string
	startSample int // 块首全局样本偏移
	syls        []negEvent
	events      []negEvent
	speechRMS   float64 // 音节列车目标 RMS（0=块无语音面）
	noiseRMS    float64 // 语音状底噪 RMS
	tvRMS       float64 // 电视噪声基级（0=无电视面）
	tvTheta     float64 // 电视慢漂移相位（逐帧推进）
	tvOmega     float64 // 每帧相位增量
	roomRMS     float64 // burst 块间隙底噪
}

// sylTrainSpec 语音列车冻结带（speech_like 与对抗音节共用机制）。
type sylTrainSpec struct {
	phraseSylLo, phraseSylHi int // 每短语音节数
	sylDurLo, sylDurHi       int // 音节时长 ms
	sylGapLo, sylGapHi       int // 音节间 gap ms
	phraseGapLo, phraseGapHi int // 短语间 gap ms
	f0Lo, f0Hi               float64
	rmsLo, rmsHi             float64 // 音节目标 RMS
	noiseRatio               float64 // 底噪/语音幅度比（远场冻结：≥2.6）
}

// speechTrainSpec 远场家人聊天冻结带（80–400Hz 共振峰状谐波，f0 110–220）。
var speechTrainSpec = sylTrainSpec{
	phraseSylLo: 2, phraseSylHi: 6,
	sylDurLo: 120, sylDurHi: 260,
	sylGapLo: 50, sylGapHi: 130,
	phraseGapLo: 400, phraseGapHi: 1600,
	f0Lo: 110, f0Hi: 220,
	rmsLo: 500, rmsHi: 1100,
	noiseRatio: 2.6,
}

// advSpecs 对抗音节模式冻结表（「小爱」「天猫」「小度」同音节结构 + 本词高混淆
// 近邻——nearconf 三音节、f0 贴本词占位 200Hz）。
var advSpecs = map[string]sylTrainSpec{
	"xiaoai":   {phraseSylLo: 2, phraseSylHi: 2, sylDurLo: 150, sylDurHi: 280, sylGapLo: 60, sylGapHi: 140, phraseGapLo: 350, phraseGapHi: 900, f0Lo: 160, f0Hi: 200, rmsLo: 700, rmsHi: 1500, noiseRatio: 2.6},
	"tianmao":  {phraseSylLo: 2, phraseSylHi: 2, sylDurLo: 150, sylDurHi: 300, sylGapLo: 60, sylGapHi: 150, phraseGapLo: 300, phraseGapHi: 850, f0Lo: 130, f0Hi: 175, rmsLo: 700, rmsHi: 1500, noiseRatio: 2.6},
	"xiaodu":   {phraseSylLo: 2, phraseSylHi: 2, sylDurLo: 130, sylDurHi: 240, sylGapLo: 50, sylGapHi: 130, phraseGapLo: 300, phraseGapHi: 800, f0Lo: 180, f0Hi: 230, rmsLo: 700, rmsHi: 1500, noiseRatio: 2.6},
	"nearconf": {phraseSylLo: 3, phraseSylHi: 3, sylDurLo: 110, sylDurHi: 200, sylGapLo: 45, sylGapHi: 110, phraseGapLo: 250, phraseGapHi: 700, f0Lo: 190, f0Hi: 235, rmsLo: 700, rmsHi: 1600, noiseRatio: 2.6},
}

// buildBlock 构造块上下文（全部参数由 (seed, blockIdx, source) 确定性派生）。
func (s *NegStream) buildBlock(k int) *negBlockCtx {
	b := s.blocks[k]
	r := xrandFrom(mix64(uint64(s.seed), uint64(k), fnv64a("neg-block:"+b.source)))
	c := &negBlockCtx{source: b.source, startSample: b.startFrame * negFrameLen}
	blockSamples := b.frames * negFrameLen
	switch b.source {
	case "speech_like":
		s.buildSyllables(c, r, blockSamples, speechTrainSpec, 0)
	case "tv_noise":
		c.tvRMS = negNearRMS * math.Pow(10, -r.u01()) // SNR −20~0dB 扰动（声级带 283–2828）
		c.tvTheta = r.u01() * math.Pi
		c.tvOmega = negFrameOmega(r)
	case "burst":
		s.buildBurst(c, r, blockSamples)
	case "mixed":
		s.buildSyllables(c, r, blockSamples, speechTrainSpec, 0)
		c.tvRMS = negNearRMS * math.Pow(10, -r.u01())
		c.tvTheta = r.u01() * math.Pi
		c.tvOmega = negFrameOmega(r)
		if r.u01() < 0.5 {
			s.appendBurstEvent(c, r, blockSamples)
		}
	default: // gen-kwsadv 音节类
		spec, ok := advSpecs[b.source]
		if !ok {
			spec = speechTrainSpec // 不可达（源表冻结）；保底防发散
		}
		s.buildSyllables(c, r, blockSamples, spec, 0)
	}
	return c
}

// negFrameOmega 电视声级慢漂移每帧相位增量（周期 2–9s）。
func negFrameOmega(r *xrand) float64 {
	periodMs := 2000 + r.u01()*7000
	return 2 * math.Pi * NegFrameMs / periodMs
}

// buildSyllables 预合成音节列车（谐波栈×sin² 包络；底噪帧级另加）。
func (s *NegStream) buildSyllables(c *negBlockCtx, r *xrand, blockSamples int, spec sylTrainSpec, _ int) {
	speechRMS := spec.rmsLo + r.u01()*(spec.rmsHi-spec.rmsLo)
	c.speechRMS = speechRMS
	c.noiseRMS = spec.noiseRatio * speechRMS // 远场冻结：底噪幅度 ≥2.6×语音
	f0 := spec.f0Lo + r.u01()*(spec.f0Hi-spec.f0Lo)
	pos := int(r.u01() * 200 * 16) // 短语起始抖动 ≤200ms
	for pos < blockSamples {
		nSyl := spec.phraseSylLo + int(r.u01()*float64(spec.phraseSylHi-spec.phraseSylLo+1))
		for k := 0; k < nSyl && pos < blockSamples; k++ {
			durMs := spec.sylDurLo + int(r.u01()*float64(spec.sylDurHi-spec.sylDurLo+1))
			sylF0 := f0 * (1 + (r.u01()-0.5)*0.12) // 音节基频抖动 ±6%
			c.syls = append(c.syls, negEvent{startSample: pos,
				wave: synthSyllable(r, sylF0, durMs, speechRMS)})
			pos += durMs*16 + (spec.sylGapLo+int(r.u01()*float64(spec.sylGapHi-spec.sylGapLo+1)))*16
		}
		pos += (spec.phraseGapLo + int(r.u01()*float64(spec.phraseGapHi-spec.phraseGapLo+1))) * 16
	}
}

// buildBurst 突发块：1–3 个事件（衰减宽带噪声+低频撞击分量）+ 间隙底噪。
func (s *NegStream) buildBurst(c *negBlockCtx, r *xrand, blockSamples int) {
	c.roomRMS = 180 + r.u01()*270 // 180–450（≥NegRMSFloor）
	n := 1 + int(r.u01()*3)
	for k := 0; k < n; k++ {
		s.appendBurstEvent(c, r, blockSamples)
	}
}

// appendBurstEvent 单突发事件：峰值 ≥近讲声级、宽带噪声主异 + 快衰减低频撞击。
func (s *NegStream) appendBurstEvent(c *negBlockCtx, r *xrand, blockSamples int) {
	durMs := 30 + int(r.u01()*120) // 30–150ms
	n := durMs * 16
	if n < negFrameLen {
		n = negFrameLen
	}
	peak := NegBurstPeakMin + 500 + r.u01()*8000 // 4500–13000（≥4000 近讲峰值口径）
	tau := 240 + r.u01()*560                     // 噪声衰减 15–50ms
	thTau := 160 + r.u01()*160                   // 撞击衰减 10–20ms（快于噪声——尾帧不低频占优）
	thAmp := 0.35 * peak
	w := 2 * math.Pi * (100 + r.u01()*80) / NegSampleRate // 撞击 100–180Hz
	wave := make([]float64, n)
	for j := 0; j < n; j++ {
		wave[j] = r.sym()*peak*math.Exp(-float64(j)/tau) +
			thAmp*math.Exp(-float64(j)/thTau)*math.Sin(w*float64(j))
	}
	start := int(r.u01() * float64(blockSamples-n))
	if start < 0 {
		start = 0
	}
	c.events = append(c.events, negEvent{startSample: start, wave: wave})
}

// synthSyllable 单音节波形：谐波栈（1/k，K≤16 且 ≤3000Hz/f0——80–400Hz 共振峰状
// 低频占优）× sin² 攻防包络，RMS 归一后缩放到目标（相位由块内确定性推进）。
func synthSyllable(r *xrand, f0 float64, durMs int, targetRMS float64) []float64 {
	n := durMs * NegSampleRate / 1000
	L := int(math.Round(NegSampleRate / f0))
	if L < 8 {
		L = 8
	}
	K := 16
	if maxK := int(3000.0 / f0); maxK < K {
		K = maxK
	}
	if K < 1 {
		K = 1
	}
	tbl := make([]float64, L)
	for k := 1; k <= K; k++ {
		w := 2 * math.Pi * float64(k) / float64(L)
		for j := 0; j < L; j++ {
			tbl[j] += math.Sin(w*float64(j)) / float64(k)
		}
	}
	var ss float64
	for _, v := range tbl {
		ss += v * v
	}
	rms := math.Sqrt(ss / float64(L))
	amp := targetRMS / (rms * 0.6124) // sin² 包络功率均值 3/8 → RMS×√(3/8)
	phase := int(r.u01() * float64(L))
	wave := make([]float64, n)
	for j := 0; j < n; j++ {
		wave[j] = tbl[(phase+j)%L] * math.Sin(math.Pi*float64(j)/float64(n)) * amp
	}
	return wave
}

// fillFrame 合成第 i 帧（帧级 iid 噪声 + 块级预合成事件叠加）。
func (s *NegStream) fillFrame(i int) {
	c := s.ctx
	rng := xrandFrom(mix64(uint64(s.seed), uint64(i), 0x51EE5EED))
	var noise float64
	switch c.source {
	case "tv_noise":
		noise = c.tvRMS * (0.65 + 0.7*sqSin(c.tvTheta))
		c.tvTheta += c.tvOmega
	case "burst":
		noise = c.roomRMS
	case "mixed":
		noise = math.Hypot(c.noiseRMS, c.tvRMS*(0.65+0.7*sqSin(c.tvTheta)))
		c.tvTheta += c.tvOmega
	default: // speech_like / 对抗音节类
		noise = c.noiseRMS
	}
	nScale := noise * negSqrt3
	fs := i * negFrameLen
	for j := 0; j < negFrameLen; j++ {
		s.pcm[j] = roundI16(rng.sym() * nScale)
	}
	for _, ev := range c.syls {
		s.overlay(fs, c.startSample+ev.startSample, ev.wave)
	}
	for _, ev := range c.events {
		s.overlay(fs, c.startSample+ev.startSample, ev.wave)
	}
}

// overlay 事件波形叠加进当前帧区间（越界自动裁剪）。
func (s *NegStream) overlay(fs, e0 int, wave []float64) {
	lo, hi := fs, fs+negFrameLen
	if e0 > lo {
		lo = e0
	}
	if e1 := e0 + len(wave); e1 < hi {
		hi = e1
	}
	for p := lo; p < hi; p++ {
		s.pcm[p-fs] = roundI16(float64(s.pcm[p-fs]) + wave[p-e0])
	}
}

func sqSin(theta float64) float64 {
	s := math.Sin(theta)
	return s * s
}

func roundI16(v float64) int16 {
	v = math.Round(v)
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}

// ── 批语义（eval-only，不切 synth-holdout） ───────────────────────────

// NegFrameRecord frames.jsonl 帧记录：sample_id + 四字段溯源戳 + 帧元数据
// （PCM 不落盘——由 (generator@version, seed, duration) 确定性重建）。
type NegFrameRecord struct {
	SampleID   string     `json:"sample_id"`
	Provenance Provenance `json:"provenance"`
	TS         int64      `json:"ts_ms"`
	Source     string     `json:"source"`
	RMS        float64    `json:"rms"`
}

// NegBatch 负样本批 manifest（对齐 GenerateBatch 产物约定：批 id+生成器+种子+
// 切分记录；负样本语义=purpose eval-only、TrainN=0/HoldoutN=0、全量入 eval 池）。
type NegBatch struct {
	ID               string             `json:"batch_id"`
	GeneratorID      string             `json:"generator_id"`
	GeneratorVersion string             `json:"generator_version"`
	Seed             int64              `json:"seed"`
	DurationMs       int                `json:"duration_ms"`
	N                int                `json:"n"`
	TrainN           int                `json:"train_n"`
	HoldoutN         int                `json:"holdout_n"`
	EvalN            int                `json:"eval_n"`
	Purpose          string             `json:"purpose"`
	SourceShares     map[string]float64 `json:"source_shares"`
	Note             string             `json:"note"`
}

// negBatchNote 批注记（spec §2 要求写进 manifest 的不切分理由与重建口径）。
const negBatchNote = "负样本批不切 synth-holdout：8:2 切分目的是防训练集污染评测；" +
	"负样本只供误唤醒评估、永不进训练管道，切分无意义且扣留 20% 只缩评估面（m2-spec §2）。" +
	"PCM 由 (generator@version, seed, duration_ms) 确定性重建（源类型参数集随版本冻结，" +
	"种子 FNV-1a 64 对齐全仓约定）；帧含远场宽带底噪（底噪幅度≥语音 2.6 倍——家庭音景" +
	"远场口径，非干净近讲录音；M3 真模型接入后同批重测）。"

// GenerateBatchNeg 生成负样本批并落盘（manifest.json + frames.jsonl；不含 PCM 本体）。
// TrainN=0/HoldoutN=0、全量入 eval 池——负样本永不进训练管道（拓扑由 T2-G0-01 断言）。
func GenerateBatchNeg(g Generator, batchesDir string, durationMs int, seed int64) (NegBatch, string, error) {
	st, err := NewNegStream(g, durationMs, seed)
	if err != nil {
		return NegBatch{}, "", err
	}
	batchDir := filepath.Join(batchesDir, fmt.Sprintf("%s-%s-seed%d-d%d", g.ID, g.Version, seed, durationMs))
	if err := os.MkdirAll(batchDir, 0o755); err != nil {
		return NegBatch{}, batchDir, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	counts := make(map[string]int)
	n := 0
	for {
		f, ok := st.Next()
		if !ok {
			break
		}
		counts[f.Source]++
		rec := NegFrameRecord{
			SampleID:   fmt.Sprintf("%s-%d-%06d", g.ID, seed, n),
			Provenance: Provenance{GeneratorID: g.ID, GeneratorVersion: g.Version, Seed: seed, UpstreamModel: negUpstreamModel(g)},
			TS:         f.TS,
			Source:     f.Source,
			RMS:        frameRMS(f.PCM),
		}
		if err := enc.Encode(rec); err != nil {
			return NegBatch{}, batchDir, err
		}
		n++
	}
	if err := os.WriteFile(filepath.Join(batchDir, "frames.jsonl"), buf.Bytes(), 0o644); err != nil {
		return NegBatch{}, batchDir, err
	}
	shares := make(map[string]float64, len(counts))
	for src, c := range counts {
		shares[src] = float64(c) / float64(n)
	}
	b := NegBatch{
		ID: filepath.Base(batchDir), GeneratorID: g.ID, GeneratorVersion: g.Version,
		Seed: seed, DurationMs: durationMs, N: n, TrainN: 0, HoldoutN: 0, EvalN: n,
		Purpose: NegPurpose, SourceShares: shares, Note: negBatchNote,
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return b, batchDir, err
	}
	if err := os.WriteFile(filepath.Join(batchDir, "manifest.json"), append(data, '\n'), 0o644); err != nil {
		return b, batchDir, err
	}
	return b, batchDir, nil
}

// ReadNegBatch 在 batchesDir 下按目录序取 generatorID 的首个负样本批 manifest
// （门禁测试只读 manifest+帧流的统一入口）。
func ReadNegBatch(batchesDir, generatorID string) (NegBatch, string, error) {
	entries, err := os.ReadDir(batchesDir)
	if err != nil {
		return NegBatch{}, "", err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(batchesDir, e.Name())
		data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
		if err != nil {
			continue
		}
		var b NegBatch
		if err := json.Unmarshal(data, &b); err != nil {
			continue
		}
		if b.GeneratorID == generatorID {
			return b, dir, nil
		}
	}
	return NegBatch{}, "", fmt.Errorf("%s 下无 %s 负样本批", batchesDir, generatorID)
}

// frameRMS 帧 RMS（int16 域）。
func frameRMS(pcm []int16) float64 {
	if len(pcm) == 0 {
		return 0
	}
	var ss float64
	for _, v := range pcm {
		ss += float64(v) * float64(v)
	}
	return math.Sqrt(ss / float64(len(pcm)))
}
