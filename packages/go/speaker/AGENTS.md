# AGENTS.md — 声纹（T5）
验收协议：docs/gates/assets/T5.md（先读，BI 编号以它为准）　阈值：configs/gates/T5.yaml（禁改）
## 本包边界
家庭成员声纹识别：注册音频（3–5 句）+ 通话流进 → 成员身份判定/拒判出。对接 T10（身份决定记忆与隐私隔离，G0 联跑）、T13（借用 SV 模型反验音色）、T14（端侧共存预算）。
## 技术路径（指导，任选+可偏离，PR 记录选择）
A SpeechBrain ECAPA-TDNN（Apache-2.0，VoxCeleb EER<1% recipe，ONNX 端侧，默认）｜B 3D-Speaker/CAM++（中文生态，与 A 同协议横评后自选）｜C 共享骨干嵌入+家庭内 N≤8 闭集轻量头（算力受限时更稳）
## 本地命令
just gate T5 all ；go test ./packages/go/speaker -run Property -count=1
## 本地必绿再提 PR
T5-G0-01 身份切换后隔离 0 泄漏（≥100 个「写入 A→询问 B」场景，与 T10 联跑）｜T5-G1-01 家庭内区分 EER≤5%（trial≥5000，兄弟姐妹对单列）｜T5-G1-02 跨会话稳定性 再识别≥95%/拒判≤3%｜T5-G1-03 3 句注册劣化≤2pp｜T5-G1-04 陌生人拒判≥90%
## 数据依赖
datasets/manifests/speaker_synth.json（synthgen 注册：合成虚拟家庭 2–6 人含儿童）；亲友家庭 ≥5 家 ×3–6 人 ×3 会话（真实 holdout，只进不出，评测服务独占，经 tools/holdout，本包代码不得直接读）
## 本包禁令（叠加根 AGENTS.md）
- VoxCeleb 等公开基准数字不可迁移（成人/朗读/棚录分布），只做选型初筛；一切阈值以自建「家庭内区分」协议为准
- 兄弟姐妹对单独报告，不得并入总体 EER
## 常见坑
真实-合成差距 >3pp 时产品文案与 T15 路由策略须按真实值设定；文本无关性/增益不变/A→B 与 B→A 对称三条属性是回归主力，改 embedding 后先跑属性再跑统计
