# W2-T7 License 台账（issue #130 交付物）

任务：儿童情绪识别双通道（SenseVoice 语音 + GoEmotions 文本）。本台账覆盖
本任务实际使用/产出的全部数据与模型资产。排除项（issue 明令不下不碰）：
emotion2vec（CC BY-NC-SA / ModelScope 页标 NC）、RAVDESS / TESS / CaFE（NC）。

| 资产 | 版本/来源 | 许可 | 商用义务 | 本任务用途 |
|---|---|---|---|---|
| SenseVoice-Small | `/root/workspace/datasets/models/sensevoice/`（FunAudioLLM，ModelScope iic/SenseVoiceSmall 等价权重） | FunASR 模型许可（Model-License-Agreement） | **商用需署名**（见下方署名块） | 语音通道基座：零样本情绪评测 + encoder embedding 提取 + 线性头微调底座 |
| GoEmotions (simplified) | `/root/workspace/datasets/emotion/goemotions/simplified/`（Google Research） | CC BY 4.0 | 署名 + 许可链接 | 文本通道训练/评测（27 情绪 + neutral） |
| CREMA-D | `/root/workspace/datasets/emotion/cremad/`（Cao et al. 2014，7442 wav） | ODbL（数据库许可，署名） | 署名 + 同许可共享数据库衍生内容（本任务仅派生特征/统计，未再分发数据库本体） | 语音通道零样本评测 + 线性头训练（说话人留出） |
| JL-Corpus | `/root/workspace/datasets/emotion/jl-corpus/`（Jessika Luthien，GitHub） | CC0 | 无强制（附礼貌引用） | 语音通道线性头训练补充（英文，4 说话人） |
| TTS 合成儿童语音 | `/root/workspace/synth/t5_sv_audio_v2`、`t4_kws_audio_v3`、`t7_audio`（stepaudio-2.5-tts 本地生成，纯合成不模仿真实人物） | 工作区内部资产（上游 TTS 商用条款由工作区统一管理） | 内部使用 | 儿童域差定性评测 |
| 本任务产出（emotion_map.json、speech_head.pt、特征 npz、评测报告） | `/root/workspace/t7-w2/` | 随仓库（训练产物，内嵌上述数据的衍生统计） | 沿用上游署名义务 | 交付物 |

## FunASR / SenseVoice-Small 署名块（接入侧须原样携带）

> The emotion recognition channel of this product is powered by
> **SenseVoice-Small** (FunAudioLLM, Alibaba), used under the FunASR Model
> License. 商用署名：本产品语音情绪识别能力基于 FunAudioLLM 团队开源的
> SenseVoice-Small 模型（FunASR 模型许可）。
> Model: https://github.com/FunAudioLLM/SenseVoice · License: FunASR Model
> License（模型许可协议，商用需保留本署名）。

## 数据署名（产品化时须进入"开源致谢"页）

- GoEmotions: "GoEmotions: A Fine-Grained Emotion Classification Dataset"
  — Google LLC, CC BY 4.0（https://github.com/google-research/google-research/tree/master/goemotions）。
- CREMA-D: "CREMA-D: Crowd-sourced Emotional Multimodal Actors Dataset"
  — Cao, Cooper, Keutmann, Gur, Nenkova, Verma, IEEE Trans. Affective Computing, 2014，
  ODbL（https://github.com/CheyneyComputerScience/CREMA-D）。
- JL-Corpus: "JL Corpus" — Jessika Lüthien, CC0
  （https://github.com/JL-corpus/JL_corpus）。

## 排除清单（负面台账，防止后续任务误引入）

| 资产 | 许可 | 排除原因 |
|---|---|---|
| emotion2vec / emotion2vec+ | CC BY-NC-SA（ModelScope 页标 NC） | 非商用 |
| RAVDESS | CC BY-NC-SA | 非商用 |
| TESS | CC BY-NC-ND | 非商用 |
| CaFE | NC | 非商用 |
| ChildEC 儿童情绪对话 | CC BY-NC | Research-Only（issue #130/#136：仅挂研究线，不入商用库） |
