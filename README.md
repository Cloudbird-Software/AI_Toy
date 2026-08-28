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

### 平台级 ACTION（§5.10，需 GitHub Web UI/CLI 操作，无法在此沙盒落地，清单供 founder 勾选）

| # | 项 | 需手工操作 | 备注 |
| --- | --- | --- | --- |
| 1 | `main` 分支保护：§5.1 required checks（`py`/`ts`/`rs`/`gates`/`combo-smoke`/`meta`/`gates-cfg-guard`）+ 线性历史 + 禁 force-push | ⚠️ 待 founder 在 GitHub Branches 设置 | 沙盒无 push/管理权限 |
| 2 | GitHub Environment `holdout`：required reviewer = founder；secret `HOLDOUT_READ_TOKEN`；自托管 runner 标签 `holdout`（无外网出口） | ⚠️ 待 founder 建 Environment + 部署自托管 runner | |
| 3 | 自托管 GPU runner 打标签 `gpu`（语音评测/TTS/端侧推理） | ⚠️ 待 founder 部署 | ci/nightly 的 `runs-on: [self-hosted, gpu]` 依赖 |
| 4 | Actions 权限最小化（只读 contents，artifact 保留 90 天） | ⚠️ 建议 founder 核对 | ci.yml 已 `permissions: contents: read`（总基线） |
| 5 | Dependabot：pip/pnpm/cargo/github-actions 周更 | ⚠️ 建议 founder 核对 | 仓库已有 `.github/dependabot.yml`（见 §5.3 规格对应） |
| 6 | `reports/exemptions.yaml` 空 + `reports/holdout-audit.jsonl` 表头注释就位 | ✅ 已在仓库落盘 | 见 reports/ 目录 |

完成判据（§14）：此后任何开发 agent clone 本仓，读根 `AGENTS.md` → `docs/gates/assets/<T>.md` → `configs/gates/<T>.yaml` 即可开工，全程无需人类解释。平台级 1–5 项是 CI 在真实 runner 上"跑起来"的前置，不影响 agent 本地开发循环。
