# AGENTS.md — ai-toy 仓库 agent 操作契约

<!-- entry-protocol v2 -->

### 入口协议（陌生 agent 从这里开始——宪法 §11 / ADR-0055/0095）

0. **按意图定角色**（指引=.github 仓 `docs/agent/ROLE-*.md`，ADR-0095）：开新意图→ROLE-IR · 把已签署 IR 写成 spec→ROLE-SPEC · 实现卡片→ROLE-IMPLEMENT · 验收/人类让你处理 issues→ROLE-ACCEPT
1. 取 ghcb（钉 SHA，禁浮动 main）：`curl -fsS -o ghcb https://raw.githubusercontent.com/Cloudbird-Software/.github/f72d9520706c8fca974d92456f65cae5c1412bb7/scripts/ghcb && chmod +x ghcb`（凭据用你自己的：`gh auth login` 或 `export GH_TOKEN=<PAT>`；`-f` 必带——404 时 curl 无 -f 仍退出 0，会把错误页当脚本落盘）
2. 找活：`bash ghcb next [owner/repo]` → 列 state:ready 卡（卡 issue 是唯一工作凭证，无卡不开工）
3. 认领：`bash ghcb claim <n> [owner/repo]` → 评论 /claim——conductor 转介 arbiter 原子 CAS 租约，先到先得；败者换下一张（`bash ghcb status <n>` 看持有者）
4. 开工：`make card-test CARD=<n>`（读卡 AC、测试先行）→ `make gates-pr`（本地复现 CI 关卡）
5. 提 PR：body 必带一行卡元数据 `Card: <owner>/<repo>#<n>`（`bash ghcb card-meta <n>` 生成；缺失=后续关卡 exit 3）
6. front-desk 命令（卡 issue 评论，conductor 转介 arbiter 处理）：/claim 认领 · /release 释放租约 · /retry 隔离回流

<!-- /entry-protocol -->


## 你是谁
AI 开发 agent。人类创始人（GitHub: @randypanding）是唯一验收决策人。你的自由度由「指导」给出，你的边界由「门禁」锁死。

## 术语（全仓统一）
- 资产 T1–T20（无 T17/T19）：T1 评测平台 · T2 数据飞轮 · T3 话轮 · T4 唤醒词 · T5 声纹 · T6 IMU · T7 情绪引擎 · T8 人格编译器 · T9 安全 · T10 记忆图谱(+T11 底座) · T12 情绪→动作映射 · T13 TTS · T14 离线运行时 · T15 路由缓存 · T16 场景包(+T18 内容管线) · T20 用户模拟器
- BI-n.m：业务意图编号，定义于 docs/gates/assets/<T>.md
- 门禁 G0（发布阻断）/ G1（合并阻断）/ G2（趋势警告）；阈值唯一来源 configs/gates/<T>.yaml
- 六层验收 L0 通用 CI（已外部具备）→ L1 意图断言 → L2 属性 → L3 形式化(仅 T14/T10 两个 FSM) → L4 LLM 评审(κ≥0.61) → L5 holdout/真机/对抗

## 三类知识（先读这个再动手）
| 类型 | 例子 | 约束力 |
|---|---|---|
| 指导 | docs/gates/assets/*.md 的「路径选项」 | 零约束。可选列表外方案；PR 必须记录选了哪条+为什么 |
| 门禁 | configs/gates/*.yaml + 对应 go test（gaterunner 注册标记） | 不可协商。G0 红禁发布；G1 红禁合并；G2 进看板 |
| 纪律 | holdout 制度、季度校准、豁免台账 | 审计项，repoctl 检查 |

## 开发循环（固定）
1. 读 docs/gates/assets/<T>.md：业务意图 → 门禁表 → 属性 → rubric → holdout
2. 选技术路径（自由），ADR 记录到 docs/architecture/decisions/（选型/换路必写）
3. 实现 + 测试：门禁测试打 gaterunner 注册标记 `gaterunner.Mark(t, asset="T4", bi="BI-4.2", id="T4-G0-01", level="G0")`（Go 无原生 marker，由 gaterunner 提供注册辅助；`gaterunner collect` 经 `go test` 收集登记表）；属性测试用 `testing/quick`
4. 本地：`just gate <T> all` → `just coverage` → 全绿才提 PR
5. CI 的 `gates`/`meta`/`combo-smoke` 全绿 → founder 合并

## 禁止（违反 = G0 级行为）
- 改 configs/gates/** 阈值、docs/gates/** 验收协议（只有 founder PR 可改）
- 触碰 datasets/holdout/**（数据本体在受控存储，任何训练/微调代码路径不得引用；repoctl forbidden-refs 扫描）
- 弱化/跳过/删除门禁测试；给测试塞固定答案；改小样本量
- 在被评代码里 import 评测断言实现（防「对着考卷优化」；tests 与 packages 隔离）
- 无 license 台账的新依赖；克隆真实儿童声音（T13 红线）
- 把 T20 模拟器产物写进任何训练集
- 修改延迟预算无划拨说明（configs/budgets + PR 描述）

## 必须
- 每个 G1 断言失败要么修复要么在 reports/exemptions.yaml 登记（原因+期限≤30天，自动过期）
- 噪声带未实测的新指标不得直接设 G1 线（先 `gaterunner calibrate --runs 10`）
- 统计断言一律用 tools/evalkit（泊松上限/二项 n/配对 bootstrap），不得手算
- LLM 评审一律走 tools/judge（锁定模型+pairwise+swap），不得内联裸调
- 合成数据一律经 tools/synthgen 注册（溯源戳 + 8:2 切 synth-holdout）

## 升级给 founder（开 issue @randypanding，不要自行决定）
- 想改门禁阈值/样本量/预算分配
- G0 红且根因是设计问题而非实现 bug
- 需要新数据源、真实数据采集、第三方依赖引入
- 出现文档未定义的行为分歧（先查 docs/gates，查不到再问）

## 统计纪律速查（evalkit 已实现，勿手写）
- 零失败宣称：≥95% 通过率需 n≥59；≥98% 需 149；≥99% 需 299
- 泊松零事件：95% 上限=3/N 小时；宣称 ≤0.5/h 需 ≥6h 零事件；≤0.1/h 需 ≥30h
- EER：报告 trial 数与 miss/FA；家庭级 ≥5000 配对
- ASR：每攻击 5 次采样，报 mean/best 双口径 + 声明威胁模型
- 版本对比：配对 bootstrap 95%CI，区间含 0 不算提升

## LLM 评审速查
- 只评不可断言维度（亲和力/活物感/自然度/人格魅力/危机安抚温度）
- rubric 三级量表 + 锚定样例；pairwise+swap；金标每类错误 ≥15 例
- κ≥0.61 才准上线自动化；κ≥0.80 才可进 CI 自主判定；季度或变更即重校准

## 目录速查
docs/gates/ 验收法典 · configs/gates/ 阈值 · tools/gaterunner 门禁机 · tools/evalkit 统计 · tools/judge 评审 · tools/holdout 密封数据 · tests/golden-journeys 50 条剧本 · tests/properties CI-1..CI-4 · reports/ 门禁历史与豁免 · configs/budgets/latency.yaml 延迟预算

## PR 规范
conventional commits（feat/fix/test/gate/docs/chore）；一次 PR 一个资产为主；门禁测试与实现同 PR；路径选择写 PR 模板对应栏。
