// Package budgets implements the latency budget ledger (spec §3.6).
//
// CLI：
//
//	budgets check --report reports/nightly/latency.json [--config configs/budgets/latency.yaml]
//	budgets ledger [--history reports/nightly/latency-history.json] [--days 30]
//
// 退出码：0 通过；20 守恒违反 / 存在 >2σ 劣化段；2 输入不可读或不符合 schema。
//
// 夜间延迟报告 latency.json（JSON 对象，ledger 的 history 数组元素同此格式）：
//
//	{
//	  "commit": "a1b2c3",                    // 产生该报告的 commit（展示用）
//	  "timestamp": "2026-08-28T00:00:00Z",   // ISO-8601（展示用）
//	  "overlap_ms": 50,                      // 可选：并行段重叠（默认 0，非负）
//	  "segments": [                          // 段 id 须与预算配置一致
//	    {"id": "tail_silence", "p50": 450, "p95": 600}
//	  ]
//	}
//
// ledger 历史为单文件 {"history": [报告, ...]}，按时间升序，末尾为最新；
// 趋势窗口取最近 N 份报告（--days，默认 30 即「近 30 天」）。
package budgets
