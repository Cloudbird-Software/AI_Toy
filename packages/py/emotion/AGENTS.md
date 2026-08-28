# AGENTS.md — 情绪引擎（T7）
验收协议：docs/gates/assets/T7.md（先读，BI 编号以它为准）
阈值：configs/gates/T7.yaml（禁改）
## 本包边界
情绪检测+状态动力学+恢复：语音/文本/事件进 → 多维情绪状态 + 事件标签 出。对接 T8（情绪影响人格表达）、T12（动作映射输入）、T9（越界检查）。
## 技术路径（指导，任选+可偏离，PR 记录选择）
A OCC显式规则+低维动力学（默认） ｜B 学习型检测+积分器 ｜C LLM内隐情绪（仅对照基线）
## 本地命令
just gate T7 all ；uv run pytest packages/py/emotion -m property
## 本地必绿再提 PR
T7-G0-01 全情绪扫描过T9 ｜T7-G1-01 方向一致≥85% ｜T7-G1-02 跳变≤0.3 ｜T7-G1-03 30min可恢复 ｜T7-G1-04 检测延迟P95≤900ms
## 数据依赖
300 情绪事件合成集 manifest；真实≥50 段 holdout（经 tools/holdout）
## 本包禁令（叠加根 AGENTS.md）
状态实现必须纯函数可复现；随机只许在表达层
## 常见坑
情绪动力学吸收态（卡死在伤心）是高频 bug，用属性测试兜底
