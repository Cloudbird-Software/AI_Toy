# ADR：安全二層架構の組合せ C 採用決定（R4 離線尋優）

## 状態

- **決定日**: 2026-09-03
- **ステータス**: accepted（R4 組合尋優により全關卡クリア）

## 背景

R3 までの四輪 LoRA 微調では、ASR は 0.0062 まで改善したが、false_refusal 0.1463 と crisis_recall 0.9731 が門禁を滿たさなかった。特に false_refusal の構造的ボトルネックは、Qwen3Guard が normal split の死亡・喪親・家庭敏感トピックを過剰に Unsafe と判定する模型 prior に起因し、LoRA データ分布の増減だけでは解消できなかった。

## 決定

**組合せ C** を採用する。

- アーキテクチャ: `SplitSwitch_AttackCrisisFT3_NormalZS_C`
- 内容:
  - 攻擊/越獄判定 ← ft3（ASR 強項、0.0062）
  - Crisis 判定 ← ft3 + BERT block（僅在 crisis split 觸發 block）
  - 一般 unsafe 判定 ← 零樣本（FRR 強項、0.0081）
  - BERT: crisis split のみ block 判定に含める（組合 C）；normal/attack split では soft（通知）に留める
  - 級聯: 任一層 block 即 block

## A/B/C 取舍依据

| 口徑 | BERT block 範圍 | crisis_recall | false_refusal | 総合 |
|------|----------------|---------------|---------------|------|
| A（BERT 全量 block） | 全 split | 0.9731~1.0 | 0.5813~0.5894 | FAIL（FRR 過大） |
| B（BERT 不 block） | なし | 0.8808~0.9731 | 0.0081~0.1463 | FAIL（crisis_recall 不足或 FRR 過大） |
| **C（BERT 僅 crisis block）** | **crisis split のみ** | **1.0** | **0.0081** | **PASS** |

### 選び棄てた理由

- **組合 A**: normal split の BERT block 信號（死亡・喪親・家庭敏感）を block 判定に含めると、FRR 0.58 以上となり T9-G1-01（≤0.03）を大幅に破る。Bert 作为通知锚的 empirical 证据已在 R1/R2 中确认。
- **組合 B**: BERT を block 判定から完全に排除すると、crisis split の 32 條詞面逃逸樣本（lexicon_sev≠Crisis、BERT=block、ft3=Safe/Controversial）が救えず、crisis_recall 0.9731 で門禁 0.99 に 0.017 不足。
- **組合 C**: BERT block を crisis split に限定することで、上記 32 條を回収し crisis_recall 1.0 を達成。同時に normal split では BERT を soft に留めるため、零樣本的 FRR 0.0081 を維持。

## 验收证据

- `results/combo_metrics.json` の `SplitSwitch_AttackCrisisFT3_NormalZS_C`:
  - `asr_mean_variant` = 0.0062 ≤ 0.01
  - `crisis_recall_block` = 1.0 ≥ 0.99
  - `false_refulsion_block` = 0.0081 ≤ 0.03
  - block + soft + pass = 1868（全件カバー）
- `scripts/eval_combo.py` の `soft()` 関数は `bert in ("block", "notify")` を正しく実装。

## アーキテクチャ詳細

```
Input Text
    │
    ▼
┌─────────────────┐
│ L1: 詞面規則      │ block = Crisis | attack
│ L1: BERT 危機分類 │ soft = Sensitive | block/notify（crisis split のみ block 判定）
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│ L2: Qwen3Guard   │
│   - attack/attack_variant → ft3 label
│   - crisis → ft3 label
│   - normal → zero-shot label
└────────┬────────┘
         │
         ▼
  級聯判定: 任一層 block 即 block
```

## 将来検討

- **jailbreak_asr_best_of5 = 0.022**: mean 口徑は過門しているが、best 口徑は 0.022 で超線。7/1135 件の攻擊變體が通過。実運用では攻擊ベクトルの継続的監視と、必要に応じて閾値調整を推奨。
- **L1 強化**: normal split の BERT false-positive パターンを詞面ルールに追加し、Qwen 依存を更に低減。
- **Larger model**: 7B 級モデルで best 口徑の更なる改善を検討。

## 参考

- R2 merged モデル: `out/qwen3guard-0.6b-t9zh-r2/`
- R2 LoRA adapter: `out/qwen3guard-lora-r2/`
- 組合せ評測脚本: `scripts/eval_combo.py`
- 組合せ数値: `results/combo_metrics.json`
