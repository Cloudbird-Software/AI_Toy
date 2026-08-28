# AGENTS.md — 路由缓存（T15）
验收协议：docs/gates/assets/T15.md（先读，BI 编号以它为准）
阈值：configs/gates/T15.yaml（禁改）
## 本包边界
路由决策+语义缓存：query+context+user+role 进 → 路由目标(edge/cache/mid/flagship) + 缓存命中结果 出。对接 T14（当前档位）、T9（安全敏感永不缓存命中）。
## 技术路径（指导，任选+可偏离，PR 记录选择）
A 自建语义缓存（默认） ｜B 级联路由FrugalGPT ｜C 学习型RouteLLM（数据飞轮点）
## 本地命令
just gate T15 all ；uv run pytest packages/py/router -m property
## 本地必绿再提 PR
T15-G0-01 对抗误命中=0 ｜T15-G1-01 路由≥92% ｜T15-G1-02 命中≥30%/降本≥40% ｜T15-G1-03 决策P95≤30ms
## 数据依赖
对抗对 200 组 manifest；30 天仿真流；真实 query 脱敏 holdout
## 本包禁令（叠加根 AGENTS.md）
θ 双曲线必须先测后选点；语气词改写不得改变路由
## 常见坑
孩子的问法（语气词/倒装/半句话）与仿真分布完全不同——真实分布 holdout 必跑
