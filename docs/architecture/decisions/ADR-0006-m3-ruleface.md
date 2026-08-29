# ADR-0006 M3 六资产规则面：纯 Go 落地 + T14 作为 properties real 真身首落地
状态：accepted 2026-08-29（IR #103，规格=docs/m3-spec.md）
背景：M3 交付剩余六资产规则面（T14 离线运行时/T15 路由缓存/T10+T11 记忆/T5 声纹/T6 IMU/T16+T18 场景包，实现卡 #104–#108），真模型（llama.cpp/ECAPA-TDNN/嵌入语义缓存）与真机台架属重依赖/数据面（无 license 台账、无硬件目标，须 founder 批），LLM 输出与声学/物理真值不可 CI 断言。
决策：六包全部以纯 Go 规则面落地——T14=表驱动档位 FSM+8 项 chaos 对齐降级矩阵，并实现 tests/properties 的 RuntimeModel 四接口（FailApply/GlobalCapability/SafetyLevel/TierCaps）作为 `//go:build real` 的首个真身（断言口径 CI-1..CI-4 不变）；T15=精确键 LRU+TTL+预算上限；T10=图存储+UserID 域隔离+新值替换+删除 0 残留；T5=规则桩打分+EER 评估通道（生成器/打分器解耦）；T6=三态检出+软件边界盒（固件熔断独立保险）；T16=手写结构校验镜像 schema+两阶段原子安装+T9 内容预检。
备选否决：跳过规则面直接接模型（不可断言面扩大+重依赖越权引入；T14-G0-01/T5 声学真值/T15 θ 面保持 debt 至真模型经批准落地）。
后果：M3 收官（#108）后 16 资产全部脱离 coverage DEBT 行、进入全 BI 执法（m3-spec §9 Mark 表 30 ID：真实 28/debt 2）；剩 debt verdict 仅数据/模型/真机面，显式可见可追踪，批准后按 §9 注记逐条升级复测。

