// Founder Console：门禁报表 / 豁免台账 / holdout 审计 / D1–D6 视图
// 仅骨架，实现由开发 agent 完成。
export interface GateRow {
  id: string;
  level: "G0" | "G1" | "G2";
  verdict: "pass" | "fail" | "warn";
  asset: string;
}
export function loadDashboard(_: unknown): GateRow[] {
  throw new Error("founder-console: not implemented");
}
