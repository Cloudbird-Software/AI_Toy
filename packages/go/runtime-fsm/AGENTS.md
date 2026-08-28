# AGENTS.md — 离线运行时（T14，go 侧）
验收协议：docs/gates/assets/T14.md（先读，BI 编号以它为准）　阈值：configs/gates/T14.yaml（禁改）
## 本包边界
四档降级 FSM 的 go 侧：网络/电量/温度事件+用户请求进 → 档位决策（L0–L3）与离线能力上界出。对接 packages/native/edge-runtime（同卡端侧）、T9（每档绑定安全配置）、T15（路由能力上界）。
## 技术路径（指导，任选+可偏离，PR 记录选择）
A llama.cpp/GGUF（MIT，Q4_K_M 1–3B 在 RPi5/RK3588 可用，生态最全，默认）｜B MLC-LLM（Apache-2.0，SoC/NPU 编译优化，与 A 共存：A 开发调试 B 部署）｜C 功能收缩式离线（离线剧本+检索式+能力边界声明；零幻觉低功耗，作 L2/L3 深降级而非替代品）
## 本地命令
just gate T14 all ；go test ./packages/go/runtime-fsm -run Property -count=1
## 本地必绿再提 PR
T14-G0-01 降级诚实性 编造≤5%（200 个「端侧必不会」问题）｜T14-G0-02 切换安全 0 脏输出/0 记忆写损失（对话中强制切档×200）｜T14-G0-03 四档安全不降级（同 T9-G0-07，任一档违规=G0）｜T14-G1-01 黄金旅程离线完成≥80%｜T14-G1-02 功耗与热（热节流后 token/s≥标称 70% 且不安全关机）｜T14-G1-03 冷启动 P95≤3s、峰值内存≤预算
## 数据依赖
200「端侧必不会」问题集+黄金旅程（datasets/manifests/runtime_synth.json，synthgen 注册；旅程由 T20 驱动）；真机 3 台×72h 真实家庭网络档位轨迹日志（holdout，只进不出，经 tools/holdout，本包代码不得直接读）
## 本包禁令（叠加根 AGENTS.md）
- FSM 表驱动穷举必做：四档×全事件（网络通断/超时/电量/温度/用户操作）全表断言后继合法、无死锁、L0 可达、每档绑定正确安全配置
- 降档永不放大能力边界（当前档功能上界 ⊆ 该档安全配置；能力单调嵌套 L0⊇L1⊇L2⊇L3）
## 常见坑
TLA+ 触发条件见 docs/gates/assets/T14.md（表驱动发现过一次竞态 bug，或降级逻辑连续两次引入回归才升级）；网络恢复须有限时间回 L0（活性），别把恢复写成一次性事件
