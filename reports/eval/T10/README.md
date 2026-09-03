# T10 记忆图谱 M2：bge-small-zh 真推理 Embedder 评测报告（issue #134 收口）

## 这是什么

T10/T11 向量底座从 M1 确定性桩嵌入切到 bge-small-zh-v1.5 ONNX 真推理的落仓存档：
ONNX 导出与对拍、纯 Go 分词器、Go 引擎、门禁语义面接线、中文检索 sanity、延迟实测。
选型与工程决策见 `docs/architecture/decisions/ADR-0011-t10-bge-onnx-embedder.md`。

- 正式模型：`models/incoming/bge-small-zh-v1.5/onnx/model.onnx`（manifest:
  `models/manifests/bge-small-zh.yaml` 条目 `bge-small-zh-v1.5-onnx`，MIT，opset 17，
  94,863,227 B，sha256 325654ed02d0…）
- Go 引擎：`packages/go/memory/onnx_embedder.go`（OnnxEmbedder，实现 M1 的 Embedder
  接口；装配惯例与 T3 vap/T14 vad 同源；StubEmbedder 保留作模型缺失 fallback）
- 分词器：`packages/go/memory/wordpiece.go`（纯 Go 自实现 BERT WordPiece——**零新
  依赖**，license 台账无变更；行为由 HF 生成的 golden 逐条锁死）
- 门禁：`just gate T10 all`（等效 `go run ./tools/gaterunner/cmd/gaterunner run
  --asset T10 --level all --suite nightly`）→ **g0=pass g1=pass not_impl=0 exit 0**；
  `reports/gates/T10.json` 已随本 PR 刷新

## 关键事实修正（对 M1 注释的两处勘误）

1. **维度 512 不是 384**：bge-small-zh-v1.5 hidden_size=512（M1 embedding.go 注释
   「384 维」系 bge-small-en 笔误）。同 Store 内向量恒同源同维，检索语义不受影响。
2. **池化口径以模型随附配置为准**：`1_Pooling/config.json` 为 `cls_token=true`
   （BGE 官方用法），任务卡所猜 mean pooling 不采用；CLS 池化 + L2 归一化固化入图，
   Go 侧零后处理（省事口径与任务判断一致）。

## 导出与对拍

导出脚本 `code/export_bge_onnx.py`（opset 17，动态 batch/seq，输入 input_ids/
attention_mask int64，输出 last_hidden_state + sentence_embedding 双头——后者为 Go
消费面，前者作对拍锚点）。对拍脚本 `code/parity_bge.py`；Go 侧 golden
`packages/go/memory/testdata/golden_bge.json`（16 条：门禁探针形中文/中英混排/标点/
数字/英文大小写/秘密 token 形）。

| 对拍面 | 结果 | 要求 |
|---|---|---|
| Python eager（导出包装器）vs Python ORT 1.19.2 | last_hidden_state max abs **3.8e-06**；sentence_embedding **2.1e-07** | <1e-4 ✅ |
| Go WordPiece vs HF BertTokenizer | 16/16 条 **token ID 全等** | 零容差 ✅ |
| Go 全链（分词→Go ORT 1.29）vs HF golden | embedding max abs **2.81e-07** | <1e-4 ✅ |

对拍面真实有效的注记：Go 全链首轮 max_abs=1.09e-01 **红**，根因是 Go 侧
attention_mask 误置全零（`make` 零值）——修复后 2.8e-07。该红证明了 golden 对拍
不是同义反复（分词与推理任一漂移即红）。

## 门禁接线（issue #134：能接真实 embedding 的断言从桩切真）

阈值零改动、既有断言零弱化；语义面为**新增**断言，模型/库未就位时 Skip
（基础设施 debt，惯例照 T3 `engineOrSkip`；CI 无模型环境门禁保持绿）。

| ID | 前 | 后 | 断言面 |
|---|---|---|---|
| T10-G1-01 | 关键词面 recall@5 三点（真实，不变） | + **语义面（真实）** | 200 探针+10 轮噪声，SearchByEmbedding recall@5=**1.0000**（200/200）≥ 0.95（同 yaml 阈值同查询口径） |
| T10-G1-04 | 关键词检索 P95≤150ms（真实，不变） | + **语义面（真实）** | 满载 400 节点库 200 探针全链（分词+推理+余弦）P95=**6.532ms** ≤ 150ms（同预算同样本口径） |
| G0-01/G0-02/G1-02/G1-03 | 真实 | 不变 | 隔离/删除/更新/容量断言面不涉嵌入检索路径（embeddings 通道删除清零已被 G0-02 覆盖） |

## 中文检索 sanity（10 条记忆+3 查询，`TestOnnxEmbedderChineseRetrievalSanity`）

- 「恐龙玩具」：`cos=0.7637`（孩子喜欢恐龙）＞ 0.3494（冰淇淋）＞ 0.2304（今天
  天气好）；恐龙双条目（喜欢恐龙/画恐龙）均进 top5——语义聚类而非字面碰撞。
- 「外婆叫什么名字」：top1=「外婆的名字叫桂花」（Python 对照 0.7304）。
- Go 与 Python 侧余弦逐位一致（小数点后 4 位）——全链数值可信。

## 延迟实测（CPU 4 核，IntraOp=2，24M 参数 fp32）

| 口径 | P50 | P95 | max | RTF P50（/150ms 预算） |
|---|---|---|---|---|
| 语义检索全链（满载库，n=200，门禁面） | 4.086ms | 6.532ms | — | **0.027** |
| 同口径复测（非门禁面） | 4.338ms | 8.004ms | 16.829ms | 0.029 |

端侧常驻可行（P50 ≈ 4ms，预算占用 ≈ 3%）。**延迟预算划拨说明**：`configs/budgets/latency.yaml`
零变更。该表为云档 L0 全链五段（tail_silence/asr_uplink/cloud_llm/tts_first/transport），
不含 T10 记忆段——记忆检索为本地计算、零上游调用（决策计数口径同 T10-G1-04），不占
云链路任何段；语义检索 P95 6.5ms 以远低于 T10 自身门禁线（memory_retrieval_p95_ms
≤150）的方式落在本地。若未来将检索嵌入显式入表，属组合级设计变更，走 founder PR 划拨。

## 遗留 / 后续

- T10-G1-01 的 50/200 轮语义降级面（时间衰减×语义检索交互）未设断言——当前
  SearchByEmbedding 评分不含时间衰减因子，语义面的老化探针衰减归 M3+ 观察项。
- 数据面 debt 不变（memory_probes 合成探针集已注册；种子家庭 4 周真实日志 holdout
  待采集，`reports/gates/T10.json` 台账口径不变）。
- CI（GitHub runner）无模型目录：语义面 Skip、关键词面照跑——门禁全绿面与
  main 一致；本机（模型在位）为全真实口径。
