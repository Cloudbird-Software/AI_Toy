# T5 声纹 CPU 训练日志（C4：冻结 ECAPA-TDNN embedding + 轻量头）

- 日期：2026-08-31 ｜ 机器：4C8G 无 GPU（Linux/OpenCloudOS）｜ 数据：`/root/workspace/synth/t5_sv_audio/`（seed 42，纯合成声纹）
- 红线：8G 内存硬约束（本任务全程 RSS 峰值 ≈0.7GB，安全）；纯合成、不克隆真实人声。

## 1. 环境

| 项 | 值 |
|---|---|
| venv | `/root/workspace/gpu-prep/.venv-cpu`（torch 2.4.1+cpu） |
| 新增依赖 | torchaudio 2.4.1+cpu（匹配 torch）、speechbrain 1.1.1（无依赖冲突） |
| 预训练模型 | `speechbrain/spkrec-ecapa-voxceleb`（Apache-2.0，hf-mirror 下载，缓存 `.cache/sv-ecapa/`） |
| 路线 | 冻结 backbone 提 192 维 embedding（全长、逐条、无截断），不做全量微调 |

## 2. 命令与耗时

```bash
HF_HOME=/root/workspace/gpu-prep/.cache HF_ENDPOINT=https://hf-mirror.com \
  /root/workspace/gpu-prep/.venv-cpu/bin/python /root/workspace/gpu-prep/train_sv_cpu.py \
  >> /root/workspace/gpu-prep/reports/t5-sv/train.log 2>&1
```

- embedding 提取：644 clip × ECAPA CPU 单进程 ≈ **107s**（RSS 0.6~0.7GB），二次运行命中 `embeddings.npz` 缓存秒级。
- 协议评估 + 线性头训练（52 类线性 300 epoch）+ bootstrap CI（1000 次）合计 <10s。全程总耗时 ≈2min。

## 3. 协议（全量交叉，不采样）

- 中心向量：每成员 8 条 enroll embedding 均值后 L2 归一化（52 个 centroid）。
- 验证 trial：verify 3 条 × 52 centroid → 正 156（自己）、负 7 956（异成员，含同家庭 666 / 跨家庭分层）。
- 陌生人 trial：24 人 × 3 clip × max-cos over 52 centroid → 72 条 imposter 分数（说话人级 24）。
- 总 trial **8 184 ≥ 5 000**（小样本旗标 false）；但正样本仅 156，EER 以 bootstrap 95% CI 呈现。

## 4. 结果（详见 metrics.json / eer_curve.csv / thresholds.csv）

| 指标 | 实测 | 门禁 | 判定 |
|---|---|---|---|
| EER（全池） | **17.23%**（CI95 15.41~18.69%，阈值 0.8154） | T5-G1-01 ≤5% | **FAIL** |
| 再识别 top-1（closed-set 52 类） | 26.9%（top-2 累计 42.3%） | ≥95%（跨会话口径） | FAIL |
| 陌生人拒判 @EER 阈值 0.8154 | clip 级 **50.0%** / 说话人级 **20.8%** | T5-G1-04 ≥90% | **FAIL** |
| 陌生人拒判 @FAR1% 阈值 0.9042 | 100%，但同阈值 TAR 仅 10.3%（把九成真成员也拒掉，非可用操作点） | — | 仅记录 |

- 轻量分类头（192→52 线性，enroll 训练/verify 测试）：train acc 100%（过拟合）→ **verify acc 23.1%**，与余弦 top-1（26.9%）互证：冻结 embedding 的类间结构对合成数据判别力弱。
- 操作点：TAR@FAR1% = 10.3%；TAR@FAR0.1% = 3.8%。

## 5. 分层与可分性分析（任务第 4 点核心结论）

| 分层 | 负样本余弦均值 | 组内 EER | 说明 |
|---|---|---|---|
| 同家庭对（666） | 0.332 | — | 跨年龄段 pitch 相反方向 → 天然易分 |
| 跨家庭对（7 290） | 0.405 | — | 同 age_group 相撞概率更高 |
| adult-only 对（1 140） | **0.791** | **36.8%** | adult pitch 恒 0、仅 speed 步进 0.016 → 与正样本 0.815 几乎重叠 |
| child / elder 组内 | — | 25.9% / 33.7% | 缩小 gallery 后全池 EER 被揭穿 |

- 分年龄段 EER（child 25.9% / adult 36.8% / elder 33.7%）**全部高于**全池 17.2%：全池 EER 的"好看"部分来自跨年龄简单负样本。
- 同成员 enroll 8 条互相关余弦仅 0.763（同参数同音色确定性渲染下），verify 对 centroid 0.855，异成员 0.399，d′=2.03：分布整体可分但重叠区大，且重叠区集中在 adult。
- **可分性 = 渲染参数可分性**：76 名说话人共用一个 TTS 基础音色（cixingnansheng），成员区分 = tts_style 指令 + ffmpeg asetrate pitch（child +4~6st / adult 0 / elder −3~−2st）+ speed。ECAPA 读到的"声纹"主要是 pitch/语速的声学投影；adult 组参数上不可分（pitch 恒 0），embedding 亦不可分，二者完全一致。
- **同参数合成声纹差距的局限性声明**：本协议中同一成员所有 clip 用完全相同的 (voice, instruction, pitch, speed) 增强，说话人内变异只来自 TTS 渲染随机性，**未包含**真实世界的信道/麦克风/环境噪声/情绪/生理波动；因此 (a) 组内分数 0.85 的高相似是确定性渲染的产物，(b) 组间不可分不能证明"真实声纹不可分"，只证明"该合成管线在固定参数下产出的音频不具备 ECAPA 可读的成员级声纹"。合成↔真实域差未测量，本 EER 不可外推到真机。

## 6. 门禁结论

- **T5-G1-01（EER≤5%）：FAIL**（17.2%，CI 下界 15.4% 仍远超线）。
- **T5-G1-04（陌生人拒判≥90%）：FAIL**（@EER 阈值 clip 50%/说话人 20.8%；可通过抬高阈值到 0.90 名义达成 100% 拒判，但 TAR 崩到 10%，系统不可用，不构成 PASS）。
- 与 T9（attack/boundary proxy FAIL）同类：**当前合成数据域无法支撑门禁级声纹指标，CPU 路线已测出冻结 embedding 路线的天花板**。

## 7. GPU 复训建议

1. **先改数据再上卡**：adult 组必须引入真实可分的声学差异（多 TTS 音色复刻接口、或引入 RIR/信道级非确定性增强打破"同参数=同声纹"绑定），否则 GPU 微调只是对渲染参数过拟合（线性头 train 100%/verify 23% 已演示）。
2. **微调路线**：`train_sv.py`（ArcFace/AAM + 三秒随机截断 + fp16），batch-pairs 16、epochs 10、lr 1e-4；微调后重跑本协议对照 EER，验证域差是否可被微调弥合。
3. **横评**：3D-Speaker CAM++（TODO(sv-2)）同协议横评，排除 ECAPA 单模型域失配因素。
4. **协议扩展**：姐妹/兄弟对单列（TODO(sv-1)）、增益/文本无关属性测试（TODO(sv-4)）。
5. 数据侧：当前 strangers 与成员同为单音色+随机参数，其中 3 人参数与成员近到 0.004~0.048（hard negative 设计合理），复训时保留。

## 8. 产出物

- `out/t5-sv-cpu/`：embeddings.npz（644×192）、embedding_meta.json、centroids.npy（52×192）、thresholds.csv、head_linear_52.pt
- `reports/t5-sv/`：metrics.json、eer_curve.csv、scores_{pos,neg}.csv、stranger_scores.csv、train.log、本 TRAINLOG.md
- 训练脚本：`/root/workspace/gpu-prep/train_sv_cpu.py`（GPU 微调骨架另见 `train_sv.py`，未动）
