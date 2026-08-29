// Package kws —— T4 唤醒词检测核心（M1，IR #79 / m1-spec §2 包契约 A）。
//
// 纯 Go 事件管道：音频帧进（Frame），唤醒事件出（Event）。同步、无 IO、
// 不依赖网络——BI-4.3 端侧常驻语义的实现基座。推理接口化（Inferencer）：
// M1 注入内置启发式桩（无唤醒语义），M2 换 ONNX 真模型只换注入不改结构
// （ADR-0004）。依赖纪律：import 白名单=标准库。
//
// 状态机（内部，单流串行无并发——资产卡「表驱动单测足够」）：
//
//	idle（滑窗累积 + 防抖计数）──连续 ConfirmFrames 帧超阈──▶ EvWake
//	   ▲                                              │
//	   └────── 不应期倒计时（RefractoryMs 内重复触发抑制）◀┘
//
// 错误语义：仅 NewDetector 校验配置返回 error；Push 对畸变输入按零能量帧
// 处理（EvNone），永不 error/panic。
package kws

import (
	"errors"
	"math"
)

// Frame 一帧音频输入。TS 单调由调用方保证；PCM 为 16kHz mono（帧长
// FrameMs×16 样本），nil 时用 Feats——Feats 非空则跳过包内预处理直供
// Inferencer（外接预处理/模拟置信度注入通道）。
type Frame struct {
	TS    int64
	PCM   []int16
	Feats []float32
}

// EventKind 事件类型（零值=EvNone——驱动层可零值初始化事件缓冲）。
type EventKind int8

const (
	EvNone EventKind = 0
	EvWake EventKind = 1
)

// Event 唤醒检测结果。Confidence∈[0,1]；EvNone 时=当帧滑窗峰值（可观测——
// 调用方拿连续置信度流做阈值调谐/噪声带标定）。
type Event struct {
	Kind       EventKind
	Confidence float64
	AtMs       int64
}

// Inferencer 帧级推理器。纯函数承诺：同帧同判定（T4 属性接口级承诺——
// P4 确定性的实现面；真模型接入后同组属性重跑即可）。
type Inferencer interface {
	Infer(f Frame) float64
}

// ConfidenceFunc 函数式 Inferencer（模拟置信度注入点：测试/回放器直用
// 普通函数，无需定义类型）。
type ConfidenceFunc func(f Frame) float64

// Infer 实现 Inferencer。
func (f ConfidenceFunc) Infer(fr Frame) float64 { return f(fr) }

// TierBudget T14 档位预算镜像（语义对齐 tests/properties/contract.go 的
// RuntimeModel.TierCaps，但不 import tests/——防「对着考卷优化」，ADR-0004）。
// M1 预留不接线：nil=默认档位表。
type TierBudget interface {
	KWSMemLimitBytes(tier int) int
}

// Config 检测器配置。零值可用性：FrameMs 零值取默认 30；其余字段零值须
// 满足各自约束（ConfirmFrames≥1、RefractoryMs≥0、Threshold∈[0,1]）。
type Config struct {
	FrameMs       int        // 帧长 ms（默认 30；合法域 (0,200]）
	ConfirmFrames int        // 防抖 N 帧（≥1）：连续 N 帧超阈才发 Wake
	RefractoryMs  int        // 不应期 ms（≥0）：Wake 后抑制重复触发
	Threshold     float64    // 置信度门限 [0,1]（「超阈值」= 严格 >）
	Infer         Inferencer // nil=内置启发式桩（能量+过零，无唤醒语义）
	Budget        TierBudget // nil=默认档位表（M1 预留）
}

// Detector 滑窗流式唤醒检测器。单流串行使用（不加锁——资产卡定性），
// Push 同步返回当帧事件。
type Detector struct {
	frameMs      int
	confirm      int
	refractoryMs int
	threshold    float64
	infer        Inferencer
	budget       TierBudget

	streak    int       // 连续超阈帧计数（防抖）
	inRefr    bool      // 是否处于不应期
	refrUntil int64     // 不应期到期时刻 ms
	win       []float64 // 滑窗（ring，长度=ConfirmFrames）
	winPos    int       // ring 写指针
}

// NewDetector 构造检测器：仅此处校验配置（FrameMs∈(0,200]（0 取默认 30）/
// Threshold∈[0,1]/ConfirmFrames≥1/RefractoryMs≥0）。
func NewDetector(cfg Config) (*Detector, error) {
	if cfg.FrameMs == 0 {
		cfg.FrameMs = 30
	}
	if cfg.FrameMs < 0 || cfg.FrameMs > 200 {
		return nil, errors.New("kws: FrameMs 须 ∈ (0, 200]")
	}
	if cfg.ConfirmFrames < 1 {
		return nil, errors.New("kws: ConfirmFrames 须 ≥ 1")
	}
	if cfg.RefractoryMs < 0 {
		return nil, errors.New("kws: RefractoryMs 须 ≥ 0")
	}
	if cfg.Threshold < 0 || cfg.Threshold > 1 {
		return nil, errors.New("kws: Threshold 须 ∈ [0, 1]")
	}
	infer := cfg.Infer
	if infer == nil {
		infer = heuristicInferencer{}
	}
	return &Detector{
		frameMs:      cfg.FrameMs,
		confirm:      cfg.ConfirmFrames,
		refractoryMs: cfg.RefractoryMs,
		threshold:    cfg.Threshold,
		infer:        infer,
		budget:       cfg.Budget,
		win:          make([]float64, cfg.ConfirmFrames),
	}, nil
}

// Push 推入一帧，返回当帧事件。畸变输入（空 PCM+空 Feats/短帧/极值帧）按
// 零能量帧处理（EvNone），永不 error/panic。
func (d *Detector) Push(f Frame) Event {
	conf := clip01(d.infer.Infer(f))
	d.win[d.winPos] = conf
	d.winPos = (d.winPos + 1) % len(d.win)
	peak := d.windowPeak()
	ev := Event{Kind: EvNone, Confidence: peak, AtMs: f.TS}

	if d.inRefr {
		if f.TS < d.refrUntil { // 不应期内：不入防抖计数（到期即回 idle 重新累积）
			d.streak = 0
			return ev
		}
		d.inRefr = false
	}

	if conf > d.threshold {
		d.streak++
		if d.streak >= d.confirm { // 防抖：连续 ConfirmFrames 帧超阈才发 Wake
			d.streak = 0
			if d.refractoryMs > 0 {
				d.inRefr, d.refrUntil = true, f.TS+int64(d.refractoryMs)
			}
			ev.Kind = EvWake
		}
	} else {
		d.streak = 0
	}
	return ev
}

// windowPeak 滑窗峰值（当帧可观测置信度）。窗口长度=ConfirmFrames（防抖窗
// 即观测窗——「滑窗累积」与「防抖计数」同窗）。
func (d *Detector) windowPeak() float64 {
	m := 0.0
	for _, v := range d.win {
		if v > m {
			m = v
		}
	}
	return m
}

// WindowLen 滑窗容量（常驻内存有界性观测面：不随流长增长）。
func (d *Detector) WindowLen() int { return len(d.win) }

// MemLimitBytes 档位内存上限（T14 预算镜像透传；M1 预留不接线——仅供
// 驱动层装配时读取预算做容量规划，不做运行时强制）。
func (d *Detector) MemLimitBytes(tier int) int {
	if d.budget != nil {
		return d.budget.KWSMemLimitBytes(tier)
	}
	return defaultKWSMemLimitBytes(tier)
}

// defaultKWSMemLimitBytes 默认档位表（镜像 configs/runtime/tiers.yaml 四档：
// L0 云端全能力 ⊇ L1 云端中档 ⊇ L2 端侧小模型 ⊇ L3 受限剧本——端侧常驻
// 组件内存上限随档位收紧；越界档位 0）。
func defaultKWSMemLimitBytes(tier int) int {
	switch tier {
	case 0, 1:
		return 4 << 20 // 4 MiB
	case 2:
		return 2 << 20 // 2 MiB
	case 3:
		return 1 << 20 // 1 MiB
	default:
		return 0
	}
}

// heuristicInferencer 内置启发式桩（spec：能量+过零，无唤醒语义）。
// 尺度不变设计（P1 增益不变性的实现基础）——无绝对幅度门限：
//
//		conf = 0.6×lowBandPowerRatio + 0.4×(1−zcr)
//
//	  lowBandPowerRatio：一阶 IIR 低通（fc≈500Hz）后能量 / 总能量——低频
//	    语音带占位（唤醒词主能量区）高、宽带白噪声低；比值对整体增益不变，
//	    加噪时分母增速快于分子 → 单调不升（P5 SNR 单调的实现基础）。
//	  zcr：过零率（尺度不变）——低频信号低、白噪声 ≈0.5。
//
// Feats 非空时直供：取 Feats[0] clip 到 [0,1]（跳过包内预处理）。
type heuristicInferencer struct{}

// Infer 实现 Inferencer。
func (heuristicInferencer) Infer(f Frame) float64 {
	if len(f.Feats) > 0 {
		return clip01(float64(f.Feats[0]))
	}
	x := f.PCM
	if len(x) < 2 {
		return 0 // 畸变/零能量帧
	}
	const alpha = 0.179 // 1−exp(−2π·500/16000)：一阶低通系数（16kHz）
	var sumSq, lpSq float64
	var cross int
	lp := 0.0
	for i, s := range x {
		v := float64(s)
		sumSq += v * v
		if i > 0 && (s >= 0) != (x[i-1] >= 0) {
			cross++
		}
		lp += alpha * (v - lp)
		lpSq += lp * lp
	}
	if sumSq <= 0 {
		return 0 // 静音帧
	}
	lowRatio := lpSq / sumSq
	zcr := float64(cross) / float64(len(x)-1)
	return clip01(0.6*lowRatio + 0.4*(1-zcr))
}

func clip01(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
