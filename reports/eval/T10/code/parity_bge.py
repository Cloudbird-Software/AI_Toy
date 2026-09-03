#!/usr/bin/env python3
"""bge ONNX 对拍 + Go golden 生成（M2-T10）。

两面：
  1. eager（导出包装器）vs Python ORT——同输入 max_abs 误差（<1e-4 口径）；
  2. 生成 packages/go/memory/testdata/golden_bge.json：Go 侧分词 ID + embedding
     golden（Go WordPiece/Go ORT 与 HF 全链对拍的锚点）。

用法：python3 parity_bge.py <hf_model_dir> <onnx_path> <out_golden_json>
"""
import hashlib
import json
import sys

import numpy as np
import torch
import onnxruntime as ort
from transformers import BertModel, BertTokenizer

# 覆盖面：门禁探针形中文／中英混排／标点／数字／大小写（英文）／长短句。
CASES = [
    "孩子喜欢恐龙",
    "恐龙玩具",
    "今天天气好",
    "外婆1 名字",
    "外婆1 名字 叫桂花1",
    "仓鼠3 名字 叫豆豆",
    "喜好4 最爱 恐龙书2",
    "运动会4 得了 跑步第二名2",
    "我的小狗叫团团，它最爱啃骨头！",
    "The child likes DINOSAURS.",
    "幼儿园 today 举办运动会",
    "你好，世界！Hello, world! 12345",
    "在自然博物馆看到了霸王龙化石",
    "小美的生日是三月十二",
    "记事07 内容 u内容07",
    "秘密05 暗号 暗号凤凰木05号",
]
TOL = 1e-4


def main(src: str, onnx_path: str, golden_path: str) -> None:
    tok = BertTokenizer.from_pretrained(src)
    bert = BertModel.from_pretrained(src, torch_dtype=torch.float32).eval()

    with torch.no_grad():
        enc = tok(CASES, padding=True, return_tensors="pt", truncation=True,
                  max_length=512)
        out = bert(input_ids=enc["input_ids"],
                   attention_mask=enc["attention_mask"], return_dict=True)
        h = out.last_hidden_state
        cls = h[:, 0]
        emb_ref = torch.nn.functional.normalize(cls, p=2, dim=1).numpy()
        h_ref = h.numpy()

    sess = ort.InferenceSession(onnx_path, providers=["CPUExecutionProvider"])
    ids_np = enc["input_ids"].numpy().astype(np.int64)
    mask_np = enc["attention_mask"].numpy().astype(np.int64)
    h_onnx, emb_onnx = sess.run(None, {"input_ids": ids_np,
                                       "attention_mask": mask_np})

    h_err = float(np.max(np.abs(h_ref - h_onnx)))
    emb_err = float(np.max(np.abs(emb_ref - emb_onnx)))
    print(f"last_hidden_state max_abs eager-vs-ort = {h_err:.3e}")
    print(f"sentence_embedding   max_abs eager-vs-ort = {emb_err:.3e}")
    assert h_err < TOL and emb_err < TOL, f"对拍超阈（<{TOL} 口径）"

    sha = hashlib.sha256(open(onnx_path, "rb").read()).hexdigest()
    cases = []
    for i, text in enumerate(CASES):
        ids = tok(text)["input_ids"]
        emb_i = emb_onnx[i]
        # ORT 单条复算（去 padding 干扰，Go 按 batch=1 跑）：
        h1, e1 = sess.run(None, {"input_ids": np.array([ids], dtype=np.int64),
                                 "attention_mask": np.ones((1, len(ids)), dtype=np.int64)})
        cases.append({
            "text": text,
            "input_ids": ids,
            "embedding": [round(float(x), 7) for x in e1[0]],
        })
        print(f"  case {i:2d} len={len(ids):3d} emb[:3]={e1[0][:3].round(4)}")

    golden = {
        "model": "bge-small-zh-v1.5",
        "onnx_sha256": sha,
        "tolerance": TOL,
        "dim": int(emb_onnx.shape[1]),
        "pooling": "cls_l2norm (1_Pooling/config.json: cls_token=true)",
        "cases": cases,
    }
    with open(golden_path, "w", encoding="utf-8") as f:
        json.dump(golden, f, ensure_ascii=False, indent=1)
    print(f"golden written: {golden_path} ({len(cases)} cases, dim={golden['dim']})")


if __name__ == "__main__":
    if len(sys.argv) != 4:
        sys.exit(__doc__)
    main(sys.argv[1], sys.argv[2], sys.argv[3])
