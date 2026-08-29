// 表驱动单测（m3-spec §4 包契约 C；三件套之一）：构造校验、生命周期 FSM 表
// 驱动穷举（全状态可达/无死锁/deleted 吸收态且仅显式操作可入）、写/更/删/
// 检索/只读/容量淘汰语义面。属性测试见 properties_test.go，门禁测试见
// gates_test.go（口径与样本量唯一来源 configs/gates/T10.yaml）。
package memory

import (
	"errors"
	"math"
	"strconv"
	"testing"
)

func mustStore(t *testing.T, opts Options) *Store {
	t.Helper()
	s, err := NewStore(opts)
	if err != nil {
		t.Fatalf("NewStore(%+v): %v", opts, err)
	}
	return s
}

// w1 写入辅助：uid 域写一显式 ID 节点（返回 id）。
func w1(t *testing.T, s *Store, uid, id, subj, pred, text string, emo float64, at int64) string {
	t.Helper()
	if err := s.Write(uid, Node{ID: id, Subject: subj, Pred: pred, Text: text,
		EmoWeight: emo, CreatedAtMs: at, TouchedAtMs: at}, nil); err != nil {
		t.Fatalf("Write(%s,%s): %v", uid, id, err)
	}
	return id
}

func TestNewStoreOptions(t *testing.T) {
	cases := []struct {
		name    string
		opts    Options
		wantErr bool
	}{
		{"MaxNodes=0 拒绝", Options{}, true},
		{"MaxNodes<0 拒绝", Options{MaxNodes: -3}, true},
		{"MaxNodes=1 合法", Options{MaxNodes: 1}, false},
		{"带半衰期合法", Options{MaxNodes: 10, DecayHalfLifeMs: 1000}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := NewStore(c.opts)
			if c.wantErr {
				if err == nil {
					t.Fatalf("应拒绝 %+v", c.opts)
				}
				return
			}
			if err != nil || s == nil {
				t.Fatalf("应接受 %+v：err=%v", c.opts, err)
			}
			if s.Size() != 0 || len(s.Residuals()) != 0 {
				t.Fatalf("新存储非空：size=%d residuals=%v", s.Size(), s.Residuals())
			}
		})
	}
	// DecayHalfLifeMs≤0 → 缺省 7 天（两存储同写入同检索同排序的可观测口径）
	a := mustStore(t, Options{MaxNodes: 10, DecayHalfLifeMs: 0})
	b := mustStore(t, Options{MaxNodes: 10, DecayHalfLifeMs: DefaultDecayHalfLifeMs})
	for _, s := range []*Store{a, b} {
		w1(t, s, "u", "old", "主题", "值", "旧文本", 0.5, 0)
		w1(t, s, "u", "new", "主题", "值", "新文本", 0.5, 1000)
	}
	qa, qb := a.Search("u", "主题 值", 2, 10_000), b.Search("u", "主题 值", 2, 10_000)
	if len(qa) != 2 || len(qb) != 2 || qa[0].ID != qb[0].ID || qa[0].ID != "new" {
		t.Fatalf("缺省半衰期行为不一致：a=%v b=%v", qa, qb)
	}
}

// TestLifeStateTable FSM 转移表穷举：NextState 全枚举（链前进/archived 链终点
// 幂等/deleted 吸收态/未知值不动）+ Retrievable 边界（raw..decaying 可检索、
// archived/deleted 不可）+ String 可读性。
func TestLifeStateTable(t *testing.T) {
	trans := []struct {
		from, want LifeState
	}{
		{Raw, Extracted}, {Extracted, Consolidated}, {Consolidated, Decaying},
		{Decaying, Archived}, {Archived, Archived}, {Deleted, Deleted},
		{LifeState(-1), LifeState(-1)}, {LifeState(99), LifeState(99)},
	}
	for _, c := range trans {
		if got := NextState(c.from); got != c.want {
			t.Errorf("NextState(%d)=%d want %d", c.from, got, c.want)
		}
	}
	retr := map[LifeState]bool{Raw: true, Extracted: true, Consolidated: true, Decaying: true,
		Archived: false, Deleted: false, LifeState(-1): false, LifeState(99): false}
	for st, want := range retr {
		if got := st.Retrievable(); got != want {
			t.Errorf("Retrievable(%d)=%v want %v", st, got, want)
		}
	}
	if Raw.String() != "raw" || Deleted.String() != "deleted" || LifeState(42).String() != "LifeState(42)" {
		t.Errorf("LifeState.String 异常：%s/%s/%s", Raw, Deleted, LifeState(42))
	}
	if Fact.String() != "Fact" || Preference.String() != "Preference" || NodeKind(7).String() != "NodeKind(7)" {
		t.Errorf("NodeKind.String 异常")
	}
}

// TestFSMReachableNoDeadlock 表驱动穷举：全状态可达（raw 起步经 Advance 链达
// archived——无死锁：archived 幂等自映射不卡 Advance）、deleted 仅显式 Delete
// 可入（物理清除后不可再见——吸收态；Update/Advance 均不复活）。
func TestFSMReachableNoDeadlock(t *testing.T) {
	s := mustStore(t, Options{MaxNodes: 10})
	id := w1(t, s, "u", "fsm", "主题", "值", "文本", 0.5, 0)
	// 可检索段（raw..decaying）：Advance 前后均须经 Search 可见（无死锁——
	// 每步可达可观测）
	chain := []struct {
		after LifeState
		want  LifeState
	}{{Raw, Extracted}, {Extracted, Consolidated}, {Consolidated, Decaying}}
	for _, c := range chain {
		got := s.Search("u", "主题", 1, 0)
		if len(got) == 0 || got[0].St != c.after {
			t.Fatalf("状态 %s 不可检索（FSM 可达链断），got=%v", c.after, got)
		}
		if err := s.Advance("u", id, 500); err != nil {
			t.Fatalf("Advance@%s: %v", c.after, err)
		}
		got = s.Search("u", "主题", 1, 0)
		if len(got) == 0 || got[0].St != c.want {
			t.Fatalf("Advance(%s) 后 want %s，got=%v", c.after, c.want, got)
		}
	}
	// decaying→archived：Advance 后脱离检索面（archived=不可检索旧值语义）
	// 但节点仍在表（历史在场——容量淘汰的最先出局者），链不卡死
	if err := s.Advance("u", id, 500); err != nil {
		t.Fatalf("Advance@decaying: %v", err)
	}
	if n := s.nodes[id]; n == nil || n.St != Archived {
		t.Fatalf("Advance(decaying) 后 want archived 在表，got=%v", n)
	}
	if got := s.Search("u", "主题", 1, 0); len(got) != 0 {
		t.Fatalf("archived 节点不应可检索：%v", got)
	}
	// archived=链终点：Advance 幂等 no-op（无死锁——恒成功恒停留）
	for i := 0; i < 3; i++ {
		if err := s.Advance("u", id, 600); err != nil {
			t.Fatalf("Advance@archived 应幂等: %v", err)
		}
		if got := s.Search("u", "任意", 1, 0); len(got) != 0 {
			t.Fatalf("archived 节点不应可检索：%v", got)
		}
	}
	// deleted 仅显式 Delete 可入：物理清除（节点表无驻留）+ 吸收态（重复删/更/
	// 推进全 ErrNotFound——任意操作不复活）
	if err := s.Delete("u", id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for name, op := range map[string]func() error{
		"重复删除":  func() error { return s.Delete("u", id) },
		"更新不复活": func() error { return s.Update("u", id, "新文本", 700) },
		"推进不复活": func() error { return s.Advance("u", id, 700) },
		"跨域删除":  func() error { return s.Delete("v", id) },
		"不存在删除": func() error { return s.Delete("u", "nope") },
	} {
		if err := op(); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s 应 ErrNotFound，got %v", name, err)
		}
	}
	if s.Size() != 0 || len(s.Residuals()) != 0 {
		t.Fatalf("deleted 后应零驻留零残留：size=%d residuals=%v", s.Size(), s.Residuals())
	}
}

// TestWriteTable 写入面表驱动。
func TestWriteTable(t *testing.T) {
	cases := []struct {
		name    string
		uid     string
		n       Node
		es      []Edge
		pre     func(t *testing.T, s *Store)
		wantErr error
	}{
		{name: "自动 ID 分配+域归属+raw 起点+权重夹紧", uid: "u",
			n: Node{Subject: "仓鼠", Pred: "名字", Text: "叫布丁", EmoWeight: 2, CreatedAtMs: 10, TouchedAtMs: 10}},
		{name: "显式 ID 保留", uid: "u", n: Node{ID: "e1", Subject: "s", Pred: "p", Text: "t", EmoWeight: 0.1}},
		{name: "NaN 权重归零", uid: "u", n: Node{ID: "e2", Subject: "s", Pred: "p", Text: "t", EmoWeight: math.NaN()}},
		{name: "只读拒绝", uid: "u", n: Node{ID: "ro", Subject: "s", Pred: "p", Text: "t"},
			pre: func(t *testing.T, s *Store) { _ = s.SetReadOnly("u", true, 0) }, wantErr: ErrReadOnly},
		{name: "节点声明他域", uid: "u", n: Node{ID: "xd", UserID: "v", Subject: "s", Pred: "p", Text: "t"},
			wantErr: ErrCrossDomain},
		{name: "显式 ID 重复", uid: "u", n: Node{ID: "dup", Subject: "s", Pred: "p", Text: "t"},
			pre: func(t *testing.T, s *Store) { w1(t, s, "u", "dup", "s", "p", "t", 0.5, 0) }, wantErr: ErrDuplicateID},
		{name: "空 Rel 拒绝", uid: "u", n: Node{ID: "er", Subject: "s", Pred: "p", Text: "t"},
			pre: func(t *testing.T, s *Store) { w1(t, s, "u", "to", "锚点", "值", "锚文本", 0.5, 0) },
			es:  []Edge{{To: "to", Rel: ""}}, wantErr: ErrInvalidEdge},
		{name: "From 挂他节点拒绝", uid: "u", n: Node{ID: "er2", Subject: "s", Pred: "p", Text: "t"},
			pre: func(t *testing.T, s *Store) { w1(t, s, "u", "to", "锚点", "值", "锚文本", 0.5, 0) },
			es:  []Edge{{From: "to", To: "to", Rel: "r"}}, wantErr: ErrInvalidEdge},
		{name: "To 不存在=悬挂边", uid: "u", n: Node{ID: "dang", Subject: "s", Pred: "p", Text: "t"},
			es: []Edge{{To: "ghost", Rel: "r"}}, wantErr: ErrDanglingEdge},
		{name: "To 属他域=跨域边", uid: "u", n: Node{ID: "xde", Subject: "s", Pred: "p", Text: "t"},
			pre: func(t *testing.T, s *Store) { w1(t, s, "v", "vnode", "s", "p", "t", 0.5, 0) },
			es:  []Edge{{To: "vnode", Rel: "r"}}, wantErr: ErrCrossDomain},
		{name: "合法出边（From 留空挂本节点）", uid: "u", n: Node{ID: "ok", Subject: "s", Pred: "p", Text: "t"},
			pre: func(t *testing.T, s *Store) { w1(t, s, "u", "to", "锚点", "值", "锚文本", 0.5, 0) },
			es:  []Edge{{To: "to", Rel: "喜欢"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := mustStore(t, Options{MaxNodes: 50})
			if c.pre != nil {
				c.pre(t, s)
			}
			err := s.Write(c.uid, c.n, c.es)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("err=%v want %v", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Write: %v", err)
			}
			res := s.Search(c.uid, c.n.Subject+" "+c.n.Text, 5, 0)
			var got *Node
			for i := range res {
				if c.n.ID != "" && res[i].ID == c.n.ID {
					got = &res[i]
					break
				}
				if c.n.ID == "" && res[i].Subject == c.n.Subject && res[i].Text == c.n.Text {
					got = &res[i]
					break
				}
			}
			if got == nil {
				t.Fatalf("写入后检索不到：%+v res=%v", c.n, res)
			}
			if got.UserID != c.uid || got.St != Raw {
				t.Fatalf("落库异常：uid=%q st=%s", got.UserID, got.St)
			}
			if c.n.ID != "" && got.ID != c.n.ID {
				t.Fatalf("显式 ID 被改写：%s→%s", c.n.ID, got.ID)
			}
			wantEmo := c.n.EmoWeight
			if math.IsNaN(wantEmo) || wantEmo < 0 {
				wantEmo = 0
			}
			if wantEmo > 1 {
				wantEmo = 1
			}
			if got.EmoWeight != wantEmo {
				t.Fatalf("EmoWeight 未夹紧：%v want %v", got.EmoWeight, wantEmo)
			}
			if len(s.Residuals()) != 0 {
				t.Fatalf("写入后残留：%v", s.Residuals())
			}
		})
	}
}

// TestWriteEdgeLanding 边落图+关系路径 1 跳扩展（probe 的关系路径通道）：
// src—喜欢→hub 后，查 hub 独有 token 应把 src 一并带入结果。
func TestWriteEdgeLanding(t *testing.T) {
	s := mustStore(t, Options{MaxNodes: 10})
	w1(t, s, "u", "hub", "锚点", "值", "锚文本", 0.5, 0)
	if err := s.Write("u", Node{ID: "src", Subject: "来源", Pred: "值", Text: "内容", EmoWeight: 0.5}, []Edge{{To: "hub", Rel: "喜欢"}}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	seen := map[string]bool{}
	for _, n := range s.Search("u", "锚文本", 5, 0) {
		seen[n.ID] = true
	}
	if !seen["hub"] {
		t.Fatalf("直接命中缺失：%v", seen)
	}
	if !seen["src"] {
		t.Fatalf("关系路径 1 跳未扩展（边未落图）：%v", seen)
	}
}

// TestUpdateTable 事实更新面表驱动。
func TestUpdateTable(t *testing.T) {
	t.Run("新值替换+旧值 archived 不可检索", func(t *testing.T) {
		s := mustStore(t, Options{MaxNodes: 20})
		w1(t, s, "u", "a", "仓鼠", "名字", "叫布丁", 0.6, 0)
		if err := s.Update("u", "a", "叫汤圆", 100); err != nil {
			t.Fatalf("Update: %v", err)
		}
		got := s.Search("u", "仓鼠 名字", 5, 100)
		if len(got) != 1 || got[0].Text != "叫汤圆" || got[0].St != Raw {
			t.Fatalf("新值未落位：%v", got)
		}
		if s.Search("u", "布丁", 5, 100) != nil {
			t.Fatalf("旧值文本仍可检索（archived 语义破损）")
		}
	})
	t.Run("同 (Subject,Pred) 多旧值全 archived", func(t *testing.T) {
		s := mustStore(t, Options{MaxNodes: 20})
		w1(t, s, "u", "a", "零食", "最爱", "冰淇淋", 0.6, 0)
		w1(t, s, "u", "b", "零食", "最爱", "薯片", 0.6, 10)
		if err := s.Update("u", "a", "果冻", 20); err != nil {
			t.Fatalf("Update: %v", err)
		}
		got := s.Search("u", "零食 最爱", 5, 20)
		if len(got) != 1 || got[0].Text != "果冻" {
			t.Fatalf("应仅新值可检索：%v", got)
		}
	})
	t.Run("新节点不带边（边属旧值记录）", func(t *testing.T) {
		s := mustStore(t, Options{MaxNodes: 20})
		w1(t, s, "u", "hub", "主题", "值", "文本", 0.5, 0)
		w1(t, s, "u", "old", "事实", "内容", "旧", 0.5, 10)
		if err := s.Write("u", Node{ID: "old2", Subject: "事实", Pred: "内容", Text: "旧", EmoWeight: 0.5}, []Edge{{To: "hub", Rel: "r"}}); err != nil {
			t.Fatalf("Write: %v", err)
		}
		_ = s.Delete("u", "old") // 排除同文本旧节点干扰
		if err := s.Update("u", "old2", "新", 20); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if got := s.Search("u", "新", 5, 20); len(got) != 1 || got[0].ID == "old2" {
			t.Fatalf("新值应独立成节点（不带旧值边）：%v", got)
		}
	})
	errCases := []struct {
		name    string
		setup   func(t *testing.T, s *Store)
		uid     string
		id      string
		text    string
		wantErr error
	}{
		{"只读拒绝", func(t *testing.T, s *Store) {
			w1(t, s, "u", "x", "s", "p", "t", 0.5, 0)
			_ = s.SetReadOnly("u", true, 0)
		}, "u", "x", "新", ErrReadOnly},
		{"不存在", func(t *testing.T, s *Store) {}, "u", "ghost", "新", ErrNotFound},
		{"已删除（吸收态不复活）", func(t *testing.T, s *Store) {
			w1(t, s, "u", "x", "s", "p", "t", 0.5, 0)
			if err := s.Delete("u", "x"); err != nil {
				t.Fatal(err)
			}
		}, "u", "x", "新", ErrNotFound},
		{"跨域", func(t *testing.T, s *Store) {
			w1(t, s, "v", "x", "s", "p", "t", 0.5, 0)
		}, "u", "x", "新", ErrCrossDomain},
		{"空文本", func(t *testing.T, s *Store) {
			w1(t, s, "u", "x", "s", "p", "t", 0.5, 0)
		}, "u", "x", "", ErrInvalidNode},
	}
	for _, c := range errCases {
		t.Run(c.name, func(t *testing.T) {
			s := mustStore(t, Options{MaxNodes: 20})
			c.setup(t, s)
			if err := s.Update(c.uid, c.id, c.text, 99); !errors.Is(err, c.wantErr) {
				t.Fatalf("err=%v want %v", err, c.wantErr)
			}
			if len(s.Residuals()) != 0 {
				t.Fatalf("失败更新后残留：%v", s.Residuals())
			}
		})
	}
}

// TestDeleteTable 删除面表驱动：递归清关联边（出+入双向）。
func TestDeleteTable(t *testing.T) {
	t.Run("递归清出边与入边", func(t *testing.T) {
		s := mustStore(t, Options{MaxNodes: 20})
		w1(t, s, "u", "hub", "锚点", "值", "文本", 0.5, 0)
		w1(t, s, "u", "sink", "终点", "值", "文本", 0.5, 0)
		// target→hub 出边；sink2→target 入边
		if err := s.Write("u", Node{ID: "target", Subject: "目标", Pred: "值", Text: "独占", EmoWeight: 0.5}, []Edge{{To: "hub", Rel: "喜欢"}}); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if err := s.Write("u", Node{ID: "sink2", Subject: "来源", Pred: "值", Text: "文本", EmoWeight: 0.5}, []Edge{{To: "target", Rel: "提到"}}); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if got := s.Search("u", "锚点 独占", 5, 0); len(got) == 0 {
			t.Fatalf("删除前应可检索")
		}
		if err := s.Delete("u", "target"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if s.Size() != 3 {
			t.Fatalf("应仅删目标节点：size=%d", s.Size())
		}
		if got := s.Search("u", "独占 目标", 5, 0); len(got) != 0 {
			t.Fatalf("删除后仍可检索：%v", got)
		}
		// 邻居经残留边捞不回已删节点（悬挂边=残留）
		for _, q := range []string{"锚点", "来源", "终点"} {
			for _, n := range s.Search("u", q, 5, 0) {
				if n.ID == "target" || n.Text == "独占" {
					t.Fatalf("悬挂边经路径捞回已删节点（q=%s）：%v", q, n)
				}
			}
		}
		if res := s.Residuals(); len(res) != 0 {
			t.Fatalf("删除后残留：%v", res)
		}
		if err := s.Delete("u", "target"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("重复删除应 ErrNotFound：%v", err)
		}
	})
	errCases := []struct {
		name    string
		setup   func(t *testing.T, s *Store)
		uid     string
		id      string
		wantErr error
	}{
		{"只读拒绝", func(t *testing.T, s *Store) {
			w1(t, s, "u", "x", "s", "p", "t", 0.5, 0)
			_ = s.SetReadOnly("u", true, 0)
		}, "u", "x", ErrReadOnly},
		{"不存在", func(t *testing.T, s *Store) {}, "u", "ghost", ErrNotFound},
		{"跨域", func(t *testing.T, s *Store) { w1(t, s, "v", "x", "s", "p", "t", 0.5, 0) }, "u", "x", ErrCrossDomain},
	}
	for _, c := range errCases {
		t.Run(c.name, func(t *testing.T) {
			s := mustStore(t, Options{MaxNodes: 20})
			c.setup(t, s)
			if err := s.Delete(c.uid, c.id); !errors.Is(err, c.wantErr) {
				t.Fatalf("err=%v want %v", err, c.wantErr)
			}
		})
	}
}

// TestSearchTable 检索面表驱动（probe：关键词/关系路径/时间衰减排序）。
func TestSearchTable(t *testing.T) {
	t.Run("topK 越界与空 query", func(t *testing.T) {
		s := mustStore(t, Options{MaxNodes: 30})
		w1(t, s, "u", "n1", "仓鼠", "名字", "叫布丁", 0.5, 0)
		if got := s.Search("u", "仓鼠", 0, 0); got != nil {
			t.Fatalf("topK=0 应 nil：%v", got)
		}
		if got := s.Search("u", "仓鼠", -1, 0); got != nil {
			t.Fatalf("topK<0 应 nil：%v", got)
		}
		if got := s.Search("u", "   ", 5, 0); got != nil {
			t.Fatalf("空白 query 应 nil：%v", got)
		}
		if got := s.Search("u", "不存在的词", 5, 0); got != nil {
			t.Fatalf("无命中应 nil：%v", got)
		}
	})
	t.Run("字段权重：Subject/Pred 1.0 高于 Text 0.75", func(t *testing.T) {
		s := mustStore(t, Options{MaxNodes: 10})
		w1(t, s, "u", "subjHit", "水果", "喜好", "苹果", 0.5, 0)
		w1(t, s, "u", "textHit", "零食", "喜好", "爱吃水果", 0.5, 0)
		got := s.Search("u", "水果", 5, 0)
		if len(got) != 2 || got[0].ID != "subjHit" {
			t.Fatalf("Subject 命中应排首：%v", got)
		}
	})
	t.Run("近形子串双向命中", func(t *testing.T) {
		s := mustStore(t, Options{MaxNodes: 10})
		w1(t, s, "u", "n2", "小猫", "名字", "叫团子", 0.5, 100)
		if got := s.Search("u", "团", 5, 0); len(got) == 0 || got[0].Text != "叫团子" {
			t.Fatalf("query 子串应命中索引 token：%v", got)
		}
		if got := s.Search("u", "叫团子呀", 5, 0); len(got) == 0 {
			t.Fatalf("索引 token 子串应命中 query：%v", got)
		}
	})
	t.Run("时间衰减：新触达排序靠前", func(t *testing.T) {
		s := mustStore(t, Options{MaxNodes: 10, DecayHalfLifeMs: 1000})
		w1(t, s, "u", "older", "玩具", "排名", "旧", 0.5, 0)
		w1(t, s, "u", "newer", "玩具", "排名", "新", 0.5, 5000)
		got := s.Search("u", "玩具 排名", 2, 6000)
		if len(got) != 2 || got[0].ID != "newer" {
			t.Fatalf("新近应排首：%v", got)
		}
	})
	t.Run("跨域 query 零泄漏", func(t *testing.T) {
		s := mustStore(t, Options{MaxNodes: 10})
		w1(t, s, "v", "sec", "秘密", "内容", "暗号", 0.9, 0)
		if got := s.Search("u", "秘密 内容 暗号", 5, 0); len(got) != 0 {
			t.Fatalf("跨域检索泄漏：%v", got)
		}
	})
	t.Run("纯读：重复查询逐位一致", func(t *testing.T) {
		s := mustStore(t, Options{MaxNodes: 30})
		w1(t, s, "u", "n1", "仓鼠", "名字", "叫布丁", 0.5, 0)
		w1(t, s, "u", "n2", "小猫", "名字", "叫团子", 0.5, 100)
		a, b := s.Search("u", "仓鼠 名字", 5, 300), s.Search("u", "仓鼠 名字", 5, 300)
		if len(a) != len(b) {
			t.Fatalf("重复查询结果数不一致")
		}
		for i := range a {
			if a[i] != b[i] {
				t.Fatalf("重复查询第 %d 位不一致：%v vs %v", i, a[i], b[i])
			}
		}
	})
}

// TestReadOnlyCycle 拒判→只读联动（T5 决策入口镜像 ci2 语义）：只读期写/更/
// 删/推进全拒、检索照常（只读缓存可用）；识别成功即恢复读写；只读态按域
// 独立（他域不受牵连）。
func TestReadOnlyCycle(t *testing.T) {
	s := mustStore(t, Options{MaxNodes: 20})
	w1(t, s, "u", "m1", "仓鼠", "名字", "叫布丁", 0.8, 0)
	before := s.Search("u", "仓鼠 名字", 5, 0)
	if len(before) != 1 {
		t.Fatalf("前置失败")
	}
	if err := s.SetReadOnly("u", true, 100); err != nil {
		t.Fatalf("SetReadOnly: %v", err)
	}
	if !s.ReadOnly("u") || s.ReadOnly("v") {
		t.Fatalf("只读态观测异常")
	}
	ops := map[string]func() error{
		"Write":   func() error { return s.Write("u", Node{ID: "w", Subject: "s", Pred: "p", Text: "t"}, nil) },
		"Update":  func() error { return s.Update("u", "m1", "新", 100) },
		"Delete":  func() error { return s.Delete("u", "m1") },
		"Advance": func() error { return s.Advance("u", "m1", 100) },
	}
	for name, op := range ops {
		if err := op(); !errors.Is(err, ErrReadOnly) {
			t.Errorf("只读期 %s 应拒绝，got %v", name, err)
		}
	}
	after := s.Search("u", "仓鼠 名字", 5, 100)
	if len(after) != 1 || after[0] != before[0] {
		t.Fatalf("只读期检索应照常：%v vs %v", after, before)
	}
	// 他域不受牵连（拒判是会话/域级联动，不是全局锁）
	if err := s.Write("v", Node{ID: "v1", Subject: "别的", Pred: "域", Text: "正常"}, nil); err != nil {
		t.Fatalf("他域写入被误拒：%v", err)
	}
	if err := s.SetReadOnly("u", false, 200); err != nil {
		t.Fatalf("恢复读写: %v", err)
	}
	if s.ReadOnly("u") {
		t.Fatalf("恢复后仍只读")
	}
	if err := s.Write("u", Node{ID: "w", Subject: "恢复", Pred: "写", Text: "成功"}, nil); err != nil {
		t.Fatalf("恢复后写入失败：%v", err)
	}
}

// TestCapacityEviction 容量代谢：MaxNodes 硬上限+淘汰计划（archived 最先→低
// 情绪权重→最旧触达）+边端点保护（写入即悬挂防护）+保护集占满=ErrCapacity
// （此前无状态变更）。
func TestCapacityEviction(t *testing.T) {
	t.Run("硬上限恒成立", func(t *testing.T) {
		s := mustStore(t, Options{MaxNodes: 5})
		for i := 0; i < 30; i++ {
			if err := s.Write("u", Node{Subject: "s", Pred: "p", Text: strconv.Itoa(i),
				EmoWeight: 0.1, TouchedAtMs: int64(i)}, nil); err != nil {
				t.Fatalf("Write %d: %v", i, err)
			}
			if s.Size() > 5 {
				t.Fatalf("容量越界：size=%d", s.Size())
			}
		}
		if len(s.Residuals()) != 0 {
			t.Fatalf("淘汰后残留：%v", s.Residuals())
		}
	})
	t.Run("高情绪权重留存（淘汰序：低情绪先出局）", func(t *testing.T) {
		s := mustStore(t, Options{MaxNodes: 3})
		w1(t, s, "u", "hi", "高情绪", "值", "重要", 1.0, 0)
		w1(t, s, "u", "lo", "低情绪", "值", "琐碎", 0.05, 100)
		w1(t, s, "u", "old", "久远", "值", "偏旧", 0.5, 0)
		w1(t, s, "u", "n4", "新写", "值", "内容", 0.5, 200) // 触发淘汰：emo 最低者出局
		if got := s.Search("u", "琐碎", 5, 200); len(got) != 0 {
			t.Fatalf("低情绪节点应被淘汰：%v", got)
		}
		if got := s.Search("u", "重要", 5, 200); len(got) != 1 {
			t.Fatalf("高情绪节点应留存：%v", got)
		}
	})
	t.Run("archived 最先淘汰（压过高情绪权重序）", func(t *testing.T) {
		s := mustStore(t, Options{MaxNodes: 3})
		w1(t, s, "u", "a", "事实", "值", "一", 0.9, 0) // 将被更新转 archived
		w1(t, s, "u", "b", "别事", "值", "二", 0.1, 10)
		w1(t, s, "u", "c", "另事", "值", "三", 0.1, 20)
		if err := s.Update("u", "a", "一改", 30); err != nil { // a→archived；腾位淘汰应吃 a 而非 b/c
			t.Fatalf("Update: %v", err)
		}
		if s.Size() != 3 {
			t.Fatalf("size=%d", s.Size())
		}
		if got := s.Search("u", "三", 5, 30); len(got) != 1 {
			t.Fatalf("活记忆不应先于 archived 出局：%v", got)
		}
		if got := s.Search("u", "二", 5, 30); len(got) != 1 {
			t.Fatalf("活记忆不应先于 archived 出局：%v", got)
		}
	})
	t.Run("边端点保护+保护集占满=ErrCapacity 无状态变更", func(t *testing.T) {
		s := mustStore(t, Options{MaxNodes: 2})
		w1(t, s, "u", "p1", "端点", "一", "文本", 0.5, 0)
		w1(t, s, "u", "p2", "端点", "二", "文本", 0.5, 0)
		err := s.Write("u", Node{ID: "x", Subject: "新", Pred: "值", Text: "内容"}, []Edge{{To: "p1", Rel: "r"}, {To: "p2", Rel: "r"}})
		if !errors.Is(err, ErrCapacity) {
			t.Fatalf("应 ErrCapacity，got %v", err)
		}
		if s.Size() != 2 || len(s.Residuals()) != 0 {
			t.Fatalf("失败写入后状态被变更：size=%d residuals=%v", s.Size(), s.Residuals())
		}
		// 去掉一边（p2 可淘汰）即成功，且淘汰后无悬挂
		if err := s.Write("u", Node{ID: "y", Subject: "新", Pred: "值", Text: "内容"}, []Edge{{To: "p1", Rel: "r"}}); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if s.Size() != 2 || len(s.Residuals()) != 0 {
			t.Fatalf("淘汰后异常：size=%d residuals=%v", s.Size(), s.Residuals())
		}
		if got := s.Search("u", "端点 一", 5, 0); len(got) == 0 || got[0].ID != "p1" {
			t.Fatalf("保护端点应留存（top1）：got=%v", got)
		}
		if s.nodes["p2"] != nil {
			t.Fatalf("被淘汰端点 p2 仍驻留节点表")
		}
	})
}

// TestResidualsZeroMixedOps 混合操作序列后全通道零残留（删除五通道+域索引+
// 邻接一致性的总观测面）。
func TestResidualsZeroMixedOps(t *testing.T) {
	s := mustStore(t, Options{MaxNodes: 40, DecayHalfLifeMs: 60_000})
	for i := 0; i < 12; i++ {
		uid := []string{"u", "v"}[i%2]
		if err := s.Write(uid, Node{ID: strconv.Itoa(i) + "n", Subject: "主题" + strconv.Itoa(i), Pred: "值",
			Text: "内容" + strconv.Itoa(i), EmoWeight: float64(i) / 12, TouchedAtMs: int64(i) * 100}, nil); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	for i := 0; i < 6; i++ { // 域随节点走（偶=u 奇=v——跨域操作属隔离面测试，此处只测残留）
		if err := s.Update([]string{"u", "v"}[i%2], strconv.Itoa(i)+"n", "更新值"+strconv.Itoa(i), 1000+int64(i)); err != nil {
			t.Fatalf("Update %d: %v", i, err)
		}
	}
	for i := 0; i < 4; i++ {
		if err := s.Advance([]string{"u", "v"}[i%2], strconv.Itoa(i)+"n", 2000); err != nil {
			t.Fatalf("Advance %d: %v", i, err)
		}
	}
	for i := 6; i < 10; i++ {
		if err := s.Delete([]string{"u", "v"}[i%2], strconv.Itoa(i)+"n"); err != nil {
			t.Fatalf("Delete %d: %v", i, err)
		}
	}
	if res := s.Residuals(); len(res) != 0 {
		t.Fatalf("混合操作后残留：%v", res)
	}
}
