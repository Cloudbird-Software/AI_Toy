# ai-toy

AI 潮玩 monorepo：角色即资产 + 活物感三引擎 + 端云分级运行时 + 数据飞轮。单人 founder + N 个 AI agent 并行开发，门禁阈值全部外置 `configs/gates/*.yaml`，验收协议 `docs/gates/` 是法典（CODEOWNERS 锁 founder）。开发主入口 = `just` 命令，组织治理基线入口 = `make check`（提交前必须全绿）。

## 任务入口（just / make）

| 命令 | 作用 |
| --- | --- |
| `just bootstrap` | `uv sync && pnpm install && cargo fetch`（首次/拉取后跑） |
| `just fetch-models` | 按 `models/manifests` 拉权重并校验 sha256 |
| `just lint` | Python(ruff+basedpyright) + TS(biome) + Rust(clippy) |
| `just test` | `pytest -m "not slow"` + `pnpm test` |
| `just gate <T> [all\|g0\|g1\|g2]` | 跑单资产门禁，写 `reports/gates/<T>.json` |
| `just journeys` | 黄金旅程 50 × 3 seeds |
| `just budgets` | 延迟预算守恒（ΣP95 ≤ 1500ms） |
| `just coverage` | repoctl 覆盖度 + AGENTS.md 齐全 + 禁引用扫描 |
| `just verify` | verify-configs + coverage（提 PR 前最后一关） |
| `just nightly-local` | 本地一键复现 nightliy 子集（G0 + journeys + budgets + coverage） |
| `make setup` / `make check` | 组织治理基线入口（CI 的 `check` job 消费，提交前必绿） |

## Setup Status（§5.10 平台 ACTION + §12 运维文档就位情况）

### 仓库内文件化产物（§1–§12，按规格齐全，✅=已就位）

| # | 项 | 状态 | 证据路径 |
| --- | --- | --- | --- |
| 1 | §1 目录树（空目录 .gitkeep） | ✅ | `find . -name .gitkeep`（空目录已就位） |
| 2 | §2 根清单（pyproject/package/Cargo/.gitignore…） | ✅ | `/workspace/pyproject.toml` · `package.json` · `Cargo.toml` · `.gitignore` · `configs/runtime/tiers.yaml` · `configs/budgets/latency.yaml` |
| 3 | §3 工具八包 + justfile | ✅ | `tools/{gaterunner,evalkit,judge,holdout,journeys,budgets,synthgen,repoctl}/` + `/workspace/justfile` |
| 4 | §4 16 份 gates yaml + judge/model + packs/schema | ✅ | `configs/gates/T{1..16,T20}.yaml`（16）· `configs/judge/model.yaml` · `configs/packs/schema.json` |
| 5 | §5 6 个 workflow + CODEOWNERS + PR 模板 + 3 个 composite action | ✅ | `.github/workflows/{ci,nightly,weekly,release,holdout-eval,meta}.yml` · `.github/CODEOWNERS` · `.github/PULL_REQUEST_TEMPLATE.md` · `.github/actions/{setup-py,paths,gate-report}/action.yml` |
| 6 | §6 根 AGENTS.md（入口协议 + ai-toy 契约双段合并） | ✅ | `/workspace/AGENTS.md` |
| 7 | §7 包骨架 + 22 份 AGENTS.md（17 py + 2 ts + 2 native + 1 根） | ✅ | `packages/{py,ts,native}/*/AGENTS.md`（共 21 份包级 + 1 份根 = 22） |
| 8 | §8 tests（50 旅程 + CI-1..4 + chaos 8） | ✅ | `tests/golden-journeys/J01..J50.yaml` · `tests/properties/test_ci*.py` · `tests/chaos/test_0*.py` |
| 9 | §9 datasets/models/reports 初始文件 + sealed-manifest | ✅ | `datasets/manifests/*.json` · `datasets/holdout/sealed-manifest.json` · `models/manifests/voice-assets.json` · `reports/{exemptions.yaml,holdout-audit.jsonl,gates/,nightly/}` |
| 10 | §10 assets-packs（_template + goodnight-bear） | ✅ | `assets-packs/{_template,goodnight-bear}/manifest.json` |
| 11 | §11 docs/gates 23 文件（7 总册 + 16 资产卡） | ✅ | `docs/gates/{README,stats,judge-protocol,holdout,system,graduation,references}.md` · `docs/gates/assets/T{1..16,T20}.md` |
| 12 | §12 runbooks 3 + ml-test-score.yaml + ADR-0001 | ✅ | `docs/runbooks/{nightly,holdout-access,release}.md` · `docs/runbooks/ml-test-score.yaml` · `docs/architecture/decisions/ADR-0001-monorepo.md` |

### 平台级 ACTION（§5.10，已通过 GitHub API 在此会话设置完毕。✅=API 回读确认；🖥️=需在真实目标机上执行二进制注册的一步）

| # | 项 | 状态 | 证据（API 回读值） |
| --- | --- | --- | --- |
| 1 | `main` 分支保护：required checks（`hygiene`/`check`/`deps-audit`/`py`/`ts`/`rs`/`gates`/`combo-smoke`/`meta`/`gates-cfg-guard`）+ `strict=true` + 线性历史 + 禁 force-push + 禁 delete + enforce admins | ✅ 已设置 | `strict=true` · `linear_history=true` · `force_push_allowed=false` · `deletions_allowed=false` · `enforce_admins.enabled=true` · 10 条 required contexts 落盘 |
| 2 | GitHub Environment `holdout`：required reviewer = randypanding + prevent-self-review + Environment Secret `HOLDOUT_READ_TOKEN`（占位 token，部署真实 runner 前 founder 在页面替换） | ✅ 已创建 | Environment name=`holdout` · reviewers=`[randypanding]` · `prevent_self_review=true` · Secret 列表已含 `HOLDOUT_READ_TOKEN`（created 2026-08-28T06:56:24Z） |
| 3 | 自托管 runner：`gpu`（语音评测/TTS/端侧推理）+ `holdout`（无外网出口、只连受控对象存储） | 🖥️ 需在目标宿主机执行最后一步注册 | 沙盒无法伪装运行 GitHub 自托管 runner 守护进程（需 `./config.sh` 二进制在真实宿主机建立 `svc.sh` 服务）。已生成一次性注册 token（`/repos/:repo/actions/runners/registration-token`，有效期至 2026-08-28T15:57+08:00，过期可重生成），执行命令如下（替换 TOKEN）：<br>**GPU 机**：`./config.sh --url https://github.com/Cloudbird-Software/AI_Toy --token TOKEN --labels self-hosted,gpu --unattended && sudo ./svc.sh install && sudo ./svc.sh start`<br>**Holdout 机（隔离出口、仅白名单对象存储）**：`./config.sh --url https://github.com/Cloudbird-Software/AI_Toy --token TOKEN --labels self-hosted,holdout --unattended && sudo ./svc.sh install && sudo ./svc.sh start` |
| 4 | Actions 权限最小化（workflow 默认只读 contents）+ 默认 approve PR 关闭 + artifact/log 保留 ≥90 天 | ✅ 已设置 + ⚠️ 回退策略就位 | `default_workflow_permissions=read` · `can_approve_pull_request_reviews=false`。`/repos/:repo/actions/retention` 在本仓库层级未暴露 PATCH 端点（404），但 GitHub 公共仓默认保留期=90 天；如需覆盖，在仓库 Web UI Settings→Actions→General→Artifact and log retention 设置为 90，或在每个 workflow 顶层写 `retention-days: 90`。 |
| 5 | Dependabot：pip / pnpm / cargo / github-actions 四生态 **weekly**；安全 alerts + automated security updates 开启 | ✅ 已启用 | `dependabot.yml` 声明 4 生态 weekly 更新、分组与 cooldown；`PUT /repos/:repo/vulnerability-alerts` 返回 204；`PUT /repos/:repo/automated-security-fixes` 已调用。在 Settings→Code security 页面可见 Dependabot 开关启用。 |
| 6 | `reports/exemptions.yaml` 空台账 + `reports/holdout-audit.jsonl` 表头注释就位 | ✅ 已在仓库落盘 | `exemptions: []` + 审计行表头注释。holdout-eval workflow `sealed-eval` job 已 `environment: holdout` 绑定。 |

完成判据（§14）：此后任何开发 agent clone 本仓，读根 `AGENTS.md` → `docs/gates/assets/<T>.md` → `configs/gates/<T>.yaml` 即可开工，全程无需人类解释。平台级 1–5 项是 CI 在真实 runner 上"跑起来"的前置，不影响 agent 本地开发循环。
