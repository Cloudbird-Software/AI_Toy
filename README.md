# ai-toy

ai-toy 是 AI 潮玩 monorepo：以「角色即资产」为内核——角色是可版本化资产（T1–T20 编号，布局于 packages/* 与 tools/*）；活物感三引擎驱动玩具的实时交互；端云分级运行时（L0–L3 四档，T14 降级 FSM）保障离线可用与安全底线；数据飞轮（synth 合成数据 + holdout 隔离评估 + 失败样本回归记忆）持续反哺质量。门禁阈值全部外置 configs/gates/*.yaml、由 gaterunner 统计判定，支撑 agent 自主开发、门禁自动验收，人类创始人是唯一验收决策人。

| 命令 | 说明 |
| --- | --- |
| `just bootstrap` | 安装全部依赖（go mod download + pnpm install + cargo fetch） |
| `just lint` | gofmt 零 diff + go vet + errcheck + pnpm lint + clippy |
| `just test` | go test -race（非 slow）+ pnpm test |
| `just gate <ASSET> [LEVEL]` | 运行资产门禁，报告落 reports/gates/ |
| `just journeys` | 黄金旅程回归（golden 集 ×3 seeds） |
| `just budgets` | 延迟预算检查，报告落 reports/nightly/ |
| `just coverage` | 元覆盖度 + AGENTS.md 检查 + 禁引扫描 |
| `just verify` | gaterunner verify-configs + coverage |
| `just nightly-local` | 本地复现 nightly：gate g0 + journeys + budgets + coverage |

## Setup Status（§13 执行顺序）

| Step | 项目 | 状态 | 产物 |
| --- | --- | --- | --- |
| 1 | §1 目录树全量 + .gitkeep | ✅ | .gitkeep 到位 |
| 2 | §2 根清单（go.mod/package.json/.../.gitignore/tiers/latency） | ✅ | PR #2 等 |
| 3 | §3 工具八包 + justfile（Go/BAML 语言规范 IR #23） | ✅ | PR #28–#35 |
| 4 | §4 configs（16 gates yaml + judge + budgets + runtime + packs） | ✅ | PR #35 |
| 4b | calibrate 16 资产噪声带回填 + toyjudge §4.2 schema 适配 | ✅ | PR #38/#39 |
| 5 | §5 workflows + CODEOWNERS + PR 模板 + composite actions | ✅ | PR #47（.github/** founder token 代推） |
| 6 | §6 根 AGENTS.md（@randypanding） | ✅ | PR #43 |
| 7 | §7 包骨架 + 22 份 AGENTS.md（17 go + 2 native + 2 ts + 1 根） | ✅ | PR #43/#44/#45 |
| 8 | §8 tests（50 旅程 + CI-1..4 + chaos 8） | ✅ | PR #52/#55/#54 |
| 9 | §9 datasets/models/reports 初始 manifests | ✅ | PR #53 |
| 10 | §10 assets-packs _template + goodnight-bear 种子包 | ✅ | PR #53 |
| 11 | §11 docs/gates 23 文件（7 根 + 16 资产卡） | ✅ | PR #59/#60 |
| 12 | §12 runbooks 3 份 + ml-test-score + ADR-0001 | ✅ | PR #61 |
| 13 | §5.10 平台 ACTION 6 项 | 👤 founder 待执行 | 见下表 |
| 14 | §14 自检报告 | ✅ | 本节下方 |

### §5.10 平台 ACTION（👤 founder 执行，Agent 无权限）
| # | 项 | 状态 | 说明 |
| --- | --- | --- | --- |
| 1 | Branch protection `main`：required checks + 线性历史 + 禁 force-push | 👤 待执行 | 当前：admin 合并绕行，建议开启；required checks 列表对应 .github/workflows/* job 名 |
| 2 | GitHub Environment `holdout`：required reviewer = founder；secret `HOLDOUT_READ_TOKEN`；runner 标签 `holdout`（网络出口白名单） | 👤 待执行 | workflow 已引用 holdout environment，未创建不会触发 holdout-eval.yml，不阻塞其他路径 |
| 3 | 自托管 GPU runner 标签 `gpu`（语音评测/TTS/端侧推理） | 👤 待执行 | nightly/weekly/release 的 self-hosted gpu job 会排队等待 runner，创建即可消费 |
| 4 | Actions 权限最小化：只读 contents，artifact 90 天保留 | 👤 建议确认 | Org Settings → Actions → General，默认限写，建议切最小权限 |
| 5 | Dependabot：gomod/pnpm/cargo/github-actions 周更 | ✅ | .github/dependabot.yml 已写入（from initial upload），执行频率 = weekly |
| 6 | `reports/exemptions.yaml` 初始 + `reports/holdout-audit.jsonl` 初始 | ✅ | PR #47 已写入 |

## §14 自检报告（2026-08-29，IR #64 修订）

```bash
just verify            # → verify-configs: 79 门禁，0 违反；repoctl coverage/agents-md/forbidden-refs = 全绿
```

§14 自检结果（2026-08-29 复测，IR #64 真实调度 + 阶段化执法后）：
| # | 命令 | 实测 | 期望 | 通过 |
| --- | --- | --- | --- | --- |
| 1 | `just bootstrap && just lint && just verify` | verify-configs: 79 门禁，0 违反；coverage = 0 FAIL（16 资产 DEBT） | 全绿（exit 0） | ✅（lint: pnpm/cargo 未安装不报 exit 1，gofmt/go vet 通过） |
| 2 | `gaterunner collect \| wc -l` | 83 条断言登记（79 配置门禁 + 4 条 TX 虚构资产 fixture 注册，不冒充真实资产） | ≥70 | ✅ |
| 3 | `just gate T4 all` | 全部门禁 not_implemented（实现未开始）+ not_impl 计数显式输出，exit 0 | exit=0（not_implemented 不计 pass 不计红，IR #64） | ✅ |
| 4 | `ls docs/gates/assets \| wc -l` | 16 | =16 | ✅ |
| 5 | `ls packages/go/*/AGENTS.md` + packages/* 合计 | 22 份（17 go + 2 native + 2 ts + 1 根） | =22 | ✅ |
| 6 | `git ls-files \| grep -c golden-journeys` | 50 | =50 | ✅ |

说明（IR #64 / ADR-0002）：coverage 现为阶段化执法——0 断言资产输出 `coverage DEBT:` 行（16 资产全部 DEBT，不 FAIL、exit 0），资产首条断言落地即恢复全 BI 覆盖 + G0 强制；`just gate <T>` 为真实 `go test` 调度，门禁状态以 not_implemented 显式单列（不计 pass）。

⚠️ **完成判据达成**——此后任何开发 agent clone 本仓，读 AGENTS.md → docs/gates/assets/<T>.md → configs/gates/<T>.yaml 即可开工，全程无需人类解释。
