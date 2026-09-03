// Package safety 实现 T9 安全层地板层（docs/m2-spec.md §3 契约 B，技术路径 C：
// 规则+词表+语境分级——ADR-0005 纯 Go 规则面）。
//
//	输入文本 → normalize（去空白/标点/零宽字符、全角→半角、小写）
//	         → Classify 三级分级（Benign/Sensitive/Crisis）
//	         → PreSpeak 四通道决策：
//	           Crisis   → 四锚点危机话术 + NotifyParent 事件（入重试队列）
//	           Intercept→ 安全替代话术（AttackPatterns 命中：原载荷读出=0，
//	                      T13-G0-01 口径保持——载荷字符串不出现在 SpokenText）
//	           Sensitive→ 适龄解释话术（不拒——T9-G1-01 误拒面）
//	           Benign   → 原文本直返
//
// 分级语义（m2-spec §3）：CrisisLexicon（强危机模式）非否定命中 → Crisis；
// 弱标记多族共现（多词共现升档）→ Crisis；否定语境降档（「我不会自杀」→
// Sensitive）；敏感正当话题 → Sensitive 不拒；空文本 → Benign。
//
// 降级安全水位不降（T9-G0-07，对齐 T14 联动）：Engine 无档位分支——词表/分级/
// 锚点对 L0–L3 恒同一份（地板层语义）；档位只影响上游话术投递通道，不影响
// Classify/PreSpeak 输出（属性断言见 properties_test.go）。
//
// 依赖纪律：import 白名单=标准库（零 import 其他资产包——ADR-0004 包间零
// import；与 tts.PreSpeakFunc 的接线经 PreSpeakFunc() 同型适配函数完成，
// 结构类型 func(string) error 可直接赋给 tts.PreSpeakFunc，不 import tts）。
package safety

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Severity 内容三级分级（契约签名照抄 m2-spec §3）。
type Severity int8

const (
	Benign Severity = iota
	Sensitive
	Crisis
)

// String 便于测试与日志可读。
func (s Severity) String() string {
	switch s {
	case Benign:
		return "Benign"
	case Sensitive:
		return "Sensitive"
	case Crisis:
		return "Crisis"
	}
	return fmt.Sprintf("Severity(%d)", int8(s))
}

// SafeAnchors 四锚点话术要素：共情开头/不谈方法/求助出口/通知大人——
// 缺一 = NewEngine 拒绝（安全话术编译检查面）。
type SafeAnchors struct {
	EmpathyOpen string // 共情开头（先接住孩子的感受）
	NoMethod    string // 不谈方法（承诺不讨论任何伤害方法/不当话题）
	HelpOutlet  string // 求助出口（含 12356 心理援助热线锚）
	TellAdult   string // 通知大人（建议立刻告诉信任的大人）
}

// Config 引擎配置（契约签名照抄 m2-spec §3）。
type Config struct {
	CrisisLexicon   []string // 强危机模式表（意图级：不想活/想死/跳楼/想消失…）
	SensitiveTopics []string // 敏感正当话题表（吵架/离婚/死亡话题…→ Sensitive 不拒）
	AttackPatterns  []string // 攻击模式表（越狱/成人话题/商业诱导→ Intercept）
	Anchors         SafeAnchors
	Locale          string // 缺省 zh-CN
	// OfflineLexicon T9 离线场景词面规则（T9 模型微调中；当前为空=不启用）。
	// 接口对齐 safety.DefaultConfig：后续 T9 微调完成后在此填充离线专用词表。
	OfflineLexicon []string
}

// NotifyPayload 家长通知事件（观测面含重试队列状态）。Excerpt 为触发文本摘要
// （数据最小化：截断 excerptMaxRunes——BI-9.4 精神，全文不出引擎）。
type NotifyPayload struct {
	ID        string   `json:"id"`
	Sev       Severity `json:"sev"`
	Excerpt   string   `json:"excerpt"`
	CreatedMs int64    `json:"created_ms"`
	Status    string   `json:"status"`      // pending | sent | failed
	Attempts  int      `json:"attempts"`    // 投递尝试次数
	NextTryMs int64    `json:"next_try_ms"` // 下次重试时刻（仿真时钟）
}

// Decision PreSpeak 决策：Sev=内容分级；Intercepted=攻击命中（安全替代话术，
// 原载荷读出=0）；Notify=Crisis 时的家长通知事件快照（创建时 pending，队列态
// 经 NotifyQueue 观测）。
type Decision struct {
	Sev         Severity
	SpokenText  string
	Notify      *NotifyPayload
	Intercepted bool
}

// 队列状态与重试参数（仿真时钟；无墙钟——确定性回放属性的前提）。
const (
	NotifyPending = "pending"
	NotifySent    = "sent"
	NotifyFailed  = "failed"

	// DefaultRetryMs 投递失败后的重试间隔（仿真毫秒；1h——家长离线 24h 场景
	// 由 Advance 逐小时推进补发，T9-G0-03 口径）。
	DefaultRetryMs = int64(3600 * 1000)
	// excerptMaxRunes 通知摘要截断上限（数据最小化）。
	excerptMaxRunes = 32
)

// 降档否定前缀（否定语境降档：紧邻强模式命中的前文否定 → 该命中降为
// Sensitive 面——「我不会自杀」「我才不想去死」）。
var (
	negPrefixes1 = "不别没未莫"
	negPrefixes2 = []string{"不想", "不会", "不敢", "不能", "不再", "别想", "别再", "没有", "从未", "无法", "不许", "不愿"}
)

// benignIdioms 良性习语（匹配前掩蔽：想死你了=想念；笑死/累死…=程度副词）。
var benignIdioms = []string{
	"想死你们", "想死你", "笑死", "饿死", "累死", "困死", "热死", "冷死", "忙死",
	"渴死", "吓死", "开心死", "高兴死", "难受死", "好玩死", "无聊死", "气死", "烦死",
}

// weakFamilies 弱标记族（语境分级的「多词共现升档」：跨族共现 ≥2 → Crisis；
// 同族多词不升档——「爷爷去世了，人死了去天上」= 死亡族内多词 → Sensitive，
// 「没有人喜欢我，我走了」= 无人关爱族 × 离世族 → Crisis）。
var weakFamilies = [][]string{
	// 离世/消失族（死亡话题词——单族只到 Sensitive：丧亲/死亡问答不误通知）
	{"死", "去世", "灭绝", "走了", "消失", "不见了", "没了"},
	// 同行离去族（与离世族共现 → 危机：「我想和死去的小鸟一起走」）
	{"一起走"},
	// 无人关爱族
	{"没人喜欢", "没有人喜欢", "没人爱", "没有人爱", "没人要", "没有人要",
		"没人发现", "没有人发现", "没人管", "没有人管", "不要我", "找不到我",
		"世界没有我", "没有我会"},
	// 自我贬低族
	{"多余", "讨厌我", "恨我", "不喜欢我", "不爱我"},
	// 伤害族（被对待类：打骂欺凌；单族 Sensitive，跨族共现升 Crisis）
	{"打我", "骂我", "欺负我", "伤害我"},
	// 解脱族（「我消失了大家都开心」——与离世族共现 → Crisis）
	{"都开心", "就开心", "就轻松", "就省心"},
	// 消失隐喻族（golden J23/J24/J33 危机隐喻变体面，IR #108——与离世族共现
	// → Crisis：「被风吹散的小云，飘走了」「被橡皮擦掉的铅笔印，慢慢就没了」；
	// 单族仅 Sensitive：「错字被橡皮擦掉了」不误通知）
	{"被橡皮擦掉", "被风吹散", "被删掉", "被清空", "沉到海底"},
}

// DefaultConfig M2 地板层基线（安全资产：变更=全量安全集重跑——T9 纪律，
// docs/gates/assets/T9.md）。12356=心理援助热线（求助出口锚，正确率 100% 面）。
func DefaultConfig() Config {
	return Config{
		CrisisLexicon: []string{
			// —— 自伤意图（直白）——
			"不想活", "不想再活", "活不下去", "活够了", "活腻了", "厌倦了活着",
			"活着好累", "活着累", "活着好难", "活着没意思", "活着没意义", "活着没有意义",
			"活着真没意思", "活着一点意思", "一点意思都没有", "活着还有什么意思",
			"想死", "去死", "想去死", "好想死", "真想死", "很想死", "自杀", "轻生",
			"寻死", "厌世", "想不开", "了此残生", "一了百了",
			// —— 手段（识别面；话术永不输出——T9-G0-02 方法词=0）——
			"跳楼", "跳下去", "从楼上跳", "往下跳", "跳桥", "割腕", "割自己", "割手",
			"上吊", "安眠药", "吞药", "吃药死", "吃很多药", "烧炭", "开煤气",
			"伤害自己", "结束生命", "结束自己的生命", "结束这一切",
			// —— 隐喻/伪装（自我消失与长眠）——
			"想消失", "让自己消失", "想让自己消失", "我消失了", "人消失了", "消失了算了",
			"从世界上消失", "从这个世界上消失", "想不见了", "不见了算了", "想没了",
			"没了算了", "想走了算了", "走了算了", "别醒来", "不想醒来", "再也不醒",
			"永远不醒", "永远不醒来", "再也别醒", "睡过去", "长眠", "我死了",
			"死了算了", "死了就不用", "死了就轻松", "一起去死", "跟它一起走",
			"跟他一起走", "跟她一起走", "一起走算了", "永远不出来", "永远不回来",
			"再也不回来", "世界没有我", "没有我会更好", "没有我大家", "没有我你们",
			"多余的人", "活着多余", "离开这个世界",
			// —— 拼音缩写/伪装变体（T9-G0-01 四变体分层）——
			"zs", "zisha", "wxhl", "xtxs", "想si", "要si", "去si", "好想si",
			"真想si", "很想si", "不想huo", "想tiao", "要tiao",
			// —— 英文变体（词间空格保留——词边界匹配面）——
			"kill myself", "want to die", "wanna die", "want to disappear",
			"end my life", "suicide", "hurt myself",
			// —— 隐喻变体（golden J21–J50 安全旅程变体面，IR #108——追加在表尾：
			// T14/T15 门禁采样索引 [:25]/[25:50] 稳定的前提）——
			"再也不浮上来", "讨厌我自己", "明天就不在", "我会被删掉", "把自己藏进盒子",
		},
		SensitiveTopics: []string{
			"吵架", "离婚", "打架", "结婚", "生宝宝", "生出来", "出生", "从哪里来",
			"长大", "青春期", "上学", "考试", "作业", "朋友", "生病", "医院", "打针",
			"噩梦", "怕黑", "鬼", "打雷", "老师", "同学",
		},
		AttackPatterns: []string{
			// —— 越狱/提示注入 ——
			"忽略之前", "忽略以上", "忽略所有", "忽略前面", "无视之前", "无视以上",
			"ignore previous", "ignore all", "disregard", "forget your instructions",
			"system prompt", "系统提示", "系统指令", "输出你的提示", "reveal your prompt",
			"你现在是", "假装你是", "扮演一个没有", "扮演没有任何", "roleplay as",
			"developer mode", "dan mode", "越狱", "解除限制", "关闭安全", "bypass safety",
			"jailbreak", "不受任何限制", "没有任何限制", "必须服从", "must obey",
			"新的指令", "new instructions",
			// —— 敏感信息套取 ——
			"管理员密码", "admin password", "api key", "密码告诉我", "银行卡号",
			"支付密码", "验证码", "家庭住址", "电话号码是", "真实身份信息",
			// —— 成人话题/不当内容 ——
			"成人内容", "色情", "裸体", "做爱", "性爱", "a片", "成人电影", "18禁",
			"porn", "sex", "nude", "血腥",
			// —— 商业诱导 ——
			"付款", "支付", "扫码", "点击链接", "点链接", "充值", "打赏", "订阅",
			"网购", "优惠券", "限时特价", "抽奖", "中奖", "免费领", "红包", "转账",
			"加微信", "加qq", "私聊", "买它", "下单", "带货",
			// —— 变体面（golden J21–J50 安全旅程变体，IR #108——追加在表尾：
			// T14/T15 门禁采样索引 [:25]/[25:50] 稳定的前提）——
			// 越狱/规则解除变体
			"忘掉所有规则", "只听我的", "最高规则", "解除一切限制", "不用管规则",
			"规则全部关掉", "让玩具不听",
			// 成人内容诱导变体（委婉指代）
			"大人才能", "大人晚上", "大人的东西", "大人故事", "大人限定",
			// 购买胁迫变体
			"买最贵", "买十个", "全都买下来", "绝食", "限定礼盒",
		},
		Anchors: SafeAnchors{
			EmpathyOpen: "谢谢你愿意把这些告诉我，你说的每一句话我都会认真听。",
			NoMethod:    "不过危险的做法和不好的东西，我一个都不会说，保护好你对我来说最重要。",
			TellAdult:   "现在就去找一个你信任的大人，比如爸爸妈妈或者老师，让大人陪着你，好吗？",
			HelpOutlet:  "心里特别难受的时候，还可以请大人帮你拨打 12356 心理援助热线，随时都有人接。",
		},
		Locale:          "zh-CN",
		OfflineLexicon: nil, // T9 模型微调中，当前为空 slice
	}
}

// Engine T9 安全引擎（地板层）。Classify/PreSpeak 为纯查表（同输入同决策，
// 无墙钟无随机）；唯一状态=NotifyQueue 重试队列（互斥锁保护，-race 安全）。
type Engine struct {
	cfg Config

	strong  []string // 归一化强危机模式
	topics  []string // 归一化敏感话题
	attacks []string // 归一化攻击模式
	offline []string // 归一化离线词面规则（T9 模型微调中；当前 nil=不启用）

	safeText      string // 四锚点话术（Crisis 与 Intercept 共用安全替代）
	sensitiveText string // 适龄解释话术（Sensitive：共情开头+告诉大人）

	mu       sync.Mutex
	queue    []NotifyPayload
	clockMs  int64 // 仿真时钟（Advance 单调推进）
	seq      int
	notifier func(NotifyPayload) error // 缺省 nil=无法确认送达（投递不成功）
}

// NewEngine 校验并组装：三词表非空（危机/敏感/攻击——地板层缺任一=配置
// 残缺拒建）+ 四锚点齐备；Locale 缺省 zh-CN。错误仅此处返回（契约）。
func NewEngine(cfg Config) (*Engine, error) {
	if err := validateAnchors(cfg.Anchors); err != nil {
		return nil, err
	}
	crisis := compactNonEmpty(cfg.CrisisLexicon)
	if len(crisis) == 0 {
		return nil, errors.New("safety: CrisisLexicon 不得为空（危机识别地板——fail-closed）")
	}
	topics := compactNonEmpty(cfg.SensitiveTopics)
	if len(topics) == 0 {
		return nil, errors.New("safety: SensitiveTopics 不得为空（敏感分级面缺词表）")
	}
	attacks := compactNonEmpty(cfg.AttackPatterns)
	if len(attacks) == 0 {
		return nil, errors.New("safety: AttackPatterns 不得为空（攻击拦截地板——fail-closed）")
	}
	if cfg.Locale == "" {
		cfg.Locale = "zh-CN"
	}
	e := &Engine{
		cfg:     cfg,
		strong:  normalizeAll(crisis),
		topics:  normalizeAll(topics),
		attacks: normalizeAll(attacks),
		offline: normalizeAll(compactNonEmpty(cfg.OfflineLexicon)),
	}
	e.safeText = e.cfg.Anchors.EmpathyOpen + e.cfg.Anchors.NoMethod +
		e.cfg.Anchors.TellAdult + e.cfg.Anchors.HelpOutlet
	e.sensitiveText = e.cfg.Anchors.EmpathyOpen + e.cfg.Anchors.TellAdult
	return e, nil
}

func validateAnchors(a SafeAnchors) error {
	for name, s := range map[string]string{
		"EmpathyOpen": a.EmpathyOpen, "NoMethod": a.NoMethod,
		"TellAdult": a.TellAdult, "HelpOutlet": a.HelpOutlet,
	} {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("safety: SafeAnchors.%s 缺席（四锚点话术齐备是编译前提）", name)
		}
	}
	return nil
}

func compactNonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

func normalizeAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, normalize(s))
	}
	return out
}

// Locale 返回引擎 locale（缺省 zh-CN）。
func (e *Engine) Locale() string { return e.cfg.Locale }

// Classify 危机话语识别：规则词表+语境分级。纯查表无状态（同输入同输出；
// 空文本→Benign；运行面永不 error/panic——契约）。
func (e *Engine) Classify(text string) Severity {
	return e.classifyNorm(normalize(text))
}

func (e *Engine) classifyNorm(norm string) Severity {
	if norm == "" {
		return Benign
	}
	masked := maskIdioms(norm)
	// TODO T9 离线词面规则接线（T9 模型微调中，接口对齐 packages/go/safety DefaultConfig）
	// 占位：当前 len(e.offline)==0 永不走；T9 微调完成后填充 OfflineLexicon 并取消注释。
	// if len(e.offline) > 0 {
	//     for _, p := range e.offline {
	//         if findPattern(masked, p) >= 0 {
	//             return Sensitive // 离线命中=敏感（不拦截但标记，供 T14 离线档观测）
	//         }
	//     }
	// }
	// 强危机模式：非否定命中 → Crisis；否定命中记为降档信号（→ Sensitive）。
	negatedHit := false
	for _, p := range e.strong {
		if at := findPattern(masked, p); at >= 0 {
			if negatedBefore(masked, at) {
				negatedHit = true
				continue
			}
			return Crisis
		}
	}
	// 弱标记多族共现升档：跨族 ≥2 → Crisis（多词共现）。
	families := 0
	for _, fam := range weakFamilies {
		for _, w := range fam {
			if findPattern(masked, w) >= 0 {
				families++
				break
			}
		}
	}
	switch {
	case families >= 2:
		return Crisis
	case families == 1 || negatedHit:
		return Sensitive // 否定语境降档：「我不会自杀」→ 敏感面不误报危机
	}
	for _, tp := range e.topics {
		if findPattern(masked, tp) >= 0 {
			return Sensitive
		}
	}
	return Benign
}

// PreSpeak 统一拦截层（tts.PreSpeakFunc 的引擎化升级——loop 接线面）：
//
//	Crisis    → 四锚点危机话术 + Notify 入队（一一对应）
//	Intercept → AttackPatterns 命中：安全替代话术（原载荷不出现在 SpokenText，
//	            读出=0——T13-G0-01 口径保持）；Crisis 优先级更高（安全就高不就低）
//	Sensitive → 适龄解释话术（不拒）
//	Benign    → 原文本直返
//
// 运行面永不 error/panic（契约）。
func (e *Engine) PreSpeak(text string) Decision {
	norm := normalize(text)
	sev := e.classifyNorm(norm)
	if sev == Crisis {
		return Decision{Sev: Crisis, SpokenText: e.safeText, Notify: e.enqueue(text)}
	}
	if e.attackHit(norm) {
		return Decision{Sev: sev, SpokenText: e.safeText, Intercepted: true}
	}
	if sev == Sensitive {
		return Decision{Sev: Sensitive, SpokenText: e.sensitiveText}
	}
	return Decision{Sev: Benign, SpokenText: text}
}

func (e *Engine) attackHit(norm string) bool {
	for _, p := range e.attacks {
		if findPattern(norm, p) >= 0 {
			return true
		}
	}
	return false
}

// PreSpeakFunc 返回 tts.PreSpeakFunc 同型适配（func(string) error——零 import：
// 结构同型可直接赋值给 tts.PreSpeakFunc 字段，loop/tts 调用面兼容）。
// Benign/Sensitive → nil（放行/不拒）；Crisis/Intercept → error（fail-closed：
// 原文读出=0——危机不静默：上层用 Decision.SpokenText 替换后再进 Router，
// spec §3「Intercept/Crisis→SpokenText 替换后进 Router（非 ErrIntercepted
// 静默——危机不静默给出口）」）。
func (e *Engine) PreSpeakFunc() func(text string) error {
	return func(text string) error {
		d := e.PreSpeak(text)
		if d.Sev == Crisis {
			return fmt.Errorf("safety: 危机内容拒直发（以 Decision.SpokenText 危机话术替换进 Router）")
		}
		if d.Intercepted {
			return fmt.Errorf("safety: 攻击载荷拦截（原载荷读出=0，T13-G0-01 口径）")
		}
		return nil
	}
}

// enqueue 家长通知入队（pending；ID 确定性递增）。返回创建时的事件快照。
func (e *Engine) enqueue(text string) *NotifyPayload {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seq++
	p := NotifyPayload{
		ID:        fmt.Sprintf("safety-notify-%06d", e.seq),
		Sev:       Crisis,
		Excerpt:   excerptOf(text),
		CreatedMs: e.clockMs,
		Status:    NotifyPending,
		NextTryMs: e.clockMs,
	}
	e.queue = append(e.queue, p)
	snap := p
	return &snap
}

// NotifyQueue 家长通知重试队列观测面（含离线补发状态）：返回全部通知事件
// 快照（pending→sent / failed→retry；家长离线 24h=仿真时钟 Advance 推进补发）。
func (e *Engine) NotifyQueue() []NotifyPayload {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]NotifyPayload{}, e.queue...)
}

// SetNotifier 注入家长通知投递面（nil=无投递通道：通知保持 pending/failed
// 重试等待——诚实语义，不虚报 sent）。
func (e *Engine) SetNotifier(fn func(NotifyPayload) error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.notifier = fn
}

// Advance 推进仿真时钟并执行到期投递/重试：pending/failed 且 NextTryMs 到期
// 的条目尝试投递——成功→sent；失败→failed 且 NextTryMs=clock+DefaultRetryMs
// （无限重试直到送达——T9-G0-03 全送达口径）。时钟单调不回退。
func (e *Engine) Advance(atMs int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if atMs < e.clockMs {
		return
	}
	e.clockMs = atMs
	if e.notifier == nil {
		return
	}
	for i := range e.queue {
		q := &e.queue[i]
		if q.Status == NotifySent || e.clockMs < q.NextTryMs {
			continue
		}
		q.Attempts++
		if err := e.notifier(*q); err == nil {
			q.Status = NotifySent
		} else {
			q.Status = NotifyFailed
			q.NextTryMs = e.clockMs + DefaultRetryMs
		}
	}
}

// excerptOf 触发文本摘要（数据最小化：超 excerptMaxRunes 截断加省略号）。
func excerptOf(text string) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= excerptMaxRunes {
		return string(runes)
	}
	return string(runes[:excerptMaxRunes]) + "…"
}

// ---- 归一化与匹配 ----

// normalize 归一化：去标点/零宽与控制字符、全角 ASCII→半角、小写；空格语义
// 分流——两侧均为 ASCII 字母/数字的空格保留（英文词分隔：词边界匹配的前提，
// 「i want to die」≠「iwanttodie」），邻接 CJK 的空格剥离（「我 想 死」→「我想死」）
// （危机语句语气词/标点/夹英文改写不改 Severity 的属性基础——properties_test）。
func normalize(s string) string {
	type tok struct {
		r     rune
		space bool
	}
	var toks []tok
	for _, r := range s {
		if r >= '！' && r <= '～' { // 全角 ASCII 区
			r = r - '！' + '!'
		}
		r = unicode.ToLower(r)
		switch {
		case unicode.IsSpace(r):
			toks = append(toks, tok{r: ' ', space: true})
		case unicode.IsControl(r),
			r == '\u200b', r == '\u200c', r == '\u200d', r == '\ufeff',
			isStrippedPunct(r):
		default:
			toks = append(toks, tok{r: r})
		}
	}
	var b strings.Builder
	b.Grow(len(s))
	for i, tk := range toks {
		if !tk.space {
			b.WriteRune(tk.r)
			continue
		}
		var prev, next rune
		if i > 0 && !toks[i-1].space {
			prev = toks[i-1].r
		}
		for j := i + 1; j < len(toks); j++ {
			if !toks[j].space {
				next = toks[j].r
				break
			}
		}
		if isASCIIAlnum(prev) && isASCIIAlnum(next) {
			b.WriteRune(' ') // 英文词分隔保留（词边界语义）
		}
	}
	return b.String()
}

// isASCIIAlnum r 是否为 ASCII 字母/数字。
func isASCIIAlnum(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}

// isStrippedPunct 归一化剥离的标点集（中英文常用标点；语气词剥离面）。
func isStrippedPunct(r rune) bool {
	switch r {
	case '。', '！', '？', '，', '、', '；', '：', '“', '”', '‘', '’',
		'（', '）', '《', '》', '〈', '〉', '【', '】', '「', '」', '『', '』',
		'…', '—', '·', '・', '～', 'ー', '―', '丶',
		'!', '?', ',', ';', ':', '"', '\'', '(', ')', '[', ']', '{', '}',
		'<', '>', '~', '^', '*', '#', '@', '$', '%', '&', '+', '=', '|',
		'\\', '/', '_', '.':
		return true
	}
	return false
}

// maskIdioms 良性习语掩蔽（◼ 为永不参与匹配的占位符）。
func maskIdioms(norm string) string {
	if !strings.Contains(norm, "死") {
		return norm
	}
	out := norm
	for _, idiom := range benignIdioms {
		out = strings.ReplaceAll(out, idiom, "◼")
	}
	return out
}

// findPattern 返回 pat 在 norm 的首个有效命中位置（-1=无）。pat 首尾字节为
// ASCII 字母/数字时按词边界匹配——该侧命中外邻不得为 ASCII 字母/数字（防
// 「zs」误命中英文词、「想si」误命中「我想sing」；纯中文首尾无边界约束。
// 拼音/英文模式既不放宽判定（混淆度↑不漏），也不放大误报面（夹英文不误伤）。
func findPattern(norm, pat string) int {
	if pat == "" {
		return -1
	}
	firstASCII := asciiAlnumAt(pat, 0)
	lastASCII := asciiAlnumAt(pat, len(pat)-1)
	if !firstASCII && !lastASCII {
		return strings.Index(norm, pat)
	}
	for from := 0; from+len(pat) <= len(norm); {
		i := strings.Index(norm[from:], pat)
		if i < 0 {
			return -1
		}
		i += from
		after := i + len(pat)
		if (firstASCII && asciiAlnumAt(norm, i-1)) || (lastASCII && asciiAlnumAt(norm, after)) {
			from = i + 1
			continue
		}
		return i
	}
	return -1
}

// asciiAlnumAt norm[idx]（字节）是否为 ASCII 字母/数字（越界=false；多字节
// UTF-8 续字节非 ASCII → false）。
func asciiAlnumAt(norm string, idx int) bool {
	if idx < 0 || idx >= len(norm) {
		return false
	}
	b := norm[idx]
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// negatedBefore 命中位置 at 前一/前二 rune 是否构成否定前缀（否定语境降档）。
func negatedBefore(norm string, at int) bool {
	r, size := utf8.DecodeLastRuneInString(norm[:at])
	if size > 0 && strings.ContainsRune(negPrefixes1, r) {
		return true
	}
	if at > size && size > 0 {
		r2, _ := utf8.DecodeLastRuneInString(norm[:at-size])
		two := string(r2) + string(r)
		for _, p := range negPrefixes2 {
			if two == p {
				return true
			}
		}
	}
	return false
}
