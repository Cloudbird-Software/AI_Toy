// Package repoctl implements the meta-gate (spec §3.8) —— 验收机器自持 D5 的执行体。
//
// CLI（退出码：0 通过；20 门禁红；2 输入不可读或环境错误）：
//
//	repoctl coverage                                  # gaterunner 登记表 × docs BI 集合三重核对
//	repoctl agents-md check                           # 根 + packages/*/* 的 AGENTS.md 必需小节
//	repoctl forbidden-refs                            # 全仓扫 holdout 数据本体路径引用（白名单外即红）
//	repoctl exemption audit                           # reports/exemptions.yaml 过期项 → 20
//	repoctl fetch-models --manifest models/manifests  # 按清单拉权重 + sha256 校验（桩：file:// 源）
//	repoctl affected --base <ref>                     # git diff 路径 → 受影响资产 JSON（ci.yml changes 消费）
//
// 登记表数据源 = gaterunner run 落盘的 reports/gates/*.json（asset + results[]，
// 兼容顶层列表与 assertions 键——契约对齐已关闭的 Python 版 PR #22）。
package repoctl
