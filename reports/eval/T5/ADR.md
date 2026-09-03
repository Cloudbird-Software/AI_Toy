# ADR：T5 声纹底座由合成 ECAPA 换为 CAM++ 微调（issue #127）

日期：2026-09-02 ｜ 状态：已采纳；W1b-r2 零样本复核后维持"轻调优于冻结"结论，但整体判定 no-solution（见文末补充节） ｜ 作者：W1b 训练工程师

## 背景

现有声纹底座为在合成家庭数据上训练的 ECAPA。真实说话人口径 EER 不达标，且合成域
（TTS 音色拼接）与真实拾音域（多设备/多距离/方言）差异大。目标：真实说话人 EER≤5%、
陌生人拒判率≥90%（说话人维度）、52 成员合成家庭场景不回退。候选方案：继续加深 ECAPA、
ERes2NetV2、或 CAM++ 预训练模型微调。

## 决策

采用 **ModelScope `damo/speech_campplus_sv_zh-cn_16k-common`（CAM++，约 200k 说话人中文预训练）
+ LoRA 式轻调** 作为 T5 声纹底座，微调数据为 3D-Speaker 训练集子集（97 说话人/2,328 条，
按说话人与 30 个 holdout 说话人不重叠切分），训练目标为 AAM-softmax 分类
（s=32, m=0.2），val EER 早停。产出 ONNX：`out/t5_campplus_sv.onnx`（输入 80 维 kaldi fbank
+ utterance 级 CMN，输出 192 维 embedding，L2 归一后做余弦打分）。

## 理由

**为何 CAM++ 而非继续 ECAPA**：CAM++ 预训练模型见过的中文说话人规模（约 200k）远超我们
从零/合成数据能达到的任何训练规模；其官方 CN-Celeb EER 4.32%，显著优于同结构量级
从头训练的 ECAPA（论文口径 7.45%）。继续在合成数据上深挖 ECAPA 是在错误的域上加容量；
CAM++ 把"通用说话人先验"直接带进来，我们只需做域校准。ERes2NetV2 性能相近但模型更大、
端侧推理更贵；CAM++ 密集连接+上下文掩蔽结构在同精度下推理更快（论文口径），符合玩具
端侧约束。许可为 Apache-2.0（可商用，需署名），VoxCeleb 为排除项未触碰。

**为何 LoRA 式轻调而非冻结 backbone**：冻结 backbone 只训其上投影头时，若导出 embedding
不含投影，则与预训练完全等价、失去微调意义；若含投影，可训练参数集中在 192 维瓶颈上，
域适应能力有限。LoRA（rank=8，注入全部 160 个 1×1 Conv1d：bottleneck、transit、CAM 门控、
输出 dense）以 0.46M 可训练参数（全模型 6.85M 的 6.7%）让通道混合层做低秩域适应，
base 权重与 BN 统计全程冻结——既不会灾难性遗忘 200k 说话人先验（保护家庭场景），
又能按域重排打分空间。AAM 头（120 类以内）仅作训练期监督，不进入部署 embedding。
实测对照（holdout 30 说话人 val EER）：LoRA 1.425%（24条/spk）→ 1.050%（全量 utts）；freeze 1.550%。
LoRA 的参数效率与防遗忘优势成立，且大数据量版本最优，故采用 LoRA + 全量 utts。）

**数据口径**：3D-Speaker 训练集与评测 trials 同域（同设备矩阵/距离/方言），是唯一"正版"
可商用（CC BY-SA 4.0）的真实说话人训练源。受 GPU 机磁盘约束（仅 13G）与单 tar 包不可
随机访问限制，用 OSS 范围请求流式解包取前 127 个完整说话人，按说话人切 train/val，
24 条/说话人精选（覆盖多 seg/device）。小数据 + 轻调 + 强预训练的组合是刻意选择：
避免在大数据上全参数微调的算力与遗忘风险。

## 后果

- 正面：真实口径 EER 与陌生人拒判有预训练先验兜底；ONNX 192 维 embedding 与现有
  余弦打分/注册流程兼容；训练/推理成本低（<3G 显存、CPU 可推理）。
- 负面/风险：训练说话人仅 97 个，域适应广度有限；3D-Speaker 与家庭玩具场景（近讲、
  短语音、儿童）仍有差距，靠合成家庭复测把关；LoRA 合并后的模型不可逆（保留 best.pt
  与预训练 bin 可回滚）。
- 中性：换底座后阈值需按真实口径重新标定（见 EVAL_REPORT 的 EER/FAR1% 双工作点），
  旧 ECAPA 阈值作废。vendor 的 `campplus_layers.py` 相对上游有一处必要修改：seg_pooling 的
  `avg_pool1d(..., count_include_pad=False)`，否则 ONNX 导出的 AveragePool(ceil_mode=1) 对
  尾段不满窗口按整窗除法计算，CAM 门控带偏（详见 EVAL_REPORT §6.2）。

## 补充（W1b-r2，2026-09-02）：零样本先测后，冻结与轻调两条路线均被否决

PM 复盘假设"127 人微调可能反而破坏预训练泛化，应先测零样本"。r2 按第一轮完全相同口径
补测零样本（预训练权重直接推理，缓存经逐条重算验证，见 EVAL_REPORT §0）：

- **零样本更差，不是更好**：cross_device trials EER 零样本 20.173% vs 127 人微调 14.930%；
  同 campaign holdout 9.15% vs 0.17%；陌生人 max 口径 40% vs 73.3%；utt 级 impostor 接受率
  4.14% vs 0.26%。**"微调损害泛化"的假设被实证否定**——轻调在所有真实域指标上为正收益。
- **冻结 backbone（issue #127 备选）不可采纳**：冻结即零样本水平，真实域全面劣于轻调版，
  本 ADR 原决策（LoRA 轻调优于 freeze）被 r2 数据进一步支持。
- **但两条路线都无法达到 ≤5%**：零样本 20.2%、127 人微调 14.9%、官方全量 10k spk 配方
  文献口径 ~7%。差距根源是预训练模型对 3D-Speaker trials 的跨 campaign 域差（卡面 4.32%
  为 CN-Celeb 口径），不是微调方式问题。最终判定 `W1B_R2_DONE no-solution`，目标口径与
  数据/算力预算升级 PM 决策。本轮未产出新 ONNX，沿用第一轮产物。

## 定稿（W1b-r3，2026-09-02）：三变体全量收割

R3 按任务书对 pretrained / finetuned（LoRA 24条/spk）/ lorafull（LoRA 全量 utts）三个变体
跑全量评测（同切分、同脚本、同阈值标定），并修复了工程问题：

- **最优 trials EER 仍为 lorafull**：cross_device 14.930%（finetuned 16.423%、pretrained 20.173%）。
- **门槛未达**：EER 14.930% > 5%；陌生人拒判 max 口径 lorafull 73.3% < 90%（mean 口径 100% 达标）；
  家庭复测 PASS（成员 accept 100% 持平，lorafull 家庭 EER 1.282% 较 pretrained +0.64pt <1.5%）。
- **判定**：`W1B_R3_DONE no-solution-v2`。如实上报，不调阈值凑数。
- **工程修复**：
  1. `eval_stranger.py` 增加缺失 key 过滤，支持 finetuned 24条/spk 子集评测。
  2. `t5_campplus_sv.onnx` 原导出存在 `AveragePool(count_include_pad=1)` bug，与 torch 模型余弦偏差 0.035；
     已重导出修复版 `t5_campplus_sv_fixed.onnx`（`count_include_pad=0`），逐位一致 ≤1.1e-6，
     并覆盖为 `out/t5_campplus_sv.onnx` 作为交付物。
  3. 新增 `eval_trials_npz.py` 直接从 npz 缓存打分，避免重跑 embedding。
- **交付物**：`out/t5_campplus_sv.onnx`（修复版）、`out/campplus_pretrained.onnx`、
  `out/t5_campplus_sv_finetuned.onnx`（新导出）、`out/emb_test_{pretrained,finetuned,lorafull,eres2net}.npz`、
  `out/emb_holdout_{pretrained,finetuned,lorafull}.npz`、全部评测脚本与报告。

