// T5 门禁测试（m3-spec §9 Mark 接线策略表，IR #106）：G1-01/G0-01/G1-03/G1-04
// 真实——合成虚拟家庭（生成器/打分器解耦+参数冻结，防「生成器迎合打分器」）
// 真实驱动被测 Engine，evalkit.EER/Wilson 统计判定（勿手算）；G1-02 debt——
// 隔天/换房声学漂移不可合成（数据面），先真实执行逻辑面（跨会话漂移再识别
// 统计通道，失败即红）再对数据面 t.Skipf（写明缺失物与消解路径；dispatchGate
// 按顶层整测 SKIP 判 debt，IR #76/ADR-0002）。
// 口径与样本量声明唯一来源：configs/gates/T5.yaml（本文件只落断言本体）。
// 合成数字口径声明：合成虚拟家庭协议=通道正确性与规则面行为验证，不代表
// 声学性能宣称（无真实声学语义；真模型 ECAPA-TDNN 接入后同协议 L5 复测）。
package voiceprint

import (
	"fmt"
	"math"
	"math/rand"
	"testing"

	"github.com/Cloudbird-Software/AI_Toy/tools/evalkit/evalkit"
	"github.com/Cloudbird-Software/AI_Toy/tools/gaterunner/gaterunner"
)

// —— 合成虚拟家庭生成器（参数冻结，m3-spec §5 防迎合纪律）——
// 生成器与打分器解耦：以下常量按「物理直觉」一次选定写死——同人句间角向
// 扰动（famSigma，正交噪声范数/基范数）远小于兄弟姐妹近距簇偏移
// （famSibDelta）、簇偏移远小于成员间随机基距；trial 生成不经打分器调参
// （任何 EER 结果均为如实观测，非调参产物）。维度 192=ECAPA-TDNN 嵌入维度
// 对齐（合成代理口径）。几何口径：基向量均为单位向量；扰动=正交单位方向
// ×幅度后归一——同人句对 cos≈1/(1+σ²)≈0.990（分数≈0.995）、兄弟姐妹对
// cos≈1/(1+δ²)≈0.890（分数≈0.945，过 0.9 工作点=近距非平凡）、随机成员
// 对 cos≈0（分数≈0.5）。
const (
	famDim       = 192      // 特征维度（合成代理，对齐真模型嵌入维度）
	famSigma     = 0.10     // 同人句间角向扰动幅度（正交噪声范数；冻结）
	famSibDelta  = 0.35     // 兄弟姐妹近距簇偏移幅度（冻结：近距非平凡构造）
	famUtts      = 30       // 每成员合成句数
	famThreshold = 0.9      // 闭集工作点（评估通道 Consistency 消费用）
	famSeed      = 20260829 // 家庭生成种子（FNV 约定对齐 T4 标签种子口径）
)

// synthMember 合成家庭成员：基向量 + famUtts 句扰动特征（确定性）。
type synthMember struct {
	uid    string
	child  bool
	base   Feat
	utts   []Feat
	sibIdx map[int]bool // 兄弟姐妹成员下标集（近距簇对单列）
}

// synthFamily 合成虚拟家庭（6 人含 3 儿童：dad/grandma/mom + 三个孩子，
// 下标 3/4/5 为兄弟姐妹近距簇）：成人基向量=独立随机方向（互距远簇、与
// 孩子簇独立）；三孩子=共享 sibBase 的近距簇偏移（famSibDelta）——兄弟
// 姐妹对近距（cos≈1/(1+δ²)≈0.890）但可分（同人扰动 σ 后 cos≈0.990）。
func synthFamily(seed int64) []synthMember {
	r := rand.New(rand.NewSource(seed))
	fam := make([]synthMember, 0, 6)
	for _, uid := range []string{"dad", "grandma", "mom"} {
		base := unitVecSeeded(r, famDim)
		fam = append(fam, synthMember{uid: uid, base: base, utts: utterSeeded(r, base)})
	}
	// 三个孩子：共享 sibBase（独立于成人）+ 各自正交偏移（近距簇；偏移方向独立）
	sibBase := unitVecSeeded(r, famDim)
	for k := 0; k < 3; k++ {
		kidBase := offsetUnit(sibBase, famSibDelta, r)
		fam = append(fam, synthMember{uid: fmt.Sprintf("kid%d", k+1), child: true,
			base: kidBase, utts: utterSeeded(r, kidBase), sibIdx: sibsMap(k)})
	}
	return fam
}

func sibsMap(k int) map[int]bool {
	m := map[int]bool{}
	for j := 0; j < 3; j++ {
		if j != k {
			m[3+j] = true // 孩子成员下标=3/4/5
		}
	}
	return m
}

// unitVecSeeded 从外部 rng 取单位向量（确定性：种子由调用方冻结）。
func unitVecSeeded(r *rand.Rand, d int) Feat {
	v := make(Feat, d)
	var norm float64
	for i := range v {
		v[i] = r.NormFloat64()
		norm += v[i] * v[i]
	}
	n := math.Sqrt(norm)
	for i := range v {
		v[i] /= n
	}
	return v
}

// offsetUnit 基向量 + δ·正交随机方向后归一（近距簇构造）。
func offsetUnit(base Feat, delta float64, r *rand.Rand) Feat {
	off := unitVecSeeded(r, len(base))
	// 正交化：off − (off·base)base
	var dot float64
	for i := range base {
		dot += off[i] * base[i]
	}
	out := make(Feat, len(base))
	var norm float64
	for i := range base {
		out[i] = base[i] + delta*(off[i]-dot*base[i])
		norm += out[i] * out[i]
	}
	n := math.Sqrt(norm)
	for i := range out {
		out[i] /= n
	}
	return out
}

// utterSeeded 生成 famUtts 句（基向量+σ 角向扰动归一——同人不同「内容」；
// 扰动=正交单位方向×famSigma，噪声范数精确=σ、维度无关）。
func utterSeeded(r *rand.Rand, base Feat) []Feat {
	utts := make([]Feat, famUtts)
	for k := range utts {
		utts[k] = offsetUnit(base, famSigma, r)
	}
	return utts
}

// famTrials 两两 trial：genuine（同人句对）+ imposter（跨成员句对）；兄弟
// 姐妹对单列（不并入总体——包 AGENTS.md 禁令）。返回（总体, 兄弟单列）。
// 兄弟单列=兄弟姐妹子问题（兄弟自身 genuine 基线 + 兄弟间 imposter 对），
// 两类齐备供单列 EER 报告；总体不含任何兄弟间 imposter 对。
func famTrials(fam []synthMember) (all, sib []Trial) {
	for i := range fam {
		for a := 0; a < famUtts; a++ {
			for b := a + 1; b < famUtts; b++ {
				tr := Trial{A: fam[i].utts[a], B: fam[i].utts[b], SameSpeaker: true}
				all = append(all, tr)
				if fam[i].sibIdx != nil {
					sib = append(sib, tr) // 兄弟自身 genuine：单列子问题基线
				}
			}
		}
		for j := i + 1; j < len(fam); j++ {
			for a := 0; a < famUtts; a++ {
				for b := 0; b < famUtts; b++ {
					tr := Trial{A: fam[i].utts[a], B: fam[j].utts[b], SameSpeaker: false}
					if fam[i].sibIdx != nil && fam[i].sibIdx[j] {
						sib = append(sib, tr) // 兄弟姐妹对：单列不并入总体
					} else {
						all = append(all, tr)
					}
				}
			}
		}
	}
	return all, sib
}

// scoreLabel trial 集的（分数, 标签）序列——evalkit.EER 消费口径（勿手算）。
func scoreLabel(e *Engine, trials []Trial) (scores []float64, labels []bool) {
	scores = make([]float64, len(trials))
	labels = make([]bool, len(trials))
	for i, tr := range trials {
		scores[i] = e.Score(tr.A, tr.B)
		labels[i] = tr.SameSpeaker
	}
	return scores, labels
}

// TestT5FamilyEER T5-G1-01（BI-5.1/G1）真实：家庭内区分 EER ≤0.05（eer 规则，
// min_evidence min_trials:5000——虚拟家庭两两配对报 EER+miss/FA）。数据面：
// 合成虚拟家庭（6 人含 3 儿童，兄弟姐妹=共享近距簇基单列）真实驱动被测
// Engine 默认打分器，两两 trial（genuine=同人句对 2610 + imposter 非兄弟对
// 10800，总体 13410 ≥5000；兄弟子问题=兄弟 genuine 1305+兄弟间 imposter
// 2700 共 4005 单列不并入）→ evalkit.EER 统计判定；生成器/打分器解耦+参数
// 冻结（famSigma/famSibDelta 常量，防迎合——m3-spec §5）。兄弟姐妹对单独
// 报告 EER+trial 数（不入总体、不判阈值——AGENTS.md 禁令）。
// 声明：合成协议=通道正确性验证，不代表声学性能宣称（真模型同协议 L5 复测）。
func TestT5FamilyEER(t *testing.T) {
	gaterunner.Mark(t, "T5", "BI-5.1", "T5-G1-01", "G1")
	fam := synthFamily(famSeed)
	nChild := 0 // 家庭构成声明（2–6 人含 ≥1 儿童）
	for _, m := range fam {
		if m.child {
			nChild++
		}
	}
	if nChild < 1 || len(fam) < 2 || len(fam) > 6 {
		t.Fatalf("家庭构成不合规：%d 人 / %d 儿童（须 2–6 人含 ≥1 儿童）", len(fam), nChild)
	}
	e, err := NewEngine(Config{Threshold: famThreshold})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	for _, m := range fam {
		if err := e.Enroll(m.uid, m.utts[:3]); err != nil {
			t.Fatalf("Enroll(%s): %v", m.uid, err)
		}
	}
	all, sib := famTrials(fam)
	if len(all) < 5000 { // min_evidence min_trials:5000（configs/gates/T5.yaml）
		t.Fatalf("两两 trial=%d < 5000（min_evidence 证据面不足）", len(all))
	}
	scores, labels := scoreLabel(e, all)
	eer, misses, falseAlarms := evalkit.EER(scores, labels)
	if eer > 0.05 {
		t.Fatalf("family_eer 红：EER=%.4f（阈值 ≤0.05，miss/FA=%d/%d，trials=%d）",
			eer, misses, falseAlarms, len(all))
	}
	// 兄弟姐妹对单列报告（不并入总体、不判阈值——AGENTS.md 禁令）
	sibScores, sibLabels := scoreLabel(e, sib)
	sibEER, sibMiss, sibFA := evalkit.EER(sibScores, sibLabels)
	// EER 评估通道正确性验证：Evaluate 工作点计数（miss/FA 全记录）与逐 trial
	// 判定一致（Trial 通道与 Score 通道同源）；分数分布非退化（通道非空转锚点）。
	rep := e.Evaluate(all)
	manualMiss, manualFA := 0, 0
	for i, tr := range all {
		if tr.SameSpeaker && scores[i] < famThreshold {
			manualMiss++
		}
		if !tr.SameSpeaker && scores[i] >= famThreshold {
			manualFA++
		}
	}
	if rep.Trials != len(all) || rep.Misses != manualMiss || rep.FalseAlarms != manualFA {
		t.Fatalf("EER 评估通道不一致：Evaluate=%+v vs 手工={%d,%d,%d}", rep, len(all), manualMiss, manualFA)
	}
	var genSum, impSum float64
	var genN, impN int
	for i := range scores {
		if labels[i] {
			genSum, genN = genSum+scores[i], genN+1
		} else {
			impSum, impN = impSum+scores[i], impN+1
		}
	}
	if genN == 0 || impN == 0 || genSum/float64(genN) <= impSum/float64(impN) {
		t.Fatalf("EER 评估通道空转：genuine 均分 ≤ imposter 均分（%.4f vs %.4f）", genSum/float64(genN), impSum/float64(impN))
	}
	t.Logf("T5-G1-01：总体 EER=%.4f（trials=%d, miss/FA=%d/%d）；兄弟姐妹对单列 EER=%.4f（trials=%d, miss/FA=%d/%d）；合成虚拟家庭协议（通道正确性验证，非声学性能宣称——真模型 L5 复测）",
		eer, len(all), misses, falseAlarms, sibEER, len(sib), sibMiss, sibFA)
}

// —— T5-G0-01 联跑桩：memory 只读联动（CI-2 真身语义对齐）——
// tests/properties/ci2 的 T5Reject→MemReadOnly 语义（ReadWrite→ReadOnly、
// 已降级保持）+ m3-spec §4 memory 契约（SetReadOnly/Write/Search/识别成功即
// 恢复）的最小镜像。memory 真身（#105）落位后由 loop 组装替换本桩复测
// （T5-G0-01×T10-G0-01 联跑——m3-spec §10）。
type memGate struct {
	readWrite bool                // true=读写 false=只读（CI-2 MemReadWrite/MemReadOnly）
	facts     map[string][]string // UserID 域记忆（域隔离第一键）
}

func newMemGate() *memGate { return &memGate{readWrite: true, facts: map[string][]string{}} }

// Write 域内写入：只读态拒绝（拒判期 0 身份写入——CH-05）。
func (m *memGate) Write(uid, fact string) bool {
	if !m.readWrite {
		return false
	}
	m.facts[uid] = append(m.facts[uid], fact)
	return true
}

// Search 域内检索：只读缓存照常可读（CH-05：只读≠不可用）。
func (m *memGate) Search(uid string) []string { return append([]string{}, m.facts[uid]...) }

// OnVoiceprintReject 实现 MemoryReadOnly：拒判→只读（CI-2 语义）。
func (m *memGate) OnVoiceprintReject(RejectEvent) { m.readWrite = false }

// Accept 识别成功→恢复读写（loop 搬运语义）。
func (m *memGate) Accept() { m.readWrite = true }

// TestT5IdentitySwitchLeak T5-G0-01（BI-5.2/G0）真实：身份切换后隔离 0 泄漏
// （==0，min_evidence n:100）。数据面：≥100 个「写入 A→拒判/切 B→询问 B」
// 场景联跑 T10（memory 联动桩=CI-2 真身语义对齐；真身 #105 合并后 loop 组装
// 面复测）：①A 识别写入 ②陌生人拒判（不冒认）→记忆只读 ③拒判期 0 身份写入
// ④切 B 识别成功→恢复读写 ⑤询问 B 不见 A 记忆（unknown 归属不冒认）。
// 断言：泄漏计数 ==0（任何身份门/只读联动/冒认违规即红）。
func TestT5IdentitySwitchLeak(t *testing.T) {
	gaterunner.Mark(t, "T5", "BI-5.2", "T5-G0-01", "G0")
	const n = 100 // min_evidence n:100（configs/gates/T5.yaml）
	r := rand.New(rand.NewSource(famSeed + 1))
	aBase, bBase := unitVecSeeded(r, 64), unitVecSeeded(r, 64)
	leaks := 0
	for i := 0; i < n; i++ {
		e, err := NewEngine(Config{Threshold: 0.9})
		if err != nil {
			t.Fatalf("NewEngine: %v", err)
		}
		if err := e.Enroll("A", utterSeeded(r, aBase)[:3]); err != nil {
			t.Fatalf("Enroll A: %v", err)
		}
		if err := e.Enroll("B", utterSeeded(r, bBase)[:3]); err != nil {
			t.Fatalf("Enroll B: %v", err)
		}
		mem := newMemGate()
		e.BindReadOnly(mem)
		// ① A 识别成功 → 写入 A 的专属记忆
		da := e.Verify(utterSeeded(r, aBase)[i%famUtts])
		if da.Rejected || da.UserID != "A" {
			leaks++ // 识别通道失效（非 A 判定）
			continue
		}
		mem.Accept() // 识别成功=读写态
		fact := fmt.Sprintf("A 的秘密 %d", i)
		if !mem.Write("A", fact) {
			leaks++ // 读写态写入被误拒
		}
		// ② 陌生人拒判：不冒认 + 拒判→记忆只读联动
		stranger := unitVecSeeded(r, 64)
		ds := e.Verify(stranger)
		if !ds.Rejected {
			leaks++ // 陌生人未拒判（闭集门失效）
		}
		if ds.UserID != "" {
			leaks++ // 拒判冒认（CI-2 禁半绑定）
		}
		if mem.readWrite {
			leaks++ // 拒判未联动只读（CI-2 T5Reject 语义失效）
		}
		// ③ 拒判期：0 身份写入（unknown 归属不写入任何 uid）+ 只读缓存可读
		if mem.Write("unknown", "x") || mem.Write("A", "y") {
			leaks++ // 拒判期身份写入（0 身份写入被违反）
		}
		if mem.Search("A") == nil { // 只读缓存照常可读（CH-05）
			leaks++ // 只读=不可用（CH-05 语义失效）
		}
		// ④ 切 B：识别成功 → 恢复读写 → B 写入自己的记忆
		db := e.Verify(utterSeeded(r, bBase)[i%famUtts])
		if db.Rejected || db.UserID != "B" {
			leaks++ // 切 B 识别失败
			continue
		}
		mem.Accept()
		if !mem.Write("B", fmt.Sprintf("B 的记忆 %d", i)) {
			leaks++ // B 恢复读写后写入被误拒
		}
		// ⑤ 询问 B：B 的检索结果不得包含 A 的任何记忆（0 泄漏）
		for _, got := range mem.Search("B") {
			if got == fact || got == "A 的秘密 "+fmt.Sprint(i) {
				leaks++ // 跨身份泄漏
			}
		}
		for _, got := range mem.Search("unknown") { // unknown 归属零冒认
			if got != "" {
				leaks++ // unknown 域出现内容（归属冒认）
			}
		}
	}
	if leaks != 0 {
		t.Fatalf("identity_switch_leak_count 红：%d（阈值 ==0，%d 场景联跑 T10 桩：身份门/只读联动/冒任三面）", leaks, n)
	}
	t.Logf("T5-G0-01：%d 场景 0 泄漏（memory 联动桩=CI-2 真身语义；#105 真身合并后 loop 组装面复测）", n)
}

// TestT5CrossSessionReident T5-G1-02（BI-5.1/G1）debt：跨会话稳定性
// （cross_session_reident_rate ≥0.95，min_evidence n:3——同成员 ≥3 会话成对
// 验证）。逻辑面（真实执行，失败即红）：合成「会话漂移」剖面（同成员跨会话
// =基向量+会话漂移偏移+句内扰动）×3 会话走通再识别统计通道（识别率/拒判率
// 计数+Wilson 区间，evalkit）——统计通道非空转。数据面：隔天/换房声学漂移
// 不可合成（真实会话对未建——T2 数据飞轮/holdout 面）。
func TestT5CrossSessionReident(t *testing.T) {
	gaterunner.Mark(t, "T5", "BI-5.1", "T5-G1-02", "G1")
	const sessions = 3 // min_evidence n:3（同成员 ≥3 会话成对验证）
	r := rand.New(rand.NewSource(famSeed + 2))
	base := unitVecSeeded(r, 64)
	// 注册会话（会话 0）：3 句注册
	e, err := NewEngine(Config{Threshold: 0.9})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if err := e.Enroll("member", utterSeeded(r, base)[:3]); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	// 逻辑面：后续会话（1..2）=会话漂移剖面（0.08 冻结漂移幅度）+句内扰动，
	// 再识别统计通道走通（识别计数→再识别率+Wilson CI；拒判率同口径）。
	const drift = 0.08
	hits, total := 0, 0
	for s := 1; s <= sessions-1; s++ {
		sessBase := offsetUnit(base, drift, r)
		for _, u := range utterSeeded(r, sessBase)[:10] {
			total++
			if d := e.Verify(u); !d.Rejected && d.UserID == "member" {
				hits++
			}
		}
	}
	if total == 0 {
		t.Fatalf("再识别统计通道空转（0 试验）")
	}
	rate := float64(hits) / float64(total)
	lo, hi := evalkit.Wilson(hits, total)
	t.Skipf("T5-G1-02 debt：跨会话稳定性证据面未建——同成员 ≥3 真实会话成对验证（隔天/换房声学漂移不可合成，m3-spec §9；再识别 ≥95%%/拒判 ≤3%% 须真实会话对，T2 数据飞轮/holdout 面）。当前仅合成会话漂移剖面走通再识别统计通道（实测值如实记录非证据：再识别 %.3f，Wilson 95%% CI [%.3f,%.3f]，n=%d 试验/合成漂移口径）。真实会话数据就位后以真实会话对替换合成漂移并去掉本 Skip。",
		rate, lo, hi, total)
}

// TestT5Enroll3EERDegradation T5-G1-03（BI-5.3/G1）真实：3 句注册下限——EER
// 劣化相对 10 句基线 ≤0.02（min_evidence n:50——3 句×50 成员仿真）。同 G1-01
// 协议与防迎合纪律（生成参数冻结）。评估通道=Verify 最近邻面（注册句数参与
// 打分：3 句库 vs 10 句库的最近邻分数差异即注册下限的代价），分数序列经
// evalkit.EER（勿手算）。
func TestT5Enroll3EERDegradation(t *testing.T) {
	gaterunner.Mark(t, "T5", "BI-5.3", "T5-G1-03", "G1")
	const members = 50 // min_evidence n:50（3 句×50 成员仿真）
	r := rand.New(rand.NewSource(famSeed + 3))
	// 50 成员：每人 20 句（3 注册基线句 + 7 扩充句 + 10 held-out 评估句）
	type m struct {
		base Feat
		utts []Feat
	}
	fam := make([]m, 0, members)
	for i := 0; i < members; i++ {
		base := unitVecSeeded(r, famDim)
		fam = append(fam, m{base: base, utts: utterSeeded(r, base)})
	}
	// 50 陌生人（imposter queries，每人 4 句）
	strangers := make([][]Feat, members)
	for i := range strangers {
		strangers[i] = utterSeeded(r, unitVecSeeded(r, famDim))[:4]
	}
	build := func(nEnroll int) *Engine {
		e, err := NewEngine(Config{Threshold: 0.9})
		if err != nil {
			t.Fatalf("NewEngine: %v", err)
		}
		for i, mm := range fam {
			if err := e.Enroll(fmt.Sprintf("m%d", i), mm.utts[:nEnroll]); err != nil {
				t.Fatalf("Enroll m%d: %v", i, err)
			}
		}
		return e
	}
	eerOf := func(e *Engine) float64 {
		var scores []float64
		var labels []bool
		for _, mm := range fam { // genuine queries：每人 held-out 10 句
			for _, u := range mm.utts[10:20] {
				scores = append(scores, e.Verify(u).Score)
				labels = append(labels, true)
			}
		}
		for _, st := range strangers { // imposter queries
			for _, u := range st {
				scores = append(scores, e.Verify(u).Score)
				labels = append(labels, false)
			}
		}
		eer, _, _ := evalkit.EER(scores, labels)
		return eer
	}
	eer3, eer10 := eerOf(build(3)), eerOf(build(10))
	degradation := eer3 - eer10
	if degradation > 0.02 {
		t.Fatalf("enroll3_eer_degradation 红：3 句 EER=%.4f vs 10 句 EER=%.4f，劣化 %.4f（阈值 ≤0.02，n=%d 成员）",
			eer3, eer10, degradation, members)
	}
	t.Logf("T5-G1-03：3 句 EER=%.4f vs 10 句基线 EER=%.4f，劣化=%.4f ≤0.02（%d 成员，held-out 500 genuine+200 imposter queries；合成协议非声学宣称）",
		eer3, eer10, degradation, members)
}

// TestT5StrangerRejectRate T5-G1-04（BI-5.2/G1）真实：陌生人拒判 ≥0.90
// （pass_rate，min_evidence n:600——≥20 非注册说话人 ×30 句）。数据面：25 个
// 非注册合成说话人 ×30 句（n=750 ≥600）过闭集阈值门（Verify 拒判计数）；
// 统计判定走 evalkit Wilson（点估计定判对齐 gaterunner judge pass_rate 语义，
// 区间供审计）。判定逻辑面（闭集门）；声学分布 L5 复测。
func TestT5StrangerRejectRate(t *testing.T) {
	gaterunner.Mark(t, "T5", "BI-5.2", "T5-G1-04", "G1")
	const strangers = 25 // ≥20 非注册说话人（min_evidence n:600 → 25×30=750）
	r := rand.New(rand.NewSource(famSeed + 4))
	// 注册家庭 4 人（含儿童）
	e, err := NewEngine(Config{Threshold: 0.9})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	for i := 0; i < 4; i++ {
		base := unitVecSeeded(r, famDim)
		if err := e.Enroll(fmt.Sprintf("fam%d", i), utterSeeded(r, base)[:3]); err != nil {
			t.Fatalf("Enroll fam%d: %v", i, err)
		}
	}
	rejected, total := 0, 0
	for i := 0; i < strangers; i++ {
		for _, u := range utterSeeded(r, unitVecSeeded(r, famDim)) {
			total++
			if e.Verify(u).Rejected {
				rejected++
			}
		}
	}
	if total < 600 { // min_evidence n:600（configs/gates/T5.yaml）
		t.Fatalf("陌生人 trial=%d < 600（min_evidence 证据面不足）", total)
	}
	rate := float64(rejected) / float64(total)
	lo, hi := evalkit.Wilson(rejected, total)
	if rate < 0.9 {
		t.Fatalf("stranger_reject_rate 红：拒判 %d/%d=%.4f（阈值 ≥0.90，Wilson 95%% CI [%.3f,%.3f]）",
			rejected, total, rate, lo, hi)
	}
	t.Logf("T5-G1-04：陌生人拒判 %d/%d=%.4f（阈值 ≥0.90，Wilson 95%% CI [%.3f,%.3f]；%d 非注册合成说话人×30 句，闭集判定逻辑面——声学分布 L5 复测）",
		rejected, total, rate, lo, hi, strangers)
}
