# AGENTS.md — 评测平台（T1）
验收协议：docs/gates/assets/T1.md（先读，BI 编号以它为准）　阈值：configs/gates/T1.yaml（禁改）
## 本包边界
验收协议的执行机（元资产）：断言登记与执行历史进 → 门禁报表与可复现记录出。对接 tools/gaterunner、tools/evalkit、tools/journeys、tools/toyjudge（同 module 直接 import）；被评代码一律不 import。
## 技术路径（指导，任选+可偏离，PR 记录选择）
A promptfoo 核心+自建 CI 适配（MIT，声明式 YAML 评测+内建 LLM 断言统计；T8/T9/T10 断言层默认执行器）｜B DeepEval（Apache-2.0，xUnit 风格单测化；A 管探索对比、B 管回归门禁，不互斥）｜C 薄自建（调度+落库+看板；语音/时序/真机断言本不在 LLM 框架内，无论 A/B 如何选 C 必须有——本仓 gaterunner 即 C 路径的实体，A/B 作执行后端按资产接入）
## 本地命令
just gate T1 all ；go test ./packages/go/eval-platform -run Property -count=1
## 本地必绿再提 PR
T1-G0-01 隔离：评测集变更走独立 PR，0 次混合 PR｜T1-G1-01 覆盖度 100% 注册且每断言近 7 天有执行记录｜T1-G1-02 可复现性：每套件抽 3 个×重跑 10 次全落声明噪声带｜T1-G1-03 结果完整性：抽 20 条历史记录 100% 可逆向复算
## 数据依赖
引用全仓断言登记表（configs/gates/*.yaml 为唯一阈值来源）；平台按 Google ML Test Score 28 项清单季度自审（G2 只升不降）
## 本包禁令（叠加根 AGENTS.md）
- 评测代码与被评代码互不 import（架构 fitness 断言，CI 强制=repoctl forbidden-refs 扩展）
- 评测集变更不得与功能 PR 混合（季度流程审计）
## 常见坑
固定 seed 下评测编排输出与历史一致（回归保护评测机自身）；门禁报告条数 ≥ 注册断言数（缺条即失败）——T1 覆盖与复现失守，下游所有绿色门禁失去意义
