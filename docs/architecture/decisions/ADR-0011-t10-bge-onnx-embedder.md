# ADR-0011 T10 M2 嵌入真推理：bge-small-zh-v1.5 ONNX + CLS 池化入图 + 纯 Go WordPiece
状态：accepted 2026-09-03（issue #134 收口，W4 M2 线）
背景：T10/T11 向量底座 M1 为确定性哈希桩（StubEmbedder，384 维注释实为笔误），语义检索无真实语义。bge-small-zh-v1.5 权重已落盘（MIT，24M 参数，中文），仓内已有 ORT Go 推理栈先例（ADR-0007，yalue/onnxruntime_go v1.35 + libonnxruntime.so.1.29.0）。需决策：导出口径、池化位置、分词器实现（Go 生态无轻量 BERT WordPiece 可用）、门禁接线方式。
决策：
1. **模型沿用 bge-small-zh-v1.5（MIT）**：任务卡指定，license 干净，24M 参数 CPU 实测 P50 ≈ 4ms/查询（远快于实时）。自导出 ONNX 为正式推理面（manifest 条目 `bge-small-zh-v1.5-onnx`，sha256 325654ed…），safetensors 原权重条目保留作导出母本。
2. **池化口径以模型随附配置为准：CLS 池化 + L2 归一化，固化入图**（`1_Pooling/config.json` cls_token=true——BGE 官方用法）。任务卡猜测的 mean pooling 不采用。图内池化输出 sentence_embedding [B,512]：Go 侧零后处理、无漂移面；同时导出 last_hidden_state 作对拍锚点（Go 会话只请求 sentence_embedding，未请求输出被 ORT 剪枝，零运行时成本）。**勘误 M1 注释：维度 512 非 384（384 系 bge-small-en 笔误）**。
3. **导出 opset 17 / fp32 / 动态 batch+seq**：fp32 91MB 对 L2 CPU 档可接受；int8 量化留作后续（体积收益 ~4×，但引入精度重对拍义务，M2 不做）。双输出导出+对拍三面（Python eager vs Python ORT 3.8e-06/2.1e-07；Go 全链 vs HF golden 2.8e-07，<1e-4 口径）。
4. **分词器纯 Go 自实现（零新依赖）**：BERT BasicTokenizer+WordPiece 管线面小（clean→CJK 加空格→标点切分→贪心最长匹配），行为由 HF BertTokenizer 生成的 golden 逐条锁死（16 条 token ID 全等）；配置以模型随附 tokenizer_config.json 为准（do_lower_case=false、strip_accents=false、CJK 切分、max_len 512）。**否决 Go 分词库路线**（sugarme/tokenizer 等引入 cgo/重依赖树，license 台账与审计面膨胀，收益仅为省 ~150 行自实现代码）。已知边界：HF `_is_control` 的 Cn 类（未赋值码位）未覆盖（unicode.Cn 表需 go1.25，模块锁 go1.23）——不在儿童记忆文本面内，golden 覆盖真实字符串面。
5. **门禁接线=新增语义面，不动阈值不弱化既有断言**：T10-G1-01/G1-04 各增 t.Run 语义子面（真实 embedding，同 yaml 阈值/预算同查询口径），模型/库未就位 Skip（基础设施 debt，惯例照 T3 engineOrSkip）——CI 无模型环境关键词面照跑保持绿，本机全真实口径。隔离/删除/更新/容量四门不改（断言面不涉嵌入检索路径）。语义面评分当前不含时间衰减因子，50/200 轮语义降级面留 M3+ 观察项。
6. **StubEmbedder 保留作 fallback**（注释更新为模型缺失 fallback 口径，废弃说法撤回）：构造期失败由调用方降级，回放/无模型环境可用；不用于语义检索口径断言。
备选否决：mean pooling 入图（与模型官方用法不符）；Go 侧后处理池化（多一段 Go/图两处实现的漂移面）；HF optimum 导出工具链（本机 optimum 未装，torch.onnx 直接导出等价且少一层封装）；quantized int8（重对拍义务，推迟）。
后果：T10 语义检索从桩到真（语义面 recall@5=1.0、P95 6.5ms 实测）；license 台账零新增条目（bge 权重条目已在案，补 ONNX 派生注记）；go.mod 零新增依赖（yalue 绑定由 indirect 转 direct）；reports/eval/T10/ 存档对拍/sanity/延迟；包 AGENTS.md 实现状态刷新。对拍曾抓出 Go 侧 attention_mask 全零真 bug（1.09e-01 红→修复 2.8e-07），证明 golden 对拍面有效。
