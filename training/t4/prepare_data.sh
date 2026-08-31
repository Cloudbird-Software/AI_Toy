#!/usr/bin/env bash
# prepare_data.sh —— T4 训练数据一次性准备（幂等，可重复执行）。
# 产出（全部在 training/t4/data/，gitignore 排除）：
#   vendor/piper-sample-generator + 音色模型     正样本 TTS 合成器
#   data/rirs/mit                                房间冲激响应（HF 流式下载）
#   data/noise/{audioset,fma}                    背景噪声（HF 下载）
#   data/features/*.npy                          预计算负样本/验证特征（HF，数 GB）
# 环境 knobs：FMA_HOURS（缺省 2）  AUDIOSET_TARS（缺省 1 个片段）
#            SKIP_FEATURES=1 跳过数 GB 的特征下载（smoke 用）
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"
T4="$ROOT/training/t4"
PY="$ROOT/training/.venv/bin/python"
[ -x "$PY" ] || { echo "error: 先跑 scripts/gpu/bootstrap.sh 建训练 venv"; exit 2; }

FMA_HOURS="${FMA_HOURS:-2}"
AUDIOSET_TARS="${AUDIOSET_TARS:-1}"

echo "==> [1/5] piper-sample-generator（正样本 TTS 合成器）"
mkdir -p "$T4/vendor"
if [ ! -d "$T4/vendor/piper-sample-generator" ]; then
  git clone --depth 1 https://github.com/rhasspy/piper-sample-generator "$T4/vendor/piper-sample-generator"
fi
if [ ! -e "$T4/vendor/piper-sample-generator/models/en_US-libritts_r-medium.pt" ]; then
  wget -q -O "$T4/vendor/piper-sample-generator/models/en_US-libritts_r-medium.pt" \
    'https://github.com/rhasspy/piper-sample-generator/releases/download/v2.0.0/en_US-libritts_r-medium.pt'
fi

echo "==> [2/5] openwakeword 特征模型（melspectrogram / embedding）"
"$PY" - <<'EOF'
import os, urllib.request, openwakeword
res = os.path.join(os.path.dirname(openwakeword.__file__), "resources", "models")
os.makedirs(res, exist_ok=True)
for name in ("melspectrogram.onnx", "embedding_model.onnx"):
    dest = os.path.join(res, name)
    if os.path.exists(dest) and os.path.getsize(dest) > 0:
        continue
    url = f"https://github.com/dscripka/openWakeWord/releases/download/v0.5.1/{name}"
    print("下载", url)
    urllib.request.urlretrieve(url, dest)
print("特征模型就绪:", res)
EOF

echo "==> [3/5] MIT 房间冲激响应（HF 流式，~数百个 wav）"
"$PY" - <<EOF
import os, scipy.io.wavfile, datasets
out = "$T4/data/rirs/mit"
os.makedirs(out, exist_ok=True)
if len([f for f in os.listdir(out) if f.endswith(".wav")]) > 0:
    print("RIRs 已存在，跳过"); raise SystemExit
rir = datasets.load_dataset("davidscripka/MIT_environmental_impulse_responses", split="train", streaming=True)
for row in rir:
    name = row["audio"]["path"].split("/")[-1]
    scipy.io.wavfile.write(os.path.join(out, name), 16000, (row["audio"]["array"] * 32767).astype("int16"))
print("RIRs 完成:", out)
EOF

echo "==> [4/5] 背景噪声（audioset 片段 ×$AUDIOSET_TARS + FMA 音乐 ${FMA_HOURS}h）"
"$PY" - <<EOF
import os, subprocess, tarfile, glob
import scipy.io.wavfile, datasets

out = "$T4/data/noise/audioset"; os.makedirs(out, exist_ok=True)
if len(glob.glob(out + "/*.wav")) == 0:
    fname = "bal_train09.tar"
    link = "https://huggingface.co/datasets/agkphysics/AudioSet/resolve/main/data/" + fname
    tgz = os.path.join(out, fname)
    subprocess.run(["wget", "-q", "-O", tgz, link], check=True)
    with tarfile.open(tgz) as tf: tf.extractall(out)
    os.remove(tgz)
    flacs = glob.glob(os.path.join(out, "audio", "**", "*.flac"), recursive=True)
    ds = datasets.Dataset.from_dict({"audio": flacs}).cast_column("audio", datasets.Audio(sampling_rate=16000))
    for row in ds:
        name = os.path.basename(row["audio"]["path"]).replace(".flac", ".wav")
        scipy.io.wavfile.write(os.path.join(out, name), 16000, (row["audio"]["array"] * 32767).astype("int16"))
    for f in flacs: os.remove(f)
print("audioset 完成")

out = "$T4/data/noise/fma"; os.makedirs(out, exist_ok=True)
if len(glob.glob(out + "/*.wav")) == 0:
    fma = datasets.load_dataset("rudraml/fma", name="small", split="train", streaming=True)
    fma = iter(fma.cast_column("audio", datasets.Audio(sampling_rate=16000)))
    for i in range($FMA_HOURS * 3600 // 30):  # FMA 全部 30s 片段
        row = next(fma)
        name = os.path.basename(row["audio"]["path"]).replace(".mp3", ".wav")
        scipy.io.wavfile.write(os.path.join(out, name), 16000, (row["audio"]["array"] * 32767).astype("int16"))
print("fma 完成")
EOF

if [ "${SKIP_FEATURES:-0}" = "1" ]; then
  echo "==> [5/5] SKIP_FEATURES=1，跳过预计算特征（smoke 模式）"
else
  echo "==> [5/5] 预计算负样本特征（数 GB，断点续传：wget -c）"
  mkdir -p "$T4/data/features"
  wget -c -q --show-progress -O "$T4/data/features/openwakeword_features_ACAV100M_2000_hrs_16bit.npy" \
    'https://huggingface.co/datasets/davidscripka/openwakeword_features/resolve/main/openwakeword_features_ACAV100M_2000_hrs_16bit.npy'
  wget -c -q --show-progress -O "$T4/data/features/validation_set_features.npy" \
    'https://huggingface.co/datasets/davidscripka/openwakeword_features/resolve/main/validation_set_features.npy'
fi

echo "==> 数据准备完成。下一步：training/t4/train.sh all"
