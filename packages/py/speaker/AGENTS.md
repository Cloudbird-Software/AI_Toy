# AGENTS.md — 声纹识别（T5）
验收协议：docs/gates/assets/T5.md（先读，BI 编号以它为准）
阈值：configs/gates/T5.yaml（禁改）
## 本包边界
声纹注册与家庭内闭集识别 + 陌生人拒判：音频片段进 → speaker_id/拒判/置信度 出。对接 T10（记忆隔离键）、T9（身份不确定进入只读模式）。
## 技术路径（指导，任选+可偏离，PR 记录选择）
A SpeechBrain ECAPA-TDNN（默认） ｜B 3D-Speaker/CAM++ ｜C 共享骨干+家庭轻量头
## 本地命令
just gate T5 all ；uv run pytest packages/py/speaker -m property
## 本地必绿再提 PR
T5-G0-01 跨成员0泄漏 ｜T5-G1-01 家庭内EER≤5%（trial≥5000） ｜T5-G1-02 跨会话稳定性 ｜T5-G1-03 3句注册劣化≤2pp ｜T5-G1-04 陌生人拒判≥90%
## 数据依赖
合成虚拟家庭（2–6 人含儿童）manifest；真实≥5家庭 holdout（经 tools/holdout）
## 本包禁令（叠加根 AGENTS.md）
VoxCeleb 数字不可迁移，只做选型初筛；兄弟姐妹对单独报告
## 常见坑
儿童声纹随年龄变化快，会话间校准需轻量重注册流程
