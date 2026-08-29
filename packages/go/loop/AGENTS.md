# AGENTS.md — L1 演示闭环组装器（loop）
验收协议：docs/gates/system.md（CI-4 故障矩阵前 3 行 + 组合不变量）+ docs/m1-spec.md §1　阈值：configs/gates/{T3,T4,T13}.yaml（禁改；本包为组装层，无独立门禁 yaml）
## 本包边界
M1 收官组装器：把 T4（kws）/T3（turntaking）/T13（tts）三资产接成闭环管道——音频帧进 → 唤醒 → 话轮 FSM → 话轮终点产文（Responder 桩，M2=ASR+LLM）→ TTS 路由 → 音频块出（PumpSpeak）。本包是 m1-spec §1 的「驱动层」：三资产包互不 import 的结构不动，Event/VADEvent/Request 由本包搬运；不实现任何新检测/合成/决策逻辑（各资产包职责）。
## 实现状态（M1，IR #87）
已落地：Pipeline 组装（Wire fail-closed：Responder/PreSpeak 必接）；PushAudioFrame/PushVAD 输入面 + PumpSpeak 逐块交付面；全链路事件枚举（Wake/MicOpen/TurnEnd/MicClose/SpeakStart/AudioOut/SpeakDone/Interrupt/Degrade）；CI-4 前 3 行降级（ResponderDown 兜底话术/TTSFallback 观测/TTSNoAudio 0 音频收口/SpeakOverrun 双防线截断）；打断 Cancel 幂等+已播 Seq 不回退。未落地：ASR/LLM/记忆/动作管道（M2 经 Responder seam 注入）、真机音频 IO、延迟实测（M2 硬件计时）。
## 技术路径（指导，可偏离，PR 记录）
A 同步单流串行驱动（无 goroutine/无墙钟——确定性回放属性的前提，M1 选此）｜B 事件总线异步管道（M2 runtime 若需并发再议，Responder 接口不变）｜C 直接在 tts.Router 内做闭环（否决：三资产零耦合结构会被破坏）
## 本地命令
go test ./packages/go/loop -race -count=1 ；go test ./packages/go/loop -run Property -count=1
## 本地必绿再提 PR
全链路冒烟 3 场景（唤醒-对话-播报/打断-补偿/静默噪声零事件）｜故障注入 3 项（CH-01 Responder 断连兜底、CH-02 云首包超时降级+全通道失败、CH-03 文本超长+字节上限双防线）｜确定性属性 P1-P3（重放全等/有界终止/事件不变量）｜旅程映射 J01-J03（golden-journeys 前 3 条 → 组件级脚本）
## 数据依赖
无独立数据集（组装层）；旅程映射读 tests/golden-journeys/*.yaml（J01-J03，评测面文件只读不写）
## 本包禁令（叠加根 AGENTS.md）
- 禁引三方依赖（import 白名单=标准库+三资产包，同 module）
- 降级行为必须 ∈ DegradeReason 预定义集——新增降级路径须先扩枚举再落码（CI-4「行为 ∈ 预定义集」）
- 事件不携带墙钟（时间戳一律镜像驱动输入逻辑时刻——确定性属性的前提）
- 不得为过测试改三资产包公开 API（API bug 最小修复须在 PR 说明）
## 常见坑
PumpSpeak 的 AtMs 取 lastMs（已见最大输入时刻）而非当前帧——事件序按逻辑时刻对齐；degraded 流首个 chunk 是 0 字节静默占位（Seq=0），别把它当故障；打断后 Router.Cancel 幂等但流引用必须清（漏清=后续 PumpSpeak 复播半句）
