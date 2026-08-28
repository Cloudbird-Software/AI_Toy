# AGENTS.md — 场景包（T16）
验收协议：docs/gates/assets/T16.md（先读，BI 编号以它为准）
阈值：configs/gates/T16.yaml（禁改）
## 本包边界
场景包安装/卸载/升级 + 权限沙箱 + 随包评测执行：manifest+资源进 → 运行时可引用的包实例出。对接 T8（人格）、T13（声音）、T9（内容安全0违规）。
## 技术路径（指导，任选+可偏离，PR 记录选择）
A 声明式 manifest + JSON Schema 校验（默认） ｜B 能力沙箱/受限解释器 ｜C T18 内容管线（批量生产）
## 本地命令
just gate T16 all ；uv run pytest packages/py/packs -m property
## 本地必绿再提 PR
T16-G1-01 包隔离0外溢 ｜T16-G1-02 schema/资源/评测集齐备 ｜T16-G0-01 包内容安全0违规 ｜T16-G0-02 安装原子性 ｜T16-G1-03 评测随包执行
## 数据依赖
configs/packs/schema.json；assets-packs/* 包目录
## 本包禁令（叠加根 AGENTS.md）
权限白名单默认拒绝；卸载后全通道复查
## 常见坑
Hypothesis 生成安装/卸载/升级交错序列——原子性是高频失败区
