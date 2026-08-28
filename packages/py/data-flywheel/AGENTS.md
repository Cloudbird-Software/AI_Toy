# AGENTS.md — 数据飞轮（T2）
验收协议：docs/gates/assets/T2.md（先读，BI 编号以它为准）
阈值：configs/gates/T2.yaml（禁改）
## 本包边界
合成数据生产 + 真实数据合规回流 + holdout 拓扑隔离 + 飞轮提升测量
## 技术路径（指导，任选+可偏离，PR 记录选择）
A TTS+声学增强合成管线 ｜B LLM 对话合成（T10探针/T7标签自动埋） ｜C 隐私回流管线（PII检测→脱敏→分流）
## 本地命令
just gate T2 all ；uv run pytest packages/py/data-flywheel -m property
## 本地必绿再提 PR
T2-G0-01 holdout零污染 ｜T2-G0-02 脱敏召回 ｜T2-G1-01 多样性 ｜T2-G1-02 飞轮转速
## 数据依赖
synthgen 管线注册；回流授权数据 manifest
## 本包禁令（叠加根 AGENTS.md）
holdout 写入者 ⊆ {评测服务}；每用户回流 ≤ 授权上限
## 常见坑
近重复检测必须用 minhash 而非精确匹配——近似重复污染训练集是最常见的假提升
