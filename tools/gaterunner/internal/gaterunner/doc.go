// Package gaterunner implements the gate runner (spec §3.1)：读 configs/gates/<asset>.yaml
// 阈值 → 调度注册标记的测试 → 收集指标 → 按统计规则判定 → 产出 JSON 报告。阈值永不
// 硬编码在测试里。
//
// CLI：collect（断言登记表）/ verify-configs（schema+统计纪律校验，违反 exit 2）/
// calibrate（噪声带建议）/ run（门禁执行+报告）。退出码：0 全绿 / 10 任一 G0 红 /
// 20 任一 G1 红 / 30 仅 G2 / 2 配置错误。报告 schema 照抄规格 §3.1 JSON 块。
//
// 测试注册约定（spec §6）：门禁测试内打 gaterunner.Mark(t, "T4", "BI-4.2",
// "T4-G0-01", "G0")；collect 扫描 *_test.go 源码收集登记表（Go 无原生 marker）。
//
// 统计底座一律 evalkit（tools/evalkit/evalkit 公开包），统计断言不得手算。
package gaterunner
