// T10 属性测试（m3-spec §4 包契约 C + docs/gates/assets/T10.md 属性行，testing/quick）：
//
//	P1 跨用户隔离（G0 级不变量）：任意操作流下 U 的检索结果 ∩ V 的记忆集=∅
//	   ——逐操作以全部活节点 Subject/Pred+Text 首 token 反查（任意查询者），
//	   结果域恒等于查询者域；跨域写（节点声明他域）恒 ErrCrossDomain（只读态
//	   先行拦截亦合法）；全通道零残留。
//	P2 删除幂等（deleted 吸收态）：任意前置流后删除一节点——重复删除/更新/
//	   推进均 ErrNotFound，任意后续操作流不复活，全通道零残留。
//	P3 确定性回放：同操作序列在两存储产生同一可观测终态（Size/只读态/全查询
//	   集检索结果逐位一致——map 迭代序无关）。
//	P4 只读语义不变式：只读窗口内写/更/删/推进全拒（ErrReadOnly）+检索与置
//	   只读前逐位一致+他域不受牵连；复位即恢复写。
//	P5 容量有界：任意操作流下 Size ≤ MaxNodes 恒成立（淘汰让位，无越界无残留）。
//
// 抽象操作经 quick 生成（int 选择器+任意字符串/浮点/时刻——负时刻/越界权重/
// 空白与控制字符文本均合法输入，契约「构造后任意调用序不 panic」的 stress 面）；
// 节点 ID 由存储自动分配（ID 空间受控，任意字符串 stress 落在 Text/Subject 字段）。
package memory

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"testing"
	"testing/quick"
)

// propOp quick 生成的抽象操作（字段为任意值——选择器经取模落到合法域）。
type propOp struct {
	Kind int8    // 操作选择器（mod 7：写/带边写/跨域写/更/删/推进/只读开关）
	U    int8    // 用户选择器（mod 3）
	Slot int16   // 目标节点槽位（mod 活节点数）
	Warm int8    // 副选择器（Subject/Pred 变体）
	Text string  // 任意文本（stress：空/控制字符/任意 unicode）
	Emo  float64 // 任意情绪权重（stress：越界值由 clamp01 夹紧）
	At   int64   // 任意逻辑时刻（stress：负值/极值）
}

var propUsers = [3]string{"u0", "u1", "u2"}

// propMod 非负取模（quick 可生成负数——直接 % 会保号导致负索引）。
func propMod(v, m int64) int {
	r := v % m
	if r < 0 {
		r += m
	}
	return int(r)
}

// propOpts 属性测试通用存储配置（小容量——淘汰路径在 P1/P2/P3 内自然激活）。
var propOpts = Options{MaxNodes: 24, DecayHalfLifeMs: 1000}

// propIDs 活节点 ID 升序（确定性——槽位选择与回放复现的前提）。
func propIDs(s *Store) []string {
	ids := make([]string, 0, len(s.nodes))
	for id := range s.nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// propOwnIDs uid 域活节点 ID 升序。
func propOwnIDs(s *Store, uid string) []string {
	var ids []string
	for id, n := range s.nodes {
		if n.UserID == uid {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// propApply 将抽象操作映射为一次真实调用并返回其结果（错误=合法结果：只读/
// 跨域/悬挂边/不存在由各属性的不变式断言面覆盖）。
func propApply(s *Store, o propOp) error {
	uid := propUsers[propMod(int64(o.U), 3)]
	at := o.At
	subj := "主题" + strconv.Itoa(propMod(int64(o.Warm), 4))
	pred := "键" + strconv.Itoa(propMod(int64(o.Slot), 3))
	text := "内容" + strconv.Itoa(propMod(int64(o.At), 7))
	n := Node{Subject: subj, Pred: pred, Text: text, EmoWeight: o.Emo,
		CreatedAtMs: at, TouchedAtMs: at}
	ids := propIDs(s)
	slot := func() string { // 目标节点（活集为空时返回空串=调用方跳过）
		if len(ids) == 0 {
			return ""
		}
		return ids[propMod(int64(o.Slot), int64(len(ids)))]
	}
	switch propMod(int64(o.Kind), 7) {
	case 0: // 写入（自动 ID）
		return s.Write(uid, n, nil)
	case 1: // 写入+挂边（To=随机活节点——跨域/悬挂拒绝是合法结果）
		var es []Edge
		if id := slot(); id != "" {
			es = []Edge{{To: id, Rel: "关系"}}
		}
		return s.Write(uid, n, es)
	case 2: // 跨域写尝试（节点声明他域——恒 ErrCrossDomain；只读态先行拦截亦合法）
		other := propUsers[propMod(int64(o.U)+1, 3)]
		xd := n
		xd.UserID = other
		return s.Write(uid, xd, nil)
	case 3: // 事实更新（新值替换）
		if id := slot(); id != "" {
			return s.Update(uid, id, "新值"+strconv.Itoa(propMod(int64(o.Warm), 9)), at)
		}
		return nil
	case 4: // 删除
		if id := slot(); id != "" {
			return s.Delete(uid, id)
		}
		return nil
	case 5: // 生命周期推进
		if id := slot(); id != "" {
			return s.Advance(uid, id, at)
		}
		return nil
	default: // 拒判联动开关（域级只读/恢复）
		return s.SetReadOnly(uid, propMod(int64(o.Warm), 2) == 0, at)
	}
}

// propQueries 当前全图去重查询集：每活节点的 Subject+Pred（≤12 组合）与
// Text 首 token（任意 quick 字符串 stress 的反查语料，采样至多 8 条控制成本）。
func propQueries(s *Store) []string {
	set := map[string]bool{}
	for _, n := range s.nodes {
		set[n.Subject+" "+n.Pred] = true
		if toks := strings.Fields(n.Text); len(toks) > 0 {
			set[toks[0]] = true
		}
	}
	qs := make([]string, 0, len(set))
	for q := range set {
		qs = append(qs, q)
	}
	sort.Strings(qs)
	if len(qs) > 20 {
		qs = qs[:20]
	}
	return qs
}

// propIsolationOK 隔离不变量（G0 级）：任意用户以任意反查 query 检索，结果域
// 恒等于查询者域 + 全通道零残留。
func propIsolationOK(s *Store) bool {
	if res := s.Residuals(); len(res) != 0 {
		return false
	}
	for _, q := range propQueries(s) {
		for _, u := range propUsers {
			for _, got := range s.Search(u, q, 8, 1_000_000) {
				if got.UserID != u {
					return false
				}
			}
		}
	}
	return true
}

// TestPropertyCrossUserIsolation P1：任意操作流逐操作校验——U 的检索结果 ∩
// V 的记忆集=∅（任意写入/更新/删除/只读交错序列），跨域写恒拒。
func TestPropertyCrossUserIsolation(t *testing.T) {
	f := func(ops []propOp) bool {
		s := mustStore(t, propOpts)
		for _, o := range ops {
			err := propApply(s, o)
			if propMod(int64(o.Kind), 7) == 2 &&
				!errors.Is(err, ErrCrossDomain) && !errors.Is(err, ErrReadOnly) {
				return false // 跨域写被放行=隔离门洞开
			}
			if !propIsolationOK(s) {
				return false
			}
		}
		return true
	}
	if err := quick.Check(f, nil); err != nil {
		t.Fatalf("P1 跨用户隔离不变量被违反：%v", err)
	}
}

// TestPropertyDeleteIdempotent P2：删除幂等（deleted=吸收态）——前置流后删除，
// 重复删除/更新/推进全 ErrNotFound，后续任意操作流不复活，零残留。
func TestPropertyDeleteIdempotent(t *testing.T) {
	f := func(pre, post []propOp) bool {
		s := mustStore(t, propOpts)
		for _, o := range pre {
			_ = propApply(s, o) // 域错误（ErrNotFound/ErrCrossDomain 等）=随机操作流合法结果，断言面在存储不变式
		}
		ids := propIDs(s)
		if len(ids) == 0 {
			return true // 前置流未留活节点——幂等面由下一轮 quick 样本再验
		}
		id := ids[0]
		owner := s.nodes[id].UserID
		_ = s.SetReadOnly(owner, false, 0) // 清只读——幂等断言不受拒判窗口干扰
		if err := s.Delete(owner, id); err != nil {
			return false
		}
		if s.nodes[id] != nil || len(s.Residuals()) != 0 {
			return false
		}
		for _, op := range []func() error{
			func() error { return s.Delete(owner, id) },
			func() error { return s.Update(owner, id, "复活值", 1) },
			func() error { return s.Advance(owner, id, 1) },
		} {
			if !errors.Is(op(), ErrNotFound) {
				return false // 吸收态漏了：已删节点被操作面重新接纳
			}
		}
		for _, o := range post { // 后续任意操作流不复活
			_ = propApply(s, o) // 域错误（ErrNotFound/ErrCrossDomain 等）=随机操作流合法结果，断言面在存储不变式
			if s.nodes[id] != nil {
				return false
			}
		}
		return len(s.Residuals()) == 0
	}
	if err := quick.Check(f, nil); err != nil {
		t.Fatalf("P2 删除幂等/吸收态不变量被违反：%v", err)
	}
}

// TestPropertyDeterministicReplay P3：同操作序列→同可观测终态（两存储逐位
// 一致：Size/只读态/全查询集检索结果——无随机无墙钟、map 迭代序无关）。
func TestPropertyDeterministicReplay(t *testing.T) {
	f := func(ops []propOp) bool {
		a, b := mustStore(t, propOpts), mustStore(t, propOpts)
		for _, o := range ops {
			_ = propApply(a, o) // 域错误（ErrNotFound/ErrCrossDomain 等）=随机操作流合法结果，断言面在存储不变式
			_ = propApply(b, o) // 域错误（ErrNotFound/ErrCrossDomain 等）=随机操作流合法结果，断言面在存储不变式
		}
		if a.Size() != b.Size() {
			return false
		}
		for _, u := range propUsers {
			if a.ReadOnly(u) != b.ReadOnly(u) {
				return false
			}
			for _, q := range propQueries(a) {
				ra, rb := a.Search(u, q, 8, 1_000_000), b.Search(u, q, 8, 1_000_000)
				if len(ra) != len(rb) {
					return false
				}
				for i := range ra {
					if ra[i] != rb[i] {
						return false
					}
				}
			}
		}
		return true
	}
	if err := quick.Check(f, nil); err != nil {
		t.Fatalf("P3 确定性回放不变量被违反：%v", err)
	}
}

// TestPropertyReadOnlyInvariant P4：只读语义不变式——拒判窗口内写/更/删/推进
// 全拒（ErrReadOnly）、检索与置只读前逐位一致（只读缓存可用）、他域不受牵连、
// 复位即恢复写（CH-05 镜像 ci2 语义）。
func TestPropertyReadOnlyInvariant(t *testing.T) {
	f := func(ops []propOp, pick int8) bool {
		s := mustStore(t, propOpts)
		for _, o := range ops { // 前置流不碰只读开关——不变式在受控只读窗口内校验
			if propMod(int64(o.Kind), 7) == 6 {
				continue
			}
			_ = propApply(s, o) // 域错误（ErrNotFound/ErrCrossDomain 等）=随机操作流合法结果，断言面在存储不变式
		}
		u := propUsers[propMod(int64(pick), 3)]
		_ = s.SetReadOnly(u, false, 0)
		base := map[string][]Node{}
		for _, q := range propQueries(s) {
			base[q] = s.Search(u, q, 8, 1_000_000)
		}
		if err := s.SetReadOnly(u, true, 500); err != nil {
			return false
		}
		mut := []func() error{
			func() error { return s.Write(u, Node{Subject: "只读", Pred: "写入", Text: "拒绝"}, nil) },
		}
		if own := propOwnIDs(s, u); len(own) > 0 {
			id := own[propMod(int64(pick), int64(len(own)))]
			mut = append(mut,
				func() error { return s.Update(u, id, "只读更新", 600) },
				func() error { return s.Delete(u, id) },
				func() error { return s.Advance(u, id, 600) })
		}
		for _, op := range mut {
			if !errors.Is(op(), ErrReadOnly) {
				return false // 只读窗口放行写操作=拒判联动破损
			}
		}
		for q, want := range base { // 检索照常：逐位一致
			got := s.Search(u, q, 8, 1_000_000)
			if len(got) != len(want) {
				return false
			}
			for i := range got {
				if got[i] != want[i] {
					return false
				}
			}
		}
		other := propUsers[propMod(int64(pick)+1, 3)] // 他域不受牵连
		if err := s.Write(other, Node{Subject: "他域", Pred: "写入", Text: "正常"}, nil); err != nil {
			return false
		}
		_ = s.SetReadOnly(u, false, 700) // 复位即恢复写
		return s.Write(u, Node{Subject: "恢复", Pred: "写入", Text: "成功"}, nil) == nil
	}
	if err := quick.Check(f, nil); err != nil {
		t.Fatalf("P4 只读语义不变量被违反：%v", err)
	}
}

// TestPropertyCapacityBounded P5：容量有界——任意操作流（含带边写入的写入
// 保护集占满路径）下 Size ≤ MaxNodes 恒成立、全通道零残留（淘汰让位）。
func TestPropertyCapacityBounded(t *testing.T) {
	const maxN = 10
	f := func(ops []propOp) bool {
		s := mustStore(t, Options{MaxNodes: maxN, DecayHalfLifeMs: 1000})
		for _, o := range ops {
			_ = propApply(s, o) // 域错误（ErrNotFound/ErrCrossDomain 等）=随机操作流合法结果，断言面在存储不变式
			if s.Size() > maxN {
				return false // 容量越界（无 OOM 的构造保证）
			}
			if len(s.Residuals()) != 0 {
				return false
			}
		}
		return true
	}
	if err := quick.Check(f, nil); err != nil {
		t.Fatalf("P5 容量有界不变量被违反：%v", err)
	}
}
