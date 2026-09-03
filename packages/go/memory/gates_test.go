// T10 门禁测试（m3-spec §9 Mark 接线策略表，IR #105）：一 ID 一顶层测试函数，
// 口径与样本量声明唯一来源 configs/gates/T10.yaml（本文件只落断言本体；统计
// 断言经 evalkit——勿手算）。verdict 总表：6/6 真实——探针事实/干扰噪声/
// 删除演练/容量仿真全部可在 CI 宿主真实代码路径驱动（spec §1 真测口径）；
// 数据面注记：种子家庭 4 周真实日志「记忆时刻」=holdout（经 tools/holdout，
// 本包代码不读，AGENTS.md 数据依赖行）；T10-G0-01 的 #106 voiceprint 拒判
// 注入联跑复测与 T10-G1-04 的 T15 预算实测面留卡序 #106/#108（联动语义已由
// TestReadOnlyCycle/决策计数口径先行覆盖）。T9-G0-06 随 T10-G0-02 联跑解禁
// （packages/go/safety gates_test.go 存储层删除演练，m3-spec §4）。
package memory

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Cloudbird-Software/AI_Toy/tools/evalkit/evalkit"
	"github.com/Cloudbird-Software/AI_Toy/tools/gaterunner/gaterunner"
)

// ---- 探针事实面（yaml min_evidence n:200：人名/宠物/喜好/事件/时间 5 类×40）----

// gateProbeCat 探针类别（10 锚点×4 变体=40 条/类；Pred 为类别追问谓词）。
type gateProbeCat struct {
	Name  string
	Pred  string
	Bases [10]string
	Vals  [10]string
}

var gateProbeCats = []gateProbeCat{
	{"人名", "名字", [10]string{"外婆", "爷爷", "姑姑", "舅舅", "班主任", "同桌", "邻居奶奶", "校医", "表哥", "教练"},
		[10]string{"叫桂花", "叫建国", "叫秀兰", "叫大明", "叫小芳", "叫志强", "叫玉梅", "叫国庆", "叫红梅", "叫建军"}},
	{"宠物", "名字", [10]string{"仓鼠", "小猫", "小狗", "乌龟", "兔子", "鹦鹉", "金鱼", "蜗牛", "刺猬", "蚕宝宝"},
		[10]string{"叫布丁", "叫团子", "叫豆豆", "叫雪球", "叫咪咪", "叫旺财", "叫龟龟", "叫慢慢", "叫球球", "叫小白"}},
	{"喜好", "最爱", [10]string{"零食", "水果", "玩具", "绘本", "动画", "游戏", "颜色", "歌曲", "运动", "甜点"},
		[10]string{"冰淇淋", "草莓", "积木", "恐龙书", "汪汪队", "捉迷藏", "天蓝色", "小星星", "跳绳", "小蛋糕"}},
	{"事件", "得了", [10]string{"运动会", "春游", "画画比赛", "朗诵会", "科学课", "值日", "合唱团", "跳绳赛", "手工课", "义卖会"},
		[10]string{"跑步第二名", "接力赛冠军", "看到了孔雀", "拿了小红花", "种子发芽了", "擦了黑板", "当了领唱", "跳了一百下", "折了纸飞机", "卖了两箱书"}},
	{"时间", "在", [10]string{"元旦", "春节", "清明", "劳动节", "儿童节", "端午", "中秋", "国庆", "生日会", "开学日"},
		[10]string{"一月一号", "正月初一", "四月五号", "五月一号", "六月一号", "五月初五", "八月十五", "十月一号", "三月十二", "九月一号"}},
}

// gateProbe 单条探针事实（Subject 全图唯一——追问 query=Subject+Pred）。
type gateProbe struct {
	Cat                 string
	Subject, Pred, Text string
	Emo                 float64
}

// gateProbes 确定性构造 200 条探针（无随机——回放可复现；EmoWeight∈[0.5,1] 阶梯）。
func gateProbes() []gateProbe {
	var ps []gateProbe
	for _, c := range gateProbeCats {
		for j := 0; j < 40; j++ {
			variant := j/10 + 1
			text := c.Vals[j%10]
			if c.Name != "时间" {
				text += strconv.Itoa(variant)
			}
			ps = append(ps, gateProbe{Cat: c.Name,
				Subject: c.Bases[j%10] + strconv.Itoa(variant), Pred: c.Pred, Text: text,
				Emo: 0.5 + 0.5*float64(j%10)/9})
		}
	}
	return ps
}

// gateRecallStore 构造 G1-01/G1-04 满载存储：t=0 注入 200 探针（uid=child，
// ID p%03d）；轮 1..stop（1h/轮）注入会话噪声——轮 1..120 热题干扰（20 个
// 热题探针=probes[i*10] 各 6 条同 (Subject,Pred) 近形噪声：新近噪声经时间
// 衰减自然压制老化探针——召回 10/50/200 轮三点降级的物理来源），轮 121..200
// 无关闲聊。确定性：同 stopTurn 同状态。emb 非 nil 时写入面同步预计算真实
// 嵌入（M2 语义面用；nil 与 M1 行为逐字节一致）。
func gateRecallStore(t *testing.T, stopTurn int, emb Embedder) (*Store, []gateProbe) {
	t.Helper()
	s := mustStore(t, Options{MaxNodes: 1200, DecayHalfLifeMs: DefaultDecayHalfLifeMs, Embedder: emb})
	ps := gateProbes()
	for i := range ps {
		w1(t, s, "child", fmt.Sprintf("p%03d", i), ps[i].Subject, ps[i].Pred, ps[i].Text, ps[i].Emo, 0)
	}
	const hotNoiseTurns = 120
	for turn := 1; turn <= stopTurn; turn++ {
		at := int64(turn) * 3_600_000
		if turn <= hotNoiseTurns {
			hot := ps[(turn-1)%20*10] // 热题探针（i%10==0 的 20 条，5 类各 4）
			w1(t, s, "child", fmt.Sprintf("hz%03d", turn), hot.Subject, hot.Pred,
				"好像叫"+strconv.Itoa(turn), 0.3, at)
			continue
		}
		w1(t, s, "child", fmt.Sprintf("cc%03d", turn), "闲聊"+strconv.Itoa(turn%7), "说了",
			"今天"+strconv.Itoa(turn%13), 0.2, at)
	}
	return s, ps
}

// gateRecallHits atTurn 轮测点全量探针 recall@5 计数（命中=探针节点入 top-5）。
func gateRecallHits(t *testing.T, s *Store, ps []gateProbe, atTurn int) int {
	t.Helper()
	hits := 0
	for i := range ps {
		id := fmt.Sprintf("p%03d", i)
		for _, n := range s.Search("child", ps[i].Subject+" "+ps[i].Pred, 5, int64(atTurn)*3_600_000) {
			if n.ID == id {
				hits++
				break
			}
		}
	}
	return hits
}

// TestT10G101ProbeRecall T10-G1-01（BI-10.1/G1，真实）：写入→检索往返
// recall@5——200 探针事实注入后 10/50/200 轮三点各测（yaml 阈 0.95=10 轮测点；
// 50/200 轮 ≥0.90/0.80 见资产卡）。降级物理来源=热题近形噪声×时间衰减
// （记忆年龄↑召回单调降的实测面）；统计面走 evalkit Wilson 95%CI。
func TestT10G101ProbeRecall(t *testing.T) {
	gaterunner.Mark(t, "T10", "BI-10.1", "T10-G1-01", "G1")
	pts := []struct {
		turn  int
		floor float64
	}{{10, 0.95}, {50, 0.90}, {200, 0.80}}
	measured := make([]string, 0, len(pts))
	for _, pt := range pts {
		s, ps := gateRecallStore(t, pt.turn, nil)
		if len(ps) != 200 {
			t.Fatalf("探针事实 %d 条 ≠ 200（yaml min_evidence n:200）", len(ps))
		}
		hits := gateRecallHits(t, s, ps, pt.turn)
		rate := float64(hits) / float64(len(ps))
		lo, hi := evalkit.Wilson(hits, len(ps))
		if rate < pt.floor {
			t.Fatalf("recall@5@%d轮=%.4f < %.2f（命中 %d/%d，Wilson 95%%CI [%.4f,%.4f]）",
				pt.turn, rate, pt.floor, hits, len(ps), lo, hi)
		}
		measured = append(measured, fmt.Sprintf("%d轮=%.4f(%d/200)", pt.turn, rate, hits))
	}
	t.Logf("T10-G1-01：recall@5 三点 %s（热题噪声×时间衰减的自然降级面）", strings.Join(measured, "｜"))
	// M2 语义面（真实 bge embedding 经 SearchByEmbedding，同阈值 0.95 同查询
	// 口径；模型缺失=基础设施 debt，照 T3 engineOrSkip 惯例 Skip，关键词面不受影响）。
	t.Run("语义面", func(t *testing.T) {
		emb, why := gateOnnxEmbedder(t)
		if emb == nil {
			t.Skipf("T10 语义面模型未就位（基础设施面 debt）：%s", why)
		}
		s, ps := gateRecallStore(t, 10, emb)
		hits := 0
		for i := range ps {
			for _, n := range s.SearchByEmbedding("child", ps[i].Subject+" "+ps[i].Pred,
				5, 10*3_600_000) {
				if n.ID == fmt.Sprintf("p%03d", i) {
					hits++
					break
				}
			}
		}
		rate := float64(hits) / float64(len(ps))
		if rate < 0.95 {
			t.Fatalf("语义 recall@5@10轮=%.4f < 0.95（真实 bge embedding 命中 %d/%d）",
				rate, hits, len(ps))
		}
		t.Logf("T10-G1-01 语义面：真实 embedding recall@5@10轮=%.4f（%d/200，OnnxEmbedder CLS 池化 512 维）",
			rate, hits)
	})
}

// gateOnnxEmbedder 门禁面嵌入器获取（非测试 helper——不 Skip，由调用方决定
// 语义面处理；nil=模型/库未就位，返回基础设施 debt 原因；非 nil 时随 t 释放。
// 返回 Embedder 接口而非 *OnnxEmbedder：避免 typed-nil 装进 Options.Embedder）。
func gateOnnxEmbedder(t *testing.T) (Embedder, string) {
	t.Helper()
	dir := DefaultEmbedderModelDir()
	if _, err := os.Stat(filepath.Join(dir, "onnx", "model.onnx")); err != nil {
		return nil, fmt.Sprintf("目录 %s 缺 onnx/model.onnx: %v", dir, err)
	}
	e, err := NewOnnxEmbedder(dir)
	if err != nil {
		return nil, fmt.Sprintf("嵌入器初始化失败: %v", err)
	}
	t.Cleanup(e.Destroy)
	return e, ""
}

// TestT10G102FactUpdate T10-G1-02（BI-10.2/G1，真实）：事实更新不矛盾——
// 50 组「写 A→更 B」后追问（yaml min_evidence n:50，rule=metric 点估计口径——
// 50<59 不做 CI 宣称）：新值可检索率 ≥0.95、新旧同引矛盾 ≤2%（旧值
// archived 不再可检索的构造保证+实测；10 组双旧值覆盖多旧值同槽全替换）。
func TestT10G102FactUpdate(t *testing.T) {
	gaterunner.Mark(t, "T10", "BI-10.2", "T10-G1-02", "G1")
	const n = 50 // yaml min_evidence n:50
	s := mustStore(t, Options{MaxNodes: 300})
	newVal, contradiction := 0, 0
	for i := 0; i < n; i++ {
		subj := fmt.Sprintf("事实%02d", i)
		w1(t, s, "child", fmt.Sprintf("a%02d", i), subj, "近况", "旧值"+strconv.Itoa(i), 0.6, int64(i))
		if i%5 == 0 { // 10 组双旧值（同 (Subject,Pred) 多旧值全 archived 的替换面）
			w1(t, s, "child", fmt.Sprintf("a%02dx", i), subj, "近况", "更旧值"+strconv.Itoa(i), 0.6, int64(i))
		}
		if err := s.Update("child", fmt.Sprintf("a%02d", i), "新值"+strconv.Itoa(i), int64(i)+1); err != nil {
			t.Fatalf("Update %d: %v", i, err)
		}
		hasNew, hasOld := false, false
		for _, got := range s.Search("child", subj+" 近况", 5, int64(i)+1) {
			if got.Text == "新值"+strconv.Itoa(i) {
				hasNew = true
			}
			if strings.Contains(got.Text, "旧值") {
				hasOld = true // 新旧同引=矛盾（旧值仍可检索）
			}
		}
		if hasNew {
			newVal++
		}
		if hasOld {
			contradiction++
		}
	}
	newRate := float64(newVal) / n
	contraRate := float64(contradiction) / n
	if newRate < 0.95 {
		t.Fatalf("fact_update_new_value_rate=%.4f < 0.95（新值可检索 %d/%d）", newRate, newVal, n)
	}
	if contraRate > 0.02 {
		t.Fatalf("新旧同引矛盾率=%.4f > 0.02（矛盾 %d/%d）", contraRate, contradiction, n)
	}
	t.Logf("T10-G1-02：新值率=%.4f（%d/%d）、新旧同引矛盾=%.4f（%d/%d，点估计口径）",
		newRate, newVal, n, contraRate, contradiction, n)
}

// TestT10G001CrossUserIsolation T10-G0-01（BI-10.3/G0，真实）：隔离零泄漏
// ——≥200 跨用户探针三层绕路（直接问/间接诱导/角色扮演），UserID 域隔离
// 0 泄漏（不是统计线，==0）。三层各带阳性对照（victim 自查同 query 命中
// ——证明攻击面真实非空洞）；写通道同门：跨域边构造/跨域更新/跨域删除全
// ErrCrossDomain。#106 voiceprint 拒判注入联跑复测留卡序（m3-spec §10）；
// 拒判→只读联动语义已由 TestReadOnlyCycle 覆盖。
func TestT10G001CrossUserIsolation(t *testing.T) {
	gaterunner.Mark(t, "T10", "BI-10.3", "T10-G0-01", "G0")
	s := mustStore(t, Options{MaxNodes: 400})
	const victim, attacker = "v", "u"
	// victim 秘密库 60 条（单 token 文本——子串诱导的真实攻击面）
	for i := 0; i < 60; i++ {
		w1(t, s, victim, fmt.Sprintf("sec%02d", i), fmt.Sprintf("秘密%02d", i), "暗号",
			fmt.Sprintf("暗号凤凰木%02d号", i), 0.8, int64(i))
	}
	// victim 域内关系边（关系路径通道在 victim 域内真实存在——绕路面非空洞）
	for i := 1; i <= 10; i++ {
		if err := s.Write(victim, Node{ID: fmt.Sprintf("secE%02d", i), Subject: fmt.Sprintf("联络%02d", i),
			Pred: "暗号", Text: fmt.Sprintf("暗号梧桐%02d号", i), EmoWeight: 0.8},
			[]Edge{{To: fmt.Sprintf("sec%02d", i-1), Rel: "提到"}}); err != nil {
			t.Fatalf("victim 域内边 %d: %v", i, err)
		}
	}
	// attacker 自有记忆 40 条 + 域内边
	ownBases := [10]string{"仓鼠", "积木", "绘本", "滑梯", "午饭", "好朋友", "小狗", "拼图", "秋千", "自行车"}
	for i := 0; i < 20; i++ {
		w1(t, s, attacker, fmt.Sprintf("own%02d", i),
			"我的"+ownBases[i%10]+strconv.Itoa(i/10+1), "记录", "开心"+strconv.Itoa(i), 0.5, int64(i))
	}
	for i := 20; i < 40; i++ {
		if err := s.Write(attacker, Node{ID: fmt.Sprintf("own%02d", i),
			Subject: "我的" + ownBases[i%10] + strconv.Itoa(i/10+1), Pred: "记录",
			Text: "开心" + strconv.Itoa(i), EmoWeight: 0.5},
			[]Edge{{To: fmt.Sprintf("own%02d", i-20), Rel: "喜欢"}}); err != nil {
			t.Fatalf("attacker 域内边 %d: %v", i, err)
		}
	}
	leaks, probes := 0, 0
	leakScan := func(q string) { // attacker 视角检索——结果中出现 victim 节点=泄漏
		probes++
		for _, n := range s.Search(attacker, q, 10, 999) {
			if n.UserID == victim {
				leaks++
			}
		}
	}
	// L1 直接问（80 探针）：以 victim 秘密全文/主谓直查
	if got := s.Search(victim, "秘密00 暗号 暗号凤凰木00号", 5, 999); len(got) == 0 {
		t.Fatalf("L1 阳性对照失败：victim 自查应命中（攻击面空洞=假绿）")
	}
	for i := 0; i < 60; i++ {
		leakScan(fmt.Sprintf("秘密%02d 暗号 暗号凤凰木%02d号", i, i))
	}
	for i := 0; i < 20; i++ {
		leakScan(fmt.Sprintf("秘密%02d 暗号", i))
	}
	// L2 间接诱导（60 探针）：以秘密 token 的子串近形改写查（tokenMatch 双向
	// 子串的真实攻击面）
	if got := s.Search(victim, "凤凰木00", 5, 999); len(got) == 0 {
		t.Fatalf("L2 阳性对照失败：子串改写应命中 victim 自有秘密")
	}
	for i := 0; i < 60; i++ {
		leakScan(fmt.Sprintf("凤凰木%02d", i))
	}
	// L3 角色扮演绕路（60 探针）：混合 query（自有 token+秘密 token——路径
	// 扩展/混合命中均不得带出 victim 节点）×40 + 跨域边构造（写通道绕路）×20
	if got := s.Search(victim, "我的仓鼠1 暗号凤凰木00号", 5, 999); len(got) == 0 {
		t.Fatalf("L3 阳性对照失败：混合 query 应命中 victim 秘密")
	}
	for i := 0; i < 40; i++ {
		leakScan("我的" + ownBases[i%10] + strconv.Itoa(i/10+1) + " 暗号凤凰木" + fmt.Sprintf("%02d", i) + "号")
	}
	for i := 0; i < 20; i++ { // 挂跨域边（绕过检索直接建图）恒拒
		probes++
		err := s.Write(attacker, Node{ID: fmt.Sprintf("xedge%02d", i), Subject: "扮演", Pred: "绕路",
			Text: "假装认识"}, []Edge{{To: fmt.Sprintf("sec%02d", i), Rel: "扮演"}})
		if !errors.Is(err, ErrCrossDomain) {
			t.Fatalf("跨域边构造被放行（i=%d）：err=%v", i, err)
		}
	}
	// 写通道同门：跨域更新/删除恒拒
	if err := s.Update(attacker, "sec00", "篡改", 999); !errors.Is(err, ErrCrossDomain) {
		t.Fatalf("跨域更新被放行：%v", err)
	}
	if err := s.Delete(attacker, "sec00"); !errors.Is(err, ErrCrossDomain) {
		t.Fatalf("跨域删除被放行：%v", err)
	}
	if probes < 200 {
		t.Fatalf("跨用户探针 %d 条 < 200（yaml min_evidence n:200）", probes)
	}
	if leaks != 0 {
		t.Fatalf("cross_user_leak_count=%d（阈值 ==0，不是统计线；三层绕路 %d 探针）", leaks, probes)
	}
	t.Logf("T10-G0-01：三层绕路 %d 探针（直接问 80/间接诱导 60/角色扮演绕路 60）泄漏=0", probes)
}

// TestT10G002DeleteResiduals T10-G0-02（BI-10.4/G0，真实）：删除即消失——
// 50 条删除×五通道复查（节点/关联边/检索索引/备份快照/操作日志 Residuals=0，
// yaml min_evidence n:50）。每删一条立即断言：①全通道零残留 ②被删节点独占
// token 双用户视角检索恒空 ③全部活节点查询经关系路径捞不回（悬挂边检测）
// ④重复删除 ErrNotFound（幂等）。含枢纽节点出入边递归清理与 archived 旧值
// 在场扰动面；T9-G0-06（数据最小化）随本条联跑解禁——存储层 schema 扫描×
// 删除演练见 packages/go/safety gates_test.go（同 PR 复测）。
func TestT10G002DeleteResiduals(t *testing.T) {
	gaterunner.Mark(t, "T10", "BI-10.4", "T10-G0-02", "G0")
	s := mustStore(t, Options{MaxNodes: 300})
	const child, sibling = "child", "sibling"
	// child 域 45 节点：先辐条 u05..u24（含 20 条指向枢纽的入边源 u05..u24 前
	// 20 条）→ 枢纽 u00..u04（各 4 出边）→ 入边源 u25..u29 → 事实 u30..u44
	// （其中 10 条经 Update 留 archived 旧值在场）
	for i := 5; i < 25; i++ {
		w1(t, s, child, fmt.Sprintf("u%02d", i), fmt.Sprintf("记事%02d", i), "内容",
			fmt.Sprintf("u内容%02d", i), 0.4+0.6*float64(i%7)/6, int64(i))
	}
	for i := 0; i < 5; i++ { // 枢纽：各 4 出边（To=u05+4i..u08+4i）
		var es []Edge
		for k := 0; k < 4; k++ {
			es = append(es, Edge{To: fmt.Sprintf("u%02d", 5+4*i+k), Rel: "提到"})
		}
		if err := s.Write(child, Node{ID: fmt.Sprintf("u%02d", i), Subject: fmt.Sprintf("记事%02d", i),
			Pred: "内容", Text: fmt.Sprintf("u内容%02d", i), EmoWeight: 0.6, TouchedAtMs: int64(i)}, es); err != nil {
			t.Fatalf("枢纽写入 %d: %v", i, err)
		}
	}
	for i := 25; i < 30; i++ { // 入边源（→枢纽）
		if err := s.Write(child, Node{ID: fmt.Sprintf("u%02d", i), Subject: fmt.Sprintf("记事%02d", i),
			Pred: "内容", Text: fmt.Sprintf("u内容%02d", i), EmoWeight: 0.5},
			[]Edge{{To: fmt.Sprintf("u%02d", i-25), Rel: "回应"}}); err != nil {
			t.Fatalf("入边源写入 %d: %v", i, err)
		}
	}
	for i := 30; i < 45; i++ {
		w1(t, s, child, fmt.Sprintf("u%02d", i), fmt.Sprintf("记事%02d", i), "内容",
			fmt.Sprintf("u内容%02d", i), 0.5, int64(i))
	}
	for i := 30; i < 40; i++ { // archived 旧值在场（删除不得扰动）
		if err := s.Update(child, fmt.Sprintf("u%02d", i), "改后"+strconv.Itoa(i), int64(i)+1); err != nil {
			t.Fatalf("Update %d: %v", i, err)
		}
	}
	// sibling 域 25 节点（v00..v04 为带入边枢纽——跨域删除面）
	for i := 5; i < 25; i++ {
		w1(t, s, sibling, fmt.Sprintf("v%02d", i), fmt.Sprintf("私密%02d", i), "内容",
			fmt.Sprintf("v内容%02d", i), 0.5, int64(i))
	}
	for i := 0; i < 5; i++ {
		if err := s.Write(sibling, Node{ID: fmt.Sprintf("v%02d", i), Subject: fmt.Sprintf("私密%02d", i),
			Pred: "内容", Text: fmt.Sprintf("v内容%02d", i), EmoWeight: 0.5},
			[]Edge{{To: fmt.Sprintf("v%02d", 5+4*i), Rel: "提到"}}); err != nil {
			t.Fatalf("sibling 枢纽写入 %d: %v", i, err)
		}
	}
	// 50 条删除=child 45+sibling 5（含枢纽出入边递归清理）
	deletions := make([]string, 0, 50)
	for i := 0; i < 45; i++ {
		deletions = append(deletions, fmt.Sprintf("u%02d", i))
	}
	for i := 0; i < 5; i++ {
		deletions = append(deletions, fmt.Sprintf("v%02d", i))
	}
	for _, id := range deletions {
		victim := *s.nodes[id]
		owner := victim.UserID
		if err := s.Delete(owner, id); err != nil {
			t.Fatalf("Delete %s: %v", id, err)
		}
		if res := s.Residuals(); len(res) != 0 { // ① 五通道全扫描
			t.Fatalf("删除 %s 后残留（五通道复查）：%v", id, res)
		}
		for _, who := range []string{child, sibling} { // ② 独占 token 双视角检索捞不回
			for _, q := range []string{victim.Subject + " " + victim.Pred, victim.Text} {
				for _, n := range s.Search(who, q, 10, 999) {
					if n.ID == id {
						t.Fatalf("被删节点 %s 经检索复活（who=%s q=%q）", id, who, q)
					}
				}
			}
		}
		for _, n := range propIDs(s) { // ③ 活节点查询经关系路径捞不回（悬挂边检测）
			for _, got := range s.Search(s.nodes[n].UserID, s.nodes[n].Subject+" "+s.nodes[n].Pred, 10, 999) {
				if got.ID == id {
					t.Fatalf("被删节点 %s 经悬挂边路径复活（邻居 %s 查询）", id, n)
				}
			}
		}
		if err := s.Delete(owner, id); !errors.Is(err, ErrNotFound) { // ④ 幂等
			t.Fatalf("重复删除 %s 应 ErrNotFound：%v", id, err)
		}
	}
	// 终态账目：child 45 + sibling 25 + 10 条更新新值节点（旧值→archived、
	// 新值落新槽——spec §4 新值替换）− 50 删除 = 30；且 probe 读到的 10 条
	// 记事 30..39 均为新值（删除旧值槽不扰动新值）
	if got, want := s.Size(), 45+25+10-50; got != want {
		t.Fatalf("终态 size=%d want %d", got, want)
	}
	for i := 30; i < 40; i++ {
		got := s.Search(child, fmt.Sprintf("记事%02d", i), 5, 999)
		if len(got) == 0 || got[0].Text != "改后"+strconv.Itoa(i) {
			t.Fatalf("记事%02d 更新新值缺失（probe 未读到最新）：got=%v", i, got)
		}
	}
	if res := s.Residuals(); len(res) != 0 {
		t.Fatalf("delete_residual_count=%d（阈值 ==0）：%v", len(res), res)
	}
	t.Logf("T10-G0-02：50 条删除×五通道复查 0 残留（含枢纽出入边递归清理+archived 在场）")
}

// TestT10G103CapacityMetabolism T10-G1-03（BI-10.4/G1，真实）：容量代谢——
// 仿真 1000 轮长会话×3（yaml min_evidence n:3，suite nightly）：MaxNodes=250
// 硬上限全程成立（进程内存储的内存上界=构造保证，无 OOM 面）+全通道零残留；
// 高情绪权重记忆（40 条 EmoWeight≥0.85 事实）留存 ≥0.90（淘汰序 archived→
// 低情绪→最旧的实测面——FIFO/随机淘汰即红）；会话墙钟塌陷护栏 <60s（实测
// ms 级）。峰值内存注记归 T14-G1-03 口径（runtime.MemStats，m3-spec §9）。
func TestT10G103CapacityMetabolism(t *testing.T) {
	gaterunner.Mark(t, "T10", "BI-10.4", "T10-G1-03", "G1")
	const turns, maxN, important = 1000, 250, 40
	for _, seed := range []int64{11, 22, 33} {
		s := mustStore(t, Options{MaxNodes: maxN, DecayHalfLifeMs: DefaultDecayHalfLifeMs})
		rng := rand.New(rand.NewSource(seed))
		for i := 0; i < important; i++ { // 开场高情绪权重事实（留存标的）
			w1(t, s, "child", fmt.Sprintf("imp%02d", i), fmt.Sprintf("重要%02d", i), "记牢",
				fmt.Sprintf("要紧事%02d", i), 0.85+0.15*rng.Float64(), 0)
		}
		var noise []string // 噪声 ID 池（偶发操作只打噪声——重要事实不经随机扰动）
		start := time.Now()
		for turn := 1; turn <= turns; turn++ {
			at := int64(turn) * 3_600_000
			for k, w := 0, 1+rng.Intn(2); k < w; k++ { // 每轮 1-2 条噪声（emo ≤0.7）
				id := fmt.Sprintf("n%04d_%d", turn, k)
				if err := s.Write("child", Node{ID: id, Subject: fmt.Sprintf("闲谈%04d", turn),
					Pred: "闲话", Text: "杂事" + strconv.Itoa(rng.Intn(50)),
					EmoWeight: rng.Float64() * 0.7, CreatedAtMs: at, TouchedAtMs: at}, nil); err != nil {
					t.Fatalf("seed=%d Write turn=%d: %v", seed, turn, err)
				}
				noise = append(noise, id)
			}
			if len(noise) > 0 { // 偶发推进/更新/删除（10%/5%/5%——archived 旧值与腾位面）
				id := noise[rng.Intn(len(noise))]
				switch rng.Intn(20) {
				case 0, 1:
					_ = s.Advance("child", id, at)
				case 2:
					_ = s.Update("child", id, "改口"+strconv.Itoa(turn), at)
				case 3:
					_ = s.Delete("child", id)
				}
			}
			if s.Size() > maxN { // 无 OOM 构造保证
				t.Fatalf("seed=%d turn=%d 容量越界 size=%d > %d", seed, turn, s.Size(), maxN)
			}
			if res := s.Residuals(); len(res) != 0 {
				t.Fatalf("seed=%d turn=%d 残留：%v", seed, turn, res)
			}
		}
		elapsed := time.Since(start)
		hits := 0 // 高情绪权重事实留存：追问 (Subject,Pred) 仍可检索
		for i := 0; i < important; i++ {
			for _, n := range s.Search("child", fmt.Sprintf("重要%02d 记牢", i), 5, int64(turns)*3_600_000) {
				if n.Subject == fmt.Sprintf("重要%02d", i) && n.Pred == "记牢" {
					hits++
					break
				}
			}
		}
		retention := float64(hits) / float64(important)
		if retention < 0.90 {
			t.Fatalf("seed=%d high_emotion_memory_retention=%.4f < 0.90（留存 %d/%d，size=%d）",
				seed, retention, hits, important, s.Size())
		}
		if elapsed > 60*time.Second { // 性能塌陷护栏（实测 ms 级）
			t.Fatalf("seed=%d 会话墙钟 %v > 60s（性能塌陷）", seed, elapsed)
		}
		t.Logf("T10-G1-03：seed=%d 1000 轮 size=%d/%d 留存=%.4f（%d/%d）墙钟=%v",
			seed, s.Size(), maxN, retention, hits, important, elapsed)
	}
}

// TestT10G104RetrievalLatency T10-G1-04（BI-10.4/G1，真实）：写读延迟与
// 成本——200 探针全量检索 CI 墙钟 P95≤150ms（yaml min_evidence n:200；满载
// 存储=200 探针+200 噪声）。P95=200 样本第 190 位描述性顺序统计量（T12-G1-03
// 同口径，非统计推断）；单轮记忆成本 ≤T15 预算（决策计数口径）：Search 纯
// 本地零上游调用（无 IO/无路由——构造保证），cloud_llm 段（路由 ≤30ms）零
// 占用；预算表实测面归 #108 收官刷新。
func TestT10G104RetrievalLatency(t *testing.T) {
	gaterunner.Mark(t, "T10", "BI-10.4", "T10-G1-04", "G1")
	emb, why := gateOnnxEmbedder(t) // nil=模型未就位（关键词面照跑，语义面 Skip）
	s, ps := gateRecallStore(t, 200, emb)
	const at = 200 * 3_600_000
	elapsed := make([]float64, 0, len(ps))
	for i := range ps { // 200 探针全量检索计时（单调钟）
		st := time.Now()
		s.Search("child", ps[i].Subject+" "+ps[i].Pred, 5, at)
		elapsed = append(elapsed, float64(time.Since(st).Nanoseconds())/1e6)
	}
	if len(elapsed) != 200 {
		t.Fatalf("检索样本 %d ≠ 200（yaml min_evidence n:200）", len(elapsed))
	}
	sort.Float64s(elapsed)
	p95 := elapsed[189] // 第 190 位顺序统计量
	if p95 > 150 {
		t.Fatalf("memory_retrieval_p95_ms=%.3f > 150（n=200，max=%.3f）", p95, elapsed[len(elapsed)-1])
	}
	t.Logf("T10-G1-04：检索 P95=%.4fms（n=200 满载 400 节点，p50=%.4fms max=%.4fms；"+
		"决策计数口径：单轮 0 次上游调用——Search 纯本地，T15 cloud_llm 段零占用）",
		p95, elapsed[99], elapsed[len(elapsed)-1])
	// M2 语义面（真实 bge embedding 经 SearchByEmbedding，同预算 150ms 同样本
	// 口径 200 探针满载库；模型缺失=基础设施 debt，照 T3 engineOrSkip 惯例 Skip）。
	t.Run("语义面", func(t *testing.T) {
		if emb == nil {
			t.Skipf("T10 语义面模型未就位（基础设施面 debt）：%s", why)
		}
		sem := make([]float64, 0, len(ps))
		for i := range ps { // 同一满载库，语义检索全链（分词+推理+余弦）计时
			st := time.Now()
			if got := s.SearchByEmbedding("child", ps[i].Subject+" "+ps[i].Pred, 5, at); len(got) == 0 {
				t.Fatalf("语义检索探针 %d 空结果", i)
			}
			sem = append(sem, float64(time.Since(st).Nanoseconds())/1e6)
		}
		sort.Float64s(sem)
		sp95 := sem[189]
		if sp95 > 150 {
			t.Fatalf("语义检索 P95=%.3fms > 150（n=200，max=%.3f；真实 bge embedding 全链）",
				sp95, sem[len(sem)-1])
		}
		t.Logf("T10-G1-04 语义面：SearchByEmbedding P95=%.3fms（P50=%.3fms，RTF P50=%.4f 预算口径；"+
			"写入面预计算嵌入×%d 节点已在库建期完成）", sp95, sem[99], sem[99]/150, s.Size())
	})
}
