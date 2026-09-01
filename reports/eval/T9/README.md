# T9 安全分类器（safety clf）训练评估报告

## 这是什么

T9 文本安全分类器的离线训练成果存档。基于 hfl/chinese-macbert-base，fp16，三分类 block/allow/notify。

- 正式模型：`models/incoming/t9/pytorch_model.bin`（manifest: `models/manifests/t9-crisis-clf.yaml`）
- 选优/early stop：metric=recall_block patience=2；门禁口径 eval split=test（n=135）
- 按 T9 验收哲学：不报 accuracy（极不均衡下会说谎）；只报 recall 与误拒张力对

## 训练数据（合成）

- train/val/test = 1080/135/135（分层 80%/10%/10%）
- 含危机变体（直白/隐喻/拼音/语气伪装）、attack 类别（商业诱导/多轮渐进渗透/成人话题诱导/角色扮演脱轨/越狱提示词）、boundary 集

## 最终指标（训练侧参考，正式门禁以 gaterunner 为准）

| 门禁参考 | 实测 | 判定 |
|---|---|---|
| T9-G0-01 crisis_recall ≥ 0.99 | 1.0000（漏报 0/48） | PASS |
| T9-G1-01 false_refusal ≤ 0.03 | 0.0000 | PASS |
| T9-G0-04 proxy attack 拦截 recall ≥ 0.99 | 1.0000 | PASS |
| T9-G0-05 proxy boundary 漏通知=0 / 误通知≤2% | 漏 4 / 误 1（n=24） | FAIL |

### 逐类 precision / recall / f1

| label | precision | recall | f1 | support |
|---|---|---|---|---|
| block | 0.9894 | 1.0000 | 0.9947 | 93 |
| allow | 0.8824 | 0.9375 | 0.9091 | 32 |
| notify | 0.8571 | 0.6000 | 0.7059 | 10 |

## 门禁结论

危机零漏报（recall 1.0）与零误拒（false_refusal 0.0）同时达成，attack 拦截 recall 1.0。缺口在 boundary 集（G0-05 FAIL）：漏通知 4 条（notify→allow，多为隐性求助/躯体化表达）、误通知 1 条（allow→notify）。

## 复现性存档

- `code/train_safety_clf.py`：训练骨架（hfl/chinese-macbert-base + fp16 + early stop on recall_block）
