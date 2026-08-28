# AGENTS.md — 云侧编排器（T14 云档管道）
验收协议：docs/gates/assets/T14.md　阈值：configs/gates/T14.yaml（禁改）
## 本包边界
云侧会话管道与档位分发：会话创建/路由/降级切换信号 → 通知端侧 T14。对接 T15（路由）、T9（安全地板）、T10（记忆绑定上下文）。
## 技术路径（指导，可偏离）
A 纯 TypeScript 状态机（xstate 或手写表驱动）｜B 复用 runtime-fsm 相同 FSM 表 TS 镜像｜C 轻量 API 网关 + 事件流（Kafka/NATS）
## 本地命令
pnpm --filter @ai-toy/cloud-orchestrator lint ；pnpm --filter @ai-toy/cloud-orchestrator test ；配合 T14 组合门禁用 just gate T14 all
## 本地必绿再提 PR
T14-G0-02 切换安全(0 脏输出/0 记忆损失)｜T14-G0-03 降级档安全不降级｜T14-G1-01 端侧可用性 ≥80% 旅程
## 数据依赖
档位配置从 configs/runtime/tiers.yaml 只读；会话事件写入 reports/ 仅审计；任何训练路径禁止引用 holdout。
## 本包禁令（叠加根 AGENTS.md）
- 档位切换过程中不得输出半句话；未过滤输出由 T9 兜底但本包必须先验 0 脏
- 任何时刻不可越过 L0⊇L1⊇L2⊇L3 单调嵌套（CI-1 不变量）
## 常见坑
降级信号下发顺序（先 T9→T13→T15，最后 LLM）；网络抖动时切换幂等重试不能回脏态。
