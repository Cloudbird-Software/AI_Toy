# AGENTS.md — 创始人控制台（T1 元资产视图）
验收协议：docs/gates/assets/T1.md　阈值：configs/gates/T1.yaml（禁改）
## 本包边界
报表前端 + 门禁聚合视图 + 豁免 / 审计台账 + D1–D6 毕业卡面板。只读消费 reports/** 与 datasets/manifests；不直接进入数据本体。
## 技术路径（指导）
A React + Vite + D3（默认）｜B 纯 HTML 单页 + 原生 JS（最简）｜C Grafana/Metabase 嵌入（看板复用）
## 本地命令
pnpm --filter @ai-toy/founder-console lint ；pnpm --filter @ai-toy/founder-console test
## 本地必绿再提 PR
T1-G1-01 覆盖度 100% + 近 7 天执行｜T1-G1-03 结果 100% 可逆向复算
## 数据依赖
reports/gates/*.json、reports/nightly/**、reports/exemptions.yaml、reports/holdout-audit.jsonl。不得直接读 datasets/holdout 本体。
## 本包禁令（叠加根 AGENTS.md）
- 控制台不得对 holdout 数据做任何 join/切片（仅 holdoutctl 聚合结果允许展示）
- 豁免编辑走 PR，禁止 console 直接写文件
## 常见坑
门禁报表列排序：G0 失败永远置顶；过期豁免显示为「红」而非「灰」。
