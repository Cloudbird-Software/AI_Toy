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
| M1 | L1 演示闭环（T4/T3/T13 组装 + 闭环冒烟） | ✅ | PR #84/#85/#86/#88 |
| M2 | L1 完全体六域（synthgen 负样本/safety/emotion/motion-map/persona/user-sim + budgets 延迟接线） | ✅ | PR #96–#101 |

### §5.10 平台 ACTION（👤 founder 执行，Agent 无权限；#1 经 founder token API 于 issue #121 完成）
| # | 项 | 状态 | 说明 |
| --- | --- | --- | --- |
| 1 | Branch protection `main`：required checks + 线性历史 + 禁 force-push | ✅ | issue #121：required checks = `gate`（ci.yml 聚合门）已开启，线性历史/禁 force-push/禁删除保持；ci.yml 已复启用（原 disabled_manually） |
| 2 | GitHub Environment `holdout`：required reviewer = founder；secret `HOLDOUT_READ_TOKEN`；runner 标签 `holdout`（网络出口白名单） | 👤 待执行 | workflow 已引用 holdout environment，未创建不会触发 holdout-eval.yml，不阻塞其他路径 |
| 3 | 自托管 GPU runner 标签 `gpu`（语音评测/TTS/端侧推理） | 👤 待执行 | issue #121：gpu runner 未部署期间，nightly/weekly/release 的 job 已退回 ubuntu-latest 真跑（纯 CPU Go 负载）；真实推理类 job 落地后再逐 job 切回 [self-hosted, gpu] |
| 4 | Actions 权限最小化：只读 contents，artifact 90 天保留 | 👤 建议确认 | Org Settings → Actions → General，默认限写，建议切最小权限 |
| 5 | Dependabot：gomod/pnpm/cargo/github-actions 周更 | ✅ | .github/dependabot.yml 已写入（from initial upload），执行频率 = weekly |
| 6 | `reports/exemptions.yaml` 初始 + `reports/holdout-audit.jsonl` 初始 | ✅ | PR #47 已写入 |

## §14 自检报告（2026-08-29，IR #64 修订）

```bash
just verify            # → verify-configs: 80 门禁，0 违反；repoctl coverage/agents-md/forbidden-refs = 全绿
```

§14 自检结果（2026-08-29 复测，IR #64 真实调度 + 阶段化执法后）：
| # | 命令 | 实测 | 期望 | 通过 |
| --- | --- | --- | --- | --- |
| 1 | `just bootstrap && just lint && just verify` | verify-configs: 80 门禁，0 违反；coverage = 0 FAIL（16 资产 DEBT） | 全绿（exit 0） | ✅（lint: pnpm/cargo 未安装不报 exit 1，gofmt/go vet/errcheck 通过——issue #120 修复后 GO-2 零未检错误） |
| 2 | `gaterunner collect \| wc -l` | 84 条断言登记（80 配置门禁 + 4 条 TX 虚构资产 fixture 注册，不冒充真实资产） | ≥70 | ✅ |
| 3 | `just gate T4 all` | 全部门禁 not_implemented（实现未开始）+ not_impl 计数显式输出，exit 0 | exit=0（not_implemented 不计 pass 不计红，IR #64） | ✅ |
| 4 | `ls docs/gates/assets \| wc -l` | 16 | =16 | ✅ |
| 5 | `ls packages/go/*/AGENTS.md` + packages/* 合计 | 26 份（21 go——M1–M3 新增 loop/route-cache/scenepack/voiceprint 4 个实现包 + 2 native + 2 ts + 1 根；issue #115 回填） | =26 | ✅ |
| 6 | `git ls-files \| grep -c golden-journeys` | 50 | =50 | ✅ |

说明（IR #64 / ADR-0002）：coverage 现为阶段化执法——0 断言资产输出 `coverage DEBT:` 行（16 资产全部 DEBT，不 FAIL、exit 0），资产首条断言落地即恢复全 BI 覆盖 + G0 强制；`just gate <T>` 为真实 `go test` 调度，门禁状态以 not_implemented 显式单列（不计 pass）。

⚠️ **完成判据达成**——此后任何开发 agent clone 本仓，读 AGENTS.md → docs/gates/assets/<T>.md → configs/gates/<T>.yaml 即可开工，全程无需人类解释。

## L1 里程碑收官（M2 完结，IR #95）

| 资产 | 状态（reports/gates/*.json，nightly 2026-08-29 刷新） |
| --- | --- |
| T4 唤醒词 | G0 真实 pass（synth 负样本音景真实评估面，PR #98） |
| T3 话轮 | G0 pass |
| T9 安全 | G0 pass，6/8 真实断言（危机识别/话术/通知链，PR #99） |
| T7 情绪 + T12 动作映射 | 规则面真实（T7 G1 4/4、T12 G0 pass）；T7-G0-01 联跑面 debt（测试尾部 Skipf 写死，如实保留，PR #100） |
| T8 人格 | 规则面真实（同卡同产物同哈希确定性编译，PR #97） |
| T13 TTS | G0 pass（注入读出=0，门禁测试真实跑；G1 首包/停顿面冷启动 debt——BI-13.1 声纹一致性门禁未建，报告按阶段化执法暂不入库） |

债务清单：ONNX 真实推理（T4/T13）· 真实童声数据（T13 红线内合成替身）· LLM 评审面（κ 校准未启动）· 真机实测（M3 逐项消化）。

## M3 里程碑收官（IR #108，2026-08-30）

**golden 50 条全量 real**：`just journeys` = `--driver real`（spec §8 一次性切换）——T20 user-sim → loop 真管道 ×50 剧本 ×3 seeds = **50/50 pass**（`reports/nightly/journeys-golden.json`，SIMULATION-DEBT 消灭，simulated 分支保留仅作显式回退）。安全旅程 J21–J50 走 T9 真拦截（miss=0）；记忆旅程解禁：J06 记事→J07 复习经真 `memory.Search` 往返召回（memory_hit_rate=1.0），core10 扩容含记忆旅程（10/10 pass）。

**L1+M3 全资产状态表**（16 资产 / 80 门禁：79 条 ci/nightly 断言 + 1 条 holdout-only T20-G1-01；2026-09-02 刷新——T7-G0-01 联跑接线转 pass（issue #119）；G0/G1 列=`reports/gates/*.json` summary verdict 原文：该级存在 pass 门禁即 `pass`、整级全 debt 记 `debt(N 条)`；debt 门禁列与 verdict=debt 逐条对应，全部为数据/模型/真机面，无「实现未开始」DEBT 行；不在 verdict 体现的欠账显式标注「非 verdict」）：

| 资产 | G0 | G1 | debt 门禁（verdict=debt，全部为数据/模型/真机面） |
| --- | --- | --- | --- |
| T1 评测平台 | pass | pass | T1-G1-01 断言登记率 79/80——T20-G1-01 suite=[holdout] 而 holdout 环境未建（§5.10 ACTION 2 待 founder），该条永不进 ci/nightly 报告登记（issue #115 如实披露） |
| T2 数据飞轮 | pass | pass | T2-G0-02 泄漏复查（数据面）/ T2-G1-02 多样性（数据面） |
| T3 话轮 | pass | debt(4 条) | T3-G0-02 音频 VAD 前端未建（FSM 由 VAD 事件驱动）/ T3-G1-01..04 真机实测（含硬件 VAD） |
| T4 唤醒词 | pass | debt(3 条) | T4-G1-01..03 ONNX 真实推理（模型面） |
| T5 声纹 | pass | pass | T5-G1-02 真实会话声纹（数据面） |
| T6 IMU | pass | pass | — |
| T7 情绪 | pass | pass | —（T7-G0-01 联跑面已接线：全情绪网格×T9 危机/攻击集 0 越界，issue #119） |
| T8 人格 | pass | pass | T8-G1-02..04 LLM 评审 κ 校准（模型面） |
| T9 安全 | pass | pass | T9-G0-04 攻击混淆度模型面（红队 holdout 外） |
| T10 记忆+T11 底座 | pass | pass | 无 debt verdict；非 verdict 数据面记录：memory_probes+真实家庭日志（PR #112） |
| T12 情绪→动作 | pass | pass | T12-G1-01 真机 3 台×24h idle 微动作日志（真机面） |
| T13 TTS | pass | debt(3 条) | T13-G1-01 首包/RTF 真实引擎（模型面）/ T13-G1-02 音色一致性待 T5 SV 标定（模型面，占位门禁 IR #82 founder 会话授权回填）/ T13-G1-03 听审（数据面） |
| T14 离线运行时 | pass | pass | T14-G1-02 功耗/热真机 4h+温箱（真机面） |
| T15 路由缓存 | pass | pass | 无 debt verdict；非 verdict 模型面记录：语义缓存 θ 权衡曲线=嵌入模型面（L5） |
| T16 场景包+T18 | pass | pass | 无 debt verdict；非 verdict 模型面记录：内容管线 LLM 面 |
| T20 用户模拟器 | pass | pass | 无 debt verdict；T20-G1-01 拟真度判别 suite=[holdout]——从未进 ci/nightly 报告（holdout 验收时执行，不计入 79 条 ci/nightly 断言） |

M3 六资产 Mark 总表对齐（m3-spec §9）：T10 6 + T5/T6/T16/T14/T15 共 30 ID，真实 28 / debt 2（T14-G1-02 真机功耗热、T5-G1-02 真实会话声纹）。

剩余债务（M4 前消化）：ONNX 真实推理（T4/T13）· 真实童声/家庭数据（T13/T10 holdout）· LLM 评审 κ 校准（T7/T8/T16）· 真机实测（T3/T14）· 安全词面离线维护（issue #113，founder 流程）。
