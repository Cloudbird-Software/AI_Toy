# ADR-0007 T4 唤醒词训练管道与 LLM 接入面：开机即训准备
状态：accepted 2026-08-31（训练前置准备，规格=docs/runbooks/training-bootstrap.md）
背景：M3 收官后全部剩余 debt 为数据/模型/真机面；其中 T4 唤醒词自训（models/manifests/openwakeword.yaml 注释登记的量产路线）与真实 LLM 合成数据（synthgen 桩）是合成数据优化闭环的前两块短板。租用 GPU 按时计费，要求 PM agent 接手即训、零准备等待。
决策：
1) T4 训练走 openWakeWord 官方自动训练链（技术路径 A，docs/gates/assets/T4.md 默认起点）：piper-sample-generator 全合成正样本 + MIT RIR/audioset/FMA 增强 + ACAV100M 预计算负特征，经 openwakeword 包内 train.py 三步（generate/augment/train）产出 onnx；训练管道落在 training/t4/（config.yaml 钉全部参数），GPU 机初始化收敛到 scripts/gpu/bootstrap.sh 一键幂等脚本。
2) 负样本双轨制：训练负数据用 openWakeWord 公开预计算特征（ACAV100M 2000h）；本仓 synthgen 负批次（gen-tneg/gen-kwsadv）维持 eval-only（T2-G0-01 红线，永入训练管道）——两者物理隔离，门禁口径不混。
3) LLM 接入面新增 tools/llmclient（纯标准库 OpenAI 兼容客户端，零新 Go 依赖）：synthgen 新增 generate-llm 子命令（溯源戳记录真实上游模型、按样本轮转模型池以满足单源 ≤30% 门禁）；toyjudge 新增 LLM_JUDGE=1 注入的 LLM 评审后端（pairwise+swap 协议与 judge 身份锁定不变）。BAML-1 重依赖路线（baml_client_go）继续搁置——llmclient 覆盖同等契约（锁定模型+哈希入报告）且无编译器引入。
4) Python 依赖 license 台账落 training/licenses.yaml（mutagen GPL-2.0 为 openwakeword 传递依赖，工具性使用）；入口协议补 Makefile（card-test/gates-pr，包装 quality/run-gates.sh 与资产包测试）；edge-runtime 补最小 crate 解除 cargo fetch 阻塞。
备选否决：直接上 microWakeWord（路径 B，ESP32 量产主路线）——需真实童声采集且 INT8 量化链重，不适合作为合成数据闭环首发；本地微调 LLM（人格蒸馏）——API 已具备，GPU 预算应集中给 T4。
后果：GPU 4060 级即可承载全部训练（openwakeword 模型 ~MB 级，瓶颈在 TTS 合成与增强吞吐，拉时长换精度成立）；5090 无必要。训练产物经 evaluate.py 预检 → register_model.py 登记 → Go 侧门禁（T4-G0-01/02 负批次、G1-01 唤醒率）复测后换装。中文唤醒词需换 TTS 音源（openwakeword 链仅英文），列 founder 决策项。
