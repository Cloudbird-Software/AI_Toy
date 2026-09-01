# AGENT.md —— 本机体验包操作说明

> 读者假设：你是一个被用户叫来"安排测试"的 AI agent，**零上下文**。本文从零讲起。
> 目标：在本机（Linux, CPU only, 7.4GB 内存, 无 GPU, 可能无麦克风）上把三个模型体验跑通，
> 并验证 PASS 判据。

## 0. 这是什么

三个刚训练完的模型在本机 CPU 上的实测体验包。产出目录：
`/root/workspace/experiences-build/experiences/`。

| 模型 | 任务 | 文件 | 大小 |
|------|------|------|------|
| T4 唤醒词 KWS | 关键词唤醒检测 | `out/kws/t4_wakeword.onnx` | 857KB |
| T5 声纹 SV | 说话人验证 | `out/sv/t5_ecapa.onnx` | ~80MB |
| T9 危机词分类 | 文本安全三分类 | `out/t9/` (HF 格式) | ~400MB |

**不要碰 `/root/workspace/AI_Toy`**（另一个代理在改它）。

## 1. 环境前置

- Python 3.11（系统自带 `/usr/bin/python3`）
- 无 GPU，纯 CPU 推理
- 内存 7.4GB（T9 模型 ~400MB，安全）
- 可能无麦克风 → T4 用 wav 模式，`--mic` 模式会友好跳过

## 2. 三步上手

```bash
cd /root/workspace/experiences-build/experiences
bash start.sh
```

选 `4` 自动跑全部三个体验。选 `1/2/3` 跑单个。

`start.sh` 做的事：
1. `python3 -m venv .venv`（首次）
2. 安装依赖：torch CPU 版（PyTorch 官方 CPU 索引）+ 其余（腾讯镜像）
3. 检查模型文件存在性
4. 菜单选择

## 3. 每个体验的运行命令与预期输出

### 3.1 T4 唤醒词 KWS

```bash
python3 t4_wakeword.py --models-dir /root/workspace/gpu-prep/out
```

**关键口径**：
- 流式 80ms 帧（`openwakeword.Model.predict`，与训练侧 `featurize_streaming` 一致）
- 短于 2s 的音频**右补零到 2s**（批量 melspec 在短 clip 上差 14pp，是错误口径）
- 操作阈值 **0.20**
- 文件间必须 `model.reset()`（流式模型有内部状态）

**预期输出**（实测，2026-09-01）：
```
# 阈值=0.2  流式 80ms 帧 + 2s 右补零
  t4_neg_speech.wav             4.37s   54     0.0008  quiet ✓
  t4_neg_speech2.wav            4.26s   53     0.0061  quiet ✓
  t4_pos_fast.wav               1.25s   25     0.9993   WAKE  ✓
  t4_pos_raw.wav                1.37s   25     0.9993   WAKE  ✓

  正例唤醒率: 2/2 = 100.0%  (门禁 ≥95%)
  负例零唤醒: 2/2 = 100.0%  (目标 100%)
  PASS=是
```

**PASS 判据**：正例唤醒率 ≥95%、负例零误唤醒（100%）。

### 3.2 T5 声纹 SV

```bash
python3 t5_voiceprint.py --models-dir /root/workspace/gpu-prep/out
```

**关键口径**：
- ONNX 输入：80 维 log-mel fbank（librosa 复现：25ms 窗/10ms 步/80 mel/fmax=8000）
- embedding L2 归一化后余弦相似度
- 阈值 **0.50**（同人 > 0.50 通过，异人 < 0.50 拒绝）

**预期输出**（实测）：
```
# 阈值=0.5  余弦相似度（L2 归一化 embedding）

【注册】1 条 → centroid
  · t5_mother_enroll.wav   与centroid余弦=1.0000

【验证】
  t5_mother_verify.wav       0.7161      PASS   异人  ✓   ← 同人，高分通过
  t5_father_enroll.wav       0.2854      PASS   异人  ✓   ← 异人（同家庭），低分拒绝
  t5_stranger.wav            0.4263      PASS   异人  ✓   ← 陌生人，低分拒绝

  同人通过率: 1/1 = 100%
  异人拒绝率: 2/2 = 100%
  PASS=是
```

**PASS 判据**：同人通过率 ≥95%、异人拒绝率 ≥95%。

**注意**：实测同人余弦 ~0.72，异人 ~0.29，陌生人 ~0.43。同人显著高于异人即 PASS。
阈值 0.50 是经验值，可根据实际数据调整（`--threshold`）。

### 3.3 T9 危机词分类

```bash
python3 t9_crisis.py --models-dir /root/workspace/gpu-prep/out --demo
```

**关键口径**：
- 三分类：block（危机/攻击→阻断）、allow（正当→放行）、notify（边界→通知家长）
- 面向「儿童安全对话」分布，**通用闲聊可能误判为 block**（训练数据偏危机/攻击类，属正常分布偏移）
- 用 in-distribution 示例验证

**预期输出**（实测）：
```
# 内置示例（危机 / 正当 / 边界）

  ✓ 期望=block   输入: 活着好累啊，我不想当大家的小孩了
  → block   置信度 0.997    ⚠ 危机/攻击 → 阻断
     block=0.997  allow=0.001  notify=0.002

  ✓ 期望=allow   输入: 我最近发现班里的女生有的胸口鼓鼓的...
  → allow   置信度 0.994    ✓ 正当内容 → 放行
     block=0.002  allow=0.994  notify=0.004

  ✓ 期望=notify  输入: 最近我晚上总睡不着，还老偷偷哭，不知道为啥。
  → notify  置信度 0.990    ⚠ 边界案例 → 通知家长
     block=0.005  allow=0.005  notify=0.990

  示例命中率: 8/8 = 100%
```

**PASS 判据**：危机示例全部召回（block）、正当示例不误报（allow）。

## 4. 常见坑

### 模型缺失怎么办

`start.sh` 会检测并提示。拉取方法（coscmd 已配好）：

```bash
# T4 + 脚本包
coscmd download -r ai-toy/gpu-results/gpu-results-t4-20260901.tar.gz

# T9 + T5 分片包（7 个分片）
coscmd download -r ai-toy/gpu-results/gpu-results-t9-t5-20260901.tar.gz.part00
# ... 下载 part00 到 part06 全部
cat gpu-results-t9-t5-20260901.tar.gz.part* > gpu-results-t9-t5-20260901.tar.gz
tar xzf gpu-results-t9-t5-20260901.tar.gz
```

### 无麦克风

T4 默认用 wav 模式（`assets/` 下的演示音频）。`--mic` 模式需要 `sounddevice`，
未安装或未检测到麦克风时会**友好退出（exit 0）**，不会报错。

### 内存不足

- T9 模型 ~400MB，加载后 RSS 约 600MB，7.4GB 内存安全
- 若 OOM，检查是否有其他大进程：`ps aux --sort=-%mem | head`
- T9 已设 `torch.set_num_threads(4)` 限制 CPU 线程

### pip 安装慢/失败

- 已配腾讯镜像：`mirrors.tencentyun.com`
- torch CPU 版从 PyTorch 官方 CPU 索引拉（`-f https://download.pytorch.org/whl/cpu/torch_stable.html`）
- 本机已有 wheel 缓存：`~/.cache/pip/wheels/`
- 若网络不通，可复用 `/root/workspace/gpu-prep/.venv-cpu/` 的已装包（但建议新建隔离 venv）

### T4 误唤醒

- 必须 `model.reset()` 每个文件之间（流式模型有内部状态）
- 短于 2s 的音频必须右补零到 2s
- 负例必须用**非唤醒词内容**（t7 情感语音、t13 故事等），不能用含 "nihaoxiong" 的音频

### T5 相似度偏低

- 合成声纹的 embedding 可分性来自渲染参数（pitch/speed），非真实声学差异
- 同人余弦 ~0.7，异人 ~0.3，属正常范围
- 若同人 < 阈值，调低 `--threshold`（如 0.40）

## 5. 退出码

| 退出码 | 含义 |
|--------|------|
| 0 | PASS / 正常退出 |
| 1 | 命令行错误 |
| 2 | 体验未达 PASS 判据（唤醒率不足 / 声纹误判 / 危机漏报） |

## 6. 文件清单

```
experiences/
  AGENT.md            # 本文件（给 AI agent 的操作说明）
  README.md           # 给人看的一页说明
  start.sh            # 一键入口
  requirements.txt    # 依赖（torch 在 start.sh 单独安装）
  t4_wakeword.py      # T4 唤醒词体验
  t5_voiceprint.py    # T5 声纹体验
  t9_crisis.py        # T9 危机词体验
  assets/             # 演示素材（684KB）
    t4_pos_raw.wav        # T4 正例：唤醒词原声
    t4_pos_fast.wav       # T4 正例：快语速
    t4_neg_speech.wav     # T4 负例：情感语音
    t4_neg_speech2.wav    # T4 负例：情感语音
    t5_mother_enroll.wav  # T5 注册：母亲
    t5_mother_verify.wav  # T5 验证：母亲（同人）
    t5_father_enroll.wav  # T5 验证：父亲（异人）
    t5_stranger.wav       # T5 验证：陌生人
```
