// Package gaterunner 是 Mark 登记的公开面（IR #73 最小扩展）：tests/ 等仓内包
// 受 Go internal 可见性约束无法 import tools/gaterunner/internal——而门禁测试
// 按 AGENTS.md/spec §6 约定书写 gaterunner.Mark(t, asset, bi, id, level)（collect
// 源码扫描正则按该调用文本登记）。本包以同名包薄转发 internal 真实现（含非法
// 参数 panic 校验与进程内登记表），无任何行为新增；阈值与调度语义仍在 internal。
package gaterunner

import (
	"testing"

	internal "github.com/Cloudbird-Software/AI_Toy/tools/gaterunner/internal/gaterunner"
)

// Mark 在门禁测试内登记断言（转发 internal 实现；语义与签名一致）。
func Mark(t testing.TB, asset, bi, id, level string) {
	internal.Mark(t, asset, bi, id, level)
}
