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

## 增补（M2 装配层落地，issue #133，2026-09-03）

债务①清偿：新增 `packages/go/tts/meloort`（镜像 T3 `turntaking/vap` 模式——核心包
tts 保持零外部依赖，装配子包实现 `tts.MeloSession` 并以编译期断言锁定接口；绑定
yalue/onnxruntime_go + libonnxruntime 1.29.0，进程内全局初始化一次，intra_op=2 与
T3/T14 口径一致）。与 vap 一处差异：meloort 直接 import tts（MeloIO 是 5 张量+4
标量的富契约，镜像必漂移；依赖箭头由装配层指向核心，核心零依赖不变——vap 的
零 import 是为平凡类型镜像，此处不适用）。导出契约校验前置于进 ORT（z_noise 8T
预留等，错误形状显式拒绝）。

附带修正两处 M1 前端与上游结构差（对拍暴露，`melophone.go`）：① 补
chinese_mix.g2p 的 `["_"]+phones+["_"]` 首尾边界符（token 数恢复同构 37/55/65）；
② intersperse pad 位 lang_ids 0（M1 误填 3）。修正后符号面与 Python 逐位一致
（1.000），声调分歧 92.7–94.6% 一致率全落债务③变调类，维持不动。

证据：会话对拍 Go ORT vs Python ORT 同输入 SNR 95–105dB r=1.0、样本数逐一相等
（reports/eval/T13/README.md §8）；Go 全链 RTF 三档×10 P50=0.791 / P95=0.893
（30/30<1，intra_op=2+nice19 开发机口径）——首包预算缺口 3.7×~25× 如实报
（整段出语义，缺口消解=债务⑤流式/分句，装配层不可解）；RTF≤0.5 门禁线仍归
T13-G1-01 真机口径（debt 维持）。fixtures 经生成脚本入库（前端张量+噪声+参考
波形 ≈1.9MB，模型权重不入 git 红线不变）。债务②（JaBert 供给）仍开放。
