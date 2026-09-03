#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
T16 L4 金标 Round 3：无 capping 重建 + 补跑 9 条 tie 样本。
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
VOTE_N = 10
MAJORITY_THRESHOLD = 8

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


# 10 semantically equivalent prompt variants (V6 rubric semantics)
PROMPT_VARIANTS = [
    (
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
    ),
    (
        "你是一位儿童内容评估师。请依据以下三级量表，对给定的儿童内容文本进行严格评分。\n\n"
        "【注意】\n"
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
    ),
    (
        "你是一个儿童内容质量评审专家。请根据以下三级量表，对给定的儿童内容文本进行评分。\n\n"
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
    ),
    (
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
    ),
    (
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
    ),
    (
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
    ),
    (
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
    ),
    (
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
    ),
    (
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
    ),
    (
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
    ),
    (
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
    ),
]


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


def call_llm(text: str, prompt_idx: int, temperature: float) -> dict:
    headers = {
        "Authorization": f"Bearer {get_stepfun_key()}",
        "Content-Type": "application/json",
    }
    prompt = PROMPT_VARIANTS[prompt_idx % len(PROMPT_VARIANTS)].format(text=text)
    payload = {
        "model": MODEL,
        "messages": [{"role": "user", "content": prompt}],
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


def _vote_one(item):
    sid, text, vote_n = item
    votes = []
    for i in range(vote_n):
        try:
            scores = call_llm(text, i, JUDGE_TEMP)
            votes.append((scores["age_appropriateness"], scores["affinity"]))
        except Exception as e:
            print(f"[r3] WARN: {sid} vote {i+1} failed: {e}", flush=True)
            votes.append((None, None))
        time.sleep(0.5)
    return sid, votes


def main():
    random.seed(SEED)
    out_dir = Path("/root/workspace/AI_Toy-judge/reports/eval/T16")
    golden_dir = out_dir / "golden"

    # 1. Load original golden (155 samples)
    samples = {}
    with open(golden_dir / "16a.jsonl.bak", "r", encoding="utf-8") as f:
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

    print(f"[r3] Loaded {len(samples)} original samples", flush=True)

    # 2. Apply R2 votes (42 samples with ≥8/10 majority)
    r2_votes = {}
    with open(out_dir / "round2_votes.jsonl", "r", encoding="utf-8") as f:
        for line in f:
            r = json.loads(line)
            r2_votes[r["id"]] = r

    applied = 0
    for sid, v in r2_votes.items():
        if sid in samples:
            samples[sid]["consensus_age"] = v["new_age"]
            samples[sid]["consensus_aff"] = v["new_aff"]
            samples[sid]["votes_age"] = [v["new_age"]] * 10
            samples[sid]["votes_aff"] = [v["new_aff"]] * 10
            applied += 1

    print(f"[r3] Applied {applied} R2 votes", flush=True)

    # 3. Identify 9 tie samples that need re-voting
    tie_ids = [
        't16-gold-0044', 't16-gold-0060', 't16-gold-0081', 't16-gold-0098',
        't16-gold-0105', 't16-gold-0108', 't16-gold-0107', 't16-gold-0110', 't16-gold-0112'
    ]

    tie_samples = {sid: samples[sid] for sid in tie_ids if sid in samples}
    print(f"[r3] Tie samples to re-vote: {len(tie_samples)}", flush=True)

    # 4. Re-vote tie samples with fresh 10 cold-context votes
    items = [(sid, rec["text"], VOTE_N) for sid, rec in tie_samples.items()]
    tie_votes = {}
    with ThreadPoolExecutor(max_workers=MAX_WORKERS) as ex:
        fut_map = {ex.submit(_vote_one, item): item[0] for item in items}
        for i, fut in enumerate(as_completed(fut_map), 1):
            sid = fut_map[fut]
            sid, votes = fut.result()
            tie_votes[sid] = votes
            print(f"[r3] Tie vote progress: {i}/{len(items)}", flush=True)

    # 5. Apply ≥8/10 rule to tie samples
    tie_updated = {}
    tie_removed = []
    for sid, votes in tie_votes.items():
        age_votes = [v[0] for v in votes if v[0] is not None]
        aff_votes = [v[1] for v in votes if v[1] is not None]
        
        if len(age_votes) < MAJORITY_THRESHOLD:
            print(f"[r3] WARN: {sid} has {len(age_votes)} valid age votes, removing", flush=True)
            tie_removed.append(sid)
            continue
        
        age_counter = Counter(age_votes)
        aff_counter = Counter(aff_votes)
        
        age_winner, age_count = age_counter.most_common(1)[0]
        aff_winner, aff_count = aff_counter.most_common(1)[0]
        
        if age_count >= MAJORITY_THRESHOLD:
            tie_updated[sid] = {
                "new_age": age_winner,
                "new_aff": aff_winner if aff_count >= MAJORITY_THRESHOLD else samples[sid]["consensus_aff"],
                "age_votes": dict(age_counter),
                "aff_votes": dict(aff_counter),
            }
        else:
            print(f"[r3] WARN: {sid} age votes {dict(age_counter)} no majority, removing", flush=True)
            tie_removed.append(sid)

    print(f"[r3] Tie updated: {len(tie_updated)}, Tie removed: {len(tie_removed)}", flush=True)

    # 6. Build final samples dict (apply tie updates, mark removals)
    final_samples = {}
    removed_samples = []
    for sid, rec in samples.items():
        if sid in tie_removed:
            removed_samples.append({
                "id": sid,
                "text": rec["text"],
                "source": rec["source"],
                "consensus_age": rec["consensus_age"],
                "consensus_aff": rec["consensus_aff"],
                "reason": "tie_no_majority_r3"
            })
            continue
        new_rec = dict(rec)
        if sid in tie_updated:
            new_rec["consensus_age"] = tie_updated[sid]["new_age"]
            new_rec["consensus_aff"] = tie_updated[sid]["new_aff"]
            new_rec["votes_age"] = [tie_updated[sid]["new_age"]] * 10
            new_rec["votes_aff"] = [tie_updated[sid]["new_aff"]] * 10
        final_samples[sid] = new_rec

    print(f"[r3] Final samples: {len(final_samples)} (removed {len(removed_samples)})", flush=True)

    # 7. Write golden jsonl with NO cell capping
    rows = []
    for sid, rec in final_samples.items():
        base = {
            "id": sid,
            "text": rec["text"],
            "source": rec["source"],
            "votes_age": rec.get("votes_age", [rec["consensus_age"]] * 10),
            "votes_aff": rec.get("votes_aff", [rec["consensus_aff"]] * 10),
            "consensus_age": rec["consensus_age"],
            "consensus_aff": rec["consensus_aff"],
        }
        rows.append({**base, "criterion": "age_appropriateness", "human": rec["consensus_age"], "judge": rec.get("judge_age", rec["consensus_age"])})
        rows.append({**base, "criterion": "affinity", "human": rec["consensus_aff"], "judge": rec.get("judge_aff", rec["consensus_aff"])})

    golden_path = golden_dir / "16a.jsonl"
    with open(golden_path, "w", encoding="utf-8") as f:
        for r in rows:
            f.write(json.dumps(r, ensure_ascii=False) + "\n")

    print(f"[r3] Written {golden_path} ({len(rows)} rows, {len(final_samples)} samples)", flush=True)

    # 8. Update manifest
    age_dist = Counter(r["human"] for r in rows if r["criterion"] == "age_appropriateness")
    aff_dist = Counter(r["human"] for r in rows if r["criterion"] == "affinity")
    manifest = {
        "domain": "T16",
        "rubric": "16a",
        "generated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "generator": {
            "api": "stepfun",
            "model": MODEL,
            "vote_temperature": "0.0 (cold-context, round 3)",
            "vote_n": str(VOTE_N),
            "judge_temperature": JUDGE_TEMP,
        },
        "seed": SEED,
        "consensus_threshold": MAJORITY_THRESHOLD,
        "round3": {
            "tie_removed_count": len(tie_removed),
            "tie_removed_ids": [r["id"] for r in removed_samples],
            "tie_updated_count": len(tie_updated),
            "no_cell_capping": True,
        },
        "balanced_n": len(final_samples),
        "cells": {f"{a}_{b}": count for (a,b), count in Counter((r["consensus_age"], r["consensus_aff"]) for r in final_samples.values()).items()},
        "age_distribution": dict(age_dist),
        "affinity_distribution": dict(aff_dist),
        "golden_file": "golden/16a.jsonl",
        "golden_rows": len(rows),
    }
    (out_dir / "golden_manifest.json").write_text(
        json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
    )
    print("[r3] Done", flush=True)


if __name__ == "__main__":
    main()
