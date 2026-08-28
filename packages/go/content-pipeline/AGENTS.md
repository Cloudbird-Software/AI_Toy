# AGENTS.md — 内容管线（T18）
验收协议：docs/gates/assets/T16.md（T16+T18 合卡，先读，BI 编号以它为准）　阈值：configs/gates/T16.yaml（禁改）
## 本包边界
内容生产管线：LLM 批量生成内容（角色卡/剧本/知识）进 → 过审内容包条目出（带溯源戳）。对接 T9（内容安全全集）、T8（人格一致性）、T13（音色契合）、packs（入包与 eval_set）。
## 技术路径（指导，任选+可偏离，PR 记录选择）
C（本包主线）T18 内容管线：LLM 批量生成→自动过 T9 全集+T8 一致性+人工抽审→入包，全带溯源（模型+prompt 版本），产能决定上架速度；合卡其余路径（A 声明式包格式=packs 地基、B 能力沙箱）见 docs/gates/assets/T16.md 路径块
## 本地命令
just gate T16 all ；just gate T9 all ；just gate T8 all ；just gate T13 all ；go test ./packages/go/content-pipeline -run Property -count=1
## 本地必绿再提 PR
内容全量过 T9+T8+T13 评审后方可入包（T18 无独立 gate id，随 T16 合卡执行：T16-G0-01 包内容安全 0 违规、T16-G1-02 缺 eval_set 拒绝构建）
## 数据依赖
溯源戳（模型+prompt 版本）随每条内容入包；生成原料（角色卡+场景模板）经 synthgen 注册（datasets/manifests/content_synth.json）；人工抽审流程照 docs/gates/assets/T16.md
## 本包禁令（叠加根 AGENTS.md）
- 未过门禁内容不得入包（内容安全不可豁免）
- 溯源戳缺失的内容视为未过门禁
## 常见坑
T18 生成内容的评审分别按 T9/T8/T13 各自协议走（T8 问卷金标 κ≥0.61、换声重过 rubric-13a），别自造评审标准；缓存/预生成内容变更后须整批重跑溯源
