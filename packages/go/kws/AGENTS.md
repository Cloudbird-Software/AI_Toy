# AGENTS.md — 唤醒词（T4）
验收协议：docs/gates/assets/T4.md（BI-4.1/4.2/4.3 以它为准）　阈值：configs/gates/T4.yaml（禁改）
## 本包边界
端侧常驻唤醒检测：音频流进 → 唤醒事件出 + 置信度。对接 T3（唤醒后进入话轮）、T14（档位/内存预算）。
## 技术路径（指导，可偏离，PR 记录）
A openWakeWord 自训练（合成管线完整，默认起点）｜B microWakeWord MCU 路线（量产主控）｜C Porcupine 商用（仅标定业界基线，非终态）
## 本地命令
just gate T4 all ；go test ./packages/go/kws -run Property -count=1 ；go run ./packages/go/kws/cmd/kws train --config configs/kws.yaml
## 本地必绿再提 PR
T4-G0-01 误唤醒(≥6h 零事件)｜T4-G0-02 对抗负样本 0 触发｜T4-G1-01 唤醒率近/远场｜T4-G1-02 儿童公平性｜T4-G1-03 RTF/内存/泄漏
## 数据依赖
datasets/manifests/kws_synth.json（synthgen 注册）；负样本音景库与 T3 共用；真实童声 ≥200 句（holdout 侧，经 tools/holdout）
## 本包禁令（叠加根 AGENTS.md）
- 真实童声与合成唤醒率分开两列报告，禁止合并
- 每次入集前 minhash 与训练增强语料去重（no-leak 夜巡会复查）
- 量化后确定性属性（同帧同判定）不得因优化回退
## 常见坑
儿童基频高、口齿不清：远场阈值单独校准；「零事件」宣称按 3/N 规则算时长，别拿 1 小时跑数据充数
