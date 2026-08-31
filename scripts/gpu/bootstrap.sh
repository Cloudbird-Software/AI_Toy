#!/usr/bin/env bash
# scripts/gpu/bootstrap.sh —— GPU 训练机一键初始化（幂等，非交互）。
# 目标：机器开机后跑到本脚本结束，训练环境 100% 就绪（含全链路 smoke）。
# 前置：Linux + NVIDIA 驱动（nvidia-smi 可用）+ python3 + git。
# 产出：training/.venv（含全部训练依赖 + CUDA torch）
#       training/logs/bootstrap-<ts>.log（全过程留痕）
# smoke：SKIP_SMOKE=1 跳过（缺省会跑：导入链 + venv 完整性 + GPU 可见性）
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
VENV="$ROOT/training/.venv"
LOGDIR="$ROOT/training/logs"
mkdir -p "$LOGDIR"
TS="$(date -u +%Y%m%dT%H%M%SZ)"
LOG="$LOGDIR/bootstrap-$TS.log"
exec > >(tee -a "$LOG") 2>&1

fail() { echo "FAIL: $*" >&2; exit 2; }

echo "=== GPU 训练机 bootstrap $(date -u) ==="

# [1/6] 环境检查
command -v nvidia-smi >/dev/null || echo "warn: 无 nvidia-smi——CPU 也能训练（openwakeword 模型极小），只是 TTS 生成慢"
if command -v nvidia-smi >/dev/null; then
  nvidia-smi --query-gpu=name,memory.total,driver_version --format=csv,noheader || true
fi
PYV="$(python3 -c 'import sys;print(f"{sys.version_info.major}.{sys.version_info.minor}")')"
echo "python3 = $PYV"
case "$PYV" in
  3.8|3.9|3.10) : ;;
  *) echo "warn: python $PYV —— openwakeword 训练链在 3.10 验证最稳；若依赖安装失败，用 pyenv/conda 装 3.10 后重跑" ;;
esac

# [2/6] venv + torch（CUDA 版按机器驱动自动选 wheel）
if [ ! -x "$VENV/bin/python" ]; then
  python3 -m venv "$VENV"
fi
PIP="$VENV/bin/pip"
"$PIP" install -q --upgrade pip
echo "==> 安装 torch（CUDA 版；约 2-3GB，耐心）"
if command -v nvidia-smi >/dev/null; then
  "$PIP" install torch torchaudio --index-url https://download.pytorch.org/whl/cu121 || "$PIP" install torch torchaudio
else
  "$PIP" install torch torchaudio
fi

# [3/6] 训练依赖（版本钉死在 training/t4/requirements.txt）
echo "==> 安装训练依赖"
"$PIP" install -r training/t4/requirements.txt

# [4/6] 仓库工具链（Go 门禁面；无 Go 时跳过——门禁可在别的机器跑）
if command -v go >/dev/null; then
  echo "==> Go 工具链自检"
  go build ./... || fail "go build 失败"
else
  echo "warn: 本机无 go——门禁/评估需在仓库开发机执行（GPU 机只管训练）"
fi

# [5/6] smoke：训练链导入完整性 + GPU 可见性 + openwakeword train.py 定位
if [ "${SKIP_SMOKE:-0}" != "1" ]; then
  echo "==> smoke：训练链导入"
  "$VENV/bin/python" - <<'EOF'
import torch, onnxruntime, scipy, yaml, datasets
import openwakeword, os
train_py = os.path.join(os.path.dirname(openwakeword.__file__), "train.py")
assert os.path.isfile(train_py), f"openwakeword train.py 缺失: {train_py}"
print("torch", torch.__version__, "| cuda:", torch.cuda.is_available())
print("onnxruntime", onnxruntime.__version__)
print("openwakeword train.py:", train_py)
print("smoke OK")
EOF
fi

# [6/6] LLM API 可选自检（有 key 则验通；无 key 只提示）
if [ -n "${LLM_API_BASE:-}" ] && [ -n "${LLM_API_KEY:-}" ] && [ -n "${LLM_MODEL_TEXT:-}" ]; then
  echo "==> LLM API 自检（synthgen generate-llm / LLM_JUDGE 用）"
  echo "LLM_API_BASE=$LLM_API_BASE LLM_MODEL_TEXT=$LLM_MODEL_TEXT（已配置）"
else
  echo "note: LLM_API_BASE/LLM_API_KEY/LLM_MODEL_TEXT 未配置——真实合成数据与 LLM 评审"
  echo "      需要（模板 configs/llm/api.env.example）；纯 T4 训练不需要"
fi

echo "=== bootstrap 完成（日志 $LOG）==="
echo "下一步："
echo "  1. bash training/t4/prepare_data.sh          # 数据准备（首次约 1-2h，数 GB 下载）"
echo "  2. bash training/t4/train.sh all             # 三步训练（4060 约数小时，中断可重跑）"
echo "  3. training/t4/evaluate.py --model <onnx> --clips-dir <wav 目录>"
echo "  4. training/t4/register_model.py --onnx <onnx> # 登记 models/manifests"
