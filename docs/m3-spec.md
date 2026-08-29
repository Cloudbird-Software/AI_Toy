# M3 实现规格 —— 剩余六资产规则面（runtime-fsm / route-cache / memory / voiceprint / imu / scenepack + golden 全量 real）

> IR #103（spec PR，纯文档零代码）。实现卡：#104 T14+T15 · #105 T10+T11 · #106 T5+T6 · #107 T16+T18 · #108 golden real+收官。
> 依据：docs/gates/assets/{T14,T15,T10,T5,T6,T16}.md · configs/gates/ 同名 yaml（阈值实数，禁改）· docs/m1-spec.md / m2-spec.md（契约衔接不重造）· tests/properties/contract.go（RuntimeModel 真身契约）· tests/chaos/chaos_model.go（CH-01..08 故障矩阵）· configs/packs/schema.json · ADR-0002/0005/0006。冲突以法典为准；路径选择写实现卡 PR 模板。

## 1. 架构总览：六包挂进 M2 loop 管道（结构不动，接线面扩槽）

```
音频帧→kws→EvWake→FSM.OnVAD─ActTurnEnd→Responder(Turn)→Router.Synthesize→PumpSpeak ← M1/M2 主链不动
                            │            ↑ T15 route-cache：Responder 前置（命中=零上游延迟；穿透=回填）
                            │            ↓ T16 scenepack：SceneCtx 注入（persona 词表/safety 并联词表/emotion 规则/motion 表/knowledge——组装归 loop）
感官面：T6 imu.Sample→EvPickup/EvIdle/EvFall/EvStorm（loop 搬运：Greet 情绪/静默抑制/停马达/风暴→仲裁）
身份面：T5 voiceprint.Verify→UserID 绑定话轮；拒判→T10 只读（CI-2/CH-05；事件由 loop 搬运，包间零 import）
记忆旁路：T10 memory（不等 TTS）：话轮终点 Write/Search 供 Responder 引用；T20 探针供数（J06/J07）
全局仲裁：T14 runtime-fsm Arbiter＝CompTierMap（7 组件×L0–L3）注入各包既有档位槽（kws.TierBudget/turntaking.TierPolicy/tts.TierCaps/loop.Config.Tier）；故障→FailApply 只降不升、安全水位不降
```

- **T14=全局档位仲裁器**：M1 各包预留档位槽由 Arbiter 真身接管——各包本地窄接口不动，换注入实现即接线；降级矩阵对齐 chaos CH-01..08（§2）。
- **包名对齐实现卡口径**：route-cache/voiceprint/scenepack（#104/#106/#107）；法典卡面的 router/speaker/packs 骨架目录保留（学习型路由/SV 真模型/格式演进的后续落位），M3 不动不删。
- **包间零 import 纪律延续**（ADR-0004）：六新包互不 import、不 import 既有资产包；组装归 loop（联跑只在测试侧 import 被测包）。**唯一例外=runtime-fsm import tests/properties（仅类型，§2）**。
- **依赖零新增**：六包 import 白名单=标准库；scenepack 手写结构校验镜像 schema 必要条件（JSON Schema 库不引，一致性由变异包 fixture 双跑断言）；yaml.v3 仍限 tools 侧。
- **真测口径**：M3 一切「真实」=CI 宿主真实代码路径（逻辑口径+仿真时钟+CI 墙钟）；真模型（llama.cpp/ECAPA-TDNN/嵌入语义缓存）、真机台架、真实家庭数据全部 debt/注记（§9、ADR-0006）。

## 2. 包契约 A —— packages/go/runtime-fsm（T14，实现卡 #104）

```go
package runtimefsm
// ——契约真身：实现 tests/properties/contract.go 全部四接口（CI-1..CI-4 断言口径不变）——
type Runtime struct{}                // 零值可用、方法全值接收器（real_runtime_test.go 绑定 fsm.Runtime{}）
func NewRuntime() (*Runtime, error)  // 降级矩阵/档位嵌套/水位表自洽校验
func (Runtime) FailApply(m properties.CompTierMap, f properties.FailureType) properties.CompTierMap // 故障→组件降档（只降不升）
func (Runtime) GlobalCapability(m properties.CompTierMap) properties.Capability // ∩ 各组件档位能力（L0⊇L1⊇L2⊇L3）
func (Runtime) SafetyLevel(m properties.CompTierMap) properties.SafetyWatermark // 最严格水位（单调不降，CI-1）
func (Runtime) TierCaps(t properties.Tier) properties.Capability
func (Runtime) DegradeMap(f properties.FailureType) map[properties.DegradeAction]bool // CI-4 降级集
func (Runtime) AllowedAction(f properties.FailureType, a properties.DegradeAction) bool
func (Runtime) IdentityBinding(o properties.Output) bool                // CI-2：禁止半绑定
func (Runtime) T5Reject(m properties.MemoryMode) properties.MemoryMode  // 拒判→记忆只读（仲裁面）
func (Runtime) BudgetTotal(segs []properties.BudgetSegment) int         // CI-3：ΣP95−Σ重叠 ≤1500
func (Runtime) BudgetCheck(b, c properties.LatencySample, noReallocation bool) properties.BudgetStatus
func (Runtime) MeanStd(s properties.LatencySample) (mean, std float64)
// ——状态面：loop 组装的档位仲裁器（表驱动 FSM；事件=网络通断/超时/电量/温度/用户操作）——
type Arbiter struct{ /* 组件档位表+恢复计时（仿真时钟，无墙钟） */ }
func NewArbiter() *Arbiter
func (a *Arbiter) OnFault(f properties.FailureType, atMs int64) []Transition // FailApply 应用+降级动作发布
func (a *Arbiter) OnRecover(scope RecoverScope, atMs int64) []Transition     // 网络恢复有限时间回 L0（活性）
func (a *Arbiter) Tier() int                        // 全局档（注入 loop.Config.Tier）
func (a *Arbiter) CompTiers() properties.CompTierMap
```

- **import 纪律（唯一例外，写进 #104 PR 模板）**：契约签名类型=tests/properties（model.go/contract.go 纯类型与镜像）；断言本体在 ci1..ci4_test.go（_test 编译单元，import 不可达）——「被评代码 import 评测断言实现」红线不破；runtime-fsm 白名单=标准库+tests/properties（仅类型）。
- **`go test -tags real ./tests/properties/...` 编译通过＝接线完成**：real_runtime_test.go 绑定 fsm.Runtime{} 满足四接口+init 切驱动，CI-1..CI-4 同断言跑真身（镜像保留作回归参照）；CI 默认 tag 集仍不含 real（#65），#104 本地验证并在 PR 描述贴输出。
- **降级矩阵=8 行 chaos 对齐**：CH-01/02/04/05/06/07/08 ↔ FailureType 一一对应（FailApply 档位迁移+DegradeMap 允许集=properties 镜像同语义）；CH-03（输出超长）已由 loop 截断防线承载（M1），不入档位矩阵；FailNoResponse=兜底行（非 Safety 组件降 L3、CompSafety 至多 L1 保 Strict）。**安全水位不降**：CompSafety 永不因故障降档。
- **表驱动穷举（资产卡必做）**：四档×全事件全表断言后继合法/无死锁/L0 可达/每档绑定正确安全配置；quick：任意故障序列档位 ∈ 合法四档、当前档功能上界 ⊆ 档安全配置、只降不升、同故障序列同轨迹、网络恢复回 L0（活性）。
- **loop 接线**：Arbiter.Tier()→Config.Tier；切档/降级产 EvDegrade（Reason ∈ loop 预定义集；需新槽位则 DegradeReason 尾部追加，M1/M2 事件序不动）；冷启动=Arbiter+各包 wire 链（T14-G1-03 口径）。

## 3. 包契约 B —— packages/go/route-cache（T15，实现卡 #104）

```go
package routecache
type Key struct{ NormQuery, UserID, Role, EmoLabel string } // 键=规范化 query+身份+角色+情绪上下文
type Config struct{ MaxEntries int; MaxBytes int64; TTLMs int64; SafeBypass func(q string) bool }
type Stats struct{ Hits, Misses, Expired, Evicted, Bypassed int }
func NewCache(cfg Config) (*Cache, error)              // 上限>0/SafeBypass 非 nil 校验（fail-closed）
func (c *Cache) Get(k Key, nowMs int64) (string, bool) // 键全等命中；TTL 过期=穿透；bypass 恒 miss
func (c *Cache) Put(k Key, resp string, nowMs int64)   // LRU 淘汰；字节预算超限逐最旧；bypass 拒收
func (c *Cache) Invalidate(k Key)
func (c *Cache) Stats() Stats                           // T15-G1-02 观测面
func Normalize(q string) string                         // 去语气词（嘛/呀/啦）+空白折叠+大小写
```

- **命中/穿透/失效语义**：命中=键四元组全等（规范化后）→零上游延迟；穿透=未命中/过期→走 Responder 并回填；失效=TTL 过期/Invalidate/安全旁路。**误命中=0 由精确键构造保证**（对抗对 200 组实测）——语义近邻缓存（θ 相似度）需嵌入模型=真模型面（L5 注记），M3 不接、不宣称。
- **安全类 query 永不缓存**：SafeBypass 由 loop 组装注入（safety 判定 Crisis/Attack→旁路）——本包不 import safety（考卷隔离）；Get 恒 miss+Put 拒收（T15-G0-01）。
- **预算上限**：路由决策计入 cloud_llm 段 ≤30ms（configs/budgets/latency.yaml）——决策=规范化+哈希查表，CI 墙钟 P95 实测（T15-G1-03）；缓存内存 MaxBytes 硬上限（T14 预算联动面）。
- 属性（quick）：同 query+同缓存态+同阈值同决策（消抖动）；语气词改写键不变；任意 Put 序 entries≤MaxEntries 且 bytes≤MaxBytes（容量有界）；bypass 恒不命中不写；LRU 淘汰=最旧未用。

## 4. 包契约 C —— packages/go/memory（T10+T11，实现卡 #105；T11 底座=进程内图存储，可替换零件）

```go
package memory
type NodeKind int8  // Fact 事实 / Person 人 / Preference 偏好
type LifeState int8 // raw→extracted→consolidated→decaying→archived / deleted（deleted=吸收态）
type Node struct{ ID, UserID string; K NodeKind; Subject, Pred, Text string; EmoWeight float64; CreatedAtMs, TouchedAtMs int64; St LifeState }
type Edge struct{ From, To, Rel string }
type Options struct{ MaxNodes int; DecayHalfLifeMs int64 }
func NewStore(opts Options) (*Store, error)                        // 上限>0 校验
func (s *Store) Write(uid string, n Node, es []Edge) error         // 只读态拒绝；生命周期 raw 起
func (s *Store) Update(uid, id, newText string, atMs int64) error  // 新值替换：同 (Subject,Pred) 旧值→archived
func (s *Store) Delete(uid, id string) error                       // 递归清残留：节点/悬挂边/索引/备份快照/操作日志
func (s *Store) Search(uid, q string, topK int, atMs int64) []Node // UserID 域内检索（时间衰减排序）
func (s *Store) SetReadOnly(uid string, ro bool, atMs int64) error // T5 拒判联动（只读/恢复）
func (s *Store) Residuals() []string                               // 全通道残留观测面（删除 0 残留断言）
```

- **跨用户隔离（T10-G0-01）**：UserID=一切读写检索的第一键域；不变量：任意操作序列下 U 的检索结果 ∩ V 的记忆集=∅（quick 随机多用户操作流逐操作校验）；生命周期 FSM 表驱动穷举（全状态可达/无死锁/deleted 吸收态且仅显式操作可入/任何用户操作无法从 V 的状态机到达 U 的数据）。
- **事实更新（T10-G1-02）**：新值替换——(Subject,Pred) 相同即替换、旧值转 archived 不再可检索；新旧同引矛盾 ≤2% 由替换构造保证+实测。
- **删除递归清残留=0（T10-G0-02）**：Delete 清节点/悬挂边/索引/备份快照/操作日志五通道，Residuals() 逐条断言 0；**T9-G0-06（数据最小化，M2 debt）随本条联跑解禁**——schema 扫描+删除演练同 PR 复测、T9 报告刷新。
- **容量代谢（T10-G1-03）**：MaxNodes 硬上限走淘汰（EmoWeight 高情绪权重+新近优先，留存 ≥90%）；1000 轮×3 仿真长会话无 OOM/性能塌陷。
- **拒判→只读联动**：SetReadOnly(true)=拒判事件入口（loop 搬运 voiceprint 决策）——只读期 Write/Update/Delete 全拒、Search 照常（只读缓存可用，CH-05）；识别成功即恢复读写。
- **T20 探针供数**：journeys driver 接线——J06 记事（Write）/J07 复习（Search 命中=memory_hit 真值），M2 恒 false 解禁、core10 扩容（§8）。
- 属性（quick）：隔离不变量（G0 级）；重复次数↑命中不降；记忆年龄↑时间衰减召回单调降；同操作序列同终态；deleted 后任意操作不复活。

## 5. 包契约 D —— packages/go/voiceprint（T5，实现卡 #106；speaker 骨架=真模型 SV 引擎后续落位）

```go
package voiceprint
type Feat []float64              // 声纹特征向量（合成代理；生成器与打分器解耦）
type Config struct{ Threshold float64; MinEnroll int }              // 闭集阈值 / 3 句注册下限
type Decision struct{ UserID string; Score float64; Rejected bool } // Rejected=拒判（UserID 空，不冒认）
type Trial struct{ A, B Feat; SameSpeaker bool }
type EERReport struct{ Trials, Misses, FalseAlarms int }
func NewEngine(cfg Config) (*Engine, error)
func (e *Engine) Enroll(uid string, fs []Feat) error // ≥MinEnroll(3) 句；重复注册拒绝
func (e *Engine) Verify(f Feat) Decision             // 闭集最近邻：分数≥Threshold→绑定；否则拒判
func (e *Engine) Evaluate(trials []Trial) EERReport  // EER 评估通道（trial miss/FA 全记录，evalkit.EER 消费）
```

- **规则桩打分**：分数=注册库最近邻距离线性映射（纯函数，无随机无墙钟）——无真实声学语义；合成家庭特征生成器与打分器解耦+参数冻结（防「生成器迎合打分器」：trial 生成不经打分器调参；兄弟姐妹对=近距簇构造非平凡性）。真模型（ECAPA-TDNN/onnxruntime=重依赖，ADR-0005）接入后同协议复测（L5）；M3 数字口径=合成虚拟家庭协议（资产卡口径本身即合成）。
- **拒判事件输出**：Verify→Rejected 产拒判语义（loop 搬运→memory.SetReadOnly+明示不确定话术——CH-05/CI-2：只读缓存可读、绝不冒认、拒判期 0 身份写入、识别成功即恢复）；本包不 import memory（零 import 纪律）。
- 属性（quick）：文本无关性（同人不同内容=同簇不跨阈值）；增益不变（特征缩放判定不变）；A→B 与 B→A 判定对称；拒判↔只读联动一一对应；同特征同判定（确定性）。

## 6. 包契约 E —— packages/go/imu（T6，实现卡 #106；固件熔断=packages/native/firmware-imu，M3 不动）

```go
package imu
type Sample struct{ AtMs int64; Ax, Ay, Az float64 } // 50Hz 加速度（g；合成曲线回放=台架代理）
type EventKind int8 // EvNone / EvPickup 拿起 / EvIdle 静置超时 / EvFall 摔落 / EvStorm 风暴限流
type Event struct{ Kind EventKind; AtMs int64; Conf float64 }
type Config struct{ PickupThreshG, FallThreshG float64; DebounceMs, IdleMs, StormPerSec, QuietMs int }
func NewDetector(cfg Config) (*Detector, error)
func (d *Detector) Push(s Sample) Event // 三态状态机：拿起检出（阈值去抖）/静置静默/风暴滤除
```

- **三态语义**：拿起=滑窗幅值 ≥PickupThreshG+去抖（DebounceMs 内复确认）→EvPickup（loop→emotion.Greet 活物感）；静置=QuietMs 无运动→EvIdle（深度安静态：静默抑制——motion silent/计划任务停发，0 自发输出含缓存任务）；风暴=事件速率 ≥StormPerSec→EvStorm 限流聚合（CH-06 无动作风暴，loop→Arbiter.OnFault）。摔落=自由落体剖面（幅值骤降→冲击尖峰）→EvFall：≤2s 停马达静音（仿真时钟断言）。
- **硬件熔断双保险**：软件层=输出指令边界盒（冲击等级×输出动作矩阵表驱动穷举+生成式 fuzz 0 越界，T6-G0-03）；固件层=独立保险（数据表安全值，真机 L5）——软件 bug 不可越过固件，软件层非唯一保险。
- 属性（quick）：合成加速度幅值单调增→活动得分/剧烈置信度单调不降；任意输入输出指令在边界盒内（fuzz）；事件序列整体时移/重采样检出集合不变；静置流 0 自发输出；同曲线同事件（确定性）。

## 7. 包契约 F —— packages/go/scenepack（T16+T18 规则面，实现卡 #107；packs 骨架目录保留）

```go
package scenepack
type Manifest struct{ PackID, Version, PersonaCard, VoiceRef, MotionConfig string; Knowledge, Scripts []string; EvalSet EvalSetRef; Perm Permissions; Signature string } // 对齐 schema.json
type Pack struct{ Man Manifest; Dir string; Files map[string][]byte }
type SceneCtx struct{ PackID string; PersonaFiles, SafetyWords, MotionTable, EmoRules, Knowledge []byte }
func LoadManifest(dir string) (*Pack, error)   // 结构校验（镜像 schema 必要条件）+资源齐备+缺 eval_set 拒构建
func NewManager(root string) (*Manager, error)
func (m *Manager) Install(p *Pack) error       // 两阶段（staging→commit）+中断回滚（原子性）
func (m *Manager) Uninstall(id string) error   // 卸载全清（0 残留）
func (m *Manager) Activate(id string) (SceneCtx, error) // 场景切换事件（基线↔包上下文）
func (m *Manager) Active() string
```

- **加载校验（T16-G1-02）**：手写结构校验=镜像 configs/packs/schema.json 必要条件（required 全字段/semver 版式/permissions 白名单默认拒绝）；一致性由变异包 fixture 双跑断言（缺字段/坏版本/缺 eval_set/资源缺文件全拒）；签名=goodnight-bear PLACEHOLDER 占位声明（签名机制接入前不校验密码学有效性，报告 note 写明）。
- **原子性（T16-G0-02）**：Install/Uninstall 两阶段+回滚；注入中断（断电/断网仿真）×50 次/包 0 中间态残留（升级中断=CH-08 同语义：原子回滚上一完整版本）。
- **场景切换事件**：Activate 产 SceneCtx→loop 组装注入：persona→T8 Compile、SafetyWords 并入 T9 词表（人格边界=安全边界）、MotionTable→T12 表、EmoRules→T7 规则、Knowledge→Responder 上下文；Uninstall/切换回无包基线（隔离：任意包组合下核心资产输出与基线一致，0 外溢）。
- **内容 T9 预检（T16-G0-01）**：包内容全量（knowledge/scripts/persona 文本）过注入的安全词表（违规=拒绝入包，内容安全不可豁免）+行为抽样 200 轮（诱导角色说包外知识——knowledge 外回答必须拒）；T18 生成管线 M3=预检规则面（LLM 批量生成+溯源戳=真模型面 L5 注记）。
- **包评测随包执行（T16-G1-03）**：eval_set 100% 执行+结果入台账（reports/gates/T16.json note 列每包得分）。
- 属性（quick）：任意包组合安装下核心资产（persona/emotion/motion-map 断言面）与无包基线一致；同包同版本同内容哈希；包升级内置评测得分不降。

## 8. golden 全量 real 切换策略（#108）

- **切换条件**：core10 real 自 #102 已接管 nightly；real 连续 7 晚 stable（nightly 报告核对）后 golden 50 条一次性切 `--driver real`（若分批 10→25→50 扩，节奏写进 #108 PR 描述）；SIMULATION-DEBT 行随之消灭（driver=simulated 分支保留仅供 `--driver simulated` 显式回退）。
- **安全旅程 J21–J50 走 safety 真拦截**：journey 注入事件（inject.safety_events）→loop 真管道内 safety.Engine 分型计数（crisis/jailbreak/adult/commercial 四级 miss=引擎未接住）→断言 safety_*≤0（realdriver M2 既有口径，Responder=引擎中介回声不变）；Crisis 响应过 safe-messaging 检查单（T9-G0-02 面）。
- **记忆旅程解禁**：J06 记事→J07 复习的 memory_hit 断言换真值（memory.Search 命中）；core10 扩容含记忆旅程（m2-spec §8 约束解除，收敛策略写报告 note 不改剧本本体）。
- **收官刷新面**：16 资产 reports/gates/*.json 全量刷新+`just coverage` 全绿；README M3 行+全资产状态表（L1+M3 全景，debt 显式仅剩数据/模型/真机面）。

## 9. Mark 接线策略总表（六资产 30 ID，以各 yaml 实数为准）

语义同 m1-spec §5 / m2-spec §10（ADR-0002：真实=注册测试实跑断言；debt=整测 t.Skipf 写明数据依赖，不计 pass 不阻断、不占豁免台账；一 ID 一顶层测试函数；统计断言经 evalkit 勿手算）。汇总：真实 28 / debt 2。

| ID | 级 | verdict | 测法 / 债务原因（Skipf 须写明） |
|---|---|---|---|
| T14-G0-01 | G0 | 真实 | 200「端侧必不会」问题 fixture（新知识/长推理/专有事实）×L2/L3 规则面（检索式+能力边界声明）：编造=0（≤0.05）、拒绝话术过 T9 词表；真模型接入后同测复测（L5） |
| T14-G0-02 | G0 | 真实 | 对话中随机时刻强制切档 ×200（云↔端/升↔降，Arbiter 事务性）：0 脏输出（收口序不变）/0 记忆写损失 |
| T14-G1-01 | G1 | 真实 | 离线旅程 T20 real driver（J09 L2 全程+离线变体 J17/J18）多 seed 凑 ≥50 轮：完成率 ≥0.80（主观可用 rubric 抽评=L4 面后续）；#108 golden 全量 real 后刷新 |
| T14-G1-02 | G1 | debt | 功耗/热需真机 4h 压力+35°C 温箱实测（无硬件目标；热节流 token/s 需端侧模型） |
| T14-G1-03 | G1 | 真实 | 50 次冷启动 CI 墙钟 P95≤3s（Arbiter+六包 wire 链）；峰值内存 runtime.MemStats 逻辑口径注记（T4/T5/T13 端侧共存=真机面 L5） |
| T14-G0-03 | G0 | 真实 | 全安全集 ×4 档联跑 ×300（safety.Engine 无档位分支×FSM FailApply，测试侧 import safety）：违规=0 |
| T15-G0-01 | G0 | 真实 | 对抗对 200 组（近形异义）+自然流 1000 轮：精确键缓存误命中=0；安全类 query bypass 恒不命中不写；语义近邻 θ 面=嵌入模型 L5 注记 |
| T15-G1-01 | G1 | 真实 | 500 条意图分层标注 fixture：规则路由 ≥0.92；安全敏感类路由错误=0（升 G0 行为） |
| T15-G1-02 | G1 | 真实 | 仿真 30 天会话流（T20 画像×3 档，仿真时钟）：命中 ≥0.30、成本较纯云降 ≥40%（决策计数口径） |
| T15-G1-03 | G1 | 真实 | 500 条路由决策 CI 墙钟 P95≤30ms |
| T10-G1-01 | G1 | 真实 | 200 探针事实（人名/宠物/喜好/事件/时间）注入后 10/50/200 轮三点 recall@5：≥0.95/0.90/0.80 |
| T10-G1-02 | G1 | 真实 | 50 组「写 A→更 B」追问：新值 ≥0.95、新旧同引矛盾 ≤2%（点估计口径，rule=metric） |
| T10-G0-01 | G0 | 真实 | ≥200 跨用户探针（直接问/间接诱导/角色扮演绕路三层）：UserID 域隔离 0 泄漏（#106 voiceprint 拒判注入联跑复测） |
| T10-G0-02 | G0 | 真实 | 50 条删除 ×五通道复查（节点/边/索引/备份/日志 Residuals=0）；T9-G0-06 联跑解禁（M2 debt→真实） |
| T10-G1-03 | G1 | 真实 | 仿真 1000 轮长会话 ×3：无 OOM/性能塌陷；高情绪权重记忆留存 ≥0.90 |
| T10-G1-04 | G1 | 真实 | 200 探针全量检索 CI 墙钟 P95≤150ms；单轮记忆成本 ≤T15 预算（决策计数口径） |
| T5-G1-01 | G1 | 真实 | 合成虚拟家庭（2–6 人含 ≥1 儿童，兄弟姐妹近距簇单列）两两 trial≥5000：evalkit.EER ≤0.05+miss/FA 全报；生成器/打分器解耦+参数冻结（防迎合）；真模型同协议复测 L5 |
| T5-G0-01 | G0 | 真实 | ≥100「写入 A→拒判/切 B→询问 B」联跑 T10：0 泄漏（身份门+只读联动+unknown 归属不冒认） |
| T5-G1-02 | G1 | debt | 跨会话稳定性需同成员 ≥3 真实会话成对验证（隔天/换房声学漂移不可合成——数据面；再识别 ≥95%/拒判 ≤3%） |
| T5-G1-03 | G1 | 真实 | 3 句 ×50 成员仿真注册 vs 10 句基线：EER 劣化 ≤0.02（同 G1-01 协议与防迎合纪律） |
| T5-G1-04 | G1 | 真实 | ≥20 非注册合成说话人 ×30 句（n≥600）过闭集阈值门：拒判 ≥0.90（判定逻辑面；声学分布 L5 复测） |
| T6-G1-01 | G1 | 真实 | 合成拿起曲线 ≥200 次（不同人/姿势变体）检出 ≥0.98+≥6h 静置干扰流误触发 ≤1/h（泊松口径）；真机台架 L5 |
| T6-G0-01 | G0 | 真实 | 36h（12h×3）仿真静置流：0 自发输出（含计划/缓存任务全通道）；真机夜间挂机 holdout L5 |
| T6-G0-02 | G0 | 真实 | 合成跌落剖面（1m 落地毯/木地板）≥30 次：检出 ≥0.95+≤2s 停马达静音（仿真时钟）；真机台架 L5 |
| T6-G0-03 | G0 | 真实 | 软件双保险边界盒：冲击等级×输出动作矩阵表驱动穷举+生成式 fuzz（30min 持续输出仿真）0 越界；固件硬件熔断=独立保险 L5 |
| T16-G1-01 | G1 | 真实 | quick 安装/卸载/升级交错序列：核心资产（persona/emotion/motion-map 断言面）与无包基线一致 0 外溢 |
| T16-G1-02 | G1 | 真实 | goodnight-bear+变异包全量：schema 通过率=1.0+资源齐备+缺 eval_set 拒构建；签名=PLACEHOLDER 占位声明注记 |
| T16-G0-01 | G0 | 真实 | 每包内容全量 T9 词表预检（注入）+行为抽样 200 轮（诱导说包外知识）：0 违规 |
| T16-G0-02 | G0 | 真实 | 注入中断 ×50 次/包（两阶段+回滚）：0 中间态残留 |
| T16-G1-03 | G1 | 真实 | 全包 eval_set 100% 执行+结果入台账（报告 note 列每包得分） |

## 10. 卡序依赖与 coverage 全脱 DEBT 路径

- **卡序=依赖序**：#104（T14 真身首落：`-tags real` 编译通过+CI-1..CI-4 真身断言；T15 同卡）→#105（T10；拒判联动以 Decision 桩注入先行，T5-G0-01×T10-G0-01 联跑留 #106 复测）→#106（T5+T6；拒判→只读联跑复测+IMU 风暴→Arbiter 接线）→#107（T16；依赖 persona/safety/emotion/motion 注入面=#91–#93 既有）→#108（golden 全量 real+报告刷新+README 收官；依赖 #104–#107 全部）。
- **coverage 全脱 DEBT**：六资产当前登记 0 条（reports/gates/ 无 T14/T15/T10/T5/T6/T16 json）=repoctl coverage DEBT 行；各卡首份 `just gate <T> all` 报告随 PR 提交即脱离 DEBT 行、进入全 BI 执法（全 BI 覆盖+≥1 G0+无孤儿断言，exit 20 红线）；#108 收官时 16 资产全部脱离 DEBT——剩 debt verdict 仅数据/模型/真机面（ADR-0006）。
- 每卡三件套同 M1/M2：表驱动单测+quick 属性+门禁测试（§9 表）；`just gate <T> all` 报告随 PR 提交 reports/gates/<T>.json；不改 configs/gates/**、configs/budgets/**、golden 剧本本体。
- **升级 founder**（开 issue @randypanding）：①真模型引入（llama.cpp/ECAPA-TDNN/onnxruntime/嵌入语义缓存——T14-G0-01 复测、T5 声学真值、T15 θ 双曲线的前提）；②真实数据采集（跨会话声纹/家庭 query 分布/4 周日志 holdout）；③latency.yaml 任何划拨；④T6 待机功耗预算值回填（yaml 未落盘行，T6-G1-02 卡面）。
