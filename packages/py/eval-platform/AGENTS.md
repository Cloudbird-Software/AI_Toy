# AGENTS.md — 评测平台（T1）
验收协议：docs/gates/assets/T1.md（先读，BI 编号以它为准）
阈值：configs/gates/T1.yaml（禁改）
## 本包边界
元资产——gaterunner + 评测断言登记表管理 + 逆向复算能力
## 技术路径（指导，任选+可偏离，PR 记录选择）
A promptfoo 核心 + CI 适配 ｜B DeepEval 回归门禁 ｜C 薄自建调度落库（默认 gaterunner 即 C）
## 本地命令
just gate T1 all ；uv run pytest packages/py/eval-platform -m property
## 本地必绿再提 PR
T1-G1-01 覆盖度100% ｜T1-G1-02 可复现性 ｜T1-G0-01 评测隔离 ｜T1-G1-03 逆向复算
## 数据依赖
引用全仓断言登记表（gaterunner collect 产出）
## 本包禁令（叠加根 AGENTS.md）
评测代码与被评代码互不 import（fitness 断言）
## 常见坑
门禁机器自身的回归——固定 seed 下历史一致性断言先行
