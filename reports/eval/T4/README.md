# T4 唤醒词（kws）训练评估报告

## 这是什么

T4 唤醒词模型的离线训练成果存档。模型基于 openWakeWord 官方管线（冻结特征管线 + auto_train 裁剪版），在 GPU 上以 45k 甜区 step-36118 候选交付，ONNX 格式，与端侧 openWakeWord 运行时 / Go M2 接入兼容。

- 正式模型：`models/incoming/t4_wakeword.onnx`（manifest: `models/manifests/t4-wakeword.yaml`）
- 操作阈值：0.20（harness 实测平台期 0.10~0.35，取平台中值偏下留 wake 余量）
- 终评 harness：`reports/eval/T4/metrics.json`（事件级口径，th=0.2）

## 训练数据（合成）

- 正样本：单 TTS 音色（cixingnansheng）+ 9 种 ffmpeg 变调/变速/回声变体，按 `word_style_seq` 哈希 8:2 底片级切分（防变体泄漏）
- 负样本：本机其他域 TTS 语音（t5/t13/t7 共 3.34h），按文件哈希 8:2 切训练/验证，与冻结批零重叠
- 远场代理（评估侧）：SNR {20,10,5}dB × {pink 平稳, babble 四人混叠}，本地合成噪声（与冻结批无参数关系）
- 无真实远场正样本、无真实儿童/成人录音；合成↔真实域差未测量

## 最终指标（终评 harness，th=0.2）

| 门 | 指标 | 实测 | 门限 | 结果 |
|---|---|---|---|---|
| G0-01 | 冻结批 tneg FP | 0 事件/6h | ≤0.5/h | PASS |
| G0-02 | kwsadv FP | 0 事件/30min | ==0 | PASS |
| G1-01 | 近讲唤醒 | 0.9936 | ≥0.97 | PASS |
| G1-01 | 远场五档 | 0.9544~0.9928 | ≥0.90 | 4/5 PASS |
| G1-01 | snr5_babble | **0.8736** | ≥0.90 | **FAIL**（差 2.6pp） |
| G1-02 | 公平性 gap | -0.36pp | ≤5pp | PASS |
| G1-03 | RTF | ≈0 | <1 | PASS |

**门禁汇总：6 门 5 PASS。**

## snr5_babble 0.8736 缺口定性

三种独立干预（选择集硬化、全步数轴 69 候选扫描、v4 纯噪声训练负样本 + 远场感知选择集）均撞同一前沿：**far≥0.90 与 FP≤0.5/h 在当前合成数据配方下不共存**。权重平均实验旁证 far 每 +1pp 代价 ~4 FP/h。

结论：**这是当前合成数据配方的能力前沿，非模型选择伪影。** snr5_babble 是合成代理口径最严档（5dB 四人 babble），真实 3m 家居场景大概率更宽松；要进一步需真实 RIR 卷积或真实远场录音数据迭代。

## 复现性存档

- `code/train_kws.py`：GPU 训练骨架（含 `--strong-noise` 通道、batch 特征 + ONNX 直接推理评估）
- `code/t4_kws_eval_cpu.py`：CPU 端评估驱动
- `code/t4_screen_candidates.py`：候选 checkpoint 秒级筛选
- `code/t4_operating_points.py`：全阈段操作点表

## 已知口径偏差

- harness 跨 clip 不 reset 有 ~0.5pp 虚增（§9.1 of TRAINLOG.md），接真机前建议统一 reset 语义
- 阈值/防抖参数在 Go 门禁帧口径（30ms）下待重标定（本轮 80ms 帧粒度为近似）
