# AGENTS.md — 离线运行时（T14）
验收协议：docs/gates/assets/T14.md（先读，BI 编号以它为准）
阈值：configs/gates/T14.yaml（禁改）
## 本包边界
四档降级 FSM + 端侧能力边界声明：网络/电量/温度/错误信号 进 → 当前档位+能力白名单出，保证 L0⊇L1⊇L2⊇L3。对接 packages/native/edge-runtime（Rust 端侧）、所有资产（档位能力查询）。
## 技术路径（指导，任选+可偏离，PR 记录选择）
A llama.cpp/GGUF（默认） ｜B MLC-LLM（NPU优化部署） ｜C 功能收缩式离线+检索式
## 本地命令
just gate T14 all ；uv run pytest packages/py/runtime-fsm -m property
## 本地必绿再提 PR
T14-G0-01 编造≤5% ｜T14-G0-02 切换0脏输出/0记忆损失 ｜T14-G0-03 四档安全不降级 ｜T14-G1-01 离线旅程≥80% ｜T14-G1-02 功耗与热 ｜T14-G1-03 冷启动P95≤3s
## 数据依赖
200「端侧必不会」问题集；黄金旅程（T20 驱动）
## 本包禁令（叠加根 AGENTS.md）
FSM 表驱动穷举必做；TLA+ 触发条件见 docs/gates/assets/T14.md
## 常见坑
脏输出检测窗口期极短——切档过程中 T9 地板层必须同步切换，不得有缝隙
