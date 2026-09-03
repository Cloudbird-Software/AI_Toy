# AGENTS.md — 话轮管理（T3）
验收协议：docs/gates/assets/T3.md（先读，BI 编号以它为准）　阈值：configs/gates/T3.yaml（禁改）
## 本包边界
端侧话轮管理：音频流进 → 话轮事件（开始/说完/打断）+ 接话触发出。对接 T4（唤醒后进入会话）、T13（话轮结束→TTS 首包计时）、T14（档位与延迟预算）。
## 技术路径（指导，任选+可偏离，PR 记录选择）
A Silero VAD（MIT，ONNX~2MB，RPi 级 RTF<0.05）+儿童语速自适应静音门限+云端轻量语义兜底，最快原型｜B 学习型话轮终点预测（VAP 四类话轮事件/TurnGPT，蒸馏 <10M 端侧模型），极致对话感｜C 混合=VAD 保底（G0 挂此层）+预测模型负延迟提前量，默认推荐，可独立回滚
## 本地命令
just gate T3 all ；go test ./packages/go/turntaking -run Property -count=1
## 本地必绿再提 PR
T3-G0-01 打断检出≥95%/响应≤300ms（一次「不理睬」=失败样本）｜T3-G0-02 全静音/纯噪声 0 触发（负样本流≥6h）｜T3-G1-01 误截断≤8%（儿童集≥300 停顿点）｜T3-G1-02 接话 P95≤900ms｜T3-G1-03 打断后上下文保持≥90%｜T3-G1-04 中停顿容忍（1.5–3s，与 T3-G1-01 同线）
## 数据依赖
datasets/manifests/turntaking_synth.json（synthgen 注册：TTS 儿童语音+停顿注入）；负样本音景库与 T4 共用；亲友儿童录音 ≥20 组（真实 holdout，只进不出，经 tools/holdout，本包代码不得直接读）
## 本包禁令（叠加根 AGENTS.md）
- 阈值不得对长停顿（1.5–3s 思考停顿）单独放宽
- VAD 判定不得永久挂起：任意输入有限帧内离开「判定中」态（属性级）
## 常见坑
儿童语速慢、中停顿多，成人静音门限直接迁移必误截断（3–6/7–9/10–12 岁分层测）；SNR 降低时判定不得更激进；真实-合成差距 >5pp 的指标只能报「合成绿+真实黄」，禁宣称达标
## 实现状态（M1，IR #80 已落地；W3 接入 IR #129 增量见末条）
- FSM 按 m1-spec §3 契约实现（turntaking.go）：3 态/7 行转移表穷举+未列组合自转移；打断=同步单步（逻辑延迟 0 ≤ BargeInWindow ≤300ms，BargeInLatencyMs 为可观测证据；链路实测延迟 M2 硬件计时）；话轮终点=尾静音 ≥SilenceMs 或累计 ≥MaxTurnMs（防挂起，行4 先于行2 判定——VAD 永不静音也截断）；AtMs 非单调事件整体丢弃（迟到帧不回放）。路径选择（PR 记录）：Idle+OnSpeakStart→Speaking 放行转移（spec §1 闭环：TurnEnd 后驱动注入 SpeakRequest 开播，不放行则打断链不可达）；尾静音基准=话轮起点或最近语音事件（唤醒后不开口也按 SilenceMs 收口）。
- 测试三件套齐：表驱动单测（转移穷举/配置校验/±1ms 边界/非单调丢弃）、quick 属性（P1 防挂起/P2 打断确定性/P3 单调性/P4 回放）、门禁接线（gates_test.go 一 ID 一顶层测试）。
- 门禁状态（IR #129 后）：T3-G0-01 打断检出=真实（50 次注入 50/50，逻辑延迟 0ms）；T3-G1-01/G1-02=真实（迁至 turntaking/vap 包——真模型 ONNX 在环断言，本包保持零依赖）；T3-G0-02/T3-G1-03/T3-G1-04=debt（≥6h 负样本音景/对话链 LLM/儿童中停顿集+自适应静音机制，Skipf 写明）。报告：reports/gates/T3.json（g0=pass g1=pass not_impl=0 exit 0）。
- M1 预留：TierPolicy 档位镜像不接线（nil=默认表，runtime-fsm 真身后注入替换）；本包不 import tests/**（防「对着考卷优化」），测试侧仅 tools/gaterunner（Mark 注册辅助，T1 先例）。
- W3 增量（IR #129，路径 C=混合式落地）：predict.go 新增 Prediction/Predictor 接缝 + FSM.PredictLead 提前量（VAPLeadPNow 门限，0=关闭零值安全）；安全不变量=用户语音进行中绝不提前、打断链零影响（predict_test.go 断言）；真引擎=packages/go/turntaking/vap（MaAI MC-VAP 中文 Kyoto 版 MIT，融合流式 ONNX，Go ORT RTF≈0.10），对拍/对照/RTF 证据见 reports/eval/T3/。

