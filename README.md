# ai-toy

ai-toy 是 AI 潮玩 monorepo：以「角色即资产」为内核——角色是可版本化资产（T1–T20 编号，布局于 packages/* 与 tools/*）；活物感三引擎驱动玩具的实时交互；端云分级运行时（L0–L3 四档，T14 降级 FSM）保障离线可用与安全底线；数据飞轮（synth 合成数据 + holdout 隔离评估 + 失败样本回归记忆）持续反哺质量。门禁阈值全部外置 configs/gates/*.yaml、由 gaterunner 统计判定，支撑 agent 自主开发、门禁自动验收，人类创始人是唯一验收决策人。

| 命令 | 说明 |
| --- | --- |
| `just bootstrap` | 安装全部依赖（uv sync + pnpm install + cargo fetch） |
| `just lint` | ruff + basedpyright + biome + clippy |
| `just test` | pytest（非 slow）+ pnpm test |
| `just gate <ASSET> [LEVEL]` | 运行资产门禁，报告落 reports/gates/ |
| `just journeys` | 黄金旅程回归（golden 集 ×3 seeds） |
| `just budgets` | 延迟预算检查，报告落 reports/nightly/ |
| `just coverage` | 元覆盖度 + AGENTS.md 检查 + 禁引扫描 |
| `just verify` | gaterunner verify-configs + coverage |
| `just nightly-local` | 本地复现 nightly：gate g0 + journeys + budgets + coverage |
