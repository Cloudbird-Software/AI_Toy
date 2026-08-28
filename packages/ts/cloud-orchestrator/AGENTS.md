# AGENTS.md — 云侧编排（资产编号 —）
验收协议：—（§7.3 表未覆盖本包，暂无 docs/gates/assets/ 卡）
阈值：—（暂无 configs/gates/*.yaml 对应条目）

> 规格表未覆盖，本文件由执行者按模板推断生成，待 spec 增补。

## 本包边界
云侧会话编排与 LLM 接入（云档 L0/L1，见 configs/runtime/tiers.yaml）：端侧上行请求进 → 云侧能力链路（LLM/云端 TTS/安全防线）编排出。对接 T15（路由的云侧承接）、T9（云档 full_cloud 防线不可旁路）、T13（cloud_stream）、configs/budgets/latency.yaml（云档全链路 P95≤1500ms，组合级）。
## 技术路径（指导，可偏离，PR 记录）
待 spec 增补（§7.3 未覆盖）。保守基线：pnpm + biome + vitest（§0 语言决策）；LLM prompt 契约经 baml/（类型化契约），不内联裸调。
## 本地命令
pnpm lint ；pnpm test（vitest，与 CI ts job 同口径）
## 本地必绿再提 PR
—（无已登记资产门禁 id，不得虚构；云档 P95≤1500ms 为组合级断言，经 tests/golden-journeys 与 just budgets 验证，不在本包单独必绿）
## 数据依赖
本包不直接持有数据集；holdout 一律经 tools/holdout，本包代码不得直接读；LLM 评审一律走 tools/judge（根 AGENTS.md），不得内联裸调
## 本包禁令（叠加根 AGENTS.md）
- LLM 调用不得绕过 baml/ 契约或 tools/judge 内联裸调
- 云档安全防线（T9 full_cloud）不得因编排层旁路或降级
- 不触碰 datasets/holdout/**；不改 configs/gates/**；延迟预算（configs/budgets）变更须带划拨说明
## 常见坑
本文件为推断生成，角色/边界以 spec 增补为准；云档延迟是全链路组合数字（ASR/LLM/TTS/RTT 各有预算位，见 configs/budgets/latency.yaml），单环节优化不等于达标；TS 包不在根 Go module 内，与 Go 侧协作走服务/契约边界而非 import
