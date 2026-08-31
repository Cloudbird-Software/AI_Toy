#!/usr/bin/env bash
# train.sh —— T4 唤醒词训练驱动（官方三步：generate_clips → augment_clips →
# train_model；模型本体是 openwakeword 包内的 train.py，本脚本只做定位与编排）。
# 用法：training/t4/train.sh [generate|augment|train|all]（缺省 all）
# 配置：training/t4/config.yaml（参数调优改那里，不改本脚本）
# 断点：三步各自幂等（generate 会续到目标条数；augment/train 重跑覆盖）。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"
T4="$ROOT/training/t4"
PY="$ROOT/training/.venv/bin/python"
CONFIG="${CONFIG:-$T4/config.yaml}"
STEP="${1:-all}"
[ -x "$PY" ] || { echo "error: 先跑 scripts/gpu/bootstrap.sh 建训练 venv"; exit 2; }

TRAIN_PY="$("$PY" -c 'import openwakeword, os; print(os.path.join(os.path.dirname(openwakeword.__file__), "train.py"))')"
[ -f "$TRAIN_PY" ] || { echo "error: openwakeword 包内未找到 train.py（安装不完整？重跑 bootstrap）"; exit 2; }

run() {
  echo "==> openwakeword train.py --$1"
  "$PY" "$TRAIN_PY" --training_config "$CONFIG" --"$1"
}

case "$STEP" in
  generate) run generate_clips ;;
  augment)  run augment_clips ;;
  train)    run train_model ;;
  all)
    run generate_clips
    run augment_clips
    run train_model
    MODEL_ONNX="$T4/data/model/$( "$PY" -c "import yaml; print(yaml.safe_load(open('$CONFIG'))['model_name'])" ).onnx"
    echo "==> 训练完成：$MODEL_ONNX"
    echo "==> 下一步："
    echo "    评估   training/t4/evaluate.py --model $MODEL_ONNX --clips-dir <验证集 wav 目录>"
    echo "    登记   training/t4/register_model.py --onnx $MODEL_ONNX"
    ;;
  *)
    echo "usage: train.sh [generate|augment|train|all]" >&2; exit 2 ;;
esac
