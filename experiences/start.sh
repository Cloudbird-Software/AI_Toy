#!/usr/bin/env bash
# 本机体验包一键入口：建 venv → 装依赖 → 解析模型路径 → 菜单选体验
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$HERE"

# ---------------------------------------------------------------- 模型路径解析
# 优先级：env AI_TOY_MODELS_DIR → 默认 /root/workspace/gpu-prep/out
MODELS_DIR="${AI_TOY_MODELS_DIR:-/root/workspace/gpu-prep/out}"
KWS_MODEL="$MODELS_DIR/kws/t4_wakeword.onnx"
SV_MODEL="$MODELS_DIR/sv/t5_ecapa.onnx"
T9_MODEL="$MODELS_DIR/t9"

# ---------------------------------------------------------------- venv
if [ ! -d .venv ]; then
  echo "[1/3] 创建 Python venv (.venv) ..."
  python3 -m venv .venv
fi
# shellcheck disable=SC1091
source .venv/bin/activate

# ---------------------------------------------------------------- pip install
if [ ! -f ./.venv/.requirements-installed ]; then
  echo "[2/3] 安装依赖（首次较慢，走腾讯镜像 + PyTorch CPU 索引）..."

  # CPU 版 torch（避免拉 2GB+ 的 CUDA 版）；+cpu 后缀在新版 PyPI 上不可用，走 extra-index-url
  pip install -q torch==2.4.1 torchaudio==2.4.1 \
    --extra-index-url https://download.pytorch.org/whl/cpu

  # 其余依赖走国内镜像
  pip install -q -r requirements.txt

  # openwakeword pip 包偶缺资源模型（melspectrogram.onnx / embedding_model.onnx）；
  # 若本机有 gpu-prep/.venv-cpu 则拷贝，否则从 GitHub release 下载。
  OWW_PKG="$(find .venv/lib* -maxdepth 4 -type d -path '*/openwakeword' 2>/dev/null | head -1)"
  if [ -n "$OWW_PKG" ] && [ ! -f "$OWW_PKG/resources/models/melspectrogram.onnx" ]; then
    SRC_RES="/root/workspace/gpu-prep/.venv-cpu/lib/python3.11/site-packages/openwakeword/resources"
    if [ -f "$SRC_RES/models/melspectrogram.onnx" ]; then
      echo "  补全 openwakeword 资源模型（从 gpu-prep/.venv-cpu 拷贝）..."
      cp -r "$SRC_RES" "$OWW_PKG/resources"
    else
      echo "  补全 openwakeword 资源模型（从 GitHub release 下载）..."
      mkdir -p "$OWW_PKG/resources/models"
      for f in melspectrogram.onnx embedding_model.onnx silero_vad.onnx; do
        curl -fsSL -o "$OWW_PKG/resources/models/$f" \
          "https://github.com/dscherek/openWakeWord/releases/download/v0.6.0/$f" \
          || echo "  [WARN] 下载 $f 失败，T4 唤醒词可能无法运行"
      done
    fi
  fi

  touch ./.venv/.requirements-installed
  echo "[2/3] 依赖安装完成。"
else
  echo "[2/3] 依赖已安装（跳过）。"
fi

# ---------------------------------------------------------------- 模型存在性检查
echo "[3/3] 模型路径: $MODELS_DIR"
miss=0
for p in "$KWS_MODEL" "$SV_MODEL" "$T9_MODEL/config.json"; do
  if [ -e "$p" ]; then
    echo "  ✓ $(basename "$p")"
  else
    echo "  ✗ 缺失: $p"
    miss=1
  fi
done
if [ "$miss" = 1 ]; then
  echo ""
  echo "!! 部分模型缺失。本机默认路径 $MODELS_DIR 下未找到。"
  echo "   可 env AI_TOY_MODELS_DIR=/path/to/models $0 指定其他位置。"
  echo "   或从 COS 拉取（见 AGENT.md「模型缺失怎么办」段）。"
  echo ""
fi

# ---------------------------------------------------------------- 菜单
echo ""
echo "========================================"
echo "  本机体验包 —— 选择体验"
echo "========================================"
echo ""
echo "  1) T4 唤醒词 KWS    —— 批量过 wav，打印分数/判定"
echo "  2) T5 声纹 SV      —— enroll → verify，打印相似度"
echo "  3) T9 危机词分类   —— 文本 REPL，打印标签+置信度"
echo "  4) 全部跑一遍（自动实测）"
echo "  q) 退出"
echo ""
read -rp "  输入编号 [1/2/3/4/q]: " choice
echo ""

export AI_TOY_MODELS_DIR="$MODELS_DIR"
export KWS_MODEL SV_MODEL T9_MODEL

case "$choice" in
  1) python3 t4_wakeword.py --models-dir "$MODELS_DIR" ;;
  2) python3 t5_voiceprint.py --models-dir "$MODELS_DIR" ;;
  3) python3 t9_crisis.py --models-dir "$MODELS_DIR" ;;
  4)
    echo "========== T4 唤醒词 =========="
    python3 t4_wakeword.py --models-dir "$MODELS_DIR"
    echo ""
    echo "========== T5 声纹 =========="
    python3 t5_voiceprint.py --models-dir "$MODELS_DIR"
    echo ""
    echo "========== T9 危机词 =========="
    python3 t9_crisis.py --models-dir "$MODELS_DIR" --demo
    ;;
  q|Q) echo "退出。"; exit 0 ;;
  *)   echo "无效输入: $choice"; exit 1 ;;
esac
