// Package meloort 是 T13 端侧合成的「注入面实现」（M2，issue #133）：把
// melotts-zh.onnx（opset 17 导出图，导出/对拍证据见 reports/eval/T13/）装配成
// tts.MeloSession。与 T3 的 turntaking/vap 同一模式：核心包 tts 保持零外部
// 依赖（ADR-0004/0008），本包只被测试 harness / 驱动层接线。
//
// 导出契约（tools/tts/export_melotts_onnx.py，批维固定 1，动态轴=token 数 t）：
//
//	tokens/tones/lang_ids int64 [1,t] · lengths int64 [1] · sid int64 [1]
//	bert float32 [1,1024,t]（ZH_MIX_EN 恒零槽）· ja_bert float32 [1,768,t]
//	sdp_noise float32 [1,2,t] · z_noise float32 [1,192,8t]（图内切到 mel 长度）
//	noise_scale/noise_scale_w/length_scale/sdp_ratio float32 标量（rank-0）
//	→ audio float32 [1,1,samples]（44.1kHz，[-1,1]）
//
// 确定性（P1）由调用方持有：噪声是显式输入（tts.MeloSynthesizer 从
// (seed,text,voice) 确定性派生），ORT 同进程同输入输出字节一致。
package meloort

import (
	"fmt"
	"os"
	"sync"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/Cloudbird-Software/AI_Toy/packages/go/tts"
)

// 编译期契约：本包产物就是 tts.MeloSession 注入面。
var _ tts.MeloSession = (*Session)(nil)

// 导出契约的 IO 名与形状常量（勿改：与 ONNX 图一一对应，名字错在
// NewDynamicAdvancedSession 加载时被 ORT 拒绝，形状错在 Run 时被拒）。
var (
	inputNames  = []string{"tokens", "tones", "lang_ids", "lengths", "sid",
		"bert", "ja_bert", "sdp_noise", "z_noise",
		"noise_scale", "noise_scale_w", "length_scale", "sdp_ratio"}
	outputNames = []string{"audio"}
)

const (
	speakerZH          = 1 // 官方音色 ZH（config.json spk2id）
	meloBertChannels   = 1024
	meloJaBertChannels = 768
	meloSdpNoiseCh     = 2
	meloZChannels      = 192
	meloZHeadroom      = 8 // z 噪声时间维预留倍数（与导出图 ZHEAD 一致）
)

// Session ORT 会话执行器（tts.MeloSession 实现）。非并发安全：单 goroutine
// 驱动（与 tts.MeloSynthesizer 同契约），并发调用须外面上锁。
type Session struct {
	sess *ort.DynamicAdvancedSession
}

// Config 会话配置。
type Config struct {
	ModelPath      string // melotts-zh.onnx 路径
	LibraryPath    string // libonnxruntime 共享库路径（首次初始化前生效）
	IntraOpThreads int    // CPU intra-op 线程数（<=0 取 defaultIntraOpThreads）
}

// defaultIntraOpThreads 与 inference/vap 一致：4 核低功耗机上 intra-op=2
// （RTF 口径三方可比）。
const defaultIntraOpThreads = 2

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

// New 构造会话并加载模型。库初始化进程内全局一次（LibraryPath 首次生效）。
func New(cfg Config) (*Session, error) {
	if cfg.ModelPath == "" {
		return nil, fmt.Errorf("meloort: ModelPath 必填")
	}
	if err := initORT(cfg.LibraryPath); err != nil {
		return nil, fmt.Errorf("meloort: onnxruntime 初始化失败（LibraryPath=%q）: %w", cfg.LibraryPath, err)
	}
	opts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("meloort: SessionOptions: %w", err)
	}
	defer opts.Destroy()
	th := cfg.IntraOpThreads
	if th <= 0 {
		th = defaultIntraOpThreads
	}
	if err := opts.SetIntraOpNumThreads(th); err != nil {
		return nil, fmt.Errorf("meloort: SetIntraOpNumThreads: %w", err)
	}
	sess, err := ort.NewDynamicAdvancedSession(cfg.ModelPath, inputNames, outputNames, opts)
	if err != nil {
		return nil, fmt.Errorf("meloort: 加载模型 %s: %w", cfg.ModelPath, err)
	}
	return &Session{sess: sess}, nil
}

// Run 实现 tts.MeloSession：音素张量+显式噪声 → 波形（[-1,1] float32 单声道）。
// 输出音频样本数数据相关（mel 长度×hop），由图动态给出。
func (s *Session) Run(io tts.MeloIO) ([]float32, error) {
	t := len(io.Tokens)
	if t == 0 {
		return nil, fmt.Errorf("meloort: tokens 空")
	}
	if len(io.Tones) != t || len(io.LangIDs) != t {
		return nil, fmt.Errorf("meloort: tokens/tones/lang_ids 不等长（%d/%d/%d）",
			t, len(io.Tones), len(io.LangIDs))
	}
	want := map[string]int{
		"bert": meloBertChannels * t, "ja_bert": meloJaBertChannels * t,
		"sdp_noise": meloSdpNoiseCh * t, "z_noise": meloZChannels * meloZHeadroom * t,
	}
	got := map[string]int{"bert": len(io.Bert), "ja_bert": len(io.JaBert),
		"sdp_noise": len(io.SdpNoise), "z_noise": len(io.ZNoise)}
	for name, n := range want {
		if got[name] != n {
			return nil, fmt.Errorf("meloort: %s 形状不符：须 %d 元素（T=%d 导出契约），got %d",
				name, n, t, got[name])
		}
	}

	tokens, err := ort.NewTensor(ort.Shape{1, int64(t)}, io.Tokens)
	if err != nil {
		return nil, fmt.Errorf("meloort: tokens 张量: %w", err)
	}
	tensors := []ort.Value{tokens}
	defer func() {
		for _, v := range tensors {
			_ = v.Destroy()
		}
	}()
	mkI64 := func(data []int64, shape ...int64) error {
		v, e := ort.NewTensor(ort.Shape(shape), data)
		if e != nil {
			return e
		}
		tensors = append(tensors, v)
		return nil
	}
	mkF32 := func(data []float32, shape ...int64) error {
		v, e := ort.NewTensor(ort.Shape(shape), data)
		if e != nil {
			return e
		}
		tensors = append(tensors, v)
		return nil
	}
	// 顺序与 inputNames 一致。
	if err := mkI64(io.Tones, 1, int64(t)); err != nil {
		return nil, fmt.Errorf("meloort: tones 张量: %w", err)
	}
	if err := mkI64(io.LangIDs, 1, int64(t)); err != nil {
		return nil, fmt.Errorf("meloort: lang_ids 张量: %w", err)
	}
	if err := mkI64([]int64{int64(t)}, 1); err != nil {
		return nil, fmt.Errorf("meloort: lengths 张量: %w", err)
	}
	if err := mkI64([]int64{speakerZH}, 1); err != nil {
		return nil, fmt.Errorf("meloort: sid 张量: %w", err)
	}
	if err := mkF32(io.Bert, 1, meloBertChannels, int64(t)); err != nil {
		return nil, fmt.Errorf("meloort: bert 张量: %w", err)
	}
	if err := mkF32(io.JaBert, 1, meloJaBertChannels, int64(t)); err != nil {
		return nil, fmt.Errorf("meloort: ja_bert 张量: %w", err)
	}
	if err := mkF32(io.SdpNoise, 1, meloSdpNoiseCh, int64(t)); err != nil {
		return nil, fmt.Errorf("meloort: sdp_noise 张量: %w", err)
	}
	if err := mkF32(io.ZNoise, 1, meloZChannels, meloZHeadroom*int64(t)); err != nil {
		return nil, fmt.Errorf("meloort: z_noise 张量: %w", err)
	}
	mkScalar := func(v float32) error {
		sc, e := ort.NewScalar(v) // rank-0 标量（导出图声明 []）
		if e != nil {
			return e
		}
		tensors = append(tensors, sc)
		return nil
	}
	if err := mkScalar(io.NoiseScale); err != nil {
		return nil, fmt.Errorf("meloort: noise_scale 标量: %w", err)
	}
	if err := mkScalar(io.NoiseScaleW); err != nil {
		return nil, fmt.Errorf("meloort: noise_scale_w 标量: %w", err)
	}
	if err := mkScalar(io.LengthScale); err != nil {
		return nil, fmt.Errorf("meloort: length_scale 标量: %w", err)
	}
	if err := mkScalar(io.SdpRatio); err != nil {
		return nil, fmt.Errorf("meloort: sdp_ratio 标量: %w", err)
	}

	// 输出样本数数据相关：nil 占位让 ORT 自动分配（库契约：调用方 Destroy）。
	outputs := []ort.Value{nil}
	if err := s.sess.Run(tensors, outputs); err != nil {
		return nil, fmt.Errorf("meloort: ORT Run: %w", err)
	}
	audioT, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, fmt.Errorf("meloort: 输出非 float32 张量：%T", outputs[0])
	}
	defer func() { _ = audioT.Destroy() }()
	shape := audioT.GetShape()
	if len(shape) != 3 || shape[1] != 1 {
		return nil, fmt.Errorf("meloort: 输出形状 %v ≠ [1,1,n]", shape)
	}
	// 拷贝出张量（张量随即销毁；返回切片与张量生命周期解耦）。
	audio := append([]float32(nil), audioT.GetData()...)
	if len(audio) == 0 {
		return nil, fmt.Errorf("meloort: 合成出空波形（tokens=%d 病态）", t)
	}
	return audio, nil
}

// Destroy 释放会话（进程退出面调用一次即可；输入张量按 Run 生命周期管理）。
func (s *Session) Destroy() error { return s.sess.Destroy() }

// DefaultModelPath 定位 melotts-zh.onnx：env T13_MELO_MODEL → 数据集落盘路径。
func DefaultModelPath() string {
	if p := os.Getenv("T13_MELO_MODEL"); p != "" {
		return p
	}
	return "/root/workspace/datasets/models/melotts-zh/onnx/melotts-zh.onnx"
}

// DefaultLibraryPath 定位 libonnxruntime 共享库：env T13_ORT_LIB → 常见系统路径
// （与 inference/vap 同一份清单，进程级初始化一次，路径首次生效）。
func DefaultLibraryPath() string {
	if p := os.Getenv("T13_ORT_LIB"); p != "" {
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
