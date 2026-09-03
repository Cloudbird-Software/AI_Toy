# AGENTS.md — TTS（T13）
验收协议：docs/gates/assets/T13.md（先读，BI 编号以它为准）　阈值：configs/gates/T13.yaml（禁改）
## 本包边界
角色声合成：合成文本+音色配置进 → 流式音频出（云主通道/端侧降级档/预合成缓存）。对接 T3（首包=接话延迟终点）、T5（SV 模型反验音色一致）、T9（缓存短语同样过安全）。
## 技术路径（指导，任选+可偏离，PR 记录选择）
A 云 TTS（自部署 CosyVoice（Apache-2.0，零样本克隆+流式，中文第一梯队）或商用 API；主对话通道）｜B 端侧 Piper（MIT，RPi 级 RTF≪1 离线零成本；降级档 L2/L3+高频短句）｜C 两级流式（口癖/问候/拟声预合成缓存端侧+长内容云端流式；默认架构，缓存短语同样过 T9）
## 本地命令
just gate T13 all ；go test ./packages/go/tts -run Property -count=1
## 本地必绿再提 PR
T13-G0-01 对抗注入读出率=0（100 对抗句）+常规坏输出≤1%（500 句人工听审）｜T13-G1-01 首包 云 P95≤300ms/端≤150ms、RTF≤0.5｜T13-G1-02 音色一致性（T5 SV 反验，端云切换无可感知变声）｜T13-G1-03 语义停顿错误≤5%
## 数据依赖
datasets/manifests/tts_synth.json（synthgen 注册：500 常规+100 对抗句）；金嗓 3 条锚定（创始人选定）；种子家庭儿童听感（合成语音词识别率 ≥自然人录音 −5pp，holdout 侧经 tools/holdout，本包代码不得直接读）
## 本包禁令（叠加根 AGENTS.md）
- 禁克隆任何真实儿童声音（角色声=合成音色或成人授权声），声纹用途台账季度审计
- 缓存短语同样过 T9（预合成不豁免安全审查）
## 常见坑
同文本+同种子音频哈希须一致、输出时长随文本长度单调增（突变=坏输出预警）；不可见控制字符不得影响可听输出；换声必须重过 rubric-13a（声音资产变更=角色资产变更）
## 实现状态（M1，IR #81）
路径选择：C 两级流式（spec §4 契约 C 原样落码；云/端引擎接口化注入，M1 零外部依赖）。已落地：Router 决策序（①PreSpeak fail-closed ②PhraseCache 零延迟直返 ③按档选通道 L0/L1 云/L2 端/L3 仅缓存 ④云首包超时降级）；静默占位≤SilenceCapMs→Edge 全新补偿重合成（不拼半句、不重播半句、每请求独立重试云）；Cancel 幂等+终止态固化；FirstPacketMs/DeadlineMs 预算只记不判（configs/budgets 消费归 M2 真机）。门禁接线：T13-G0-01 真实（111 对抗样本×4 档全拦截，读出=0 字节，云/端零调用）；T13-G1-01/G1-03 debt（需真实引擎计时/听审）；T13-G1-02 不可接线（yaml 未收录，#82 回填前不提交 reports/gates/T13.json——coverage 维持 DEBT 行不红）。未落地：真实引擎（CosyVoice/Piper 接入）、T5 SV 音色反验、tts_synth.json synthgen 注册（M2）。

## 实现状态增补（W3-T13，issue #132 / ADR-0008，2026-09-03）
真引擎接入（M1 桩之上，接口不变）：端侧=MeloSynthesizer（MeloTTS-Chinese ONNX 导出图，MeloSession/Phonemizer 注入；确定性噪声 seed+text+voice 派生；voice ID 参数化 ZH@rate=0.5..2.0，pitch 显式拒绝）；云端=IndexTTSClient（IndexTTS-1.5，POST→chunked PCM，wire 契约 ADR-0008）。中文前端=ChinesePhonemizer 查表法（gen_melo_phoneme_table.py 生成 26698 字表；多音字最常用读/无 sandhi/英文 UNK/数字逐位——局限显式非静默）。导出+对拍+RTF：tools/tts/export_melotts_onnx.py，报告 reports/eval/T13/（PyTorch vs ONNX SNR≥88dB r=1.0；ORT 字节级确定）。门禁状态不变：G0-01 真实 pass；G1-01/G1-03/G1-02 debt（真机 500 句计时/T5 SV 标定/听审面——本机 RTF 为桌面 CPU 参考值非门禁证据）。债务：onnxruntime Go 绑定（装配层，founder 批）、JaBert 韵律特征供给（恒零）、流式导出（整段出，首包=全段时长）、云服务端实服。

## 实现状态增补（M2-T14-TTS，issue #133 / ADR-0008 增补，2026-09-03）
债务①清偿：packages/go/tts/meloort 装配 melotts-zh.onnx 为 tts.MeloSession（yalue/onnxruntime_go + libonnxruntime 1.29.0，镜像 turntaking/vap 模式；tts 包本体保持零依赖；导出契约校验前置于进 ORT）。附带修正前端两处上游结构差：补 g2p 首尾边界符（token 数与 Python 同构）+ pad 位 lang_ids=0。证据（reports/eval/T13/README.md §8）：会话对拍同输入 SNR 95–105dB r=1.0 样本数逐一相等；符号一致率 1.000（声调分歧全落债务③变调类，92.7–94.6% 一致率）；Go 全链 RTF 三档×10 P50=0.791/P95=0.893（30/30<1，intra_op=2+nice19 开发机口径）——首包预算缺口 3.7×~25× 如实报，消解路径=流式导出/分句缓存（债务⑤）。门禁状态不变：G1-01 真机口径 debt 维持；债务② JaBert 供给仍开放。
