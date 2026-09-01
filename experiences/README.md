# 本机体验包（experiences）

三个刚训练完的模型在本机 CPU 上的**真机实测**体验入口。零 GPU 依赖，全部跑通。

## 三步上手

```bash
cd /root/workspace/experiences-build/experiences
bash start.sh
```

`start.sh` 会自动：
1. 创建 `.venv` 并安装依赖（首次约 2–5 分钟，走国内镜像 + PyTorch CPU 索引）
2. 检查模型文件是否存在（默认 `/root/workspace/gpu-prep/out`，可用 `AI_TOY_MODELS_DIR` 覆盖）
3. 弹出菜单选择体验

## 三个体验

| # | 体验 | 脚本 | 做什么 | 实测 PASS 判据 |
|---|------|------|--------|----------------|
| 1 | T4 唤醒词 KWS | `t4_wakeword.py` | 批量过 wav，80ms 帧流式打分，阈值 0.20 判定唤醒 | 正例唤醒率 ≥95%、负例零误唤醒 |
| 2 | T5 声纹 SV | `t5_voiceprint.py` | enroll 多条 → centroid，verify 同人/陌生人打印余弦相似度 | 同人通过率 ≥95%、异人拒绝率 ≥95% |
| 3 | T9 危机词分类 | `t9_crisis.py` | 文本 REPL，block/allow/notify 三分类 + 置信度 | 危机示例全部召回、正当示例不误报 |

## 直接运行单个体验

```bash
source .venv/bin/activate
python3 t4_wakeword.py --models-dir /root/workspace/gpu-prep/out
python3 t5_voiceprint.py --models-dir /root/workspace/gpu-prep/out
python3 t9_crisis.py --models-dir /root/workspace/gpu-prep/out --demo
```

## 模型路径

解析顺序：`AI_TOY_MODELS_DIR` 环境变量 → 默认 `/root/workspace/gpu-prep/out`。

```
<models-dir>/kws/t4_wakeword.onnx     # T4 唤醒词（857KB）
<models-dir>/sv/t5_ecapa.onnx         # T5 声纹（~80MB）
<models-dir>/t9/                      # T9 危机分类（HF 格式，~400MB）
```

模型缺失时见 AGENT.md「模型缺失怎么办」。

## 实测输出摘要

详见 AGENT.md「预期输出」段（含真实数字）。
