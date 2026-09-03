// Package vap 是 T3 话轮预测的真引擎（IR #129）：MaAI MC-VAP 中文 Kyoto 版
// （MIT，maai-kyoto/vap_mc_ch_kyoto，10hz/20s 上下文）经融合流式 ONNX 导出后，
// 由 ONNX Runtime Go 绑定驱动，输出映射到 turntaking.Prediction 语义
// （p_now/p_future 双说话人 [user, system] + VAD）。
//
// 导出契约（导出脚本与对拍证据见 reports/eval/T3/）：
//   - 单 ONNX 融合 CPC encoder×2 + VAP GPT 主干 + 输出头，全部固定形状；
//   - 输入：wave1/wave2 [1,1,1920]（320 左上下文 + 1600 新样本 = 16kHz×100ms）、
//     LSTM hidden×4 [1,1,256]、cache_mask [1,1,1,199]、KV 缓存×28 [1,4,199,64]；
//   - 输出：p_now/p_future/p_bins_now/p_bins_future [1,1,2]、vad [1,2]、
//     新状态（mask 199 + hidden 4 + 缓存 28）；
//   - 缓存裁剪（保留最新 199 帧）在图内完成，与 Maai.process 流式语义精确等价
//     （Python 对拍：eager 2.4e-7 / ORT 1.8e-5，300 步全幅音频）。
//
// 依赖纪律：本包是 T3 的「注入面实现」——turntaking 包保持零依赖（ADR-0004），
// 本包只被测试 harness / 驱动层接线；包间零 import（结构化实现 turntaking.Predictor）。
package vap

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	ort "github.com/yalue/onnxruntime_go"
)

// 导出契约常量（勿改：与 ONNX 图形状一一对应）。
const (
	SampleRate      = 16000 // 输入采样率
	PadSamples      = 320   // 帧间左上下文样本（Maai frame_contxt_padding）
	NewSamples      = 1600  // 每帧新样本（16000 / 10hz）
	WinSamples      = PadSamples + NewSamples
	PastFrames      = 199 // KV 缓存保留帧数（20s 上下文 −1）
	Heads           = 4
	HeadDim         = 64
	HiddenDim       = 256 // CPC AR-LSTM hidden
	NumCacheTensors = 28  // ar1/ar2 各 1 层 + cross 四组各 3 层，每层 k/v 两张
	FrameMs         = 100 // 每步音频时长
)

// Engine 单流串行推理引擎（同 turntaking.FSM 契约：不加锁，调用方单线程驱动）。
type Engine struct {
	sess     *ort.DynamicAdvancedSession
	inNames  []string
	outNames []string

	in  []*ort.Tensor[float32]
	out []*ort.Tensor[float32]

	pad1  [PadSamples]float32 // 上一窗口尾部（下一帧左上下文）
	pad2  [PadSamples]float32
	wave1 []float32 // 当前窗口（len WinSamples）
	wave2 []float32

	last  turntakingPrediction
	have  bool
	steps int
	wall  time.Duration // Step 纯推理墙钟累计（RTF 观测面）
}

// turntakingPrediction 是 turntaking.Prediction 的本地镜像（包间零 import：
// 不 import packages/go/turntaking，由驱动层做结构化转换）。
type turntakingPrediction struct {
	PNowUser      float32
	PNowSystem    float32
	PFutureUser   float32
	PFutureSystem float32
	VADUser       float32
	VADSystem     float32
}

// Config 引擎配置。
type Config struct {
	ModelPath      string // 融合 ONNX 路径
	LibraryPath    string // libonnxruntime 共享库路径（首次初始化前生效）
	IntraOpThreads int    // CPU intra-op 线程数（默认 2）
}

var (
	ortOnce sync.Once
	ortErr  error
)

func initORT(libPath string) error {
	ortOnce.Do(func() {
		if libPath != "" {
			ort.SetSharedLibraryPath(libPath)
		}
		ortErr = ort.InitializeEnvironment()
	})
	return ortErr
}

// New 构造引擎并加载模型。库初始化全局一次（LibraryPath 首次生效）。
func New(cfg Config) (*Engine, error) {
	if cfg.ModelPath == "" {
		return nil, fmt.Errorf("vap: ModelPath 必填")
	}
	if err := initORT(cfg.LibraryPath); err != nil {
		return nil, fmt.Errorf("vap: onnxruntime 初始化失败（LibraryPath=%q）: %w", cfg.LibraryPath, err)
	}
	inNames, outNames, err := ioNames(cfg.ModelPath)
	if err != nil {
		return nil, err
	}
	if len(inNames) != 7+NumCacheTensors {
		return nil, fmt.Errorf("vap: 模型输入数 %d ≠ 契约 %d（7 状态 + %d 缓存）",
			len(inNames), 7+NumCacheTensors, NumCacheTensors)
	}
	opts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("vap: SessionOptions: %w", err)
	}
	defer opts.Destroy()
	th := cfg.IntraOpThreads
	if th <= 0 {
		th = 2
	}
	if err := opts.SetIntraOpNumThreads(th); err != nil {
		return nil, fmt.Errorf("vap: SetIntraOpNumThreads: %w", err)
	}
	sess, err := ort.NewDynamicAdvancedSession(cfg.ModelPath, inNames, outNames, opts)
	if err != nil {
		return nil, fmt.Errorf("vap: 加载模型 %s: %w", cfg.ModelPath, err)
	}

	e := &Engine{sess: sess, inNames: inNames, outNames: outNames,
		wave1: make([]float32, WinSamples), wave2: make([]float32, WinSamples)}
	if err := e.allocTensors(); err != nil {
		_ = sess.Destroy()
		return nil, err
	}
	return e, nil
}

// ioNames 返回导出契约固定的 IO 名（与导出脚本 in_names/out_names 一致；
// 名称错误会在 NewDynamicAdvancedSession 加载时被 ORT 拒绝）。
func ioNames(modelPath string) (ins, outs []string, err error) {
	_ = modelPath
	ins = []string{"wave1", "wave2", "lstm_h1", "lstm_c1", "lstm_h2", "lstm_c2", "cache_mask"}
	for _, g := range cacheGroups {
		ins = append(ins, "past_"+g+"_k", "past_"+g+"_v")
	}
	outs = []string{"p_now", "p_future", "vad", "p_bins_now", "p_bins_future",
		"cache_mask_out", "lstm_h1_out", "lstm_c1_out", "lstm_h2_out", "lstm_c2_out"}
	for _, g := range cacheGroups {
		outs = append(outs, "past_"+g+"_k_out", "past_"+g+"_v_out")
	}
	return ins, outs, nil
}

// cacheGroups 与导出脚本 CACHE_GROUPS 一致：(组名, 层号)。
var cacheGroups = func() []string {
	var gs []string
	for _, g := range []struct {
		name string
		n    int
	}{{"ar1", 1}, {"ar2", 1}, {"cross1", 3}, {"cross2", 3}, {"cross1_c", 3}, {"cross2_c", 3}} {
		for i := 0; i < g.n; i++ {
			gs = append(gs, g.name+"_"+fmt.Sprint(i))
		}
	}
	return gs
}()

// allocTensors 预分配全部固定形状张量（in/out 各一套，步间 out→in 滚动）。
func (e *Engine) allocTensors() error {
	mk := func(shape ...int64) (*ort.Tensor[float32], error) {
		return ort.NewTensor[float32](ort.Shape(shape), make([]float32, dimOf(shape)))
	}
	// 顺序与 ioNames 一致：wave1, wave2, h1, c1, h2, c2, mask, cache×28
	addIn := func(t *ort.Tensor[float32], err error) error {
		if err != nil {
			return err
		}
		e.in = append(e.in, t)
		return nil
	}
	if err := addIn(mk(1, 1, WinSamples)); err != nil {
		return err
	}
	if err := addIn(mk(1, 1, WinSamples)); err != nil {
		return err
	}
	for i := 0; i < 4; i++ {
		if err := addIn(mk(1, 1, HiddenDim)); err != nil {
			return err
		}
	}
	if err := addIn(mk(1, 1, 1, PastFrames)); err != nil {
		return err
	}
	for i := 0; i < NumCacheTensors; i++ {
		if err := addIn(mk(1, Heads, PastFrames, HeadDim)); err != nil {
			return err
		}
	}
	// 输出（与 outNames 顺序一一对应）：p_now [1,1,2], p_future [1,1,2],
	// vad [1,2], p_bins_now [1,1,2], p_bins_future [1,1,2], mask_out, h/c×4, cache×28
	addOut := func(shape ...int64) error {
		t, err := mk(shape...)
		if err != nil {
			return err
		}
		e.out = append(e.out, t)
		return nil
	}
	if err := addOut(1, 1, 2); err != nil { // p_now
		return err
	}
	if err := addOut(1, 1, 2); err != nil { // p_future
		return err
	}
	if err := addOut(1, 2); err != nil { // vad
		return err
	}
	if err := addOut(1, 1, 2); err != nil { // p_bins_now
		return err
	}
	if err := addOut(1, 1, 2); err != nil { // p_bins_future
		return err
	}
	if err := addOut(1, 1, 1, PastFrames); err != nil { // mask_out
		return err
	}
	for i := 0; i < 4; i++ {
		if err := addOut(1, 1, HiddenDim); err != nil {
			return err
		}
	}
	for i := 0; i < NumCacheTensors; i++ {
		if err := addOut(1, Heads, PastFrames, HeadDim); err != nil {
			return err
		}
	}
	return nil
}

func dimOf(shape []int64) int {
	n := 1
	for _, d := range shape {
		n *= int(d)
	}
	return n
}

// Step 推进一帧（100ms 音频）：滚动窗口 → ORT 推理 → 状态滚动 → 预测输出。
// chunk 长度须 = NewSamples。单流串行（不加锁）。
func (e *Engine) Step(chunk1, chunk2 []float32) (turntakingPrediction, error) {
	var zero turntakingPrediction
	if len(chunk1) != NewSamples || len(chunk2) != NewSamples {
		return zero, fmt.Errorf("vap: chunk 长度须 %d，got %d/%d", NewSamples, len(chunk1), len(chunk2))
	}
	// 滚动窗口：pad = 上一窗口尾部 320 样本；新窗口 = pad + chunk。
	copy(e.wave1, e.wave1[NewSamples:])
	copy(e.wave1[PadSamples:], chunk1)
	copy(e.wave2, e.wave2[NewSamples:])
	copy(e.wave2[PadSamples:], chunk2)
	copy(e.pad1[:], e.wave1[NewSamples:])
	copy(e.pad2[:], e.wave2[NewSamples:])
	// 窗口写入 ORT 输入张量（in[0]/in[1] = wave1/wave2）。
	copy(e.in[0].GetData(), e.wave1)
	copy(e.in[1].GetData(), e.wave2)

	begin := time.Now()
	if err := e.sess.Run(toValues(e.in), toValues(e.out)); err != nil {
		return zero, fmt.Errorf("vap: ORT Run: %w", err)
	}
	e.wall += time.Since(begin)
	e.steps++

	// 状态滚动（out → in）：mask、LSTM hidden、28 张缓存。
	copy(e.in[6].GetData(), e.out[5].GetData())
	for i := 0; i < 4; i++ {
		copy(e.in[2+i].GetData(), e.out[6+i].GetData())
	}
	for i := 0; i < NumCacheTensors; i++ {
		copy(e.in[7+i].GetData(), e.out[10+i].GetData())
	}

	// 预测语义映射：speaker1=ch1=user（话筒/孩子），speaker2=ch2=system。
	e.last = turntakingPrediction{
		PNowUser:      e.out[0].GetData()[0],
		PNowSystem:    e.out[0].GetData()[1],
		PFutureUser:   e.out[1].GetData()[0],
		PFutureSystem: e.out[1].GetData()[1],
		VADUser:       e.out[2].GetData()[0],
		VADSystem:     e.out[2].GetData()[1],
	}
	e.have = true
	return e.last, nil
}

// Predict 实现「最近一帧预测」读取面（结构化满足 turntaking.Predictor 语义）。
func (e *Engine) Predict() (turntakingPrediction, bool) { return e.last, e.have }

// Steps / Wall / RTF 返回 RTF 观测面（推理墙钟口径）。
func (e *Engine) Steps() int          { return e.steps }
func (e *Engine) Wall() time.Duration { return e.wall }

// RTF 每帧音频时长上的推理墙钟比（帧数 0 时返回 0）。
func (e *Engine) RTF() float64 {
	if e.steps == 0 {
		return 0
	}
	audio := time.Duration(e.steps) * FrameMs * time.Millisecond
	return float64(e.wall) / float64(audio)
}

// Reset 清空流式状态（新话轮/新会话起点；模型与张量保留）。
func (e *Engine) Reset() {
	for i := range e.wave1 {
		e.wave1[i] = 0
		e.wave2[i] = 0
	}
	e.pad1 = [PadSamples]float32{}
	e.pad2 = [PadSamples]float32{}
	for _, t := range e.in {
		d := t.GetData()
		for i := range d {
			d[i] = 0
		}
	}
	for _, t := range e.out {
		d := t.GetData()
		for i := range d {
			d[i] = 0
		}
	}
	e.last, e.have, e.steps, e.wall = turntakingPrediction{}, false, 0, 0
}

// Destroy 释放会话（进程退出面调用一次即可；张量由 GC 回收）。
func (e *Engine) Destroy() error { return e.sess.Destroy() }

// DefaultModelPath 定位融合 ONNX：env T3_VAP_MODEL → 仓根 models/incoming/。
func DefaultModelPath() (string, error) {
	if p := os.Getenv("T3_VAP_MODEL"); p != "" {
		return p, nil
	}
	root, err := repoRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "models", "incoming", "vap_maai_ch_kyoto_mc_10hz.onnx"), nil
}

// DefaultLibraryPath 定位 ORT 共享库：env T3_VAP_ORT_LIB → 常见系统路径。
func DefaultLibraryPath() string {
	if p := os.Getenv("T3_VAP_ORT_LIB"); p != "" {
		return p
	}
	for _, p := range []string{
		"/usr/local/lib/libonnxruntime.so.1.29.0",
		"/usr/lib/libonnxruntime.so.1.29.0",
		"/usr/local/lib/libonnxruntime.so",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "" // 让 ort 用其默认查找（或由调用方显式配置）
}

// repoRoot 自包目录向上找 go.mod。
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		up := filepath.Dir(dir)
		if up == dir {
			break
		}
		dir = up
	}
	return "", fmt.Errorf("vap: 未找到仓根（自 %s 向上）", dir)
}

func toValues(ts []*ort.Tensor[float32]) []ort.Value {
	vs := make([]ort.Value, len(ts))
	for i, t := range ts {
		vs[i] = t
	}
	return vs
}
