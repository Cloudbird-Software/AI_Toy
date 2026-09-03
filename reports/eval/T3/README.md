# T3 话轮预测（MaAI MC-VAP 中文 Kyoto 版）接入评测报告

## 这是什么

T3 话轮预测真模型接入（W3 接入线 IR #129）的落仓存档：选型、融合 ONNX 导出与对拍、
门禁升级、RTF 实测、对照评测。选型与工程决策记录见 `docs/architecture/decisions/ADR-0007`。

- 正式模型：`models/incoming/vap_maai_ch_kyoto_mc_10hz.onnx`（manifest: `models/manifests/t3-vap-maai.yaml`，MIT）
- Go 引擎：`packages/go/turntaking/vap`（ONNX Runtime Go 绑定，仓内首个推理栈，T5/T14 复用）
- 门禁：`just gate T3 all`（本机无 just，等效命令 `go run ./tools/gaterunner/cmd/gaterunner run --asset T3 --level all --report reports/gates/T3.json`）→ **g0=pass g1=pass not_impl=0 exit 0**（debt 3：G0-02/G1-03/G1-04，均数据面）

## 选型（license 优先）

| 候选 | license | 结论 |
|---|---|---|
| vap_mono_mimi_ch 12.5hz（任务卡建议） | **CC-BY-NC-ND 4.0**（属 vap_ch 仓） | 否决：不可商用 |
| vap_ch 通用中文全系 | CC-BY-NC-ND 4.0 | 否决：不可商用 |
| **vap_mc_ch_kyoto 10hz（MC-VAP 噪声鲁棒）** | **MIT**（训练语料=Online Conversation Dataset，京都大学） | **选用** |
| vap_ch_kyoto 10hz（plain） | MIT | 对照基线（同架构消融） |
| 原版 VAP（Erik Ekstedt，英语） | 代码 MIT、checkpoint 训练数据 Switchboard/LDC 许可链不适配商用，权重未随任务提供 | 不作对照，见下文口径说明 |

关键发现：MaAI 权重的 MIT 只覆盖 `*_kyoto` 子仓；任务卡所述「单声道模型（MIT）」与 HF 实际
分层不符（mono 属 NC 仓）。商用红线（AGENTS.md）下唯一可用中文权重为 kyoto 子仓。

## 导出与对拍

融合流式 ONNX（opset 17，24.6MB）：CPC encoder×2 + AR-LSTM hidden + GPT 主干 KV + 输出头，
全固定形状；输入 1920 样本滚动窗（320 左上下文+1600 新帧），缓存裁剪（保留最新 199 帧）入图。
等价性依据：ALiBi 偏置对动态缓存为逐行常数平移、softmax 平移不变。

| 对拍面 | 结果 | 要求 |
|---|---|---|
| Python eager（导出包装器）vs 官方 Maai 流式参考 | max abs err **2.4e-7**（300 步全幅音频） | — |
| Python ORT 1.19.2 vs 官方 Maai 流式参考 | max abs err **1.8e-5**（300 步） | <1e-3 ✅ |
| Go ORT 1.29（运行时栈）vs golden 参考向量 | 40 帧全绿（容差 1e-3） | <1e-3 ✅ |

golden 向量与生成/对拍脚本：`packages/go/turntaking/vap/testdata/golden.json`、`code/`。

## RTF 实测（CPU，本机 4 核）

| 口径 | 数值 |
|---|---|
| Go ORT（运行时真身，IntraOp=2） | **RTF=0.102（P50 9.5ms/帧，100 帧均值；P95 10.1ms）** |
| Python ORT（导出侧参考） | RTF=0.363（36.3ms/帧） |

端侧常驻可行（RTF≪1）。**延迟预算划拨说明**：预算未变更。VAP 逐帧推理与音频采集流水线
并行，端点判定增量 ≤10ms/帧（最终帧），被 `configs/budgets/latency.yaml` 的
`tail_silence`（T3，P95 600ms）段吸收；G1-02 的 900ms 全链（话轮终点→TTS 首包）中
ASR/LLM/TTS 段与固定硬件 ×3 计时归 M2，T3 面内增量以门禁锁定 ≤100ms。

## 门禁升级（IR #129：从桩值到真实模型输出断言）

| ID | 前 | 后 | 断言面 |
|---|---|---|---|
| T3-G1-01 | debt | **真实**（迁 turntaking/vap） | 真模型在环 300 停顿点：误截断=0；语音中零提前（安全不变量）；全静音 200 帧 PNowSystem<0.7（G0-02 互补面） |
| T3-G1-02 | debt | **真实**（迁 turntaking/vap） | 端点判定增量 P95=10.1ms ≤100ms（检测级实时）；900ms 全链归 M2 |
| T3-G1-03 | debt | debt（不变） | 需 LLM 对话链（M2） |
| T3-G1-04 | debt | debt（不变） | VAP 提前量不解决中停顿——需自适应静音门限+真实儿童集 ≥100 |
| T3-G0-01/02 | 真实/debt | 不变 | 打断链不受预测接缝影响（predict_test 断言） |

诚实性边界：G1-01/G1-02 的 harness 音频为确定性合成语音代理（正弦突发，测试内生成），
真实儿童集（3–6/7–9/10–12 岁分层 ≥300 停顿点）仍未建——本批断言锁机制面，
业务阈值外推见下方「真实语音行为」与遗留问题。

## 真实语音行为与对照评测（对照表）

真实成人语音（MaAI 示例 wav：eng/jpn 各 1）× SNR {clean,20,10,5}dB，帧级 VAD 与能量
伪真值的一致性（评估口径伪真值，非人工标注；5dB 档伪真值本身退化，数字仅作趋势）：

| 场景 | MC-VAP（选用） | plain ch_kyoto（对照） |
|---|---|---|
| eng clean / 20dB | 0.847 / 0.860 | 0.813 / 0.480 |
| **eng 10dB** | **0.880** | **0.473** |
| jpn clean / 20dB | 0.900 / 0.897 | 0.903 / 0.903 |
| **jpn 10dB** | **0.897** | **0.307** |
| 5dB（babble/白噪） | 0.557 / 0.817 | 0.537 / 0.783 |
| 全静音 p_now_system 上限 | 0.505（<0.7 门限 ✅） | 0.519 |

结论：**MC 噪声鲁棒性量化成立**——10dB 家居噪声下 plain 崩塌（0.31–0.47）、MC 保持
（0.88–0.90）；clean/20dB 两者相当；全静音两构型均不越过提前量门限 0.7。
数据：`comparison.json`；脚本：`code/eval_compare.py`。

**英语原版 VAP 基线对照口径说明**（issue #129 第 5 条）：原版权重未随本任务落盘，且其
训练语料（Switchboard，LDC 许可）不适配商用链路；MaAI 官方 `vap_en` 亦为 NC。故对照
采用同 MIT、同架构、同导出管线的 `ch_kyoto`（plain）作消融基线——差异维度=噪声鲁棒
训练（MC 的唯一变量），结论可直接归因。行为级对照（p_now 分布、静音面）两模型同表可查。

## 遗留问题

1. **真实儿童集未建**（G1-01 全量业务断言、G1-04 整面）：3–6/7–9/10–12 岁分层 ≥300 停顿点
   双人标注子集；真实-合成差距未测，禁宣称儿童面达标（AGENTS.md 常见坑）。
2. **自适应静音门限机制未建**（G1-04 前置）：VAP 提前量只加速收口，不修复 1.5–3s 中停顿误截断。
3. **≥6h 负样本音景流**（G0-02 数据面）：与 T4 共用音景库，synthgen 注册流程未走。
4. **LLM 对话链**（G1-03）：M2 接入。
5. **全链 P95 ≤900ms**（G1-02 完整面）：需固定硬件 ×3 实测（M2）。
6. `just` 本机未安装（等效命令见文首）；`repoctl forbidden-refs` 存在 1 处 T4 遗留违规
   （`reports/eval/T4/code/train_kws.py:11`，commit 0b12a37 引入，非本任务文件）。

## 复现性存档

- `code/export_vap_onnx.py`：融合导出 + eager/ORT 对拍（上游权重路径见文件头；CPC checkpoint
  `~/.cache/cpc/60k_epoch4-d0f474de.pt`，源 dl.fbaipublicfiles.com/librilight）
- `code/make_golden.py`：golden 向量生成（Go 可复现公式）+ 真实语音行为探针
- `code/eval_compare.py`：对照评测（本文对照表来源）
- 环境纪律：Python 重活经 `systemd-run --scope -p MemoryMax=3G nice -n 19`；本机 loadavg<2 未启用 GPU 机
