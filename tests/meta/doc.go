// Package meta 承载 T1 元层门禁（IR #73 / W10-C2）：T1 是元资产——验收机器自己
// 也要过验收。四个门禁测试（BI-1.3 mixed-PR 隔离 / BI-1.1 执行记录率 / BI-1.2
// 重跑一致性 / BI-1.2 记录完整性）即门禁本体：经 tools/gaterunner/gaterunner 的
// Mark 登记、由 gaterunner run 的 dispatchGate 实跑（evidence=真实 go test 命令串，
// 退出码=verdict）。阈值与口径声明在 configs/gates/T1.yaml，验收协议见
// docs/gates/assets/T1.md。
package meta
