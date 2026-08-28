# AGENTS.md — 数据飞轮（T2）
验收协议：docs/gates/assets/T2.md（先读，BI 编号以它为准）　阈值：configs/gates/T2.yaml（禁改）
## 本包边界
合成生产→隐私回流→增值回路：授权回流数据+合成管线配置进 → 训练池/holdout 池数据集出（synthgen 注册）。对接 T4/T5/T13（共用一条合成管线，一次建设多资产消费）、T10/T7（探针注入埋事实/情绪标签）、tools/holdout（holdout 池只进不出）。
## 技术路径（指导，任选+可偏离，PR 记录选择）
A TTS+声学增强合成管线（多说话人 TTS→家庭音景噪声/混响/codec/增益扰动→标注自动继承）｜B LLM 对话合成（角色卡+场景模板+儿童语言风格约束批量生成；合成分布窄必须配多样性指标+真实分布校准）｜C 隐私回流管线（授权采集→PII 检测脱敏（声纹不可逆处理用于训练副本）→质量过滤→分流训练池/holdout 池；COPPA 级合规审计挂在管线上而非事后）
## 本地命令
just gate T2 all ；go test ./packages/go/data-flywheel -run Property -count=1
## 本地必绿再提 PR
T2-G0-01 holdout 零污染：0 条近重复进训练集（全量 minhash+管道拓扑不可达）｜T2-G0-02 脱敏召回：PII 残留=0、声纹再识别≤3%（每批 200 条探针）｜T2-G1-01 合成多样性：分布距离≤阈值（分维度报告）、单一来源占比≤30%｜T2-G1-02 飞轮转速：≥50% 回流周期 ≥1 核心指标统计显著提升（bootstrap CI 不含 0）
## 数据依赖
synthgen 管线（tools/synthgen 为 A/B 管线注册器）；回流授权数据（脱敏+授权+过滤后成原料）；holdout 一律经 tools/holdout，本包代码不得直接读
## 本包禁令（叠加根 AGENTS.md）
- holdout 池写入者集合 ⊆ {评测服务}（任何训练组件引用 holdout 路径即 CI 红=repoctl forbidden-refs）
- 每用户回流数据量 ≤ 授权上限
## 常见坑
同原料+同管线版本→同输出哈希（数据集可重建）；脱敏强度↑→PII 召回不降且下游指标不升（权衡曲线，同 T15 方法）——脱敏过狠把信号也脱掉是常见翻车点
