# ADR-0008 T13 端云双档 TTS：MeloTTS（MIT）端侧 + IndexTTS（Apache-2.0）云端
状态：accepted 2026-09-03（issue #132 / W3-T13，红线=禁克隆真实儿童声音，AGENTS.md G0）
背景：T13 此前无真实合成引擎（M1 桩，ADR-0004 接口化注入位）。商用合规中文 TTS 三选项中 MeloTTS-Chinese（MIT，CPU 实时）许可最干净、IndexTTS-1.5（Apache-2.0，零样本音色）质量最高；F5-TTS 基座因 Emilia CC BY-NC 排除（issue #132）。
决策：
1. **端侧离线档=MeloTTS-Chinese**：官方 checkpoint 导出单 ONNX 图（音素序列+噪声张量→44.1kHz 波形，opset 17；采样噪声显式输入化——sdp 噪声 [2×T] 直入，z 噪声按 mel 长度数据相关、以 8×T 时间冗余张量图内动态切片——确定性 P1 由 Go 侧 splitmix64+Box-Muller 从 (seed,text,voice) 派生，同输入同音频）。Go 侧 `MeloSynthesizer` 实现 `Synthesizer`，会话/前端接口化注入（`MeloSession`/`Phonemizer`，镜像 ADR-0004 kws 模式，包本体零外部依赖）；中文前端为查表法 `ChinesePhonemizer`（pypinyin 最常用读音 × opencpop-strict，26698 字生成表）。
2. **云端高质量档=IndexTTS-1.5 客户端**：`IndexTTSClient` 实现 `Synthesizer`（POST JSON→chunked PCM s16le 流），wire 契约归自部署服务（服务端不在本仓交付）；voice 仅透传官方音色 ID，服务端白名单裁决——客户端不提供克隆引用面。
3. **路由沿用 tts.Router 既有决策序不变**（ADR-0004/m1-spec §4 契约 C）：L0/L1=IndexTTS 云优先，首包超时→静默占位≤2s→MeloTTS 端侧全新补偿重合成；L2=MeloTTS 直走；L3=仅预合成缓存（短语同样过 T9）。T15 路由缓存联动：云返回结果可入 PhraseCache 复用（离线期高频短语零延迟）。
4. **儿童音色合规过渡**：官方音色 ZH + voice ID 参数化语法 `ZH@rate=<0.5..2.0>`（语速经 length_scale 原生进图）；pitch 参数显式拒绝（端侧 DSP 面未落地，静默忽略=不诚实）。拟真儿童音色训练挂 Research-Only 线（仅合成音色），禁克隆真实儿童录音。
5. **档间音色一致性（T13-G1-02 占位门禁）**：端（MeloTTS ZH）与云（IndexTTS 官方音色）是两个模型的音色，天然有差——选听感最近官方音色对 + T5 SV 嵌入反验标定后才可判定「无可感知变声」；标定前 G1-02 维持 debt verdict，不提交 reports/gates/T13.json。
备选否决：CosyVoice 云档（Apache 2.0 合规，留作 IndexTTS 服务化受阻时的备选，接口不变）；端侧跑 IndexTTS（gpt.pth 1.1GB 超端侧档内存预算）；Go 侧内嵌 onnxruntime 绑定（新 cgo 依赖须 founder 批+CI 基建，M2 装配层再议——本包接口化不阻塞）；中文前端移植完整 pypinyin+jieba+tone sandhi（jieba 词表/sandhi 规则量大且 licenses 面待理清，查表法先闭环端到端路径，局限显式声明：多音字取最常用读、无变调、英文 UNK、数字逐位读）。
后果：T13 具备真实端云双档引擎接入面与确定性合成路径；债务显式入表——① onnxruntime Go 绑定（装配层，M2+founder 批）；② JaBert 韵律特征供给（恒零=韵律降质可听）；③ 中文前端 sandhi/多音字/位级数字；④ 云端服务端部署与 wire 契约实服验证；⑤ 流式导出（当前整段出，首包=全段推理时长，T13-G1-01 端侧≤150ms 需流式/分句达成）；⑥ T13-G1-02 端云音色 SV 标定。L4 合成自然度评审经 tools/judge（锁定模型+pairwise+swap），κ≥0.61 前不自动化。
