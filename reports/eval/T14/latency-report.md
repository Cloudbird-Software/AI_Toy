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
> TTS 仍为桩，待补测。

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
