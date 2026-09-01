# T5 声纹（sv）训练评估报告

## 这是什么

T5 声纹识别模型的离线训练成果存档（CPU 路线天花板探索）。冻结 speechbrain/spkrec-ecapa-voxceleb 预训练 embedding，训练轻量分类头，未微调 backbone。

- 正式模型：`models/incoming/t5_ecapa.onnx`（manifest: `models/manifests/t5-voiceprint-ecapa.yaml`）
- 路线：冻结 backbone 提 192 维 embedding（全长、逐条、无截断），52 类线性 300 epoch

## 训练数据（合成）

- 52 成员 + 24 陌生人，纯合成声纹（单 TTS 音色 cixingnansheng + ffmpeg pitch/speed 确定性增强）
- 成员区分 = tts_style 指令 + ffmpeg asetrate pitch（child +4~6st / adult 0 / elder −3~−2st）+ speed
- 76 名说话人共用一个 TTS 基础音色；未包含真实人声变异（信道/设备/情绪/生理）
- 合成↔真实域差未测量，本 EER 不可外推到真机

## 最终指标

| 指标 | 实测 | 门禁 | 判定 |
|---|---|---|---|
| EER（全池） | 17.23%（CI95 15.41~18.69%，阈值 0.8154） | T5-G1-01 ≤5% | **FAIL** |
| 再识别 top-1（closed-set 52 类） | 26.9% | ≥95% | FAIL |
| 陌生人拒判 @EER 阈值 | clip 50.0% / 说话人 20.8% | T5-G1-04 ≥90% | **FAIL** |

## 门禁结论

- **T5-G1-01（EER≤5%）：FAIL**（17.2%，CI 下界 15.4% 仍远超线）
- **T5-G1-04（陌生人拒判≥90%）：FAIL**（可通过抬高阈值名义达成 100% 拒判，但 TAR 崩到 10%，系统不可用，不构成 PASS）

## 可分性分析（核心结论）

**可分性 = 渲染参数可分性。** ECAPA 读到的"声纹"主要是 pitch/语速的声学投影；adult 组参数上不可分（pitch 恒 0），embedding 亦不可分，二者完全一致。全池 EER 17.2% 的"好看"部分来自跨年龄简单负样本（分年龄段 EER：child 25.9% / adult 36.8% / elder 33.7%，全部更高）。

当前合成数据域无法支撑门禁级声纹指标，CPU 路线已测出冻结 embedding 路线的天花板。GPU 微调（`code/train_sv.py`，ArcFace/AAM）待训。

## 复现性存档

- `code/train_sv.py`：GPU 微调骨架（ArcFace/AAM + 三秒随机截断 + fp16）
