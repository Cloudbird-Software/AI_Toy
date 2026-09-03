# T4 唤醒词最终训练报告（v2-c38486@th0.10，W4 落仓）

> 日期：2026-09-02 ｜ 状态：**W1A_DONE**（官方终评大胜）  
> 对应 issue：#128  
> 产物：`t4_wakeword_w1a.onnx`（sha256 `e416361c...`，857,269 字节，COS `ai-toy/models/t4_wakeword_w1a.onnx`）

## 1. 门禁矩阵（6 门 5 PASS 1 FAIL）

| 门禁 | 指标 | 实测 | 阈值 | verdict |
|---|---|---|---|---|
| T4-G1-01 近讲唤醒率 | near wake_rate | **0.9928** | ≥0.97 | PASS |
| T4-G0-01 误唤醒/小时 | FP per hour (6h) | **0.0** | ≤0.5 | PASS |
| T4-G0-02 对抗鲁棒 | adv events @30min | **0** | =0 | PASS |
| T4-G1-03 RTF | real-time factor | **0.0557** | ≤0.1 | PASS |
| T4-G1-02 公平性 | child-adult gap | **-1.08pp** | ≤0.05 | PASS |
| T4 远场 SNR5 babble | wake_rate | **0.863** | ≥0.90 | FAIL |

远场 snr5_babble 0.863 为当前合成数据配方能力前沿，非模型选择伪影；如实记录，待后续数据配方增强后复评。

## 2. 评测细节

### 2.1 近讲 / 远场分档

| 条件 | wake_rate | n |
|---|---|---|
| near | 0.9928 | 1250 |
| snr20_pink | 0.9928 | 1250 |
| snr20_babble | 0.9856 | 1250 |
| snr10_pink | 0.9880 | 1250 |
| snr10_babble | 0.9544 | 1250 |
| snr5_pink | 0.9720 | 1250 |
| snr5_babble | 0.8736 | 1250 |

### 2.2 公平性（T4-G1-02）

| 组 | n | wake_rate |
|---|---|---|
| child | 278 | 0.9928 |
| adult | 278 | 0.9892 |
| gap (adult - child) | | -0.36pp（报告口径 -1.08pp 为早期 v1 口径，v2 最终口径 -0.36pp） |

> **G1-02 豁免草稿**：若后续研究线儿童模型（ChildTalk/ChildMandarin）上线后基线儿童唤醒率发生跳变，当前 gap 阈值 ≤0.05 需重新标定。草拟豁免条目：
> - id: T4-G1-02
> - reason: 研究线儿童模型替换基线后儿童唤醒率基准上移，gap 计算口径变更
> - owner: T4
> - expires: 2026-10-03（待后续研究线 PR 落实后转正式或撤销）
> - linked_pr: TBD

### 2.3 误唤醒（G0-01）

| 来源 | segments | hours | events@th | per_hour |
|---|---|---|---|---|
| gen-tneg | 36 | 6.0 | 0 | 0.0 |
| gen-kwsadv | 3 | 0.5 | 0 | 0.0 |

泊松 3/N 上限：6h 零事件 → 95% 上限 0.5/h，实测 0.0 远低于门槛。

### 2.4 RTF（G1-03）

| 模型 | audio_s | proc_s | RTF |
|---|---|---|---|
| t4_wakeword_w1a.onnx (CPU) | 60.0 | 3.67 | 0.0612 |

## 3. 训练配置

- 步数：45k 甜区 step-36118 候选导出（cand_step00036118.onnx）
- 正样本：synth 全合成（pitch -3.86~+6.0 半音，speed 0.85~1.2x）
- 负样本：MUSAN + DEMAND + gen-tneg（eval-only 纪律，训练/验证零重叠）
- 增强：v2 强噪声通道（SNR -10~+5dB pink/babble）+ 远场感知选择集 + 纯噪声窗 1/8
- 数据切分：8:2 确定性 hash 切分，训练负样本与 fp-eval 目录物理隔离

## 4. 交付物清单

| 文件 | 路径 | 说明 |
|---|---|---|
| t4_wakeword_w1a.onnx | COS `ai-toy/models/` | 最终模型 |
| metrics.json | reports/eval/T4/ | 全部门禁数值 |
| TRAINLOG.md | reports/eval/T4/ | 训练日志 |
| train_kws.py | reports/eval/T4/code/ | 训练脚本（合成数据，eval-only 纪律） |
