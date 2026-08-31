# Runbook: GPU 训练机开机即训（PM agent 交接入口）

> 目标：租用 GPU 后零等待进入训练。本 runbook 是唯一入口——从机器就绪到
> 门禁回填的完整序列，每步幂等、可断点重跑。
> 决策依据：ADR-0007。依赖 license 台账：training/licenses.yaml。

## 0. 前提清单（缺一先补，再继续）

| 项 | 要求 | 检查 |
|---|---|---|
| GPU 机器 | Linux + NVIDIA 驱动（4060 即够；见 §5 GPU 选型） | `nvidia-smi` |
| Python | 3.8–3.10（3.10 最稳） | `python3 -V` |
| Go | ≥1.22（门禁面；无则训练完在开发机跑门禁） | `go version` |
| LLM API（可选） | OpenAI 兼容端点 + ≥4 个文本模型 | `configs/llm/api.env.example` |
| 仓库 | 本仓已含训练前置（ADR-0007 落地） | `ls training/t4/` |

LLM key 配置（有 key 时）：
```bash
cp configs/llm/api.env.example ~/llm.env   # 编辑后
set -a; source ~/llm.env; set +a
```

## 1. 一键序列（每步可独立重跑）

```bash
bash scripts/gpu/bootstrap.sh      # ① 环境：venv + torch(CUDA) + 依赖 + smoke（~20min）
bash training/t4/prepare_data.sh   # ② 数据：piper/RIR/噪声/负特征（首次 ~1-2h，数 GB）
bash training/t4/train.sh all      # ③ 训练：生成→增强→训练（4060 数小时；中断重跑续）
```

参数调优只改 `training/t4/config.yaml`（n_samples/steps/target_phrase）。
唤醒词变更 = 改 `target_phrase` 一行（注意 §5 已知边界第 1 条）。

## 2. 训练后闭环（评估 → 登记 → 门禁）

```bash
# ① synth 面预检（wake_rate ≥0.90 通过线；0.97 量产线由 Go 门禁执法）
training/.venv/bin/python training/t4/evaluate.py \
  --model training/t4/data/model/ai-toy-wakeword.onnx \
  --clips-dir <train.py 产出的验证 wav 目录>          # find training/t4/data/model -name '*.wav' 定位

# ② 登记模型（复制到 models/incoming/ + 回填 sha256 到 models/manifests）
training/.venv/bin/python training/t4/register_model.py \
  --onnx training/t4/data/model/ai-toy-wakeword.onnx
just fetch-models                                     # 校验 sha256

# ③ 门禁复测（换装新模型后跑 T4 全量门禁；负样本批次是现成的 6h+30min）
make card-test CARD=<卡号> ASSET=T4                   # 入口协议（AGENTS.md）
# 或 just gate T4 all
```

门禁证据面：T4-G0-01（≥6h 家庭音景零事件，泊松 3/N）与 T4-G0-02（≥30min
对抗负样本）由 Go 侧 kws 门禁 + synthgen 负批次执法，不依赖训练机。

## 3. LLM 合成数据与评审（真实 LLM 面）

```bash
# 合成语料批次（溯源戳记真实模型；模型池轮转保单源占比 ≤30%）
go run ./tools/synthgen/cmd/synthgen register --id gen-llm-1 --version 1.0.0 \
  --seed-policy fixed --outputs-manifest datasets/manifests/turntaking_synth.json
go run ./tools/synthgen/cmd/synthgen generate-llm --id gen-llm-1 --n 4000 --seed 42
go run ./tools/synthgen/cmd/synthgen dist-check --batch <批次目录名>   # 多样性门禁

# LLM 评审（judge 身份沿用 configs/judge/model.yaml 锁定模型名——
# API 端点须提供同名模型，否则改 model.yaml 是 founder PR）
LLM_JUDGE=1 go run ./tools/judge/cmd/toyjudge run --rubric 7a --targets <目录> \
  --out reports/judge/7a-llm.jsonl
```

## 4. 卡工作流（AGENTS.md 入口协议）

训练/数据/换模型按卡走：`bash ghcb next` → `claim` → `make card-test CARD=<n>
ASSET=<T4>` → 实现 → `make gates-pr` → PR（body 带 `Card: <owner>/<repo>#<n>`）。
一次性环境问题（bootstrap 脚本 bug）可直修但须同 PR 说明。

## 5. 已知边界 / 红线速查

1. **中文唤醒词**：openwakeword 官方 TTS 链（piper-sample-generator）仅英文
   音色。当前默认 `hey jarvis`（与开发期占位模型一致）。中文唤醒词须换 TTS
   音源（Piper 中文音色/paddlespeech）——**founder 决策项**，勿自行引入。
2. **单模型池**：LLM_MODELS_TEXT_POOL 只配 1 个模型时，generate-llm 的批次
   dist-check 必然违规（单源=1.00>0.30）。这不是 bug，是多样性门禁的执法。
3. **训练负样本双轨**：训练用 openWakeWord 公开预计算特征（ACAV100M）；
   本仓 synthgen 负批次（gen-tneg/gen-kwsadv）eval-only——**永入训练管道**
   （T2-G0-01 G0 红线）。同理 datasets/holdout/** 与 T20 模拟器产物。
4. **GPU 选型**：4060（8GB）足够——openwakeword 模型 MB 级，瓶颈是 TTS 合成
   与数据增强吞吐，拉时长即可（time-vs-quality 线性可换）。5090 无必要；
   4090 只在批量扫参（多 config 并行）时有价值。
5. **真机面 debt 不消**：T4-G1-03（RTF/内存）、真机唤醒率、真实童声 ≥200 条
   （T4-G1-01 的证据要求之一）不在合成数据闭环内，列 founder 数据采集决策。
6. **豁免台账**：G1 断言失败要么修复要么 reports/exemptions.yaml 登记
   （≤30 天自动过期），不得静默跳过。
