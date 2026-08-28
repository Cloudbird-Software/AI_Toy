# AGENTS.md — 端侧运行时 edge-runtime（Rust，T14）
验收协议：docs/gates/assets/T14.md　阈值：configs/gates/T14.yaml（禁改）
## 本包边界
T14 端侧引擎：四档降级 FSM + GGUF 模型推理宿主 + 崩溃安全(panic=catch→降档不重启) + 内存预算。对接 Python 侧 runtime-fsm（FSM 表同源）、models/manifests（权重 sha256 校验）。
## 技术路径（指导）
A llama.cpp/GGUF（Q4_K_M 1–3B，默认）｜B MLC-LLM（SoC/NPU 编译优化，量产并行路线）｜C 功能收缩式离线（检索式+剧本，L3 深降级）
## 本地命令
cargo clippy --package edge-runtime -- -D warnings；cargo nextest run -p edge-runtime；just gate T14 all（组合级）
## 本地必绿再提 PR
T14-G0-01 编造 ≤5%｜T14-G0-02 切换 0 脏输出/0 记忆损失｜T14-G0-03 四档安全不降级｜T14-G1-03 冷启动 P95≤3s
## 数据依赖
models/manifests 权重校验和（repoctl fetch-models 拉取，权重不入 git）；synth 200 端侧必不会题集
## 本包禁令（叠加根 AGENTS.md）
- unsafe 已 workspace 级 forbid；FSM 表驱动穷举必做（表×事件全可达）
- 量化模型同帧同判定（确定性属性），任何优化不得回退
## 常见坑
panic 捕获后必须先关输出再降档；FSM 表与 runtime-fsm 同源双写，CI 校验哈希一致。
