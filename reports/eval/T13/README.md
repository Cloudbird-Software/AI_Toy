# T13 W3 评测报告：MeloTTS-Chinese ONNX 导出·对拍·RTF 实测（issue #132 / ADR-0008）

日期：2026-09-03　环境：4 核 x86-64 Linux（开发宿主机，非目标端侧硬件）· torch 2.4.1+cpu ·
onnx 1.17.0 · onnxruntime 1.19.2（CPUExecutionProvider，intra_op=4）· Python 3.11
导出物：`/root/workspace/datasets/models/melotts-zh/onnx/melotts-zh.onnx`（170,372,099 B，
opset 17，批维固定 1，动态轴=token 数 t 与音频样本数；manifest `models/manifests/t13-melotts-zh.yaml`）

## 1. 导出语义

上游 melo/api.py→SynthesizerTrn.infer 逐行重写为单图（音素序列+噪声张量→44.1kHz 波形），
唯二改动=采样噪声显式化：

- SDP reverse 内部 `randn(1,2,T)` → 输入 `sdp_noise`（图内 ×noise_scale_w）；
- `z_p = m_p + randn_like(m_p)·exp(logs_p)·noise_scale` → 输入 `z_noise`
  （形状数据相关=mel 长度 Tm：输入时间维按 8×T 预留，图内动态切片到 Tm）；
- `noise_scale/noise_scale_w/length_scale/sdp_ratio` 为标量输入（上游默认 0.6/0.8/1.0/0.2；
  语速=length_scale=1/rate）；BERT 特征槽为图输入（ZH_MIX_EN 路径 1024 维槽恒零、
  768 维 mBERT 槽承载韵律特征——端侧 Go 路径当前恒零，见 §5 债务）。

随机性显式化后，确定性 P1 由调用方持有：Go 侧 splitmix64+Box-Muller 从
(seed,text,voice) 派生噪声（`packages/go/tts/melo.go`），同输入两次合成字节级一致。

## 2. 对拍一：导出忠实性（原版 infer vs 导出图，同噪声张量）

monkeypatch 法：拦截原版 infer 内部唯二随机点喂入指定噪声 → ref；同噪声喂导出图 → patch。
`tools/tts/export_melotts_onnx.py parity`，5 句：

| 句 | max_abs（原版 vs 图） | SNR（图 vs PyTorch） | Pearson r |
|---|---|---|---|
| 你好呀，我是小云。 | **0.0（逐位一致）** | 88.5 dB | 1.000000 |
| 今天我们一起搭积木好不好？ | **0.0** | 102.1 dB | 1.000000 |
| 从前有一只小兔子，它住在森林里。 | **0.0** | 108.7 dB | 1.000000 |
| 一二三四五，上山打老虎。 | **0.0** | 103.8 dB | 1.000000 |
| 小猫咪，毛茸茸，看见老鼠喵喵叫。 | **0.0** | 104.4 dB | 1.000000 |

证据链：原版 infer ≡ 补丁图（bitwise）⇒ 补丁图 ≡ ONNX（max_abs≤1.17e-05，fp32 数值噪声级）
⇒ **ONNX 导出物 = MeloTTS-Chinese 官方模型**。ORT 同输入两次运行字节级一致（确定性）。
逐句明细：`melotts-parity.json`。

## 3. 对拍二：Go 前端（ChinesePhonemizer）vs Python 参考 g2p

`tools/tts/dumpphonemes`（Go）× `tools/tts/compare_go_frontend.py`（参考=chinese_mix g2p v2
全量：jieba+tone sandhi+pypinyin），同 5 句：

- **电话音符号序列 5/5 全对齐（mean_phone_agreement=1.0）**——含四→`s i0`（opencpop
  卷舌韵母）、呀→轻声等细节全部命中；
- 声调分歧 5 句均有，且**全部落在 ADR-0008 已声明债务类**：三三变调（你 3→2）、
  「一」变调（一 1→4）、上上变调（老 3→2）、多音字（茸 1→2）——查表法按字典本调，
  无上下文变调；
- **Go token 序列全部成功经 ONNX 合成出音频**（`samples/melotts-zh-gofrontend-*.wav`，
  JaBert 恒零口径=真实 Go 端侧路径预演）。

明细：`go-frontend-parity.json`。

## 4. RTF 端侧实测（CPU 参考值，非门禁证据）

`rtf` 子命令，20 句儿童向对话，推理面（不含前端），44.1kHz：

| 指标 | 值 | 门禁/资产卡线 |
|---|---|---|
| RTF p50 | **0.457** | ≤0.5（BI-13.2）——贴线 |
| RTF p95 | **1.192** | 超线（短句被整段推理固定开销支配） |
| RTF max / mean | 1.338 / 0.613 | — |
| 推理时长 p50 / p95 | 1126 / 1971 ms | 端首包≤150ms（BI-13.2）——整段出语义下不适用 |
| 前端（g2p+BERT）均值 | 296 ms | 端侧需 Go 化/轻量化 |

结论：**当前导出（整段非流式）+ ORT fp32 在本机不满足 RTF≤0.5@p95 与端侧首包预算**；
本机为桌面 4 核 CPU 非目标端侧硬件，数据仅作优化基线。故 **不动
configs/budgets/latency.yaml**（无目标硬件数据，写入误导性数字违背预算纪律），
T13-G1-01 维持 debt verdict（真机 500 句口径 M2 复测）。优化方向（ADR-0008 债务表）：
int8 量化、流式/分句导出、端侧目标机复测。

试听样例（官方音色 ZH，语速 1.0）：`samples/melotts-zh-official-{0,1,2}.wav`。

## 5. 门禁与债务状态

- 门禁 `configs/gates/T13.yaml`（gaterunner run --suite nightly，exit 0）：
  T13-G0-01 对抗注入读出=0 **pass**；T13-G1-01/G1-03/G1-02 **debt**（与接线前一致，
  本分支未弱化未放宽）；coverage 16 资产/79 断言过；verify-configs 80 门 0 违反。
- L4 合成自然度评审：tools/judge 锁模型+金标锚定面未在本任务展开（金嗓 3 条待创始人
  选定），未内联裸调任何 LLM——缺口如实记录。
- 债务（ADR-0008）：onnxruntime Go 绑定（装配层）｜JaBert 韵律特征供给｜变调/多音字/
  位级数字前端｜英文 g2p｜流式导出｜云服务端实服｜真机 RTF 复测。

## 6. 红线合规

禁克隆真实儿童声音：两模型仅官方音色（端=ZH spk1；云=服务端白名单裁决）；
儿童音色偏好经 `ZH@rate=` 语速参数化（0.5..2.0），pitch 参数显式拒绝不静默。
许可：MeloTTS MIT（repo rev 2091453）/ IndexTTS-1.5 Apache-2.0，台账
`configs/licenses/ledger.yaml` + `models/manifests/` 双登记。

## 7. 复现

```bash
# 导出（~9min，内存≤3G）
python3 tools/tts/export_melotts_onnx.py export
# 对拍 + RTF + 前端对拍
python3 tools/tts/export_melotts_onnx.py parity --json reports/eval/T13/melotts-parity.json
python3 tools/tts/export_melotts_onnx.py rtf --json reports/eval/T13/melotts-rtf.json
go run ./tools/tts/dumpphonemes < sentences.txt > go.json
python3 tools/tts/compare_go_frontend.py go.json --json reports/eval/T13/go-frontend-parity.json
```
