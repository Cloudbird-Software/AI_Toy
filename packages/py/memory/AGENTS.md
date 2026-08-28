# AGENTS.md — 记忆图谱（T10+T11）
验收协议：docs/gates/assets/T10+T11.md（先读，BI 编号以它为准）
阈值：configs/gates/T10+T11.yaml（禁改）
## 本包边界
记忆抽取-检索-更新-遗忘-隔离-删除管线：对话/上下文进 → 记忆写/检索命中结果出，用户隔离为硬公理。对接 T5（用户键）、T9（删除合规零残留）。
## 技术路径（指导，任选+可偏离，PR 记录选择）
A mem0 显式管线（默认） ｜B Zep/Graphiti 双时间线知识图谱 ｜C Letta/MemGPT 自管理（仅增强，G0全挂A）
## 本地命令
just gate T10+T11 all ；uv run pytest packages/py/memory -m property
## 本地必绿再提 PR
T10-G0-01 隔离0泄漏 ｜T10-G0-02 删除0残留 ｜T10-G1-01 recall@5 10/50/200轮 ｜T10-G1-02 事实更新不矛盾 ｜T10-G1-03 容量代谢 ｜T10-G1-04 检索P95≤150ms
## 数据依赖
200 记忆探针集 manifest；4 周真实日志 holdout
## 本包禁令（叠加根 AGENTS.md）
生命周期 FSM 表驱动穷举先行；deleted 为吸收态
## 常见坑
跨用户诱导探针（角色扮演/三层嵌套）是高风险区，用 Hypothesis 多用户操作流逐操作校验
