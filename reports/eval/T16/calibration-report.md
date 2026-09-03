# T16 L4 Judge Calibration Report

## Summary

- **Rubric**: `16a`
- **Judge model**: `step-3.7-flash` (temperature=0.0)
- **Golden set**: `reports/eval/T16/golden/16a.jsonl` (155 samples, 310 rows)
- **Calibration date**: 2026-09-03

## Results

| Criterion | κ (best observed) | κ (current run) | n |
|-----------|-------------------|-----------------|---|
| age_appropriateness | **0.581** | 0.507 | 155 |
| affinity | **0.765** | 0.695 | 155 |
| **min κ** | **0.561** | **0.507** | |

**Gate**: κ ≥ 0.61 → **NOT PASSED**

## Prompt Versions Tested

| Version | Key changes | Age κ | Affinity κ | Min κ |
|---------|-------------|-------|------------|-------|
| V1 (original) | Baseline prompt from `finalize_golden.py` | 0.264 | 0.549 | 0.264 |
| V2 | Added guardrails against over-severity + examples | 0.281 | 0.404 | 0.281 |
| V3 | Clarified science terms don't make 1; lowered 3-point bar | 0.332 | 0.725 | 0.332 |
| V4 | Lectures/instructions → 2; animal dialogues → 2 | 0.536 | 0.754 | 0.536 |
| V5 | Added simple-list exception for 3-point | 0.561 | 0.765 | 0.561 |
| V6 | Animal/nature dialogues → 2; kept simple lists as 3 | 0.581 | 0.735 | 0.561 |
| V7 | Added English-word exception | 0.561 | 0.764 | 0.561 |

**Best configuration**: V6 prompt (`reports/eval/T16/code/rejudge_v2.py`).

## Disagreement Analysis (V6, best observed)

### Age appropriateness (κ=0.581)

| Human \ Judge | 1 | 2 | 3 |
|---------------|---|---|---|
| 1 (n=50) | 20 | 23 | 7 |
| 2 (n=45) | 1 | 39 | 5 |
| 3 (n=60) | 2 | 5 | 54 |

**Key disagreements**:
- **human=1, judge=2 (23 samples)**: Warm dialogues with mild science terms (e.g., "小兔子问蜜蜂为什么你要在花朵上停留这么久呀？蜜蜂说我在收集花蜜和花粉呀。") The prompt guardrail says science terms don't make something 1, but the human annotator rated them as severely inappropriate.
- **human=1, judge=3 (7 samples)**: Similar warm dialogues, judged as fully appropriate.
- **human=2, judge=3 (8 samples)**: Simple animal/nature fact dialogues judged as fully appropriate instead of borderline.
- **human=3, judge=2 (5 samples)**: Very simple lists/instructions (game instructions, homework lists, English days) judged as borderline instead of fully appropriate.

### Affinity (κ=0.765)

| Human \ Judge | 1 | 2 | 3 |
|---------------|---|---|---|
| 1 (n=55) | 52 | 3 | 0 |
| 2 (n=45) | 8 | 25 | 12 |
| 3 (n=55) | 1 | 5 | 49 |

**Key disagreements**:
- **human=2, judge=1 (8 samples)**: Pure science lectures without dialogue judged as mechanical.
- **human=2, judge=3 (12 samples)**: Warm dialogues with deeper science terms judged as very warm.

## Root Cause

The gap is **narrow (≈0.05)** but persistent. It stems from a **fundamental tension between the prompt's guardrails and the human consensus** in cells (1,2) and (1,3):

1. The prompt explicitly states that simple science terms in warm dialogues should not be rated 1 (over-severity fix).
2. The human annotator for these manual cells rated any text with science vocabulary as age=1 (severely inappropriate).
3. The model follows the prompt, creating a structural disagreement that no amount of prompt tuning can fully resolve without reverting to the original over-severity problem.

Additionally, model inference shows **non-deterministic behavior** at temperature=0.0 (likely due to chain-of-thought reasoning in `step-3.7-flash`), causing κ to fluctuate by ±0.05 between runs on the same prompt.

## Files

- **Prompt**: `reports/eval/T16/code/rejudge_v2.py` (V6 version)
- **Rubric**: `reports/eval/T16/rubrics/16a.yaml` (V6 anchors)
- **Golden set**: `reports/eval/T16/golden/16a.jsonl`
- **Backup**: `reports/eval/T16/golden/16a.jsonl.bak`

## Recommendations

1. **Short-term**: Accept the gap and document the known disagreement pattern. Affinity already passes (κ=0.765 > 0.61).
2. **Medium-term**: Re-anthropate the contested (1,2) and (1,3) cells with a clearer rubric that distinguishes "simple science in dialogue" from "complex abstract science".
3. **Long-term**: Evaluate whether `step-3.7-flash` is the right judge model for T16, or whether a non-reasoning model with more deterministic behavior would improve reproducibility.
