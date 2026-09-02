// Package gaterunner 是 Mark 登记的公开面（IR #73 最小扩展）：tests/ 等仓内包
// 受 Go internal 可见性约束无法 import tools/gaterunner/internal——而门禁测试
// 按 AGENTS.md/spec §6 约定书写 gaterunner.Mark(t, asset, bi, id, level)（collect
// 源码扫描正则按该调用文本登记）。本包以同名包薄转发 internal 真实现（含非法
// 参数 panic 校验与进程内登记表），无任何行为新增；阈值与调度语义仍在 internal。
package gaterunner

import (
	"fmt"
	"testing"

	internal "github.com/Cloudbird-Software/AI_Toy/tools/gaterunner/internal/gaterunner"
)

// Mark 在门禁测试内登记断言（转发 internal 实现；语义与签名一致）。
func Mark(t testing.TB, asset, bi, id, level string) {
	internal.Mark(t, asset, bi, id, level)
}

// Observe 在门禁测试内声明观测值（issue #116）：输出 `GATE-OBSERVE <metric> <value>`
// 结构化标记行（stdout 直通面，不用 t.Log——其输出带文件:行号前缀）。gaterunner
// run 的 go_test_exit_code 路径解析该标记回填报告 observed 字段；metric 须与该门禁
// configs 声明的 metric 一致才会被采信。无标记时报告 observed=null（未采集）。
func Observe(t testing.TB, metric string, value float64) {
	t.Helper()
	fmt.Printf("GATE-OBSERVE %s %g\n", metric, value)
}
