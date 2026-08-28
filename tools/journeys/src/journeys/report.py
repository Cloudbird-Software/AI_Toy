"""报告序列化：JSON 到 stdout 或 --out 文件（spec §3.5）。"""

import json
from pathlib import Path

def emit(report, out_path=None):
    text = json.dumps(report, ensure_ascii=False, indent=2)
    if out_path:
        Path(out_path).write_text(text + "\n", encoding="utf-8")
    else:
        print(text)
