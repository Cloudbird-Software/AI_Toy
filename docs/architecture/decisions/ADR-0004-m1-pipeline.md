# ADR-0004 M1 演示闭环：纯 Go 事件管道 + 接口化推理 + 零三方依赖
状态：accepted 2026-08-29（IR #78，规格=docs/m1-spec.md）
背景：M1 要交付 L1 演示闭环三包（kws/turntaking/tts，实现卡 #79/#80/#81），但 ONNX 推理引擎、音频设备 IO、云 TTS 客户端与真实数据集均未就绪（数据面依赖 synthgen 注册与 ≥6h 音景库，归 M2）。
决策：三包以纯 Go 事件管道落地——kws.Push/FSM.OnVAD/Router.Synthesize 皆为同步纯逻辑，推理/IO/网络全部收敛为包内窄接口（Inferencer/Synthesizer/PhraseCache/PreSpeakFunc），M1 一律注入桩实现（能量启发式/内存缓存/脚本化流），真引擎 M2 只换注入不改结构；T14 档位以本地镜像接口（TierBudget/TierPolicy/TierCaps）预留，语义对齐 tests/properties/contract.go 的 RuntimeModel.TierCaps 但不 import tests/（防「对着考卷优化」）；go.mod 零新增，import 白名单=标准库（测试侧另许 tools/gaterunner）；三包互不 import（Event/VADEvent/Request 平凡类型由驱动层搬运，M1 驱动=测试回放器）。
备选否决：直接绑 onnxruntime 绑定（无 license 台账+目标硬件未定，违反依赖纪律）；三包共享事件总线（M1 无跨包需求，耦合先于需求）；import tests/ 复用 contract.go（违反考卷隔离铁律）。
后果：门禁数据面全 debt（t.Skipf 写明数据依赖，ADR-0002 通道）但逻辑面真实可测（打断 50/50、拦截读出=0 等）；桩无唤醒/延迟语义，RTF·首包 P95 等实测门禁归 M2 真机；接口即 seam，真模型接入后同组属性测试重跑即可（T4 属性接口级承诺）。
