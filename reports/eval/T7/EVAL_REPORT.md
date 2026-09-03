# W2-T7 情绪引擎双通道评测报告（issue #130）

> 本报告覆盖语音通道（SenseVoice-Small 零样本 + 线性头微调）与文本通道（GoEmotions TF-IDF+LogReg）
> 的 test/holdout 结果，以及儿童域差定性数据。所有数值均带 bootstrap 95% CI。

## 1. 数据与映射总览

| 数据集 | 用途 | 样本量 | 标签空间 | 备注 |
|---|---|---|---|---|
| CREMA-D | 语音零样本 / 线性头训练 | 7442 wav（4 类子集 4900） | 4 → T7 | DIS/FEA 无发布权重对应，单列 runner-up |
| JL-Corpus | 线性头训练补充 | 2400 wav | 4 → T7 | 说话人留出仅限 CREMA-D |
| GoEmotions simplified | 文本通道训练/评测 | train 43410 / val 5426 / test 5427 | 27+1 → T7 9 | sleepy 无来源类，文本通道边界 |
| T5-SV audio v2 | 儿童域差同文对照 | child 198 / adult 220 / elder 154 | 4 → T7 | common5 模板配对 |
| T4-KWS audio v3 | 儿童域差组级对照 | child 1440 / adult 1440 | 4 → T7 | 6 style × 2 aug × 2 group |
| T7-audio | 儿童域差 10 类合成 | 1200 | 10 intended → T7 | aug 漂移分析 |

### 核心映射表（唯一事实源：data/emotion_map.json）

**GoEmotions 27+1 → T7 9 类（多标签负性优先 tie-break）**

| GoEmotions | T7 | V | A | 备注 |
|---|---|---|---|---|
| admiration / approval / caring / gratitude / relief | content | + | low/mid | 温和满足 |
| amusement / curiosity / joy / love / optimism / pride | happy | + | mid | 正性参与 |
| anger / annoyance / disgust / disapproval | angry | - | mid/high | 愤怒族 |
| confusion / realization | surprised | ± | mid | 突发认知更新 |
| desire / excitement | excited | + | high | 高唤醒期待 |
| fear / nervousness | scared | - | high | 恐惧族 |
| sadness / disappointment / embarrassment / grief / remorse | sad | - | low | 悲伤族 |
| surprise | surprised | ± | high | 同型映射 |
| neutral | calm | 0 | low | 低唤醒中性 |

> **sleepy**：GoEmotions 27 类无来源类（Reddit 语料无"困倦"细粒度标注），文本通道不覆盖。
> 生产兜底由状态机上下文（时间/交互史）与语音通道低唤醒特征承担。

**SenseVoice 4+1 → T7**

| SenseVoice | T7 | 生产语义 |
|---|---|---|
| HAPPY | happy | — |
| SAD | sad | — |
| ANGRY | angry | — |
| NEUTRAL | calm | — |
| EMO_UNKNOWN | calm | 无情绪信号→回退 calm 带，交状态机融合 |

**CREMA-D → SenseVoice 4**

| CREMA-D | SenseVoice | 说明 |
|---|---|---|
| ANG | ANGRY | — |
| HAP | HAPPY | — |
| SAD | SAD | — |
| NEU | NEUTRAL | — |
| DIS / FEA | null | 无发布权重对应类，runner-up 单列分析 |

**JL-Corpus → SenseVoice 4**

| JL | SenseVoice | 说明 |
|---|---|---|
| happy / excited | HAPPY | 唤醒向粗粒度合并 |
| sad / apologetic | SAD | 唤醒向粗粒度合并 |
| angry | ANGRY | — |
| neutral | NEUTRAL | — |

## 2. 语音通道：SenseVoice-Small 零样本（CREMA-D 4 类）

### 2.1 双口径对比

| 口径 | macro recall | CI95 | 说明 |
|---|---|---|---|
| raw（生产） | 0.0680 | [0.0609, 0.0746] | 92.5% 预测为 EMO_UNKNOWN |
| forced（四选一） | 0.7381 | [0.7257, 0.7497] | 强制枚举 4 类 |

### 2.2 Forced 口径 per-class recall（n=4900）

| 类 | n | recall | CI95 |
|---|---|---|---|
| NEUTRAL | 1087 | 0.7424 | [0.7157, 0.7672] |
| HAPPY | 1271 | 0.5452 | [0.5185, 0.5720] |
| SAD | 1271 | 0.9072 | [0.8906, 0.9229] |
| ANGRY | 1271 | 0.7577 | [0.7341, 0.7813] |

### 2.3 τ-门控工作点（部分）

| τ | macro recall | 拒识率（EMO_UNKNOWN） |
|---|---|---|
| 0.0 | 0.0680 | 92.5% |
| 1.0 | 0.0006 | 99.9% |
| 3.0 | 0.0000 | 100% |

### 2.4 DIS/FEA 落点（发布权重未训出，不作生产输出）

- DISGUST forced 分布：SAD 797 / NEUTRAL 221 / ANGRY 138 / HAPPY 115
- FEAR forced 分布：SAD 804 / NEUTRAL 143 / ANGRY 153 / HAPPY 171
- FEARFUL runner-up rank 均值 4.67；DISGUSTED runner-up rank 均值 3.72

## 3. 文本通道：GoEmotions TF-IDF + LogisticRegression（test）

### 3.1 模型与数据

- 模型：TF-IDF(word 1-2 + char_wb 3-5) + LogisticRegression(C=0.5, class_weight=balanced)
- 训练：43410 / 验证：5426 / 测试：5427
- 多标签样本按负性优先级归一为单标签（scared > angry > sad > surprised > excited > happy > content > calm）

### 3.2 Per-class recall（T7 映射后）

| 类 | n | recall | CI95 |
|---|---|---|---|
| sad | 345 | 0.5565 | [0.5043, 0.6087] |
| scared | 98 | 0.7551 | [0.6633, 0.8367] |
| angry | 819 | 0.5665 | [0.5323, 0.6007] |
| sleepy | 0 | — | — |
| calm | 1606 | 0.5610 | [0.5373, 0.5853] |
| surprised | 394 | 0.5102 | [0.4619, 0.5609] |
| content | 1042 | 0.5845 | [0.5547, 0.6152] |
| happy | 952 | 0.5651 | [0.5336, 0.5956] |
| excited | 171 | 0.4971 | [0.4211, 0.5731] |

- **macro recall（8 类有支持类）**：0.5745，CI95=[0.5558, 0.5931]
- **Cohen's κ**：0.4710
- **多标签歧义率（test）**：11.5%

### 3.3 映射统计要点

- 最大源类：neutral（14219 条，映射到 calm）
- 负性高权重源类：admiration(4130)→content, amusement(2328)→happy, anger(1567)→angry, sadness(1326)→sad, fear(596)→scared
- 目标分布 test Top3：calm 1606 / angry 819 / content 1042

## 4. 儿童域差评估（T7 合成语音）

### 4.1 T5-SV 同文对照（child vs adult vs elder）

| 组 | n | strong_emotion_rate | CI95 |
|---|---|---|---|
| child | 198 | 95.45% | [92.42%, 97.98%] |
| adult | 220 | 50.00% | [43.64%, 56.82%] |
| elder | 154 | 75.32% | [68.18%, 81.82%] |

- **common5 模板配对（child 18 clips vs adult 20 clips）**
  - child strong rate：100%，CI=[100%, 100%]
  - adult strong rate：95%，CI=[85%, 100%]
  - delta（child − adult）：+5.16pp，CI=[0, 15.0]pp

### 4.2 T4-KWS 组级对照（child vs adult，各 1440）

| 组 | n | strong_emotion_rate | CI95 |
|---|---|---|---|
| child | 1440 | 86.94% | [85.21%, 88.68%] |
| adult | 1440 | 38.89% | [36.39%, 41.39%] |

- **delta（child − adult）**：+48.06pp，CI=[44.93, 51.18]pp
- **by style 最大差**：shout（child 95.8% vs adult 49.2%，Δ=46.7pp）

### 4.3 T7-Audio 10 类合成片段（n=1200，SenseVoice forced 4 类映射到 T7 后 per-class recall）

| 类 | n（intended） | recall | CI95 | 说明 |
|---|---|---|---|---|
| angry | 360 | 0.2667 | [0.2222, 0.3139] | disgust/frustration 合并落点 |
| calm | 120 | 0.7667 | [0.6917, 0.8417] | — |
| excited | 120 | 0.0000 | [0.0000, 0.0000] | SenseVoice 无高唤醒正性细粒度 |
| happy | 120 | 0.9167 | [0.8667, 0.9667] | — |
| sad | 240 | 0.6583 | [0.6000, 0.7167] | sadness+loneliness 合并 |
| scared | 120 | 0.0000 | [0.0000, 0.0000] | SenseVoice 无独立 fear 输出 |
| surprised | 120 | 0.0000 | [0.0000, 0.0000] | SenseVoice 无独立 surprise 输出 |
| **macro** | — | **0.3726** | **[0.3558, 0.3896]** | 7 类有支持类 |

> 注：excited/scared/surprised 在 T7 标签带中无对应 SenseVoice 发布类，recall=0 属粗粒度映射局限。

### 4.4 Aug 变体漂移（相对 none）

| aug | n_pairs | ΔHAPPY (point) | ΔSAD (point) | ΔANGRY (point) |
|---|---|---|---|---|
| instruction_v2 | 300 | +0.71% | +2.64% | +1.36% |
| pitch+2st | 300 | -3.65% | -4.67% | +3.32% |
| pitch-2st | 300 | -10.33% | +5.70% | +0.00% |

## 5. 许可台账

本任务实际使用/产出的资产许可如下（完整台账见 LICENSE.md）：

| 资产 | 许可 | 商用义务 |
|---|---|---|
| SenseVoice-Small | FunASR 模型许可 | **商用需署名**（见下方署名块） |
| GoEmotions simplified | CC BY 4.0 | 署名 + 许可链接 |
| CREMA-D | ODbL | 署名 + 同许可共享数据库衍生内容 |
| JL-Corpus | CC0 | 无强制（附礼貌引用） |
| TTS 合成儿童语音 | 工作区内部资产 | 内部使用（上游 TTS 商用条款由工作区统一管理） |

### FunASR / SenseVoice-Small 署名块（接入侧须原样携带）

> The emotion recognition channel of this product is powered by
> **SenseVoice-Small** (FunAudioLLM, Alibaba), used under the FunASR Model
> License. 商用署名：本产品语音情绪识别能力基于 FunAudioLLM 团队开源的
> SenseVoice-Small 模型（FunASR 模型许可）。
> Model: https://github.com/FunAudioLLM/SenseVoice · License: FunASR Model
> License（模型许可协议，商用需保留本署名）。

## 6. 关键发现与风险

1. **英文域零样本失效**：CREMA-D raw 口径 92.5% 输出 EMO_UNKNOWN，forced 四选一将 macro 拉到 0.738，但 DIS/FEA 仍无法区分。
2. **文本通道覆盖边界**：sleepy 在 GoEmotions 中无来源类，recall 为空；需状态机上下文兜底。
3. **儿童域强情绪偏移**：童声合成（TTS）显著偏向高唤醒正性/负性（child strong rate 86-95% vs adult 39-50%），真实儿童语音（ChildEC）被排除在商用库外，当前儿童域数据仅为合成近似。
4. **SenseVoice 粗粒度瓶颈**：T7 标签带 9 类 vs SenseVoice 4+1 类，excited/scared/surprised/loneliness 等无法在语音通道直接输出，需文本通道或状态机补位。
