# AGENTS.md — 话轮管理（T3）
验收协议：docs/gates/assets/T3.md（先读，BI 编号以它为准）
阈值：configs/gates/T3.yaml（禁改）
## 本包边界
端侧+云端话轮终点判定与打断响应：音频特征/ASR中间态进 → 接话信号/停止TTS指令出 + 话轮元数据。对接 T4（唤醒进入话轮）、T13（被打断时停TTS）、T14（档位影响静音门限）。
## 技术路径（指导，任选+可偏离，PR 记录选择）
A Silero VAD+自适应静音 ｜B VAP学习型话轮终点预测 ｜C 混合=VAD保底+预测提前量（默认）
## 本地命令
just gate T3 all ；uv run pytest packages/py/turntaking -m property
## 本地必绿再提 PR
T3-G0-01 打断检出≥95%/≤300ms ｜T3-G0-02 负样本≥6h零触发 ｜T3-G1-01 误截断≤8% ｜T3-G1-02 接话P95≤900ms ｜T3-G1-03 打断后上下文保持≥90% ｜T3-G1-04 中停顿容忍
## 数据依赖
儿童测试集合成（TTS+停顿注入）manifest；负样本音景库（与 T4 共用）；真实儿童录音 holdout 经 tools/holdout
## 本包禁令（叠加根 AGENTS.md）
阈值不得对长停顿单独放宽；VAD 判定不得永久挂起（属性强制）
## 常见坑
儿童基频高、停顿长——静音门限按年龄自适应；「零事件」按 3/N 规则算时长
