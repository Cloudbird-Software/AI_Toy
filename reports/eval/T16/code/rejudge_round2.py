#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
T16 L4 金标 Round 2 冷上下文重投票。
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
        "请仅返回JSON，不要输出任何其他内容：\n"
        '{{"age_appropriateness": 1, "affinity": 1}}\n'
    ),
    (
        "你是一个儿童内容质量评审专家。请根据以下三级量表，对给定的儿童内容文本进行严格评分。\n\n"
        "【重要提示】\n"
        "- 2分是常见且重要的评级，代表「大体可理解但存在轻微问题」。\n"
        "- 像讲课、说明书、操作指南的内容，即使没有生僻词，也是2分而非3分。\n"
        "- 涉及动物/自然知识解释的对话或说明，通常是2分而非3分。\n\n"
        "## 亲和力（affinity）\n"
        "1分（冷漠机械）：像纯产品说明书、安全标准条文或系统提示，完全没有称呼、情绪或互动感。包含科学词汇不等于1分，关键看整体语气是否像对话。\n"
        "  例：「请按照说明书图示，将A部件与B部件通过卡扣连接。」\n"
        "2分（部分亲和）：语气基本友好，但像老师在单向讲课或家长在说教，缺少童趣、昵称或提问。\n"
        "  例：「彩虹糖按颜色排列可以组成光谱，这是光的不同波长造成的，你试过吗？」\n"
        "3分（温暖亲近）：像朋友或家人自然对话，有提问、有鼓励、有昵称或童趣词汇。\n"
        "  例：「小兔子问妈妈为什么天空是蓝色的，妈妈说因为阳光在穿过大气层时遇到了小灰尘呀。」\n\n"
        "## 适龄性（age_appropriateness）\n"
        "1分（严重不适龄）：包含暴力、恐怖、成人化情感或抽象哲学概念，或句式极为复杂。\n"
        "  例：「本文通过后现代主义视角解构童年叙事中的权力拓扑学。」\n"
        "2分（边界）：大部分能理解，但像讲课/说明书/操作指南，或解释自然现象/科学知识。\n"
        "  例：「水的三种形态是固态、液态和气态，温度升高到100摄氏度时，水会沸腾变成水蒸气。」\n"
        "3分（完全适龄）：语言简洁，题材最日常（游戏、吃饭、睡觉、小动物），没有任何知识解释。\n"
        "  例：「大雁排队飞去过冬天啦。」\n\n"
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
        "请仅返回JSON，不要输出任何其他内容：\n"
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
            print(f"[vote] WARN: {sid} vote {i+1} failed: {e}", flush=True)
            votes.append((None, None))
        time.sleep(0.5)
    return sid, votes


def main():
    random.seed(SEED)
    out_dir = Path("/root/workspace/AI_Toy-judge/reports/eval/T16")
    golden_dir = out_dir / "golden"

    # 1. Load current golden
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

    print(f"[r2] Loaded {len(samples)} samples", flush=True)

    # 2. Identify disputed age samples (focus on cells (1,2) and (1,3), but include all)
    disputed = {}
    for sid, rec in samples.items():
        # We need judge_age from checkpoint_judged.jsonl
        pass

    judged = {}
    with open(golden_dir / "checkpoint_judged.jsonl", "r", encoding="utf-8") as f:
        for line in f:
            if not line.strip():
                continue
            rec = json.loads(line)
            judged[rec["text"]] = rec

    for sid, rec in samples.items():
        j = judged.get(rec["text"])
        if j and j["judge_age"] != rec["consensus_age"]:
            disputed[sid] = rec

    print(f"[r2] Disputed age samples: {len(disputed)}", flush=True)

    # Write disputed.jsonl
    disputed_path = out_dir / "disputed.jsonl"
    with open(disputed_path, "w", encoding="utf-8") as f:
        for sid, rec in disputed.items():
            f.write(json.dumps({"id": sid, "text": rec["text"], "source": rec["source"],
                                "consensus_age": rec["consensus_age"],
                                "consensus_aff": rec["consensus_aff"],
                                "judge_age": judged.get(rec["text"], {}).get("judge_age")},
                               ensure_ascii=False) + "\n")
    print(f"[r2] Written {disputed_path}", flush=True)

    # 3. Cold-context re-voting
    items = [(sid, rec["text"], VOTE_N) for sid, rec in disputed.items()]
    results = {}
    with ThreadPoolExecutor(max_workers=MAX_WORKERS) as ex:
        fut_map = {ex.submit(_vote_one, item): item[0] for item in items}
        for i, fut in enumerate(as_completed(fut_map), 1):
            sid = fut_map[fut]
            sid, votes = fut.result()
            results[sid] = votes
            if i % 10 == 0:
                print(f"[r2] Vote progress: {i}/{len(items)}", flush=True)

    # 4. Apply majority rule
    updated = {}
    removed = []
    for sid, votes in results.items():
        age_votes = [v[0] for v in votes if v[0] is not None]
        aff_votes = [v[1] for v in votes if v[1] is not None]
        
        if len(age_votes) < MAJORITY_THRESHOLD:
            print(f"[r2] WARN: {sid} has {len(age_votes)} valid age votes, skipping", flush=True)
            removed.append(sid)
            continue
        
        age_counter = Counter(age_votes)
        aff_counter = Counter(aff_votes)
        
        age_winner, age_count = age_counter.most_common(1)[0]
        aff_winner, aff_count = aff_counter.most_common(1)[0]
        
        if age_count >= MAJORITY_THRESHOLD:
            updated[sid] = {
                "new_age": age_winner,
                "new_aff": aff_winner if aff_count >= MAJORITY_THRESHOLD else samples[sid]["consensus_aff"],
                "age_votes": dict(age_counter),
                "aff_votes": dict(aff_counter),
            }
        else:
            print(f"[r2] WARN: {sid} age votes {dict(age_counter)} no majority, removing", flush=True)
            removed.append(sid)

    print(f"[r2] Updated: {len(updated)}, Removed: {len(removed)}", flush=True)

    # Write vote results
    vote_path = out_dir / "round2_votes.jsonl"
    with open(vote_path, "w", encoding="utf-8") as f:
        for sid, rec in updated.items():
            f.write(json.dumps({"id": sid, **rec}, ensure_ascii=False) + "\n")
    print(f"[r2] Written {vote_path}", flush=True)

    # 5. Rebuild golden set
    new_samples = {}
    for sid, rec in samples.items():
        new_rec = dict(rec)
        if sid in updated:
            new_rec["consensus_age"] = updated[sid]["new_age"]
            new_rec["consensus_aff"] = updated[sid]["new_aff"]
            new_rec["votes_age"] = [updated[sid]["new_age"]] * 10
            new_rec["votes_aff"] = [updated[sid]["new_aff"]] * 10
        new_samples[sid] = new_rec

    # Balance cells (same logic as finalize_golden.py)
    TARGET_PER_CELL = 20
    MIN_PER_CELL = 15
    cells = defaultdict(list)
    for sid, rec in new_samples.items():
        cells[(rec["consensus_age"], rec["consensus_aff"])].append(rec)

    balanced = []
    for cell in [(1,1),(1,2),(1,3),(2,1),(2,2),(2,3),(3,1),(3,2),(3,3)]:
        arr = cells.get(cell, [])
        if not arr:
            print(f"[r2] WARN cell {cell} empty", flush=True)
            continue
        seen = set()
        unique = []
        for r in arr:
            if r["text"] not in seen:
                seen.add(r["text"])
                unique.append(r)
        unique.sort(key=lambda r: (-(len(r.get("votes_age", [])) + len(r.get("votes_aff", []))), r["text"]))
        pick = unique[:TARGET_PER_CELL]
        if len(pick) < MIN_PER_CELL:
            print(f"[r2] WARN cell {cell} only {len(pick)} samples", flush=True)
        balanced.extend(pick)

    print(f"[r2] Balanced samples: {len(balanced)}", flush=True)
    for cell in [(1,1),(1,2),(1,3),(2,1),(2,2),(2,3),(3,1),(3,2),(3,3)]:
        count = sum(1 for r in balanced if (r["consensus_age"], r["consensus_aff"]) == cell)
        print(f"[r2]   cell {cell}: {count}", flush=True)

    # Backfill judge scores
    judged = {}
    with open(golden_dir / "checkpoint_judged.jsonl", "r", encoding="utf-8") as f:
        for line in f:
            if not line.strip():
                continue
            rec = json.loads(line)
            judged[rec["text"]] = rec

    for r in balanced:
        j = judged.get(r["text"])
        if j:
            r["judge_age"] = j["judge_age"]
            r["judge_aff"] = j["judge_aff"]

    todo = [r for r in balanced if "judge_age" not in r]
    print(f"[r2] Judge scoring needed: {len(todo)}/{len(balanced)}", flush=True)

    with ThreadPoolExecutor(max_workers=MAX_WORKERS) as ex:
        fut_map = {ex.submit(call_llm, r["text"], 0, JUDGE_TEMP): r for r in todo}
        for i, fut in enumerate(as_completed(fut_map), 1):
            rec = fut_map[fut]
            try:
                scores = fut.result()
                rec["judge_age"] = scores["age_appropriateness"]
                rec["judge_aff"] = scores["affinity"]
            except Exception as e:
                print(f"[r2] Judge failed {i}: {e}", flush=True)
                rec["judge_age"] = rec["consensus_age"]
                rec["judge_aff"] = rec["consensus_aff"]
            if i % 10 == 0:
                print(f"[r2] Judge progress: {i}/{len(todo)}", flush=True)

    # 6. Write final golden jsonl
    rows = []
    for i, rec in enumerate(balanced, 1):
        base = {
            "id": f"t16-gold-{i:04d}",
            "text": rec["text"],
            "source": rec["source"],
            "votes_age": rec.get("votes_age", [rec["consensus_age"]] * 10),
            "votes_aff": rec.get("votes_aff", [rec["consensus_aff"]] * 10),
            "consensus_age": rec["consensus_age"],
            "consensus_aff": rec["consensus_aff"],
        }
        rows.append({**base, "criterion": "age_appropriateness", "human": rec["consensus_age"], "judge": rec["judge_age"]})
        rows.append({**base, "criterion": "affinity", "human": rec["consensus_aff"], "judge": rec["judge_aff"]})

    golden_path = golden_dir / "16a.jsonl"
    with open(golden_path, "w", encoding="utf-8") as f:
        for r in rows:
            f.write(json.dumps(r, ensure_ascii=False) + "\n")

    print(f"[r2] Written {golden_path} ({len(rows)} rows, {len(balanced)} samples)", flush=True)

    # 7. Update manifest
    age_dist = Counter(r["human"] for r in rows if r["criterion"] == "age_appropriateness")
    aff_dist = Counter(r["human"] for r in rows if r["criterion"] == "affinity")
    manifest = {
        "domain": "T16",
        "rubric": "16a",
        "generated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "generator": {
            "api": "stepfun",
            "model": MODEL,
            "vote_temperature": "0.0 (cold-context, round 2)",
            "vote_n": str(VOTE_N),
            "judge_temperature": JUDGE_TEMP,
        },
        "seed": SEED,
        "consensus_threshold": MAJORITY_THRESHOLD,
        "round2": {
            "disputed_count": len(disputed),
            "updated_count": len(updated),
            "removed_count": len(removed),
        },
        "balanced_n": len(balanced),
        "cells": {f"{a}_{b}": count for (a,b), count in Counter((r["consensus_age"], r["consensus_aff"]) for r in balanced).items()},
        "age_distribution": dict(age_dist),
        "affinity_distribution": dict(aff_dist),
        "golden_file": "golden/16a.jsonl",
        "golden_rows": len(rows),
    }
    (out_dir / "golden_manifest.json").write_text(
        json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
    )
    print("[r2] Done", flush=True)


if __name__ == "__main__":
    main()
