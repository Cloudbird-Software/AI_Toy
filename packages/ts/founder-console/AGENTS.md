# AGENTS.md — 创始人验收控制台（资产编号 —）
验收协议：—（§7.3 表未覆盖本包，暂无 docs/gates/assets/ 卡）
阈值：—（暂无 configs/gates/*.yaml 对应条目）

> 规格表未覆盖，本文件由执行者按模板推断生成，待 spec 增补。

## 本包边界
创始人验收控制台/看板：读 reports/（gates/nightly 门禁历史、reports/exemptions.yaml 豁免台账）→ 验收视图与 G2 趋势看板出；含演示资产（D6 数据看板、家长控制台记忆查看/一键删除验证入口）。只读消费工具产物，不产出门禁判定。
## 技术路径（指导，可偏离，PR 记录）
待 spec 增补（§7.3 未覆盖）。保守基线：pnpm + biome + vitest（§0 语言决策）。
## 本地命令
pnpm lint ；pnpm test（vitest，与 CI ts job 同口径）
## 本地必绿再提 PR
—（无已登记资产门禁 id，不得虚构；本包为验收视图层，门禁判定以 tools/gaterunner 产物为准）
## 数据依赖
reports/gates/*.json、reports/nightly/、reports/exemptions.yaml（committed 产物，只读）；不含 holdout 数据本体与儿童 PII
## 本包禁令（叠加根 AGENTS.md）
- 控制台只读：不得提供改阈值/改门禁/删豁免的入口（阈值与验收协议变更只有 founder PR）
- holdout 数据与儿童 PII 不得进看板
- 不得把 G2（趋势警告）渲染为「绿灯通过」；豁免须展示到期日（≤30 天自动过期）
## 常见坑
本文件为推断生成，角色/边界以 spec 增补为准；门禁状态以 reports/ 产物为准，不要在控制台内重算或缓存出第二套口径；演示路径（10 分钟三房间之记忆房）依赖 T10 一键删除断言，控制台只是入口不是判定者
