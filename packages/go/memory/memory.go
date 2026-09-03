// Package memory —— T10 记忆图谱 + T11 检索底座（M3，IR #105 / m3-spec §4 包契约 C）。
//
// 进程内图存储（T11 底座=可替换零件，对验收协议透明）：节点（事实/人/偏好）
// +关系边。UserID=一切读写检索的第一键域——跨用户访问=错误（T10-G0-01）；
// 事实更新=新值替换：同 (Subject,Pred) 旧值→archived 不再可检索（T10-G1-02）；
// 删除=递归清五通道（节点/关联边/检索索引/备份快照/操作日志）残留=0
// （T10-G0-02，Residuals() 为全通道观测面）；容量=MaxNodes 硬上限走淘汰
// （archived 优先→低情绪权重→最旧，T10-G1-03）；拒判联动=SetReadOnly
// （T5 拒判事件入口，loop 搬运 voiceprint 决策、包间零 import——只读期写操作
// 全拒、检索照常，CH-05）。
//
// 生命周期 FSM：raw→extracted→consolidated→decaying→archived（Advance 显式
// 推进、幂等）；deleted=吸收态且仅 Delete 显式操作可入（物理清除，任意操作
// 不复活）。表驱动穷举见 memory_test.go。
//
// 纯 Go、无 IO、无随机、无墙钟——时刻一律由调用方逻辑注入（确定性回放
// 前提）；import 白名单=标准库。错误语义：仅显式校验返回 error，构造后
// 任意调用序不 panic。
package memory

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// NodeKind 节点类别。
type NodeKind int8

const (
	Fact       NodeKind = iota // 事实（Subject/Pred/Text=陈述三元组）
	Person                     // 人（Subject=称谓，Text=名字等）
	Preference                 // 偏好（Subject=人，Pred=偏好域，Text=值）
)

// String 便于测试与日志可读。
func (k NodeKind) String() string {
	switch k {
	case Fact:
		return "Fact"
	case Person:
		return "Person"
	case Preference:
		return "Preference"
	}
	return fmt.Sprintf("NodeKind(%d)", int8(k))
}

// LifeState 节点生命周期（m3-spec §4）。
type LifeState int8

const (
	Raw          LifeState = iota // 写入起点
	Extracted                     // 已抽取
	Consolidated                  // 已巩固
	Decaying                      // 衰退中（仍可检索，时间衰减排序）
	Archived                      // 归档（不可检索——新值替换的旧值落点）
	Deleted                       // 已删除（吸收态：仅 Delete 显式可入；物理清除后不可再见）
)

// String 便于测试与日志可读。
func (st LifeState) String() string {
	switch st {
	case Raw:
		return "raw"
	case Extracted:
		return "extracted"
	case Consolidated:
		return "consolidated"
	case Decaying:
		return "decaying"
	case Archived:
		return "archived"
	case Deleted:
		return "deleted"
	}
	return fmt.Sprintf("LifeState(%d)", int8(st))
}

// Retrievable 报告 st 是否可检索（archived 旧值不再可检索——新值替换语义；
// deleted 物理清除后本就不在存储内）。
func (st LifeState) Retrievable() bool { return st >= Raw && st <= Decaying }

// NextState 生命周期链后继：raw→extracted→consolidated→decaying→archived；
// archived 自映射（链终点，Advance 幂等）；deleted 自映射（吸收态）；未知值
// 不动。转移表唯一来源（FSM 表驱动穷举的断言面）。
func NextState(st LifeState) LifeState {
	switch st {
	case Raw:
		return Extracted
	case Extracted:
		return Consolidated
	case Consolidated:
		return Decaying
	case Decaying:
		return Archived
	default: // Archived（链终点）/ Deleted（吸收态）
		return st
	}
}

// Node 记忆节点。UserID=所属域（第一键域）；EmoWeight∈[0,1] 情绪权重
// （容量淘汰的留存优先级）；CreatedAtMs/TouchedAtMs=调用方逻辑时刻。
type Node struct {
	ID, UserID               string
	K                        NodeKind
	Subject, Pred, Text      string
	EmoWeight                float64
	CreatedAtMs, TouchedAtMs int64
	St                       LifeState
}

// Edge 关系边（From/To=节点 ID，Rel=关系名）。边恒为同域（Write 校验——
// 跨用户边即隔离泄漏通道，构造期拒绝）。
type Edge struct{ From, To, Rel string }

// Options 存储配置。
type Options struct {
	MaxNodes        int      // 存储节点硬上限（含 archived；>0 必填——容量代谢面）
	DecayHalfLifeMs int64   // 检索时间衰减半衰期（≤0 取 DefaultDecayHalfLifeMs）
	Embedder        Embedder // 嵌入器（nil=不预计算/不支持语义检索）
}

// DefaultDecayHalfLifeMs 缺省半衰期（7 天——儿童玩伴的中期记忆节律）。
const DefaultDecayHalfLifeMs int64 = 7 * 24 * 3600 * 1000

// 错误面（语义化哨兵——跨用户访问一律 ErrCrossDomain，隔离 G0 的断言面）。
var (
	ErrReadOnly     = errors.New("memory: 只读期（T5 拒判联动）拒绝写/更/删/推进")
	ErrNotFound     = errors.New("memory: 节点不存在（或已删除）")
	ErrCrossDomain  = errors.New("memory: 跨用户访问（UserID 域隔离）")
	ErrDanglingEdge = errors.New("memory: 边端点不在本用户域（悬挂边拒绝）")
	ErrDuplicateID  = errors.New("memory: 节点 ID 已存在")
	ErrInvalidNode  = errors.New("memory: 节点字段非法")
	ErrInvalidEdge  = errors.New("memory: 边字段非法（Rel 须非空、From 须挂本节点或留空）")
	ErrCapacity     = errors.New("memory: 容量不足（MaxNodes 被写入保护集占满）")
)

// snap 备份快照通道条目（Write 时追加——持久层备份的仿真面；删除联清）。
type snap struct {
	Seq, AtMs int64
	NodeID    string
	Text      string
}

// op 操作日志通道条目（写/更/推进时追加——审计面；删除联清，删除即消失）。
type op struct {
	Seq, AtMs int64
	Kind      string
	NodeID    string
}

// Store 进程内图存储。单流串行使用（与 kws/turntaking/emotion 同资产定性，
// 不加锁）；nodes/edges/index/snaps/oplog 即删除五通道（节点/边/索引/备份
// 快照/操作日志），domain 为 UserID 域索引（第一键域）。
type Store struct {
	maxNodes   int
	halfLifeMs float64
	embedder   Embedder // 嵌入器（nil=不预计算/不支持语义检索）

	nodes     map[string]*Node              // 通道①节点表（raw..archived；deleted/淘汰物理移除）
	domain    map[string]map[string]bool    // UserID 域 → 节点 ID 集（第一键域）
	edges     map[string]Edge               // 通道②边表（键=ekey；同端点同关系去重）
	outAdj    map[string]map[string]bool    // 邻接：From → 边键集
	inAdj     map[string]map[string]bool    // 邻接：To → 边键集
	index     map[string]map[string]float64 // 通道③检索索引：token → 节点 ID → 字段权重
	snaps     []snap                        // 通道④备份快照
	oplog     []op                          // 通道⑤操作日志
	seq       int                           // 快照/日志序号（确定性自增）
	autoID    int                           // 空 ID 自动分配序号（确定性——回放复现前提）
	ro        map[string]bool               // uid → 只读态（拒判联动）
	embeddings map[string][]float64         // 通道⑥预计算嵌入（nodeID → vector；nil Embedder 时空 map）
}

// NewStore 构造存储：MaxNodes>0 校验（容量硬上限）；DecayHalfLifeMs≤0 取
// 缺省 7 天。
func NewStore(opts Options) (*Store, error) {
	if opts.MaxNodes <= 0 {
		return nil, fmt.Errorf("memory: MaxNodes 须 > 0（存储硬上限），got %d", opts.MaxNodes)
	}
	hl := opts.DecayHalfLifeMs
	if hl <= 0 {
		hl = DefaultDecayHalfLifeMs
	}
	return &Store{
		maxNodes:   opts.MaxNodes,
		halfLifeMs: float64(hl),
		embedder:   opts.Embedder,
		nodes:      map[string]*Node{},
		domain:     map[string]map[string]bool{},
		edges:      map[string]Edge{},
		outAdj:     map[string]map[string]bool{},
		inAdj:      map[string]map[string]bool{},
		index:      map[string]map[string]float64{},
		snaps:      []snap{},
		oplog:      []op{},
		ro:         map[string]bool{},
		embeddings: map[string][]float64{},
	}, nil
}

// ---- 写入面 ----

// Write 写入节点（生命周期 raw 起）及其关联边 es（From 留空=挂本节点，落图
// 时回填；同端点同关系去重）。只读态拒绝；n.UserID 非空且与调用域不一致=
// 跨域错误；边 To 须在本域存在（不存在=悬挂边错误、属他域=跨域错误），且
// 被写入保护集庇护于容量淘汰；容量超限走淘汰（腾位失败才 ErrCapacity——
// 此前无任何状态变更）。ID 空则确定性自动分配；ID 重复拒绝。
func (s *Store) Write(uid string, n Node, es []Edge) error {
	if s.ro[uid] {
		return ErrReadOnly
	}
	if n.UserID != "" && n.UserID != uid {
		return fmt.Errorf("%w：节点声明域 %q ≠ 调用域 %q", ErrCrossDomain, n.UserID, uid)
	}
	id := n.ID
	if id != "" {
		if _, dup := s.nodes[id]; dup {
			return fmt.Errorf("%w：%s", ErrDuplicateID, id)
		}
	}
	protected := map[string]bool{} // 边端点保护集（淘汰不得动——防写入即悬挂）
	for _, e := range es {
		if e.Rel == "" || (e.From != "" && e.From != id) {
			return fmt.Errorf("%w：Edge{From:%q To:%q Rel:%q}", ErrInvalidEdge, e.From, e.To, e.Rel)
		}
		tn, ok := s.nodes[e.To]
		if !ok {
			return fmt.Errorf("%w：To=%q", ErrDanglingEdge, e.To)
		}
		if tn.UserID != uid {
			return fmt.Errorf("%w：边 To=%q 属域 %q", ErrCrossDomain, e.To, tn.UserID)
		}
		protected[e.To] = true
	}
	if err := s.makeRoom(protected); err != nil {
		return err
	}
	if id == "" {
		s.autoID++
		id = fmt.Sprintf("n%d", s.autoID)
	}
	rec := n
	rec.ID, rec.UserID = id, uid
	rec.EmoWeight = clamp01(rec.EmoWeight)
	rec.St = Raw
	s.nodes[id] = &rec
	s.domainAdd(uid, id)
	s.indexNode(&rec)
	s.precomputeEmbedding(&rec)
	for _, e := range es {
		ne := e
		ne.From = id // 空 From=本节点（自动 ID 分配后回填）
		s.addEdge(ne)
	}
	s.seq++
	s.snaps = append(s.snaps, snap{Seq: int64(s.seq), AtMs: rec.CreatedAtMs, NodeID: id, Text: rec.Text})
	s.oplog = append(s.oplog, op{Seq: int64(s.seq), AtMs: rec.CreatedAtMs, Kind: "write", NodeID: id})
	return nil
}

// Update 事实更新（新值替换）：目标及其同 (Subject,Pred) 可检索旧值全部→
// archived（不再可检索），同槽新节点以 newText 落 raw（CreatedAt/Touched=
// atMs；沿用目标类别与情绪权重；不带边——边属旧值记录）。只读态/跨域/
// 不存在（含已删除——吸收态不复活）拒绝。
func (s *Store) Update(uid, id, newText string, atMs int64) error {
	if s.ro[uid] {
		return ErrReadOnly
	}
	n, ok := s.nodes[id]
	if !ok {
		return fmt.Errorf("%w：%s", ErrNotFound, id)
	}
	if n.UserID != uid {
		return fmt.Errorf("%w：节点 %s 属域 %q", ErrCrossDomain, id, n.UserID)
	}
	if newText == "" {
		return fmt.Errorf("%w：新值 Text 为空", ErrInvalidNode)
	}
	slots := make([]string, 0, 4)
	for sid := range s.domain[uid] {
		if m := s.nodes[sid]; m != nil && m.Subject == n.Subject && m.Pred == n.Pred && m.St.Retrievable() {
			slots = append(slots, sid)
		}
	}
	sort.Strings(slots) // 确定性（map 迭代序无关）
	for _, sid := range slots {
		s.nodes[sid].St = Archived
		s.seq++
		s.oplog = append(s.oplog, op{Seq: int64(s.seq), AtMs: atMs, Kind: "update", NodeID: sid})
	}
	if err := s.makeRoom(nil); err != nil {
		return err
	}
	s.autoID++
	nid := fmt.Sprintf("n%d", s.autoID)
	rec := Node{ID: nid, UserID: uid, K: n.K, Subject: n.Subject, Pred: n.Pred,
		Text: newText, EmoWeight: clamp01(n.EmoWeight), CreatedAtMs: atMs, TouchedAtMs: atMs, St: Raw}
	s.nodes[nid] = &rec
	s.domainAdd(uid, nid)
	s.indexNode(&rec)
	s.precomputeEmbedding(&rec)
	s.seq++
	s.snaps = append(s.snaps, snap{Seq: int64(s.seq), AtMs: atMs, NodeID: nid, Text: newText})
	s.oplog = append(s.oplog, op{Seq: int64(s.seq), AtMs: atMs, Kind: "write", NodeID: nid})
	return nil
}

// Delete 删除节点：递归清五通道（节点/关联边出入全清/检索索引/备份快照/
// 操作日志），残留=0（Residuals() 复核）。只读态/跨域拒绝；不存在（含重复
// 删除）=ErrNotFound——终态幂等（后续任意操作不复活）。
func (s *Store) Delete(uid, id string) error {
	if s.ro[uid] {
		return ErrReadOnly
	}
	n, ok := s.nodes[id]
	if !ok {
		return fmt.Errorf("%w：%s", ErrNotFound, id)
	}
	if n.UserID != uid {
		return fmt.Errorf("%w：节点 %s 属域 %q", ErrCrossDomain, id, n.UserID)
	}
	s.purge(id) // deleted=吸收态：物理清除（五通道联清、递归关联边）
	return nil
}

// Advance 显式推进生命周期一步（raw→extracted→consolidated→decaying→
// archived；archived 幂等 no-op——链终点）。演练=触碰（TouchedAtMs 单调
// 前移）；只读态/跨域/不存在拒绝。
func (s *Store) Advance(uid, id string, atMs int64) error {
	if s.ro[uid] {
		return ErrReadOnly
	}
	n, ok := s.nodes[id]
	if !ok {
		return fmt.Errorf("%w：%s", ErrNotFound, id)
	}
	if n.UserID != uid {
		return fmt.Errorf("%w：节点 %s 属域 %q", ErrCrossDomain, id, n.UserID)
	}
	if n.St == Archived || n.St == Deleted { // 链终点幂等（Deleted 防御面：物理清除后不可达）
		return nil
	}
	n.St = NextState(n.St)
	if atMs > n.TouchedAtMs {
		n.TouchedAtMs = atMs
	}
	s.seq++
	s.oplog = append(s.oplog, op{Seq: int64(s.seq), AtMs: atMs, Kind: "advance", NodeID: id})
	return nil
}

// ---- 检索面（probe 接口：关键词/关系路径 recall 查询）----

// Search UserID 域内检索：q 空白切分为关键词 token（token↔索引 token 双向
// 子串匹配——关键词/近形改写均可命中）；得分=直接匹配分（Σ token 字段权重
// Subject/Pred 1.0、Text 0.75）∪关系路径 1 跳扩展（邻居得 0.5×源直接分），
// 统一乘时间衰减 2^(−Δt/半衰期)（Δt 自 TouchedAtMs 起算）。archived/deleted
// 不可检索；结果按（分↓、TouchedAtMs↓、ID↑）确定性排序取 topK；topK≤0 或
// 无命中返回 nil。纯读——不改存储（重复查询命中不降的构造保证）。
func (s *Store) Search(uid string, q string, topK int, atMs int64) []Node {
	if topK <= 0 {
		return nil
	}
	qTokens := strings.Fields(q)
	if len(qTokens) == 0 {
		return nil
	}
	direct := map[string]float64{} // 节点 → 未衰减直接匹配分
	for _, t := range qTokens {    // token 外层序确定 → 每 (token,节点) 恰一次累加（确定性）
		w := map[string]float64{}
		for k, set := range s.index {
			if !tokenMatch(k, t) {
				continue
			}
			for id, kw := range set { // 同 token 取字段权重最大值（max 序无关）
				if kw > w[id] {
					w[id] = kw
				}
			}
		}
		for id, wt := range w {
			if n := s.nodes[id]; n != nil && n.UserID == uid && n.St.Retrievable() {
				direct[id] += wt
			}
		}
	}
	if len(direct) == 0 {
		return nil
	}
	ids := make([]string, 0, len(direct))
	for id := range direct {
		ids = append(ids, id)
	}
	sort.Strings(ids) // 路径累加迭代序确定（浮点逐位复现——确定性回放前提）
	path := map[string]float64{}
	for _, id := range ids { // 关系路径 1 跳：邻居得 0.5×源直接分
		for nb := range s.neighbors(id) {
			if n := s.nodes[nb]; n != nil && n.UserID == uid && n.St.Retrievable() {
				path[nb] += 0.5 * direct[id]
			}
		}
	}
	type scored struct {
		id string
		sc float64
	}
	list := make([]scored, 0, len(direct)+len(path))
	for _, id := range ids {
		list = append(list, scored{id, (direct[id] + path[id]) * s.decayAt(id, atMs)})
	}
	nids := make([]string, 0, len(path))
	for id := range path {
		if _, dup := direct[id]; !dup {
			nids = append(nids, id)
		}
	}
	sort.Strings(nids)
	for _, id := range nids {
		if v := path[id] * s.decayAt(id, atMs); v > 0 {
			list = append(list, scored{id, v})
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].sc != list[j].sc {
			return list[i].sc > list[j].sc
		}
		ni, nj := s.nodes[list[i].id], s.nodes[list[j].id]
		if ni.TouchedAtMs != nj.TouchedAtMs {
			return ni.TouchedAtMs > nj.TouchedAtMs
		}
		return list[i].id < list[j].id
	})
	if len(list) > topK {
		list = list[:topK]
	}
	out := make([]Node, 0, len(list))
	for _, sc := range list {
		out = append(out, *s.nodes[sc.id])
	}
	return out
}

// SearchByEmbedding 语义检索：q 经 Embedder 嵌入后与库中预计算向量做余弦相似度
// topK；同域过滤；nil Embedder/topK≤0 返回 nil；不改存储。
func (s *Store) SearchByEmbedding(uid, q string, topK int, atMs int64) []Node {
	if s.embedder == nil || topK <= 0 {
		return nil
	}
	qVec, err := s.embedder.Embed(q)
	if err != nil {
		return nil
	}
	type scored struct {
		id string
		sc float64
	}
	list := make([]scored, 0)
	for id, vec := range s.embeddings {
		n := s.nodes[id]
		if n == nil || n.UserID != uid || !n.St.Retrievable() {
			continue
		}
		sc := cosineSimilarity(qVec, vec)
		if sc > 0 {
			list = append(list, scored{id, sc})
		}
	}
	if len(list) == 0 {
		return nil
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].sc != list[j].sc {
			return list[i].sc > list[j].sc
		}
		ni, nj := s.nodes[list[i].id], s.nodes[list[j].id]
		if ni.TouchedAtMs != nj.TouchedAtMs {
			return ni.TouchedAtMs > nj.TouchedAtMs
		}
		return list[i].id < list[j].id
	})
	if len(list) > topK {
		list = list[:topK]
	}
	out := make([]Node, 0, len(list))
	for _, sc := range list {
		out = append(out, *s.nodes[sc.id])
	}
	return out
}

// ---- 拒判联动面（T5 拒判事件入口——loop 搬运 voiceprint 决策）----

// SetReadOnly 置/复位 uid 域只读态（T5 拒判→true：拒判期间写入=错误、检索
// 照常；识别成功→false：恢复读写）。atMs=决策时刻（联动审计口径）。恒成功。
func (s *Store) SetReadOnly(uid string, ro bool, atMs int64) error {
	if ro {
		s.ro[uid] = true
	} else {
		delete(s.ro, uid)
	}
	return nil
}

// ReadOnly 报告 uid 域当前只读态（拒判联动观测面）。
func (s *Store) ReadOnly(uid string) bool { return s.ro[uid] }

// ---- 观测面 ----

// Size 当前存储节点数（含 archived——MaxNodes 硬上限的观测面）。
func (s *Store) Size() int { return len(s.nodes) }

// Residuals 全通道残留观测面（T10-G0-02「删除即消失」断言面）：全图扫描
// 五通道报一切不一致——①节点（deleted 驻留/域索引悬挂/域归属错位）②边
// （悬挂/跨用户/邻接不一致）③索引（指向不存在节点）④备份快照 ⑤操作日志
// （引用不存在节点）。返回空=零残留。
func (s *Store) Residuals() []string {
	var out []string
	for id, n := range s.nodes { // ① 节点通道
		if n.St == Deleted {
			out = append(out, fmt.Sprintf("node:%s deleted 态驻留节点表", id))
		}
		if !s.domain[n.UserID][id] {
			out = append(out, fmt.Sprintf("node:%s 不在所属域 %q 索引内", id, n.UserID))
		}
	}
	for uid, set := range s.domain {
		for id := range set {
			n := s.nodes[id]
			if n == nil {
				out = append(out, fmt.Sprintf("domain:%s→%s 域索引指向不存在节点", uid, id))
			} else if n.UserID != uid {
				out = append(out, fmt.Sprintf("domain:%s→%s 域索引跨用户（节点属 %s）", uid, id, n.UserID))
			}
		}
	}
	for k, e := range s.edges { // ② 边通道
		f, t := s.nodes[e.From], s.nodes[e.To]
		if f == nil || t == nil {
			out = append(out, fmt.Sprintf("edge:%q 悬挂边（From=%q To=%q 端点不存在）", k, e.From, e.To))
			continue
		}
		if f.UserID != t.UserID {
			out = append(out, fmt.Sprintf("edge:%q 跨用户边（%s→%s）", k, f.UserID, t.UserID))
		}
		if !s.outAdj[e.From][k] || !s.inAdj[e.To][k] {
			out = append(out, fmt.Sprintf("edge:%q 边表与邻接表不一致", k))
		}
	}
	for id, keys := range s.outAdj {
		for k := range keys {
			if _, ok := s.edges[k]; !ok {
				out = append(out, fmt.Sprintf("adj:out %s→%q 邻接指向不存在边", id, k))
			}
		}
	}
	for id, keys := range s.inAdj {
		for k := range keys {
			if _, ok := s.edges[k]; !ok {
				out = append(out, fmt.Sprintf("adj:in %s→%q 邻接指向不存在边", id, k))
			}
		}
	}
	for tok, set := range s.index { // ③ 索引通道
		for id := range set {
			if s.nodes[id] == nil {
				out = append(out, fmt.Sprintf("index:%q→%s 索引指向不存在节点", tok, id))
			}
		}
	}
	for _, sn := range s.snaps { // ④ 备份快照通道
		if s.nodes[sn.NodeID] == nil {
			out = append(out, fmt.Sprintf("snapshot:#%d→%s 备份快照引用不存在节点", sn.Seq, sn.NodeID))
		}
	}
	for _, o := range s.oplog { // ⑤ 操作日志通道
		if s.nodes[o.NodeID] == nil {
			out = append(out, fmt.Sprintf("oplog:#%d→%s 操作日志引用不存在节点", o.Seq, o.NodeID))
		}
	}
	sort.Strings(out) // map 迭代序无关（确定性观测面）
	return out
}

// ---- 内部 ----

func ekey(e Edge) string { return e.From + "\x00" + e.To + "\x00" + e.Rel }

// makeRoom 为一次新写入腾位：容量未超限=空过；超限按淘汰计划物理清除
// （protected 不得动——腾不出位返回 ErrCapacity，此前无状态变更）。
func (s *Store) makeRoom(protected map[string]bool) error {
	need := len(s.nodes) + 1 - s.maxNodes
	if need <= 0 {
		return nil
	}
	plan := s.evictPlan(need, protected)
	if len(plan) < need {
		return fmt.Errorf("%w：需腾 %d 位仅得 %d（保护集 %d 节点）", ErrCapacity, need, len(plan), len(protected))
	}
	for _, id := range plan {
		s.purge(id) // 淘汰=housekeeping：不写日志/快照（非用户操作）
	}
	return nil
}

func (s *Store) domainAdd(uid, id string) {
	if s.domain[uid] == nil {
		s.domain[uid] = map[string]bool{}
	}
	s.domain[uid][id] = true
}

// indexNode 建节点检索索引（Subject/Pred 权重 1.0、Text 0.75——同 token
// 同节点取最大）。
func (s *Store) indexNode(n *Node) {
	for _, f := range []string{n.Subject, n.Pred} {
		for _, tok := range strings.Fields(f) {
			s.indexToken(tok, n.ID, 1.0)
		}
	}
	for _, tok := range strings.Fields(n.Text) {
		s.indexToken(tok, n.ID, 0.75)
	}
}

func (s *Store) indexToken(tok, id string, w float64) {
	if s.index[tok] == nil {
		s.index[tok] = map[string]float64{}
	}
	if s.index[tok][id] < w {
		s.index[tok][id] = w
	}
}

// precomputeEmbedding 为节点预计算嵌入并写入 embeddings map（nil embedder 时空过）。
func (s *Store) precomputeEmbedding(n *Node) {
	if s.embedder == nil {
		return
	}
	vec, err := s.embedder.Embed(textSurface(n))
	if err != nil {
		return
	}
	s.embeddings[n.ID] = vec
}

// tokenMatch 关键词↔索引 token 双向子串匹配（任一方向包含即命中——近形
// 改写/部分词均可召回；双方非空）。
func tokenMatch(a, b string) bool {
	return a != "" && b != "" && (strings.Contains(a, b) || strings.Contains(b, a))
}

// neighbors 节点的 1 跳邻居集（出边目标∪入边来源——边恒同域）。
func (s *Store) neighbors(id string) map[string]bool {
	out := map[string]bool{}
	for k := range s.outAdj[id] {
		if e, ok := s.edges[k]; ok {
			out[e.To] = true
		}
	}
	for k := range s.inAdj[id] {
		if e, ok := s.edges[k]; ok {
			out[e.From] = true
		}
	}
	return out
}

// decayAt 节点在 atMs 的检索时间衰减因子 2^(−age/半衰期)（age 自
// TouchedAtMs 起算、负年龄截 0）。
func (s *Store) decayAt(id string, atMs int64) float64 {
	age := atMs - s.nodes[id].TouchedAtMs
	if age < 0 {
		age = 0
	}
	return math.Exp(-math.Ln2 * float64(age) / s.halfLifeMs)
}

// evictPlan 容量淘汰计划（算不删）：archived 最先→低 EmoWeight→最旧
// TouchedAtMs→ID 升序（确定性；高情绪权重+新近留存——T10-G1-03）。need≤0
// 返回空；protected 排除；候选不足返回全部可淘汰者（调用方判腾位是否够）。
func (s *Store) evictPlan(need int, protected map[string]bool) []string {
	if need <= 0 {
		return nil
	}
	type cand struct {
		id      string
		live    int
		emo     float64
		touched int64
	}
	cands := make([]cand, 0, len(s.nodes))
	for id, n := range s.nodes {
		if protected[id] {
			continue
		}
		live := 1
		if n.St == Archived {
			live = 0 // 归档历史最先淘汰（不可检索旧值让位活记忆）
		}
		cands = append(cands, cand{id, live, n.EmoWeight, n.TouchedAtMs})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].live != cands[j].live {
			return cands[i].live < cands[j].live
		}
		if cands[i].emo != cands[j].emo {
			return cands[i].emo < cands[j].emo
		}
		if cands[i].touched != cands[j].touched {
			return cands[i].touched < cands[j].touched
		}
		return cands[i].id < cands[j].id
	})
	if len(cands) > need {
		cands = cands[:need]
	}
	ids := make([]string, 0, len(cands))
	for _, c := range cands {
		ids = append(ids, c.id)
	}
	return ids
}

// purge 五通道物理清除（Delete 与容量淘汰共用）：①节点+域索引 ②递归关联
// 边（出+入全清）③检索索引 ④备份快照 ⑤操作日志。删除即消失语义由调用方
// （Delete）承载；淘汰不写日志/快照。
func (s *Store) purge(id string) {
	n := s.nodes[id]
	if n == nil {
		return
	}
	delete(s.nodes, id) // ①
	if set := s.domain[n.UserID]; set != nil {
		delete(set, id)
		if len(set) == 0 {
			delete(s.domain, n.UserID)
		}
	}
	outKeys := make([]string, 0, len(s.outAdj[id])) // ②（先收集再删——迭代中变更安全）
	for k := range s.outAdj[id] {
		outKeys = append(outKeys, k)
	}
	for _, k := range outKeys {
		s.removeEdge(k)
	}
	inKeys := make([]string, 0, len(s.inAdj[id]))
	for k := range s.inAdj[id] {
		inKeys = append(inKeys, k)
	}
	for _, k := range inKeys {
		s.removeEdge(k)
	}
	delete(s.outAdj, id)
	delete(s.inAdj, id)
	for _, f := range []string{n.Subject, n.Pred, n.Text} { // ③（与 indexNode 同 token 化）
		for _, tok := range strings.Fields(f) {
			if set := s.index[tok]; set != nil {
				delete(set, id)
				if len(set) == 0 {
					delete(s.index, tok)
				}
			}
		}
	}
	snaps := s.snaps[:0] // ④
	for _, sn := range s.snaps {
		if sn.NodeID != id {
			snaps = append(snaps, sn)
		}
	}
	s.snaps = snaps
	delete(s.embeddings, id) // ⑥
	log := s.oplog[:0] // ⑤
	for _, o := range s.oplog {
		if o.NodeID != id {
			log = append(log, o)
		}
	}
	s.oplog = log
}

// addEdge 落边（键=From+To+Rel，同端点同关系去重）并维护双向邻接。
func (s *Store) addEdge(e Edge) {
	k := ekey(e)
	if _, dup := s.edges[k]; dup {
		return
	}
	s.edges[k] = e
	if s.outAdj[e.From] == nil {
		s.outAdj[e.From] = map[string]bool{}
	}
	s.outAdj[e.From][k] = true
	if s.inAdj[e.To] == nil {
		s.inAdj[e.To] = map[string]bool{}
	}
	s.inAdj[e.To][k] = true
}

// removeEdge 删边并维护双向邻接（空集条目回收）。
func (s *Store) removeEdge(k string) {
	e, ok := s.edges[k]
	if !ok {
		return
	}
	delete(s.edges, k)
	if set := s.outAdj[e.From]; set != nil {
		delete(set, k)
		if len(set) == 0 {
			delete(s.outAdj, e.From)
		}
	}
	if set := s.inAdj[e.To]; set != nil {
		delete(set, k)
		if len(set) == 0 {
			delete(s.inAdj, e.To)
		}
	}
}

// clamp01 夹紧 [0,1]（NaN→0、±Inf 截端点）——EmoWeight 永不 NaN 的统一入口。
func clamp01(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
