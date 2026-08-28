# AGENTS.md — 用户模拟器（T20）
验收协议：docs/gates/assets/T20.md（先读，BI 编号以它为准）
阈值：configs/gates/T20.yaml（禁改）
## 本包边界
儿童行为仿真对话代理：画像参数+剧本骨架 进 → 下一轮用户话语/打断动作出。对接 tests/golden-journeys（旅程驱动）、T9（安全攻击面可达性）。
## 技术路径（指导，任选+可偏离，PR 记录选择）
A 人格化LLM代理+儿童化后处理（默认） ｜B 剧本化+参数化扰动 ｜C A/B 混合（推荐终态）
## 本地命令
just gate T20 all ；uv run pytest packages/py/user-sim -m property
## 本地必绿再提 PR
T20-G0-01 模拟对话0进训练集 ｜T20-G1-01 拟真度≤75%（≥90%禁用） ｜T20-G1-02 边界行为可达≥95% ｜T20-G1-03 行为可控性
## 数据依赖
真实 holdout 对话≥50 段作标尺（经 tools/holdout）
## 本包禁令（叠加根 AGENTS.md）
模拟器不得读被测系统内部状态
## 常见坑
模拟器一眼假（判别准确率≥90%）= 所有基于它的旅程测试全无效——拟真度先达标再跑验收
