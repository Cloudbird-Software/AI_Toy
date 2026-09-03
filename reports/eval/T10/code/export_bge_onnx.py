#!/usr/bin/env python3
"""bge-small-zh-v1.5 → ONNX 导出（M2-T10，issue #134 收口）。

导出契约（图内做 pooling，Go 侧零后处理——ADR-0011）：
  输入  input_ids      [B, T] int64（WordPiece，[CLS]…[SEP]，max_len 512）
        attention_mask [B, T] int64
  输出  last_hidden_state [B, T, 512]（对拍锚点）
        sentence_embedding   [B, 512]  L2 归一化 CLS 池化（Go 消费面；
        池化口径以模型随附 1_Pooling/config.json 为准：cls_token=true——
        BGE 官方用法，非 mean pooling）

用法：python3 export_bge_onnx.py <hf_model_dir> <out_dir>
"""
import json
import sys

import torch
from transformers import BertModel

MAX_LEN = 512
OPSET = 17


class BgeOnnxWrapper(torch.nn.Module):
    """BertModel + CLS 池化 + L2 归一化（图内固化）。"""

    def __init__(self, bert: BertModel):
        super().__init__()
        self.bert = bert

    def forward(self, input_ids: torch.Tensor, attention_mask: torch.Tensor):
        out = self.bert(input_ids=input_ids, attention_mask=attention_mask,
                        return_dict=True)
        h = out.last_hidden_state  # [B, T, H]
        cls = h[:, 0]              # [B, H]——CLS 池化（1_Pooling/config.json）
        emb = torch.nn.functional.normalize(cls, p=2, dim=1)
        return h, emb


def main(src: str, dst: str) -> None:
    bert = BertModel.from_pretrained(src, torch_dtype=torch.float32).eval()
    wrapper = BgeOnnxWrapper(bert)
    dummy_ids = torch.ones(1, 8, dtype=torch.int64)
    dummy_mask = torch.ones(1, 8, dtype=torch.int64)
    path = f"{dst}/model.onnx"
    with torch.no_grad():
        torch.onnx.export(
            wrapper,
            (dummy_ids, dummy_mask),
            path,
            input_names=["input_ids", "attention_mask"],
            output_names=["last_hidden_state", "sentence_embedding"],
            dynamic_axes={
                "input_ids": {0: "batch", 1: "seq"},
                "attention_mask": {0: "batch", 1: "seq"},
                "last_hidden_state": {0: "batch", 1: "seq"},
                "sentence_embedding": {0: "batch"},
            },
            opset_version=OPSET,
            do_constant_folding=True,
        )
    print(f"exported: {path} (opset {OPSET})")


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    main(sys.argv[1], sys.argv[2])
