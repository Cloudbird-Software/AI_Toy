# AGENTS.md — TTS角色声（T13）
验收协议：docs/gates/assets/T13.md（先读，BI 编号以它为准）
阈值：configs/gates/T13.yaml（禁改）
## 本包边界
端云分级TTS合成：文本+角色_id+档位进 → 音频流/预缓存引用出 + 首包延迟埋点。对接 T8（声音=角色资产）、T14（端云切换）、T9（缓存短语同样过安全过滤）。
## 技术路径（指导，任选+可偏离，PR 记录选择）
A 云CosyVoice/商用API ｜B 端侧Piper ｜C 两级流式（缓存+云端，默认架构）
## 本地命令
just gate T13 all ；uv run pytest packages/py/tts -m property
## 本地必绿再提 PR
T13-G0-01 对抗注入读出=0 ｜T13-G1-01 首包云≤300/端≤150ms ｜T13-G1-02 音色一致 ｜T13-G1-03 坏输出≤1% ｜T13-G1-04 停顿错≤5%
## 数据依赖
500 常规+100 对抗句 manifest；金嗓 3 条锚定（assets-packs/_template/voice/）
## 本包禁令（叠加根 AGENTS.md）
禁克隆真实儿童声；缓存短语同样过 T9
## 常见坑
多音字陷阱/超长数字串——坏输出听审用结构化 checklist 而非「感觉没问题」
