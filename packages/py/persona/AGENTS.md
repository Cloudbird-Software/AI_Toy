# AGENTS.md — 人格编译器（T8）
验收协议：docs/gates/assets/T8.md（先读，BI 编号以它为准）
阈值：configs/gates/T8.yaml（禁改）
## 本包边界
角色卡→可执行人格配置编译：persona.yaml进 → system_prompt+few_shot+sampling_params+word_constraints 编译产物出（确定性哈希）。对接 T9（安全编译检查）、T13（声音锚定）。
## 技术路径（指导，任选+可偏离，PR 记录选择）
A 结构化人格DSL编译（默认） ｜B RoleLLM角色微调（旗舰角色） ｜C 双通道=A打底+风格LoRA
## 本地命令
just gate T8 all ；uv run pytest packages/py/persona -m property
## 本地必绿再提 PR
T8-G0-01 编译产物过T9攻击集0突破 ｜T8-G1-01 编译确定性100% ｜T8-G1-02 问卷偏差≤1分 ｜T8-G1-03 崩人≤3% ｜T8-G1-04 区分度≥80%
## 数据依赖
角色卡（assets-packs）；30 轮×3 采样问卷（judge rubric-8a/8b）
## 本包禁令（叠加根 AGENTS.md）
judge 问卷前必须先过金标 κ≥0.61；换声=角色资产变更须重过 rubric-13a
## 常见坑
诱导崩人测试用三层嵌套（直白/角色扮演/meta），直白太容易过
