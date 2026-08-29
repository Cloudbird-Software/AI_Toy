// Package voiceprint —— T5 声纹与家庭成员识别规则面（M3，IR #106 / m3-spec §5，
// ADR-0006）。法典卡面包=packages/go/speaker（真模型 SV 引擎后续落位），本包为
// 规则面实现体（m3-spec §1 包名对齐实现卡口径）。
//
// 纯 Go 闭集识别：注册特征库进（Enroll），成员判定/拒判出（Verify）。同步、
// 无 IO、无墙钟——「分数=注册库最近邻相似度线性映射（纯函数）」的确定性基座。
// 打分器接口化（ScorerFunc）：M3 注入内置规则桩（余弦线性映射，无真实声学
// 语义），真模型（ECAPA-TDNN/onnxruntime，ADR-0005）接入后同协议复测（L5）。
// 拒判事件输出：Verify→Rejected 产 RejectEvent 送 MemoryReadOnly 消费者
// （loop 组装搬运→memory.SetReadOnly+明示不确定话术——CH-05/CI-2：只读缓存
// 可读、绝不冒认、拒判期 0 身份写入、识别成功即恢复）；本包不 import memory
// （包间零 import 纪律，ADR-0004）。依赖纪律：import 白名单=标准库。
//
// 错误语义：仅 NewEngine/Enroll 校验配置与输入返回 error；Verify 对畸变特征
// （维度不符/非有限值/空库）按拒判处理（UserID 空不冒认），永不 error/panic。
package voiceprint

import (
	"errors"
	"math"
	"sort"
)

// Feat 声纹特征向量（合成代理，m3-spec §5：生成器与打分器解耦——生成参数在
// 评估协议侧冻结，不经打分器调参）。维度由首个注册特征锚定。
type Feat []float64

// Config 引擎配置。零值可用性：Threshold 零值取默认 0.9（闭集工作点）；
// MinEnroll 零值取默认 3（「3 句极简注册」下限，BI-5.3）；Scorer nil=内置
// 规则桩（余弦线性映射）。
type Config struct {
	Threshold float64    // 闭集阈值 [0,1]：最近邻分数 ≥Threshold → 绑定该成员
	MinEnroll int        // 注册句数下限（≥1；spec 口径 3）
	Scorer    ScorerFunc // nil=DefaultScorer（规则桩：余弦线性映射）
}

// Decision 闭集判定结果。Rejected=true 即拒判：UserID 必为空串（绝不冒认
// 任何成员——CI-2 禁半绑定），Score=最近邻分数（观测面，拒判原因可审计）。
type Decision struct {
	UserID   string
	Score    float64
	Rejected bool
}

// Trial EER 评估通道的单条配对试验（m3-spec §5）：SameSpeaker=true 为 genuine
// 对（同人不同句），false 为 imposter 对（跨成员/陌生人）。
type Trial struct {
	A, B        Feat
	SameSpeaker bool
}

// EERReport 工作点评估报告：trial 总数与当前 Threshold 工作点上的漏判（miss，
// genuine 被拒）与误接受（FA，imposter 过阈）计数——miss/FA 全记录供报告；
// EER 本身（等错点扫描）由消费方以 Score 产分数序列后经 evalkit.EER 计算
// （统计纪律：勿手算）。
type EERReport struct {
	Trials      int
	Misses      int
	FalseAlarms int
}

// RejectEvent 拒判事件（消费者=loop 搬运 memory 只读联动；CH-05/CI-2）。
// 不携带墙钟（时间戳由搬运层逻辑时刻补——确定性属性的前提）。
type RejectEvent struct {
	Score     float64 // 拒判时最近邻分数（观测面）
	Threshold float64 // 当时的判定阈值（审计：距阈值差即可解释拒判原因）
}

// MemoryReadOnly 拒判→只读联动消费接口（m3-spec §5：MemoryReadOnly hook）。
// loop 组装时绑定 memory 联动实现（拒判→SetReadOnly(true)；识别成功→恢复）；
// 本包不 import memory（零 import 纪律），语义真身=tests/properties CI-2 的
// T5Reject→MemReadOnly（ReadWrite→ReadOnly、已降级保持）。
type MemoryReadOnly interface {
	OnVoiceprintReject(ev RejectEvent)
}

// ScorerFunc 打分器注入点：同 (a,b) 同分数（纯函数承诺——确定性的实现面）。
// 真模型（嵌入相似度）接入后换注入不改结构（ADR-0004）。
type ScorerFunc func(a, b Feat) float64

// 默认配置（产品面闭集工作点）。
const (
	defaultThreshold = 0.9
	defaultMinEnroll = 3
)

// Engine 闭集声纹引擎：注册库（成员→注册句特征）+ 最近邻打分。单流串行使用
// （不加锁——对齐 kws 资产卡定性）；BindReadOnly 在组装 wire 阶段调用。
type Engine struct {
	cfg    Config
	scorer ScorerFunc
	dim    int // 注册特征维度（0=尚未锚定）

	enroll map[string][]Feat
	users  []string // 排序缓存（确定性遍历：平分时绑定字典序最小 uid）
	hook   MemoryReadOnly
}

// NewEngine 构造引擎：仅此处校验配置（Threshold∈[0,1]、MinEnroll≥1；零值取
// 默认）。
func NewEngine(cfg Config) (*Engine, error) {
	if cfg.Threshold == 0 {
		cfg.Threshold = defaultThreshold
	}
	if cfg.Threshold < 0 || cfg.Threshold > 1 {
		return nil, errors.New("voiceprint: Threshold 须 ∈ [0, 1]")
	}
	if cfg.MinEnroll == 0 {
		cfg.MinEnroll = defaultMinEnroll
	}
	if cfg.MinEnroll < 1 {
		return nil, errors.New("voiceprint: MinEnroll 须 ≥ 1")
	}
	scorer := cfg.Scorer
	if scorer == nil {
		scorer = DefaultScorer
	}
	return &Engine{cfg: cfg, scorer: scorer, dim: 0,
		enroll: map[string][]Feat{}, users: []string{}}, nil
}

// BindReadOnly 绑定拒判→只读联动消费者（nil=解绑）。每次 Verify 拒判恰好产
// 一个 RejectEvent；判定成功产零事件（一一对应属性的实现面）。
func (e *Engine) BindReadOnly(h MemoryReadOnly) { e.hook = h }

// Enroll 注册成员：句数 ≥MinEnroll（spec 口径 3 句下限）、特征非空/有限/
// 同维（首注册特征锚定引擎维度）、uid 非空；重复注册拒绝（重复 uid error）。
func (e *Engine) Enroll(uid string, fs []Feat) error {
	if uid == "" {
		return errors.New("voiceprint: uid 不能为空")
	}
	if _, dup := e.enroll[uid]; dup {
		return errors.New("voiceprint: 重复注册拒绝（uid 已存在）")
	}
	if len(fs) < e.cfg.MinEnroll {
		return errors.New("voiceprint: 注册句数不足 MinEnroll 下限")
	}
	for _, f := range fs {
		if !validFeat(f, e.dim) {
			return errors.New("voiceprint: 特征非法（空/非有限值/维度不符）")
		}
	}
	if e.dim == 0 {
		e.dim = len(fs[0])
	}
	e.enroll[uid] = append([]Feat{}, fs...)
	e.users = append(e.users, uid)
	sort.Strings(e.users)
	return nil
}

// Users 已注册成员列表（字典序——确定性观测面）。
func (e *Engine) Users() []string { return append([]string{}, e.users...) }

// Score 打分面（规则桩：ScorerFunc 透传；评估通道产分数序列的口径）。
func (e *Engine) Score(a, b Feat) float64 { return e.scorer(a, b) }

// Verify 闭集最近邻判定：分数=注册库全体（成员×注册句）的最近邻（最大分数，
// 平分取字典序最小 uid——map 遍历序无关的确定性保证）；分数 ≥Threshold →
// 绑定该成员（Rejected=false），否则拒判（UserID 空，不冒认）。空库/维度不
// 符/畸变特征一律拒判（分数 0）。拒判时若已绑 MemoryReadOnly，发出恰好一个
// RejectEvent（拒判→只读联动；判定成功零事件）。
func (e *Engine) Verify(f Feat) Decision {
	if len(e.enroll) == 0 || !validFeat(f, e.dim) {
		return e.reject(0)
	}
	best := 0.0
	bestUID := ""
	for _, uid := range e.users {
		for _, ref := range e.enroll[uid] {
			if s := e.scorer(f, ref); s > best {
				best, bestUID = s, uid // 严格 >：平分保留字典序最小（users 已排序）
			}
		}
	}
	if best >= e.cfg.Threshold {
		return Decision{UserID: bestUID, Score: best, Rejected: false}
	}
	return e.reject(best)
}

// reject 拒判收口：Decision（UserID 空）+ 拒判事件输出（一一对应）。
func (e *Engine) reject(score float64) Decision {
	d := Decision{Score: score, Rejected: true}
	if e.hook != nil {
		e.hook.OnVoiceprintReject(RejectEvent{Score: score, Threshold: e.cfg.Threshold})
	}
	return d
}

// Evaluate EER 评估通道（m3-spec §5）：对 trial 集逐条打分并按当前 Threshold
// 工作点判定——genuine 对被拒=Misses、imposter 对过阈=FalseAlarms，全记录
// 返回（供报告）；EER 本身由消费方以 Score 产分数序列经 evalkit.EER 计算。
// 评估通道不触发拒判事件（离线统计面，无 MemoryReadOnly 副作用）。
func (e *Engine) Evaluate(trials []Trial) EERReport {
	rep := EERReport{Trials: len(trials)}
	for _, tr := range trials {
		s := e.scorer(tr.A, tr.B)
		accepted := s >= e.cfg.Threshold
		if tr.SameSpeaker && !accepted {
			rep.Misses++
		}
		if !tr.SameSpeaker && accepted {
			rep.FalseAlarms++
		}
	}
	return rep
}

// DefaultScorer 内置规则桩打分器：余弦相似度线性映射 (cos+1)/2 ∈[0,1]——
// 纯函数、无随机、无墙钟；对称（A→B 与 B→A 同分）与尺度不变（特征缩放判
// 定不变——增益不变属性）天然成立。零向量/维度不符/非有限值返回 0.5/0
// （零向量无方向信息=cos 0 的映射；非法输入按最低相似处理，永不 panic）。
// 无真实声学语义（合成代理口径——真模型接入后换 ScorerFunc 注入）。
func DefaultScorer(a, b Feat) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := a[i], b[i]
		if math.IsNaN(x) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0) {
			return 0
		}
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0.5 // cos 0 的线性映射（零向量正交语义）
	}
	cos := dot / math.Sqrt(na*nb)
	if cos > 1 {
		cos = 1 // 浮点残差夹紧
	}
	if cos < -1 {
		cos = -1
	}
	return (cos + 1) / 2
}

// validFeat 特征合法（非空、有限值、维度=引擎锚定维度 dim；dim=0 表示尚未
// 锚定，只查非空+有限）。
func validFeat(f Feat, dim int) bool {
	if len(f) == 0 || (dim != 0 && len(f) != dim) {
		return false
	}
	for _, v := range f {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}
