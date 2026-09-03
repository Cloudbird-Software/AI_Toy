# W2-T7 架构决策记录（ADR）

issue #130。记录双通道设计、SenseVoice 选型、儿童域差处理与 #136 研究线衔接的决策。

## ADR-001：双通道架构（语音 + 文本）

**状态**：已采纳

**背景**：T7 状态机需要 9 类情绪标签（sad / scared / angry / sleepy / calm / surprised / content / happy / excited），单一模态难以覆盖儿童陪伴场景的噪声、方言、口吃、隐喻与上下文依赖。

**决策**：
- **语音通道**：以 SenseVoice-Small 为基座，zero-shot / linear-probe 输出 4 类粗粒度情绪，映射到 T7 4 类（happy / sad / angry / calm）。负责"声学真值"与低延迟唤醒检测。
- **文本通道**：以 GoEmotions 27+1 类为监督源，TF-IDF + LogisticRegression 输出 T7 9 类概率分布。负责细粒度语义映射与多标签归一化（负性优先 tie-break）。
- **融合规则**（状态机侧）：语音通道提供粗粒度置信度与唤醒先验；文本通道提供细粒度候选；状态机按 V×A 带约束、交互史、时间上下文做最终裁决。

**理由**：
1. 域互补：英文 CREMA-D 对 SenseVoice 零样本几乎失效（raw 92.5% EMO_UNKNOWN），但线性头微调后可恢复区分度；中文童声零样本可用，但缺乏细粒度（无 excited/scared）。
2. 模态互补：儿童口语常伴随含糊发音、拟声词、省略，文本通道可从 ASR 文本补捉语义意图；反之，哭喊/笑声等非语言 vocalization 文本通道失效，需语音兜底。
3. 许可与成本：SenseVoice 与 GoEmotions 均为商用友好许可；线性头与 TF-IDF 推理成本低，CPU 可部署。

**后果**：
- 双通道维护成本：需同步两套特征提取管线与映射表。
- 标签空间不一致：语音通道 4+1 类 vs T7 9 类，sleepy / excited / scared / surprised / content 等需文本通道或状态机补位。
- 线性头微调仅覆盖 4 类（happy/sad/angry/calm），其余 5 类在语音通道退化为 calm 或低置信度。

## ADR-002：SenseVoice-Small 零训练选型

**状态**：已采纳

**背景**：语音情绪识别基线可选 emotion2vec（ModelScope）、RAVDESS/TESS/CaFE 微调、或 SenseVoice 零样本。

**决策**：采用 SenseVoice-Small 发布权重，不额外训练 SER 头；后续仅在 CREMA-D+JL 上训练 512→4 线性头。

**理由**：
1. **许可阻断**：emotion2vec 为 CC BY-NC-SA（ModelScope 页标 NC），不可商用；RAVDESS / TESS / CaFE 为 NC/ND，同样排除。
2. **零样本结论已足够支撑决策**：CREMA-D raw 口径全出 EMO_UNKNOWN，坐实"英文域零样本情绪失效"，无需更多基线对比；若继续换模型，只会在同一许可困境中打转。
3. **工程效率**：SenseVoice 已有 FunASR 推理栈，可一次扫描产出 raw/forced 情绪 + 9 token logits + event + 512d embedding，与线性头训练管线复用同一批特征，无需额外数据下载与预处理。
4. **性能可接受**：forced 4 类 macro recall 0.738（NEU 0.742 / HAP 0.545 / SAD 0.907 / ANG 0.758），线性头可进一步提升。

**后果**：
- 语音通道上限受限于 SenseVoice 4+1 类输出，T7 标签带 9 类中仅 4 类可直接来自语音。
- DIS/FEA 无发布权重对应类，仅能通过 runner-up rank 分析或线性头隐式学习。
- 若未来更换基座模型（如 future 版本 SenseVoice 扩展情绪类），需重新评估映射与线性头。

## ADR-003：儿童域差局限与 #136 研究线

**状态**：已采纳（数据定性分析，不做根本改进）

**背景**：儿童域差数据（t5_sv / t4_kws / t7_audio）显示童声合成语音显著偏向高唤醒强情绪（child strong rate 86-95% vs adult 39-50%），但 TTS 合成并非真实儿童语音，分布偏移不等于真实儿童域误差。

**决策**：
- 儿童域差评估仅作为**定性数据**挂接 #136 研究线，不直接用于修正当前 T7 状态机参数或训练集重采样。
- 当前生产语音通道使用 CREMA-D（成人演员）+ JL-Corpus（成人说话人）线性头，对童声的泛化依赖合成数据近似，不做正式域适应声明。
- #136 研究线目标：接入 ChildEC 等真实儿童情绪对话数据，评估真实域差并设计域适应方案。

**理由**：
1. **合成语音≠真实儿童**：TTS 合成数据（stepaudio-2.5-tts）在音色、韵律、噪声环境上均与真实儿童麦克风采集存在系统性差异，直接用合成数据的 strong rate 差值校正生产模型风险不可控。
2. **许可边界**：ChildEC 为 CC BY-NC，仅能做 Research-Only 基线对比，不可进入商用库。当前工作区将 ChildEC 列入排除清单，防止后续任务误引入。
3. **问题定义未收敛**：儿童域差具体体现为"音调偏高→误判为 excited"还是"发音含糊→EMO_UNKNOWN 增多"，需真实数据上的消融实验才能回答。合成数据只能证明"风格/唤醒对 TTS 分布有影响"，不能证明"真实儿童误差主要来自域差"。

**后果**：
- 当前 T7 引擎在真实童声上的性能未经验证，产品文档须披露"主要基于成人数据训练，童声适用性未充分验证"。
- #136 需独立立项：ChildEC 数据接入 → 真实童声 test set 构建 → 域适应实验（对抗训练 / 风格迁移 / 儿童特定合成增强）。
- 若 #136 证明域差显著，需回滚修改 T7 融合策略或增加儿童专用情绪先验。
