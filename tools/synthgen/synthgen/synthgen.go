// Package synthgen 是负样本帧流的公开面（m2-spec §2/§10，IR #90）：packages 侧
// 门禁测试（如 packages/go/kws 的 T4 门禁）受 Go internal 可见性约束无法 import
// tools/synthgen/internal——而门禁须以 gen-tneg/gen-kwsadv 冻结帧流真实驱动被测
// 检测器。本包以同名包薄转发 internal 冻结实现（类型别名零拷贝），无任何行为
// 新增；源类型参数集冻结与版本纪律仍在 internal（改参数=新 version 重新注册）。
package synthgen

import (
	internal "github.com/Cloudbird-Software/AI_Toy/tools/synthgen/internal/synthgen"
)

// 冻结生成器标识/版本与帧口径常量（转发 internal 冻结值）。
const (
	TNegGeneratorID   = internal.TNegGeneratorID
	KWSAdvGeneratorID = internal.KWSAdvGeneratorID
	TNegImplVersion   = internal.TNegImplVersion
	KWSAdvImplVersion = internal.KWSAdvImplVersion
	NegFrameMs        = internal.NegFrameMs
	NegSampleRate     = internal.NegSampleRate
	NegPurpose        = internal.NegPurpose
)

// 类型别名：生成器记录与负样本帧流/批（零拷贝薄转发，契约见 internal 包文档）。
type (
	Generator = internal.Generator
	NegFrame  = internal.NegFrame
	NegStream = internal.NegStream
	NegBatch  = internal.NegBatch
)

// TNegGen / KWSAdvGen 冻结生成器记录（免注册直用面——门禁测试侧取冻结参数集）。
func TNegGen() Generator { return internal.TNegGen() }

func KWSAdvGen() Generator { return internal.KWSAdvGen() }

// NewTNegStream 家庭音景负样本帧流（gen-tneg 冻结参数集，16kHz mono int16 PCM）。
func NewTNegStream(durationMs int, seed int64) (*NegStream, error) {
	return internal.NewTNegStream(durationMs, seed)
}

// NewKWSAdvStream 对抗同音节负样本帧流（gen-kwsadv 冻结参数集）。
func NewKWSAdvStream(durationMs int, seed int64) (*NegStream, error) {
	return internal.NewKWSAdvStream(durationMs, seed)
}

// NegSeed 门禁/调用方种子：FNV-1a 64 对齐全仓约定（label 唯一 → 种子唯一）。
func NegSeed(label string) int64 { return internal.NegSeed(label) }

// GenerateBatchNeg 生成负样本批并落盘（manifest.json + frames.jsonl，eval-only
// 不切 synth-holdout；PCM 由 (generator@version, seed, duration_ms) 确定性重建）。
func GenerateBatchNeg(g Generator, batchesDir string, durationMs int, seed int64) (NegBatch, string, error) {
	return internal.GenerateBatchNeg(g, batchesDir, durationMs, seed)
}

// ReadNegBatch 在 batchesDir 下按目录序取 generatorID 的首个负样本批 manifest。
func ReadNegBatch(batchesDir, generatorID string) (NegBatch, string, error) {
	return internal.ReadNegBatch(batchesDir, generatorID)
}
