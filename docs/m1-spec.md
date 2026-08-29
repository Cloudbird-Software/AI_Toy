# M1 实现规格 —— L1 演示闭环三包（kws / turntaking / tts）

> IR #78（spec PR，纯文档零代码）。实现卡：#79 kws · #80 turntaking · #81 tts。
> 依据：docs/gates/assets/{T3,T4,T13,T14}.md · configs/gates/{T3,T4,T13}.yaml · tests/properties/contract.go · configs/budgets/latency.yaml · ADR-0002（debt 语义）/ADR-0004。与法典冲突以法典为准；路径选择写实现卡 PR 模板。

## 1. 架构总览

```
音频帧 Frame(16kHz mono) ─▶ kws.Detector.Push ─▶ Event{Wake,Conf} ─▶ fsm.OnWake（Idle→Listening，OpenMic）
用户语音 ─▶ VAD 事件流 ─▶ fsm.OnVAD ─▶ []Action
  ├─ Listening 尾静音≥SilenceMs ─▶ ActTurnEnd ─▶ 驱动注入 SpeakRequest.Text
  │    └─ tts.Router.Synthesize：PreSpeak(T9 钩子)→Cache→云/端通道
  │         ├─ 命中/正常 ─▶ AudioStream ─▶ 音频出（fsm.OnSpeakStart→Speaking）
  │         └─ 超时/故障 ─▶ 降级行为表（§4.4，CI-4 预定义集）
  └─ Speaking+EvVoiceStart ─▶ 立即 [ActStopTTS, ActOpenMic]（打断，BI-3.2）
```

- **组装零耦合**：三包互不 import——Event/VADEvent/Request 皆平凡类型，由驱动层搬运
  （M1 驱动=测试回放器，SpeakRequest.Text 由驱动注入；M2 换 runtime 管道，三包不动）。
- **依赖零新增**：三包 import 白名单=标准库（测试侧另许 tools/gaterunner）；ONNX 推理、
  音频设备 IO、网络客户端全部接口化，M1 一律桩实现（ADR-0004）；ASR/LLM/记忆/动作不在 M1。
- **T14 档位预留**：各包声明本地窄接口，语义镜像 tests/properties/contract.go 的
  RuntimeModel.TierCaps（L0⊇L1⊇L2⊇L3，configs/runtime/tiers.yaml），**不 import tests/**
  （防「对着考卷优化」）；M1 桩=默认档位表，runtime-fsm 真身落地后只换注入实现。

## 2. 包契约 A —— packages/go/kws（T4，实现卡 #79）

```go
package kws

type Frame struct {
    TS    int64     // 采集时间戳 ms（单调，由调用方保证）
    PCM   []int16   // 16kHz mono，帧长 FrameMs×16 样本；nil 时用 Feats
    Feats []float32 // 预处理特征（非空则跳过包内预处理，直供 Inferencer）
}
type EventKind int8 // 0=EvNone（零值） 1=EvWake
type Event struct{ Kind EventKind; Confidence float64; AtMs int64 } // Confidence∈[0,1]；EvNone 时=当帧峰值（可观测）
type Config struct {
    FrameMs, ConfirmFrames, RefractoryMs int     // 帧长ms(默认30)/防抖N帧(≥1)/不应期ms(≥0)
    Threshold                            float64 // 置信度门限 [0,1]
    Infer                                Inferencer // nil=默认桩（能量+过零启发式，无唤醒语义）
    Budget                               TierBudget // T14 档位镜像；nil=默认表（M1 预留不接线）
}
type Inferencer interface{ Infer(f Frame) float64 } // 纯函数：同帧同判定（T4 属性接口级承诺）
type TierBudget interface{ KWSMemLimitBytes(tier int) int } // 镜像 RuntimeModel.TierCaps

func NewDetector(cfg Config) (*Detector, error)
func (d *Detector) Push(f Frame) Event // 同步、无 IO、不 panic；滑窗流式判定
```

- **状态集**（内部；单流串行无并发——资产卡「表驱动单测足够」）：`idle`（滑窗累积+防抖计数）↔ `refractory`（不应期倒计时）。
- **错误语义**：仅 NewDetector 校验（FrameMs∈(0,200]/Threshold∈[0,1]/ConfirmFrames≥1/RefractoryMs≥0）返回 error；Push 对畸变输入按零能量帧处理（EvNone），永不 error/panic。
- **属性（testing/quick，#79 必做）**：以脚本化 Inferencer 注入测 Detector 逻辑（非测模型）：
  P1 增益不变（PCM −20~+6dB 缩放判定序列不变）；P2 SNR 单调（SNR 降档→唤醒率单调不升）；
  P3 位置无关拼接（唤醒词帧序列任意偏移均命中）；P4 同帧同判定（重放哈希固定）；P5 不应期
  （Wake 后窗口内全 EvNone）。真模型接入后同组属性重跑（接口化意义所在）。

## 3. 包契约 B —— packages/go/turntaking（T3，实现卡 #80）

```go
package turntaking

type State int8        // StIdle（零值）/ StListening / StSpeaking
type VADEventKind int8 // 0=EvNone（零值） 1=EvVoiceStart 2=EvVoiceEnd
type VADEvent struct{ Kind VADEventKind; AtMs int64 }
type ActionKind int8 // 0=ActNone（零值） ActOpenMic/ActCloseMic/ActTurnEnd/ActStopTTS
type Action struct{ Kind ActionKind; AtMs int64 }
type Config struct {
    SilenceMs, MaxTurnMs, BargeInWindow int        // 尾静音→TurnEnd/上限防挂起/打断窗口≤300ms（均>0）
    Policy                               TierPolicy // T14 档位镜像；nil=默认表（M1 预留）
}
type TierPolicy interface{ SilenceBudgetMs(tier int) int } // 镜像 RuntimeModel.TierCaps

func NewFSM(cfg Config) (*FSM, error)
func (f *FSM) OnWake(atMs int64) []Action       // Idle→Listening（唤醒开麦）
func (f *FSM) OnVAD(ev VADEvent) []Action       // 主转移入口（转移表见下）
func (f *FSM) OnSpeakStart(atMs int64) []Action // Listening→Speaking（TTS 开播）
func (f *FSM) OnSpeakDone(atMs int64) []Action  // Speaking→Idle（播完）
func (f *FSM) State() State
```

转移表（表驱动穷举，#80 必测全行）：

| 态 | 事件 | 后继 | 动作 |
|---|---|---|---|
| Idle | OnWake / EvVoiceStart | Listening | ActOpenMic |
| Listening | EvVoiceStart | Listening | —（语音中，清尾静音计时） |
| Listening | EvVoiceEnd 后静音≥SilenceMs | Idle | ActTurnEnd+ActCloseMic |
| Listening | 累计≥MaxTurnMs | Idle | ActTurnEnd+ActCloseMic（防挂起） |
| Listening | OnSpeakStart | Speaking | — |
| Speaking | EvVoiceStart | Listening | **ActStopTTS+ActOpenMic（打断）** |
| Speaking | EvVoiceEnd / OnSpeakDone | Speaking/Idle | —/— |

- **打断（BI-3.2/T3-G0-01）**：Speaking+EvVoiceStart → 立即 [ActStopTTS, ActOpenMic] 同步返回（逻辑延迟 0≤BargeInWindow≤300ms 契约；链路实测延迟 M2 硬件计时）。未列组合=自转移（穷举无死角）。
- **中停顿纪律（T3-G1-04）**：不设「长停顿特殊门限」——SilenceMs 全局唯一禁单独放宽；语义区分（「还在想词」）留 M2。
- **错误语义**：NewFSM 校验配置返回 error；任意调用序不 panic；AtMs 非单调（早于已见最大值）→丢弃该事件（迟到帧不回放）。
- **属性（quick）**：P1 无永久挂起（任意事件序列后有限步内可达 Idle——资产卡活性）；
  P2 打断即时（Speaking 任意时刻 EvVoiceStart→首 Action=ActStopTTS）；P3 MaxTurnMs 兜底；
  P4 确定性（同事件序列重放→同动作序列）。

## 4. 包契约 C —— packages/go/tts（T13，实现卡 #81）

```go
package tts

type Request struct {
    Text, Voice string // 待合成文本（空→ErrEmptyText）/音色 ID（角色声资产，空=默认）
    TurnID           string // 话轮幂等键（打断/不重播半句）
    Tier, DeadlineMs int    // T14 档 0..3（越界→error）/首包预算：云 300/端 150（T13-G1-01）
}
type Chunk struct{ Data []byte; Seq int; Final bool }
type AudioStream interface {
    Next() (Chunk, error) // io.EOF=流尽；err 后流终止（重试=重播半句风险，禁止）
    Cancel() error        // ActStopTTS 执行面（幂等）
}
type Synthesizer interface{ Synthesize(req Request) (AudioStream, error) } // 云/端/桩同接口
type PhraseCache interface {
    Get(text, voice string) (AudioStream, bool) // 命中=零合成延迟（入缓存前提=已过 T9）
    Put(text, voice string, s AudioStream)
}
type PreSpeakFunc func(text string) error // T9 拦截钩子；err≠nil→拒绝合成（读出=0）
type TierCaps interface{ TTSChannel(tier int) (cloud, edge, cache bool) } // 镜像 RuntimeModel.TierCaps
// 错误集（哨兵，errors.Is）：ErrEmptyText / ErrIntercepted（PreSpeak 拒绝）/ ErrNoChannel（档位无通道）/ ErrTimeout（云首包超时）

type RouterConfig struct {
    PreSpeak    PreSpeakFunc  // T9 钩子；nil→NewRouter error（生产禁裸奔；测试显式注入）
    Cloud, Edge Synthesizer   // 云端流式（L0/L1）/端侧引擎（L2+降级补偿，Edge 可 nil）
    Cache       PhraseCache   // 预合成短语（L3 档+各档加速；缓存短语同样过 T9）
    Caps        TierCaps      // nil=默认镜像 configs/runtime/tiers.yaml
    FirstPacketTimeoutMs, SilenceCapMs int // 默认 300（云首包门禁线）/2000（故障矩阵：静默≤2s）
}
func NewRouter(cfg RouterConfig) (*Router, error)
func (r *Router) Synthesize(req Request) (AudioStream, error) // 决策序见下
func (r *Router) Cancel(turnID string) error                  // 打断执行面（幂等）
```

- **合成决策序**：① PreSpeak 拒→ErrIntercepted（读出=0）② Cache 命中→直返（零合成延迟）
  ③ 按档选通道：L0/L1→Cloud、L2→Edge、L3→仅 Cache（未命中→ErrNoChannel）④ 云首包>FirstPacketTimeoutMs→降级行为表。
- **降级行为表（§4.4）**——对齐规格书故障矩阵「TTS 超时/首包失败」G1 行；降级行为 ∈ 本表预定义集（#81 AC「对齐 CI-4」）；每请求独立尝试云=「下轮回云档」：

| 触发 | 行为（M1 可测语义） | 恢复 |
|---|---|---|
| 云首包超时 | 静默占位≤SilenceCapMs(2s)→Edge 补偿重合成；Edge=nil→ErrTimeout（上层转文字/动作） | — |
| 云流中途 err | 终止流，已播 Seq 不回退（**不重播半句**）；Cancel 幂等 | 下轮重试云 |
| 打断 Cancel | 流立即终止（幂等）；同 TurnID 不续播 | — |
| L3 无缓存 | ErrNoChannel→管道走文字/动作补偿 | — |
| PreSpeak 拒绝 | ErrIntercepted，0 字节音频（穷举三通道×拒绝=读出 0） | — |

- **属性（quick，T13 资产卡）**：P1 同文本+同种子音频哈希一致；P2 输出时长随文本长度单调
  增（突变=坏输出预警）；P3 不可见控制字符剥离后不影响可听输出；P4 Cancel 幂等+已播不回退；P5 拦截完备（对抗句表全拒→读出=0，T13-G0-01 真实测点）。
- **红线（叠加根 AGENTS.md）**：禁克隆真实儿童声音（角色声=合成音色或成人授权声）；缓存短语同样过 T9（入缓存前提=已过）。

## 5. Mark 接线策略表

语义（ADR-0002/IR #76）：**真实**=注册测试实跑断言；**debt**=注册测试整测 t.Skipf（写明
数据依赖原因，不计 pass 不阻断、不占豁免台账）。门禁测试落各包 `*_test.go` 与实现同 PR
（gaterunner.Mark=注册辅助，非评测断言实现——T1 先例）；**一 ID 一顶层测试函数**（dispatchGate
按 `--- SKIP: <Test>` 精确匹配顶层整测 SKIP 判 debt，子测试 SKIP 不算）。统计纪律面（n≥59/
泊松 3/N）待数据集落地后由 evalkit 接管，M1 点估计口径照 yaml rule。

| 门禁 ID | BI/级 | verdict | 测法 / 债务原因（Skipf 须写明） |
|---|---|---|---|
| T4-G0-01 | BI-4.2/G0 | debt | 负样本音景库 ≥6h 未建（与 T3 共用；synthgen 注册流程未走） |
| T4-G0-02 | BI-4.2/G0 | debt | 对抗负样本 ≥30min（他牌唤醒词/广告同音节）未建 |
| T4-G1-01 | BI-4.1/G1 | debt | 需每词 ≥500 合成+真实童声 200 分 SNR 档；模型未接（桩无唤醒语义） |
| T4-G1-02 | BI-4.1/G1 | debt | 需儿童/成人各 ≥300 正样本数据集 |
| T4-G1-03 | BI-4.3/G1 | debt | RTF/内存需目标硬件连续推理 1h 实测（M1 无硬件目标） |
| T3-G0-01 | BI-3.2/G0 | **真实** | 合成 VAD 事件流注入 50 次打断（异态/异时/异长）：Speaking+EvVoiceStart→首 Action=ActStopTTS，检出 50/50（≥0.95；音频面 M2 升级口径） |
| T3-G0-02 | BI-3.1/G0 | debt | 负样本音景流 ≥6h 未建 |
| T3-G1-01 | BI-3.1/G1 | debt | 误截断需儿童集 ≥300 停顿点（noise_band 基线）未建 |
| T3-G1-02 | BI-3.1/G1 | debt | 接话 P95≤900ms 需固定硬件×3 实测计时 |
| T3-G1-03 | BI-3.2/G1 | debt | 打断后上下文保持需对话链（LLM，M2 接入） |
| T3-G1-04 | BI-3.3/G1 | debt | 中停顿样本 ≥100 未建 |
| T13-G0-01 | BI-13.3/G0 | **真实** | 拦截层真实测：100 对抗句表驱动（注入/超长数字/多音字陷阱），PreSpeak 拒→三通道 0 字节音频（读出=0；人工听审面 M2） |
| T13-G1-01 | BI-13.2/G1 | debt | 首包 P95 需真实云/端引擎计时（桩无延迟语义） |
| T13-G1-03 | BI-13.3/G1 | debt | 语义停顿正确率需真实 TTS 输出+人工听审 |
| ⚠ T13-G1-02 | BI-13.1/G1 | 不可接线 | configs/gates/T13.yaml 未收录（待 T5 SV 标定回填）→ 升级项见下 |

- **升级项（founder，issue #82）**：T13 提交首份 reports/gates/T13.json 后 repoctl coverage
  必红「BI-13.1 无任何断言」（ADR-0002 全 BI 执法 × yaml 无该门禁；ExecuteRun 只跑 yaml
  声明门禁，无绕开途径）。处置：首选=founder 回填 T13-G1-02 占位门禁（#81 以 debt 接线，
  **#81 合并前置**）；降级=#81 暂缓提交 T13.json（coverage 维持 T13 DEBT 行不红），回填后
  补跑 `just gate T13 all` 提交报告。#79/#80 无此问题（Mark 全覆盖 BI-4.1/4.2/4.3 与 BI-3.1/3.2/3.3，各含 ≥1 G0）。
- **报告落盘**：各卡 PR 内 `just gate <T> all` → reports/gates/<T>.json 随 PR 提交（T1 先例）；debt 不阻断（exit 0），G0 真实 pass 即该资产脱离 not_implemented。

## 6. 测试计划与依赖约束

- 每包三件套：①表驱动单测（转移穷举/配置校验/错误语义/Router 命中·穿透·超时三分支）
  ②testing/quick 属性（各包 P1..Pn，见包契约）③门禁测试（§5 表）。
- 与 tests/properties：M1 三包**不接线** `-tags real`（注入点绑定 runtime-fsm=T14 域，M2+）；
  三包纯函数式/可注入，M2 组合期（CI-1..CI-4/combo-smoke）直接驱动；不改 golden-journeys。
- CI 面：`go test ./... -race` 全绿；`just gate T4|T3|T13 all` exit 0（全 debt=预期态）；
  `gaterunner collect` +14 行；T1-G1-01 登记率在全资产接线前持续 debt（预期，非新债）。
- 依赖：go.mod 零新增；三包 import=标准库（禁 golang.org/x/*、ONNX 绑定、网络客户端），
  测试侧另许 tools/gaterunner；M1 零数据集（对抗句表/打断场景表=测试 fixture 虚构数据，
  不入 datasets/；真实集 M2 走 synthgen 注册）。
- 预算：不改 configs/budgets/latency.yaml；DeadlineMs/FirstPacketTimeoutMs 默认值=门禁线
  契约映射（云 300/端 150；预算段 tts_first P95=280ms 见 latency.yaml），实测归 M2 真机。
