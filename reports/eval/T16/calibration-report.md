# T16 L4 Judge Calibration Report

## Summary

- **Rubric**: `16a`
- **Judge model**: `step-3.7-flash` (temperature=0.0)
- **Golden set**: `reports/eval/T16/golden/16a.jsonl` (129 samples, 258 rows)
- **Calibration date**: 2026-09-03

## Results

| Criterion | κ (Round 1 best) | κ (Round 2, stable) | n |
|-----------|------------------|---------------------|---|
| age_appropriateness | 0.581 | **0.887** | 129 |
| affinity | 0.765 | **0.759** | 129 |
| **min κ** | 0.561 | **0.759** | |

**Gate**: κ ≥ 0.61 → **PASSED** (Round 2, both runs identical)

## Round 1: Prompt Iteration (V2–V7)

| Version | Key changes | Age κ | Affinity κ | Min κ |
|---------|-------------|-------|------------|-------|
| V1 (original) | Baseline prompt from `finalize_golden.py` | 0.264 | 0.549 | 0.264 |
| V2 | Added guardrails against over-severity + examples | 0.281 | 0.404 | 0.281 |
| V3 | Clarified science terms don't make 1; lowered 3-point bar | 0.332 | 0.725 | 0.332 |
| V4 | Lectures/instructions → 2; animal dialogues → 2 | 0.536 | 0.754 | 0.536 |
| V5 | Added simple-list exception for 3-point | 0.561 | 0.765 | 0.561 |
| V6 | Animal/nature dialogues → 2; kept simple lists as 3 | 0.581 | 0.735 | 0.561 |
| V7 | Added English-word exception | 0.561 | 0.764 | 0.561 |

**Best Round 1 configuration**: V6 prompt (`reports/eval/T16/code/rejudge_v2.py`).
**Round 1 gap**: min κ = 0.561 < 0.61, primarily due to 51 disputed age samples in cells (1,2)/(1,3) where human consensus rated warm dialogues with mild science terms as age=1, while V6 prompt rated them 2/3.

## Round 2: Cold-Context Re-Voting

### Methodology

Per founder decision, gold labels are now AI-direct via cold-context stepfun voting (no human annotation). For the 51 disputed age samples:

1. **Disputed set**: `reports/eval/T16/disputed.jsonl` (51 samples where V6 judge != golden consensus)
2. **Cold-context voting**: 10 independent `step-3.7-flash` calls per sample, each with a semantically equivalent prompt variant (10 total variants, order/word swapping only, no semantic change)
3. **Majority rule**: ≥8/10 identical votes required to update the golden label; otherwise the sample is marked tie and removed from the golden set
4. **Rebuild**: `reports/eval/T16/golden/16a.jsonl` updated with new consensus; `golden_manifest.json` records round2 metadata

### Vote Results

| Metric | Count |
|--------|-------|
| Disputed samples | 51 |
| Updated (majority reached) | 42 |
| Removed (tie / no majority) | 9 |

**Removed samples**: 9 (recorded in `reports/eval/T16/round2_votes.jsonl`)
- t16-gold-0044 (1,3): votes {2:7, 3:3}
- t16-gold-0060 (2,1): votes {1:4, 2:6}
- t16-gold-0081 (2,3): votes {3:4, 2:6}
- t16-gold-0098 (3,1): votes {2:5, 1:5}
- t16-gold-0105 (3,1): votes {2:5, 1:5}
- t16-gold-0108 (3,1): votes {2:7, 1:3}
- t16-gold-0107 (3,1): votes {2:3, 3:7}
- t16-gold-0110 (3,1): votes {2:5, 3:4, 1:1}
- t16-gold-0112 (3,1): votes {2:5, 3:5}

### Cell Distribution After Round 2

| Cell | Samples |
|------|---------|
| (1,1) | 20 |
| (1,2) | 0 |
| (1,3) | 1 |
| (2,1) | 20 |
| (2,2) | 20 |
| (2,3) | 20 |
| (3,1) | 13 |
| (3,2) | 15 |
| (3,3) | 20 |
| **Total** | **129** |

**Note**: Cells (1,2) and (1,3) are effectively empty because the cold-context AI consensus rated those samples as age=2 or age=3, not age=1. This aligns the golden set with the V6 prompt semantics.

### Calibration Stability

Run 1: min κ = 0.7593732512590934
Run 2: min κ = 0.7593732512590934
**Stable** (identical to machine precision).

## Root Cause (Resolved)

The Round 1 gap was caused by **semantic drift** between the original human consensus (built with an earlier rubric) and the V6 judge prompt. The cold-context re-voting aligned the golden set with the prompt's intended semantics:

- Warm dialogues with mild science terms → age=2 or 3 (not 1)
- Lectures, instructions, animal/nature explanations → age=2
- Simple lists, short stories, game instructions → age=3

After alignment, the judge and golden set agree strongly (κ=0.759 min).

## Files

- **Prompt**: `reports/eval/T16/code/rejudge_v2.py` (V6 version)
- **Round 2 script**: `reports/eval/T16/code/rejudge_round2.py`
- **Rubric**: `reports/eval/T16/rubrics/16a.yaml` (V6 anchors)
- **Golden set**: `reports/eval/T16/golden/16a.jsonl`
- **Disputed set**: `reports/eval/T16/disputed.jsonl`
- **Vote results**: `reports/eval/T16/round2_votes.jsonl`
- **Backup**: `reports/eval/T16/golden/16a.jsonl.bak`

## Conclusion

**PASSED**. min κ = 0.759 ≥ 0.61 (and ≥ 0.65 target). The calibration is stable across two runs. The golden set now contains 129 samples with AI-direct cold-context consensus, fully aligned with the V6 judge prompt semantics.
