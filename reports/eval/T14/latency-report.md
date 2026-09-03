# T14 端侧离线运行时全链路延迟实测报告（桩阶段）

> 工具链说明：本机 `just` 不可用，延迟采样由 `go test ./packages/go/loop -run Latency` 的旁路
> `latencyTracker` 产出（逻辑时钟口径）；墙钟口径同文件 `LatencyWallReport()`。
> 真模型接入 sherpa-onnx/ORT Go 绑定后需补测并替换本报告。

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
  - VAD：Silero VAD ONNX 帧级推理
  - ASR：FireRedASR2 encoder/decoder int8 ONNX 流式
  - LLM：Qwen3-0.6B Q4_K_M GGUF llama.cpp 端侧
  - TTS：T13 melotts-zh 或 Piper 端侧合成

## 结论

当前为桩实现，全链路延迟为 0。不修改 `configs/budgets/latency.yaml`（无真实数据，
且 issue 明确要求如需修改必须写划拨说明；桩数据不具备划拨理由）。

待 M2 真模型接入后，需补充以下报告：
1. 各段 P50/P95/P99（ms）
2. RTF（ASR/LLM/TTS 各段）
3. 首字延迟（LLM 首 token / TTS 首包）
4. 与 `configs/budgets/latency.yaml` 预算的守恒对比
