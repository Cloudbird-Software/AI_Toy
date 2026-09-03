# T14 端侧离线运行时全链路延迟实测报告

> 工具链说明：本机 `just` 不可用，延迟采样由 `go test ./packages/go/loop -run Latency` 的旁路
> `latencyTracker` 产出（逻辑时钟口径）；墙钟口径同文件 `LatencyWallReport()`。
>
> **2026-09-03 更新（feat/t14-m2-asr-vad，M2-prep）**：VAD/ASR 已接真 ONNX 推理
> （Silero VAD + FireRedASR2-AED int8，yalue/onnxruntime_go 绑定，与 T3 vap 同装配），
> 实测数据见文末「真模型 RTF 补测」节；桩阶段历史数据保留于前两节。
>
> **2026-09-03 更新（feat/t14-m2-llm，M2）**：LLM 已接真推理（Qwen3-0.6B Q4_K_M GGUF，
> 自研最小 llama.cpp dlopen 绑定，v0.3.0 ABI，ADR-0012），实测见「LLM（Qwen3-0.6B GGUF）」节。
> TTS 已接真合成（MeloTTS ORT，meloort 装配层，PR #153），对拍与 RTF 见文末「TTS（MeloTTS ORT）」节。
>
> **2026-09-03 更新（feat/t14-m2-asr-stream，M2-T14-ASR 换型 PoC，issue #133）**：
> ASR 换型流式 zipformer（founder 决策），三延迟实测见文末「流式换型实测」节——
> 定稿延迟 ≤2s 与 RTF ≤0.5 两条验收线均达成；选型记录 ADR-0013（原 ADR-0012 重编号）。

## 测试环境

- 机器：Linux 4 核 7.4G（本机开发机）
- Go：1.23
- 时间：2026-09-03
- 运行方式：`go test ./packages/go/loop -run Latency -count=1`

## 分段延迟（桩实现）

| 段 ID         | 说明               | 桩固定值 (ms) | 备注                         |
|---------------|--------------------|---------------|------------------------------|
| tail_silence  | VAD 尾静音等待     | 0             | 桩同步推进，无真实 VAD 延迟  |
| asr_uplink    | ASR 定稿与上行     | 0             | `FireRedASR2.Recognize` 桩直返 |
| cloud_llm     | LLM 首句生成       | 0             | `Qwen3LLM.Generate` 桩直返   |
| tts_first     | TTS 首包           | 0             | `gateSynthStub` 零延迟       |
| transport     | 通路与播放启动     | 0             | 同步交付桩                   |
| **total_p95** | **全链路 P95**     | **0**         | 桩阶段总和                   |

## RTF / 首字延迟

- RTF (Real-Time Factor)：N/A（桩阶段无真实推理耗时）
- 首字延迟 (Time-To-First-Token)：N/A（桩阶段无流式生成）
- 待 M2 接入 sherpa-onnx 真模型后补测：
  - VAD：Silero VAD ONNX 帧级推理 ✅（见下节）
  - ASR：FireRedASR2 encoder/decoder int8 ONNX 流式 ✅（见下节，非流式 PoC）
  - LLM：Qwen3-0.6B Q4_K_M GGUF llama.cpp 端侧 ✅（见下节，非流式 PoC）
  - TTS：T13 melotts-zh 或 Piper 端侧合成（待 M2）

## 真模型 RTF 补测（2026-09-03，feat/t14-m2-asr-vad）

口径：本机 CPU（4 核 7.4G，进程 nice -n 19）、ONNX Runtime 1.29.0、
`go test ./packages/go/inference -run RTF -v`，每配置 10 次取 P50/P95；
音频 = sherpa-onnx FireRedASR2 随包 `test_wavs/1.wav`（16kHz PCM16，5.10s）。
ASR 为非流式整句（fbank→CMVN→encoder→KV cache 贪心解码，40 token）。

### VAD（Silero VAD v5 ONNX，帧长 576 样本 = 36ms）

| 指标 | 值 | 备注 |
|---|---|---|
| RTF P50 | **0.0038** | 单帧推理 ≈1.3ms，相对 36ms 帧时长 |
| RTF P95 | **0.0046** | |
| 实时富余 | ≈260× | 流式逐帧驱动无压力，36ms 预算内占比 <0.5% |

正确性锚点：同 wav 语音帧 max 概率 1.000 / 均值 0.819；2s 零输入帧 max 0.0089
（`TestVADRealSpeechVsSilence`）。

### ASR（FireRedASR2-AED int8，encoder+decoder 两阶段）

| 配置 | RTF P50 | RTF P95 | encoder 墙钟 | decoder 墙钟 |
|---|---|---|---|---|
| intra-op=2（默认，T3 对齐口径） | **0.984 ~ 1.008**（两轮 10 次） | 1.042 ~ 1.072 | ≈2.6~2.7s | ≈2.2~2.3s |
| intra-op=4（T14_INTRA_OP_THREADS=4） | **0.844** | 0.868 | 2.36s | 1.88s |

正确性锚点：Go 全链路输出与独立 Python 参考管线（Python ORT + kaldi-native-fbank
1.22.3，官方 FireRedASR2S 预处理口径）**逐字符一致**（1.wav / 3-sichuan.wav 精确对拍）；
fbank 移植与 knf 参考特征 max|Δ|=1.04e-3（float32 精度级，508 帧×80 维全量比对，
`TestFbankParityKnf`）。

### LLM（Qwen3-0.6B Q4_K_M GGUF，llama.cpp v0.3.0，2026-09-03，feat/t14-m2-llm）

口径：本机 CPU（4 核 7.4G，进程 nice -n 19）、自研最小 cgo dlopen 绑定（ADR-0012，
libllama.so.0.3.0）、Qwen3 ChatML 非思考模板 + 贪心解码、`go test ./packages/go/inference
-run TestLLMReal -v`，吞吐 10 次取 P50/P95（预热 1 次）；默认 n_ctx=512 / maxNew=128。

| 指标 | threads=4 | threads=2 | 备注 |
|---|---|---|---|
| 生成吞吐 P50 | **29.53 tok/s** | 28.82 tok/s | 逐 token 墙钟；生成受内存带宽限制，线程数增益小 |
| 生成吞吐 P95 | 30.95 tok/s | 34.17 tok/s | |
| prompt 处理（51 tok，末次） | 346 ms | 725 ms | 预填充为算力受限，随线程近线性 |
| TTFT（prompt 解码墙钟口径） | ≈0.35 s | ≈0.73 s | 51 tok prompt + 首步 ≈30 ms |
| 单句回复墙钟（20~70 tok） | ≈0.7 ~ 2.3 s | 同量级 | 含 prompt 处理 |

内存（`TestLLMRealMemory`，/proc/self/status 进程 RSS，单跑口径）：基线 105 MB →
**加载增量 ≈764 MB**（462 MB 权重 + ggml 计算缓冲/KV）→ 生成增量 ≈4 MB
（HWM 869 MB）；Go 堆 HeapInuse ≈1 MB（llama 权重/KV 在 C 堆，进程 RSS 为准）。
断言口径为增量（加载 <2GB、生成 <512MB），合跑时进程含其他引擎残留不作绝对值断言。

切题性人工读（`TestLLMRealChineseQASanity`，贪心确定性输出）：

| 输入 | 输出 | 判读 |
|---|---|---|
| 你好，你是谁呀？ | 你好！我是小云雀，一个陪伴儿童的语音助手。有什么可以帮到你的吗？ | 人设正确，切题 |
| 1加1等于几？ | 1加1等于2。 | 正确 |
| 天上的月亮为什么会跟着我们走？ | 月亮是地球的卫星，它在天空中运行，与我们一样，只是我们站在不同的地方。 | 切题（表述朴素，可接受） |
| 给我讲一个关于小兔子的短故事。 | 小兔子小花最喜欢跳房子……和朋友们一起玩。 | 通顺成篇，逻辑略跳跃（0.6B 量级可接受） |

无思考链漏出（空 `<think>` 前置 + stripThink 兜底，断言面）。

### 结论与预算提示

1. **VAD 可忽略**：RTF≈0.4%，`configs/budgets/latency.yaml` 的 VAD 段无需调整。
2. **ASR 贴实时线**：4 核 CPU 上 int8 非流式 RTF≈0.84~1.0（5s 句 ≈4.3~5.1s 墙钟），
   且当前 PoC 为整句识别——真正的话轮交互要等「尾静音→定稿」后才开始解码，
   话轮级感知延迟 ≈ ASR 全句墙钟。按 intra-op=4 口径，5s 话轮 ≈4.3s 端到端语音定稿延迟，
   已超典型对话体验线（>2s）。M2 若做流式/分段增量解码或换 streaming 模型
   （FireRedASR2S-LLM 通道 / SenseVoice 类），可显著压缩首字延迟。
3. **不修改 `configs/budgets/latency.yaml`**（同桩阶段纪律：无 founder 决策不动阈值）；
   上述 ASR 段缺口升级给 founder 做预算划拨决策（M2 正式排期时）。
4. 测试内 RTF 断言仅做合理性校验（<1.5，容忍 nice 抖动），非预算门禁。
5. **LLM 可用但有缺口**：4 线程生成 ≈29.5 tok/s，单句回复墙钟 0.7~2.3s（句长相关），
   叠加 ASR 定稿延迟后全链语音回合将显著超典型对话线；预算划拨与 LLM 段目标值
   升级给 founder（M2 正式排期时），同第 3 条不动 configs/budgets。
6. **LLM 内存面**：进程 RSS ≈869MB，与 VAD/ASR/TTS 同进程部署时需按机器内存档
   （本机 7.4G 可容纳；更低端目标硬件待 founder 定档）。

## 流式换型实测（2026-09-03，feat/t14-m2-asr-stream，issue #133）

口径：本机 CPU（4 核 7.4G，进程 nice -n 19）、ONNX Runtime 1.29.0、intra-op=2
（默认，T3 对齐口径）、`go test ./packages/go/inference -run 'TestASRStreaming' -v`，
各 10 次取 P50/P95；音频 = `test_wavs/1.wav`（16kHz PCM16，5.10s）。
模型 = sherpa-onnx-streaming-zipformer-bilingual-zh-en-2023-02-20（Apache-2.0，
encoder int8 + decoder fp32 + joiner int8；选型与块语义见 ADR-0013）。

三延迟定义（流式引擎 `StreamingZipformer`，消费方按 40ms 块喂入）：

- **首字延迟**：音频起点（t0）到首个非 blank token 出现的墙钟（实时节奏口径 =
  按墙钟节拍喂入，模拟麦克风流；尽速口径 = 不等节拍，纯计算面）。
- **RTF**：整句识别总墙钟 / 音频时长（尽速喂入）。
- **定稿延迟（endpointing 后）**：端点（话轮尾）触发 `Finish()` 到最终文本可用的
  墙钟——流式下文本已随音频增量产出，定稿仅刷新尾块（≤320ms 音频的零填充解码）。

| 指标（10 次） | 流式 zipformer int8 | 非流式 FireRedASR2 int8（对照） |
|---|---|---|
| RTF P50 / P95 | **0.121 / 0.149** | 0.984~1.008 / 1.042~1.072 |
| 首字延迟 P50 / P95（实时节奏） | **1350 / 1376 ms** | 不可用（整句解码，无中间文本） |
| 首字延迟 P50（尽速，纯计算面） | 128 ms | — |
| 定稿延迟 P50 / P95（Finish） | **26 / 30 ms**（实时节奏口径） | 4.3~5.1s（= 全句墙钟，intra-op=4 口径） |

补充：intra-op=4 时流式 RTF 反升至 P50=0.250（块级小推理线程争抢），
维持默认 2 线程口径；非流式首字/定稿两栏按定义不存在/等同全句墙钟。

### 验收线结论（如实报）

1. **话轮级定稿延迟 ≤2s：达成**（26ms，缺口消除；非流式为 4.3~5.1s）。
2. **RTF ≤0.5：达成**（P50=0.121、P95=0.149，约为非流式 1/8）。
3. **首字延迟 1350ms（<2s 但贴线）**：下限是模型块缓冲——首块须攒满 390ms，
   首个非 blank token 出现于第 4 块（≈1.35s 音频处），计算占比 <50ms。
   再压须换更小首块的导出档或更小模型，本 PoC 不追求（正式换型时再议）。

### 正确性锚点与取舍

- 1.wav Go 全链输出 `这是第一种第二种叫呃与▁ALWAYS▁ALWAYS什么意思啊` 与 Python ORT
  流式原型、FireRedASR2 非流式 golden **三方逐字一致**（`TestASRStreamingGoldenTranscript`）。
- 8 条测试 wav：7 条 16kHz 全部出合理中英混文本（8k.wav 重采样口径外跳过）；
  增量部分文本前缀单调（`TestASRStreamingIncrementalMonotonic`）。
- **精度口径注记**：流式模型对重口音条目与 FireRed 有词级差异（3-sichuan
  「自己就是在那个…情节…戏演得特别好」→「纸巾就是在那个…清洁…是演的特别好」），
  正式换型前建议以 T2 数据飞轮真实儿童语音做一次双模型 WER 对比（M2 收口项）。
- **量纲陷阱（实测踩坑）**：本模型为 icefall 归一化波形（[-1,1]）裸 log-mel 口径，
  与 FireRedASR2 的 int16 量纲相差 2·ln32768≈20.7 的 log-mel 偏移；错喂量纲时
  encoder 无报错但缓存数值 3~4 倍畸变、转写全空。经逐元素特征对拍定位后以
  `pcm16ToUnitDomain` 修正（详见 ADR-0013 决策 4）。
- 不修改 `configs/budgets/latency.yaml`（ASR 段预算划拨仍留 founder 决策）。

## TTS（MeloTTS ORT 真合成，2026-09-04，feat/t13-m2-melo-ort / PR #153）

口径：本机 CPU（4 核 7.4G，进程 nice -n 19）、ONNX Runtime 1.29.0、
`go test ./packages/go/tts -run 'TestMelo' -v`，三档句长各 10 次取 P50/P95；
前端=查表 g2p + 确定性噪声（splitmix64+Box-Muller），推理面=ORT 会话 Run
（`packages/go/tts/meloort`，yalue/onnxruntime_go v1.35.0）。
数据源：`reports/eval/T13/melotts-rtf-go.json`（n=30，Go 全链）；Python 基线
`reports/eval/T13/melotts-rtf.json`（n=20，intra_op=4）。

| 档 | 句例 | RTF P50 / P95 | 推理 P50（infer_ms） |
|---|---|---|---|
| 短（≈7 字） | 你好呀。 | 0.826 / 0.909 | 745 ms |
| 中（≈18 字） | 今天天气真好，我们一起去公园玩吧。 | 0.789 / 0.829 | 2437 ms |
| 长（≈40 字） | 从前有一只小兔子… | 0.779 / 0.893 | 5034 ms |
| **全量 n=30** | — | **0.791 / 0.893**（max 0.909） | 2383 ms |

- 30/30 全部 RTF<1（实时可合成）；对照 Python ORT intra_op=4 同机 rtf_p50=0.457：
  线程减半（Go intra_op=2）+ nice 19，RTF 升至 ~0.79 属预期口径差。
- 闭环 wav：`reports/eval/T13/samples/melotts-zh-goort-{0,1}.wav`。
- 对拍（会话级）：3 句 Go/Python ORT 逐样本 max_abs ≤7.3e-06、SNR ≥95 dB、
  Pearson r=1.0；前端结构符号一致率 1.000。

### 延迟预算缺口（如实陈述）

1. **tts_first P50 200ms / P95 280ms（BI-13.2 首包≤300ms）**：非流式整段出语义下
   first_packet≈infer_ms（短句 745ms、长句 5.0s）→ **缺口 3.7×~25×**。
   消解路径=流式导出（ADR-0008 债务⑤）或分句+预合成缓存（路径 C 架构），
   非装配层可解。
2. **RTF≤0.5（T13-G1-01 端侧线）**：本机 intra_op=2 + nice 19 口径 P50=0.791 未达
   （Python intra_op=4 基线 0.457 贴线）；目标硬件真机 500 句实测归 T13-G1-01
   门禁 debt，维持不变。

## 全链 E2E 组装（VAD→ASR→LLM→TTS，2026-09-04）

以下汇总四段真模型实测数字（均为本机 4 核 7.4G 开发机口径，非目标端侧）：

| 段 | 模型/引擎 | 关键指标 | 备注 |
|---|---|---|---|
| VAD | Silero VAD v5 ONNX | RTF P50=0.0038 / P95=0.0046 | 可忽略（≈260× 实时富余） |
| ASR | sherpa-onnx streaming zipformer 双语 int8（ADR-0013） | RTF P50=0.121 / P95=0.149；定稿延迟 26/30ms；首字延迟 1350/1376ms | 话轮级定稿≤2s ✓、RTF≤0.5 ✓ |
| LLM | Qwen3-0.6B Q4_K_M GGUF（ADR-0012） | 生成吞吐 29.53 tok/s；TTFT ≈0.35s；单句回复 0.7~2.3s；RSS ≈869MB | cgo 申报待批 |
| TTS | MeloTTS ORT 真合成（PR #153） | RTF P50=0.791 / P95=0.893；推理 P50 2383ms；对拍 max_abs ≤7.3e-06 | tts_first 首包缺口 3.7×~25×；RTF≤0.5 未达（debt） |

### 结论

- **计算面分段全部真模型落地**：VAD/ASR/LLM/TTS 四段均已有 ONNX/GGUF 真权重 +
   Go 推理代码 + 单元测试/对拍证据。
- **体验面瓶颈 = TTS 首包（非流式整段出）**：短句 first_packet≈745ms、长句≈5.0s，
  远超 BI-13.2 首包≤300ms。根因=当前 MeloTTS 导出为整段 wav，无流式接口；
  消解路径已登记 ADR-0008 债务⑤（流式导出）或分句+PhraseCache 预合成。
- **不改 `configs/budgets/latency.yaml`**：TTS 段预算划拨仍留 founder 决策；
  T13-G1-01 RTF≤0.5 维持 debt（真机 500 句实测后复评）。
- issue #133 验收第 2 条「全链路端侧延迟实测报告」补齐；后续待 T14 M2 全链
   E2E 联调（VAD→ASR→LLM→TTS 端到端墙钟）后统一验收。
