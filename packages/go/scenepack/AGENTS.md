# AGENTS.md — 场景包运行时（T16+T18 规则面）
验收协议：docs/gates/assets/T16.md（T16+T18 合卡，先读，BI 编号以它为准）　阈值：configs/gates/T16.yaml（禁改）
## 本包边界
场景包规则面：包（manifest+人格卡+音色+动作配置+知识/剧本+评测集）进 → 加载校验/两阶段原子安装/场景切换事件（SceneCtx）/内容 T9 预检/包内考卷执行与台账出。对接 configs/packs/schema.json（镜像校验）、assets-packs/*（种子包只读消费）、T9（SafetyClassifyFunc 注入——包实现零 import，测试侧联跑 persona/emotion/motion-map）。packs/骨架目录保留不落码。
## 实现状态（M3，IR #107 已落地）
核心已实现（m3-spec §7 包契约 F 路径 A 声明式包格式）：LoadManifest 手写结构校验=镜像 schema.json 必要条件（required/semver/资源齐备/引用逃逸拒绝，一致性变异包双跑）；Manager 两阶段原子安装（stage→commit，错误自回滚+崩溃重启 Recover 收敛 0 中间态）；Activate→SceneCtx（persona 词表/safety 并联词表/motion 表/emotion 规则/knowledge 显式注入面）；CheckContentSafety 全量预检（注入 T9 分类器，fail-closed）；ExecuteEvalSet 考卷随包执行（规则面应答器=包内逐字回包/包外拒答，零编造）+台账。T18 生成管线 M3=预检规则面（LLM 批量生成+溯源戳=真模型面 L5 注记）。门禁 5 条全真实 pass（T16-G1-01/02/03、G0-01/02），报告 reports/gates/T16.json。
## 技术路径（指导，任选+可偏离，PR 记录选择）
A 声明式包格式（manifest+镜像 schema+权限白名单默认拒绝，本实现）｜B 能力沙箱（包逻辑走受限解释器，包带行为逻辑时必要）｜C T18 内容管线（LLM 批量生成→自动过 T9 全集+人工抽审→入包带溯源戳；M3 落预检规则面，真模型面 L5）
## 本地命令
go run ./tools/gaterunner/cmd/gaterunner run --asset T16 --level all --report reports/gates/T16.json ；go test ./packages/go/scenepack -run Property -count=1
## 本地必绿再提 PR
T16-G0-01 包内容安全 0 违规（全量预检+行为抽样 200 轮/包诱导说包外知识）｜T16-G0-02 安装/卸载原子性：注入中断 ×50 次/包 0 中间态残留｜T16-G1-01 包隔离 0 外溢（quick 交错序列，核心资产断言面=无包基线）｜T16-G1-02 包完整性 schema 通过率=1.0+变异包全拒+镜像一致性｜T16-G1-03 包评测随包执行 100%+台账
## 数据依赖
configs/packs/schema.json（镜像断言只读）；assets-packs/goodnight-bear、_template（种子包只读消费）；T9 分类器注入（safety.Engine，测试侧）；第三方创作者真实包（隔离与安全断言新作者手上重新全跑，经 tools/holdout，本包代码不得直接读）
## 本包禁令（叠加根 AGENTS.md）
- 权限白名单默认拒绝（未声明的能力一律不给）
- T9 分类器缺省=拒绝执行（fail-closed——内容安全不可豁免）
- 卸载后全通道复查（0 残留是 G0，不是清理建议）；种子包目录只读（安装走 staging→registry 副本）
## 常见坑
同包同版本同内容哈希（Dir 不参与、Files 键序无关）；包升级内置评测得分不得下降（内容不许负优化）；考卷应答恒 ∈ 包内语料原句∪拒答脚手架（零编造面）；签名=PLACEHOLDER 占位声明制（密码学有效性归签名机制接入后）
