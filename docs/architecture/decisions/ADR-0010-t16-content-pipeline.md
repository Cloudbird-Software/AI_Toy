# ADR-0010 T16/T18 内容管线：TinyStories 角色 + 中文 synthgen 合成设计
状态：proposed 2026-09-03（IR #134，规格=issue-134.md）
背景：T16 场景包需要故事内容。商用可用的中文儿童故事语料缺位；TinyStories（CDLA-Sharing 1.0）提供 219MB 英语儿童故事结构学习语料，可直接用于故事结构/叙述节奏建模，但不能直接充当中文内容。
决策：TinyStories 仅作为英语故事结构学习语料（角色：结构先验），不直接进入中文场景包内容；中文故事内容通过 tools/synthgen 合成管线生成（LLM 生成 + 溯源戳 + 8:2 synth-holdout 切分）。T20 模拟器产物明确禁止进入任何训练/微调/合成 Holdout 集（AGENTS.md 红线）。
备选否决：直接用英语 TinyStories 充当中文内容（语言不匹配、适龄性不可控）； scraped 中文童书（商用授权不明，法务风险高）。
后果：synthgen 管线需实现 8:2 切分逻辑与溯源戳注册；T16-G0-01 内容安全门禁覆盖合成内容全量；T18 内容评测（L4 κ≥0.61 rubric）覆盖合成集与 Holdout 集；新增依赖（LLM API）须在 M2 由 founder 审批后接线，M1 仅保留管线骨架与设计契约。
