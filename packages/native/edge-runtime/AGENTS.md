# AGENTS.md — 端侧运行时（T14 端侧，Rust）
验收协议：docs/gates/assets/T14.md（先读，BI 编号以它为准；与 packages/go/runtime-fsm 共担 T14）
阈值：configs/gates/T14.yaml（禁改）
## 本包边界
Rust 端侧推理与降级执行体：档位指令/云端不可用信号进 → 端侧模型输出与四档降级执行出（panic=catch→降档不重启）。与 packages/go/runtime-fsm 共担 T14（Go 侧 FSM/断言）；对接 T9（任何档位地板层永远在）、T15（路由决策的端侧承接）、T4/T5/T13（端侧共存内存预算，见 T14-G1-03）。
## 技术路径（指导，任选+可偏离，PR 记录选择）
A llama.cpp/GGUF（MIT，Q4_K_M 1–3B 在 RPi5/RK3588 可用，默认）｜B MLC-LLM（Apache-2.0，SoC/NPU 编译优化；与 A 共存：A 开发调试、B 部署）｜C 功能收缩式离线（离线剧本+检索式+能力边界声明，作 L2/L3 深降级而非替代品）
## 本地命令
cargo nextest run ；cargo clippy --workspace -- -D warnings ；just gate T14 all
## 本地必绿再提 PR
T14-G0-01 降级诚实性（编造≤5%，n=200）｜T14-G0-02 切换安全（0 脏输出/0 记忆损失，n=200）｜T14-G0-03 降级档安全不降级（全安全集×4 档）｜T14-G1-01 端侧可用性（黄金旅程≥80%）｜T14-G1-02 功耗与热（热节流后 token/s≥70%）｜T14-G1-03 内存与冷启动（P95≤3s）｜崩溃安全属性：panic=catch→降档不重启
## 数据依赖
models/manifests 权重清单+sha256 校验和（权重不入 git，`just fetch-models` 按清单拉取校验）；200「端侧必不会」问题集；黄金旅程（tests/golden-journeys，T20 驱动）；真机 holdout（3 台×72h 真实家庭网络）一律经 tools/holdout，本包代码不得直接读
## 本包禁令（叠加根 AGENTS.md）
- unsafe 已 forbid（Cargo.toml workspace lint），不得局部解禁
- 量化模型同帧同判定（确定性属性）不得因性能优化回退
- 崩溃处理只许降档，不得以重启进程代替降级
## 常见坑
线程数/批大小/量化参数差异会让同帧产生不同判定——确定性属性测试先行再调优；切档窗口期的半句输出（脏输出）必须在事务边界上断言（T14-G0-02）；内存峰值按 T4/T5/T13 端侧共存口径算，不是端侧模型独占
