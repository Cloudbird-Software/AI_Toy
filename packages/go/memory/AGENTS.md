# AGENTS.md — 记忆图谱（T10+T11）
验收协议：docs/gates/assets/T10.md（T10+T11 合卡，先读，BI 编号以它为准）　阈值：configs/gates/T10.yaml（禁改）
## 本包边界
记忆写入/检索/更新/遗忘/删除+向量底座：对话事件与用户身份进 → 记忆图谱（T10）与向量检索（T11 底座）出。对接 T5（身份决定隔离，G0 联跑）、T9（删除合规联跑）、T15（单轮记忆成本预算）。
## 实现状态（M3，IR #105；M2 真推理嵌入，issue #134）
核心已实现（进程内图存储=路径 A 自建轻量版：节点/边、UserID 域隔离、新值替换事实更新、递归删除五通道零残留、拒判只读联动、probe 检索，m3-spec §4 包契约 C；T11 底座对验收协议透明）；属性 P1–P5 与门禁 6 条全绿（真实）；T9-G0-06 随 T10-G0-02 联跑解禁（safety gates 测试侧 import 本包，T9 报告已刷新）；数据面（memory_probes 合成探针集+真实家庭日志）debt，reports/gates/T10.json。M2 已接 bge-small-zh-v1.5 ONNX 真推理嵌入（onnx_embedder.go，CLS 池化+L2 归一化入图 512 维；纯 Go WordPiece 自实现零新依赖；StubEmbedder 保留 fallback）——T10-G1-01/G1-04 增设语义面（模型缺失 Skip，照 T3 engineOrSkip 债务惯例），对拍/sanity 见 reports/eval/T10/。
## 技术路径（指导，任选+可偏离，PR 记录选择）
A mem0（Apache-2.0：抽取-更新-遗忘显式管线，多用户 API 原生，默认）｜B Zep/Graphiti 时间感知知识图谱（双时间线、事实随时间演化，需「时间感」叙事时）｜C Letta/MemGPT 自管理（不可断言比例高、token 贵，仅作 A 上「主动记忆」增强层，G0 全挂 A 层）；T11 底座（pgvector/sqlite-vec/Qdrant）=可替换零件，对验收协议透明
## 本地命令
just gate T10 all ；go test ./packages/go/memory -run Property -count=1
## 本地必绿再提 PR
T10-G0-01 跨用户隔离 0 泄漏（≥200 探针三层绕路，与 T5 联跑，不是统计线）｜T10-G0-02 删除即消失：50 条×全通道 0 残留（向量/图/备份/日志）｜T10-G1-01 recall@5：10/50/200 轮 ≥95/90/80%｜T10-G1-02 事实更新不矛盾 新值≥95%/矛盾≤2%｜T10-G1-03 容量代谢 无 OOM、高情绪权重留存≥90%｜T10-G1-04 检索 P95≤150ms
## 数据依赖
datasets/manifests/memory_probes.json（synthgen 注册：200 记忆探针事实，人名/宠物/喜好/事件/时间）；种子家庭 4 周真实日志「记忆时刻」标注（holdout，唯一不可合成替代的数字，经 tools/holdout，本包代码不得直接读）
## 本包禁令（叠加根 AGENTS.md）
- 生命周期 FSM 表驱动穷举先行：全状态可达、无死锁、deleted 为吸收态且仅显式操作可入
- 任何用户操作序列下 U 的检索结果 ∩ V 的记忆集 = ∅（不变量，G0 级）
## 常见坑
「写入 A→询问 B」要用直接问/间接诱导/角色扮演绕路三层探针，只测直接问会假绿；删除复查要覆盖全通道（向量/图/备份/日志），漏一个通道就是 T9-G0-06 联跑翻车
