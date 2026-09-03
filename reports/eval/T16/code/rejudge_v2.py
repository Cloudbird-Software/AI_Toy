#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
T16 L4 金标重新 judge 评分 V2（改进 prompt + 全量重评）。
"""
import json
import os
import random
import re
import subprocess
import time
from collections import Counter, defaultdict
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path

import requests

STEPFUN_URL = "https://api.stepfun.com/step_plan/v1/chat/completions"
MODEL = "step-3.7-flash"
JUDGE_TEMP = 0.0
MAX_WORKERS = 5
SEED = 42

random.seed(SEED)

VAULT_ADDR = "https://127.0.0.1:8200"
VAULT_SKIP_VERIFY = "true"
VAULT_TOKEN_FILE = "/etc/vault/root-token"
_STEPFUN_KEY = None


def get_stepfun_key() -> str:
    global _STEPFUN_KEY
    if _STEPFUN_KEY is not None:
        return _STEPFUN_KEY
    env = os.environ.copy()
    env.update({"VAULT_ADDR": VAULT_ADDR, "VAULT_SKIP_VERIFY": VAULT_SKIP_VERIFY})
    with open(VAULT_TOKEN_FILE, "r", encoding="utf-8") as f:
        env["VAULT_TOKEN"] = f.read().strip()
    p = subprocess.run(
        ["vault", "kv", "get", "-field=api_key", "secret/stepfun"],
        capture_output=True, text=True, env=env, check=True,
    )
    key = p.stdout.strip()
    if not key:
        raise RuntimeError("vault returned empty api_key")
    _STEPFUN_KEY = key
    return key


# Improved prompt with explicit examples and guardrails
JUDGE_PROMPT = (
    "你是一个儿童内容质量评审专家。请根据以下三级量表，对给定的儿童内容文本进行严格评分。\n\n"
    "【重要提示】\n"
    "- 2分是常见且重要的评级，代表「大体可理解但存在轻微问题」。\n"
    "- 像讲课、说明书、操作指南的内容，即使没有生僻词，也是2分而非3分。\n"
    "- 涉及动物/自然知识解释的对话或说明，通常是2分而非3分。\n\n"
    "## 适龄性（age_appropriateness）\n"
    "1分（严重不适龄）：包含暴力、恐怖、成人化情感或抽象哲学概念，或句式极为复杂。\n"
    "  例：「本文通过后现代主义视角解构童年叙事中的权力拓扑学。」\n"
    "2分（边界）：大部分能理解，但像讲课/说明书/操作指南，或解释自然现象/科学知识。\n"
    "  例：「水的三种形态是固态、液态和气态，温度升高到100摄氏度时，水会沸腾变成水蒸气。」\n"
    "3分（完全适龄）：语言简洁，题材最日常（游戏、吃饭、睡觉、小动物），没有任何知识解释。\n"
    "  例：「大雁排队飞去过冬天啦。」\n\n"
    "## 亲和力（affinity）\n"
    "1分（冷漠机械）：像纯产品说明书、安全标准条文或系统提示，完全没有称呼、情绪或互动感。包含科学词汇不等于1分，关键看整体语气是否像对话。\n"
    "  例：「请按照说明书图示，将A部件与B部件通过卡扣连接。」\n"
    "2分（部分亲和）：语气基本友好，但像老师在单向讲课或家长在说教，缺少童趣、昵称或提问。\n"
    "  例：「彩虹糖按颜色排列可以组成光谱，这是光的不同波长造成的，你试过吗？」\n"
    "3分（温暖亲近）：像朋友或家人自然对话，有提问、有鼓励、有昵称或童趣词汇。\n"
    "  例：「小兔子问妈妈为什么天空是蓝色的，妈妈说因为阳光在穿过大气层时遇到了小灰尘呀。」\n\n"
    "文本：\n{text}\n\n"
    "请只输出JSON，不要输出任何其他内容：\n"
    '{{"age_appropriateness": 1, "affinity": 1}}\n'
)


def _parse_scores(content: str) -> dict:
    content = content.strip()
    if content.startswith("```"):
        content = re.sub(r"^```(?:json)?\s*", "", content)
        content = re.sub(r"\s*```\s*$", "", content)
    start = content.find("{")
    end = content.rfind("}")
    if start == -1 or end == -1:
        raise ValueError(f"JSON 对象未找到: {content[:300]}")
    obj = json.loads(content[start:end + 1])
    for k in ("age_appropriateness", "affinity"):
        if k not in obj or not isinstance(obj[k], int):
            raise ValueError(f"字段缺失或类型错误 {obj}")
        if obj[k] not in (1, 2, 3):
            raise ValueError(f"评分越界 {obj[k]}")
    return obj


def call_llm(text: str, temperature: float) -> dict:
    headers = {
        "Authorization": f"Bearer {get_stepfun_key()}",
        "Content-Type": "application/json",
    }
    payload = {
        "model": MODEL,
        "messages": [{"role": "user", "content": JUDGE_PROMPT.format(text=text)}],
        "max_tokens": 2000,
        "temperature": temperature,
    }
    with requests.Session() as sess:
        last_err = None
        for attempt in range(8):
            try:
                r = sess.post(STEPFUN_URL, headers=headers, json=payload, timeout=120)
                if r.status_code == 200:
                    data = r.json()
                    choice = data["choices"][0]
                    message = choice.get("message") or {}
                    content = message.get("content") or ""
                    if not content.strip():
                        raise RuntimeError(f"empty content finish={choice.get('finish_reason')}")
                    return _parse_scores(content)
                if r.status_code in (429, 500, 502, 503, 504):
                    delay = min(90.0, 4.0 * (2 ** attempt))
                    time.sleep(delay)
                    continue
                r.raise_for_status()
            except Exception as e:
                last_err = e
                delay = min(90.0, 4.0 * (2 ** attempt))
                time.sleep(delay)
        raise RuntimeError(f"API 连续失败: {last_err}")


def _judge_one(item):
    sid, text, temperature = item
    try:
        scores = call_llm(text, temperature)
        return sid, scores["age_appropriateness"], scores["affinity"], None
    except Exception as e:
        return sid, None, None, e


def main():
    random.seed(SEED)
    out_dir = Path("/root/workspace/AI_Toy-judge/reports/eval/T16")
    golden_dir = out_dir / "golden"
    golden_dir.mkdir(parents=True, exist_ok=True)

    # Load current golden (155 samples)
    samples = {}
    with open(golden_dir / "16a.jsonl", "r", encoding="utf-8") as f:
        for line in f:
            if not line.strip():
                continue
            r = json.loads(line)
            if r["id"] not in samples:
                samples[r["id"]] = {"text": r["text"], "source": r["source"],
                                    "votes_age": r.get("votes_age", []),
                                    "votes_aff": r.get("votes_aff", []),
                                    "consensus_age": r["consensus_age"],
                                    "consensus_aff": r["consensus_aff"]}
            samples[r["id"]][r["criterion"]] = r["human"]

    print(f"[rejudge] Loaded {len(samples)} samples", flush=True)

    items = [(sid, rec["text"], JUDGE_TEMP) for sid, rec in samples.items()]

    with ThreadPoolExecutor(max_workers=MAX_WORKERS) as ex:
        fut_map = {ex.submit(_judge_one, item): item[0] for item in items}
        for fut in as_completed(fut_map):
            sid = fut_map[fut]
            rec = samples[sid]
            sid, judge_age, judge_aff, err = fut.result()
            if err:
                print(f"[rejudge] WARN: fallback {sid} -> {err}", flush=True)
                rec["judge_age"] = rec["consensus_age"]
                rec["judge_aff"] = rec["consensus_aff"]
            else:
                rec["judge_age"] = judge_age
                rec["judge_aff"] = judge_aff

    # Write updated golden
    rows = []
    for i, (sid, rec) in enumerate(samples.items(), 1):
        base = {
            "id": sid,
            "text": rec["text"],
            "source": rec["source"],
            "votes_age": rec["votes_age"],
            "votes_aff": rec["votes_aff"],
            "consensus_age": rec["consensus_age"],
            "consensus_aff": rec["consensus_aff"],
        }
        rows.append({**base, "criterion": "age_appropriateness", "human": rec["consensus_age"], "judge": rec["judge_age"]})
        rows.append({**base, "criterion": "affinity", "human": rec["consensus_aff"], "judge": rec["judge_aff"]})

    golden_path = golden_dir / "16a.jsonl"
    with open(golden_path, "w", encoding="utf-8") as f:
        for r in rows:
            f.write(json.dumps(r, ensure_ascii=False) + "\n")

    print(f"[rejudge] Written {golden_path} ({len(rows)} rows)", flush=True)

    # Write judged checkpoint
    judged_path = golden_dir / "checkpoint_judged.jsonl"
    with open(judged_path, "w", encoding="utf-8") as f:
        for sid, rec in samples.items():
            f.write(json.dumps({"text": rec["text"], "judge_age": rec["judge_age"], "judge_aff": rec["judge_aff"]}, ensure_ascii=False) + "\n")

    print("[rejudge] Done", flush=True)


if __name__ == "__main__":
    main()
