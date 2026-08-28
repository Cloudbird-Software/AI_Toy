// T14 云侧编排：档位控制 / 多资产会话管道 / 路由分发入口
// 实现由开发 agent 完成；骨架仅占位出口。
export type Tier = "L0" | "L1" | "L2" | "L3";
export interface SessionContext {
  sessionId: string;
  tier: Tier;
  userId?: string;
}
export function createSession(_ctx: SessionContext): SessionContext {
  throw new Error("cloud-orchestrator: not implemented");
}
