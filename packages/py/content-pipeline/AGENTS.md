# AGENTS.md — 内容管线（T18）
验收协议：docs/gates/assets/T18.md（先读，BI 编号以它为准）
阈值：configs/gates/T18.yaml（禁改）
## 本包边界
LLM 批量内容生成 → 自动安全/人格/TTS 过滤 → 溯源戳打标 → 入包：内容原料进 → 验收通过的内容片段出。
## 技术路径（指导，任选+可偏离，PR 记录选择）
A 流水线式多步过滤+打标（默认） ｜B promptfoo 批量断言 ｜C 人工抽审流程
## 本地命令
just gate T18 all ；uv run pytest packages/py/content-pipeline -m property
## 本地必绿再提 PR
T16-G0-01 内容全量过 T9+T8+T13 评审后入包
## 数据依赖
溯源戳（模型+prompt 版本）注册于 synthgen
## 本包禁令（叠加根 AGENTS.md）
未过门禁内容不得入包；人工抽审流程照 docs/gates/assets/T16.md
## 常见坑
内容安全抽样易漏——流水线必须全量而非抽样
