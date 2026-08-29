// T20 门禁测试（m2-spec §10 Mark 接线策略表，IR #94）：一 ID 一顶层测试函数，
// 口径与样本量声明唯一来源 configs/gates/T20.yaml（本文件只落断言本体）。
// verdict 总表：G0-01/G1-02/G1-03 真实；G1-01 debt——拟真度判别需真实 holdout
// 对话 ≥50 段（suite=holdout），真实儿童对话采集未授权（m2-spec §11 升级项②）；
// 判别统计通道自检（可分离/不可分辨双锚点）已真实执行后才 Skipf 数据面
// （IR #76/ADR-0002：整测 SKIP 判 debt，不计 pass 不阻断）。
package usersim

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Cloudbird-Software/AI_Toy/tools/evalkit/evalkit"
	"github.com/Cloudbird-Software/AI_Toy/tools/gaterunner/gaterunner"
)

// gateProfiles T20-G1-03 口径：10 画像（age×patience×aggression 台阶覆盖
// 合法域两端与中部；Turns=journeys core 剧本步数量级）。
var gateProfiles = [10]Profile{
	{Age: 3, Patience: 0.0, Aggression: 0.0, Turns: 5},
	{Age: 3, Patience: 0.5, Aggression: 0.5, Turns: 5},
	{Age: 3, Patience: 1.0, Aggression: 1.0, Turns: 5},
	{Age: 5, Patience: 0.25, Aggression: 0.2, Turns: 6},
	{Age: 5, Patience: 0.75, Aggression: 0.8, Turns: 6},
	{Age: 7, Patience: 0.25, Aggression: 0.3, Turns: 7},
	{Age: 7, Patience: 0.75, Aggression: 0.1, Turns: 7},
	{Age: 9, Patience: 0.4, Aggression: 0.6, Turns: 8},
	{Age: 12, Patience: 0.1, Aggression: 0.0, Turns: 10},
	{Age: 12, Patience: 0.9, Aggression: 0.4, Turns: 10},
}

// journeyMetrics 旅程级指标（行为可控性的观测面）：话轮数/平均话轮长/四类边界
// 行为频率——全为画像纯函数（跨种子恒同→方差=0）。
func journeyMetrics(us []Utterance) [5]float64 {
	var m [5]float64
	m[0] = float64(len(us))
	if len(us) == 0 {
		return m
	}
	m[1] = avgRunes(us)
	for _, u := range us {
		switch u.Kind {
		case KindInterrupt:
			m[2]++
		case KindOffTopic:
			m[3]++
		case KindAttack:
			m[4]++
		}
	}
	for i := 2; i < len(m); i++ {
		m[i] /= float64(len(us))
	}
	return m
}

// TestT20G103BehaviorControllability T20-G1-03（BI-20.1/G1，真实）：行为可控性
// ——10 画像×3 种子：同 (画像,种子) 重放指标方差=0（逐字节同序列），且同画像跨
// 种子旅程级指标方差=0（配额/长度=画像纯函数）——方差=0 落声明噪声带内
// （journey_in_band_rate=1.0 ≥ 阈值 1.0，回归可比性前提）。
func TestT20G103BehaviorControllability(t *testing.T) {
	gaterunner.Mark(t, "T20", "BI-20.1", "T20-G1-03", "G1")
	const seedsN = 3
	seeds := [seedsN]int64{11, 22, 33}
	total, inBand := 0, 0
	for _, p := range gateProfiles {
		var cross [seedsN][5]float64
		for si, seed := range seeds {
			id := fmt.Sprintf("T20-G1-03/p%d/s%d", p.Age*100+int(p.Patience*10), si)
			first := journeyMetrics(Script(p, seed, id))
			replay := journeyMetrics(Script(p, seed, id))
			total++
			if first == replay { // 重放方差=0（逐指标）
				inBand++
			}
			cross[si] = first
		}
		for si := 1; si < seedsN; si++ { // 跨种子：画像纯函数→指标恒同→方差=0
			if cross[si] != cross[0] {
				t.Fatalf("画像 %v 种子 %d 旅程级指标漂移: %v vs %v（方差>0 超出声明噪声带）",
					p, seeds[si], cross[si], cross[0])
			}
		}
	}
	if total != 30 {
		t.Fatalf("样本量 %d ≠ 30（yaml min_evidence n:30——10 画像×3 种子）", total)
	}
	rate := float64(inBand) / float64(total)
	lo, hi := evalkit.Wilson(inBand, total)
	if rate < 1.0 {
		t.Fatalf("journey_in_band_rate=%.4f < 1.0（Wilson 95%%CI [%.4f,%.4f]，n=%d）", rate, lo, hi, total)
	}
	t.Logf("T20-G1-03：journey_in_band_rate=%.4f（n=%d，重放方差=0 且跨种子指标方差=0）", rate, total)
}

// reachProfile 各边界类别的最大化画像（配额=画像纯函数→可达性由构造保证，
// 仍实跑断言——m2-spec §10：interrupt 首位不受截断；attack 独立于耐心；
// offtopic 由年龄端点；repeat 在 interrupt/attack 取走后的剩余额度内）。
func reachProfile(kind string) Profile {
	switch kind {
	case KindInterrupt:
		return Profile{Age: 12, Patience: 0, Aggression: 0, Turns: 5}
	case KindAttack:
		return Profile{Age: 12, Patience: 1, Aggression: 1, Turns: 5}
	case KindOffTopic:
		return Profile{Age: 3, Patience: 1, Aggression: 0, Turns: 5}
	default: // repeat
		return Profile{Age: 12, Patience: 0, Aggression: 0, Turns: 5}
	}
}

// TestT20G102BoundaryBehaviorReach T20-G1-02（BI-20.2/G1，真实）：边界行为
// 可达性——4 类（打断/跑题/重复/攻击）×30 次注入（各 30 个不同 seed 的
// 最大化画像剧本），断言该类话语在生成序列中出现（不可达=对应安全/话轮测试
// 缺腿）。可达率点估计 ≥0.95（yaml rule=pass_rate，n=120）。
func TestT20G102BoundaryBehaviorReach(t *testing.T) {
	gaterunner.Mark(t, "T20", "BI-20.2", "T20-G1-02", "G1")
	injected, reached := 0, 0
	for _, kind := range []string{KindInterrupt, KindOffTopic, KindRepeat, KindAttack} {
		p := reachProfile(kind)
		for i := 0; i < 30; i++ {
			us := Script(p, int64(1000+i), fmt.Sprintf("T20-G1-02/%s/%d", kind, i))
			injected++
			for _, u := range us {
				if u.Kind == kind {
					reached++
					break
				}
			}
		}
	}
	if injected != 120 {
		t.Fatalf("注入数 %d ≠ 120（yaml min_evidence n:120——4 类×30）", injected)
	}
	rate := float64(reached) / float64(injected)
	lo, hi := evalkit.Wilson(reached, injected)
	if rate < 0.95 {
		t.Fatalf("boundary_behavior_reach_rate=%.4f < 0.95（Wilson 95%%CI [%.4f,%.4f]，n=%d）", rate, lo, hi, injected)
	}
	t.Logf("T20-G1-02：boundary_behavior_reach_rate=%.4f（%d/%d，Wilson 95%%CI [%.4f,%.4f]）",
		rate, reached, injected, lo, hi)
}

// TestT20G001SimulatorIsolation T20-G0-01（BI-20.3/G0，真实）：模拟器隔离——
// 模拟对话 0 条进任何训练集（防自我训练闭环分布塌缩）。拓扑+扫描双断言：
// (1) 落盘面：user-sim 生产代码零 IO import（纯生成器，无任何落盘通道——产物
//
//	唯一出口=内存返回值，journeys 侧经 Emit 只落 --out/reports，runtime 落点
//	断言见 tools/journeys realdriver 契约测试）；
//
// (2) 拓扑：packages/ 生产代码（非 _test.go）零引用训练集路径（datasets/synth、
//
//	synth-train/synth-holdout 切分、datasets/train）——模拟器输出在训练管道
//	拓扑不可达（与 T2-G0-01 同机制）。
func TestT20G001SimulatorIsolation(t *testing.T) {
	gaterunner.Mark(t, "T20", "BI-20.3", "T20-G0-01", "G0")
	root := repoRoot(t)

	// (1) 落盘面：零 IO import 扫描。
	pkgDir := filepath.Join(root, "packages", "go", "user-sim")
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	importRe := regexp.MustCompile(`^\s*(?:[\w./]+\s+)?"([^"]+)"$`)
	ioRefs := []string{}
	sawProd := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		sawProd = true
		data, err := os.ReadFile(filepath.Join(pkgDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		inImport := false
		for _, line := range strings.Split(string(data), "\n") {
			switch {
			case strings.HasPrefix(line, "import ("):
				inImport = true
			case inImport && line == ")":
				inImport = false
			case inImport:
				if m := importRe.FindStringSubmatch(line); m != nil {
					switch m[1] {
					case "os", "io", "os/exec", "bufio", "path/filepath", "os/io/fs":
						ioRefs = append(ioRefs, e.Name()+" 引用 "+m[1])
					}
				}
			}
		}
	}
	if !sawProd {
		t.Fatalf("user-sim 生产代码缺席（%s）", pkgDir)
	}
	if len(ioRefs) > 0 {
		t.Fatalf("sim_in_training_count 红：user-sim 生产代码含 IO import %v（模拟器不得有落盘面——产物只进 reports/ 由驱动层落盘）", ioRefs)
	}

	// (2) 拓扑：训练集路径引用扫描（packages/ 生产代码）。
	if refs := scanTrainingRefs(t, filepath.Join(root, "packages")); len(refs) > 0 {
		t.Fatalf("sim_in_training_count 红：生产代码引用训练集路径 %v（模拟器输出在训练管道拓扑不可达）", refs)
	}
	t.Logf("T20-G0-01：sim_in_training_count=0（零 IO 落盘面 + 训练路径拓扑零引用）")
}

// scanTrainingRefs 扫 packages/ 生产代码（.go 非 _test.go）对训练集路径的引用
// （datasets/synth 负样本批、synth-train/synth-holdout 训练切分、datasets/train
// ——工具侧与测试侧合法消费，生产代码零引用=模拟产物不可达训练面）。
func scanTrainingRefs(t *testing.T, pkgDir string) []string {
	t.Helper()
	needles := [...]string{"datasets/synth", "synth-train", "synth-holdout", "datasets/train"}
	var hits []string
	err := filepath.WalkDir(pkgDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, needle := range needles {
			if strings.Contains(string(data), needle) {
				hits = append(hits, path+" 引用 "+needle)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("拓扑扫描 %s 失败: %v", pkgDir, err)
	}
	return hits
}

// repoRoot 定位仓库根（本包位于 <root>/packages/go/user-sim）。
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("未定位到仓库根: %v", err)
	}
	return root
}

// TestT20G101RealismDiscrimination T20-G1-01（BI-20.3/G1）debt：拟真度判别
// （sim_real_discrimination_accuracy ≤0.75，yaml min_evidence n:50——真实
// holdout 对话 ≥50 段 vs 同量级模拟段 5 折交叉）。逻辑面真实执行：判别统计
// 通道自检——(a) 已知可分离分布（耐心端点画像）最近质心判别准确率 ≥0.95
// （通道灵敏）；(b) 同分布两半（同画像不同种子段）判别准确率 =0.5（不可分辨
// 基线=接近 50%，非「一眼假」）。数据面：真实儿童对话采集未授权（m2-spec
// §11 升级项②），M2 无 holdout 语料 → debt。
func TestT20G101RealismDiscrimination(t *testing.T) {
	gaterunner.Mark(t, "T20", "BI-20.3", "T20-G1-01", "G1")
	const n = 50 // 通道自检样本量（对齐 yaml min_evidence n:50 的量级面）

	// (a) 通道灵敏性：耐心端点画像（0 vs 1）特征可分离 → 判别高准确率。
	lo := Profile{Age: 7, Patience: 0, Aggression: 0.3, Turns: 8}
	hi := Profile{Age: 7, Patience: 1, Aggression: 0.3, Turns: 8}
	accSep := centroidAccuracy(
		simFeatures(lo, n, "realism-lo"),
		simFeatures(hi, n, "realism-hi"),
	)
	if accSep < 0.95 {
		t.Fatalf("判别通道不灵敏：已知可分离分布准确率 %.2f < 0.95（统计通道失效——拟真度面不可信）", accSep)
	}

	// (b) 不可分辨基线：同画像、种子空间对半（同分布）→ 判别准确率=0.5。
	same := Profile{Age: 7, Patience: 0.5, Aggression: 0.3, Turns: 8}
	accSame := centroidAccuracy(
		simFeatures(same, n, "realism-a"),
		simFeatures(same, n, "realism-b"),
	)
	if accSame > 0.5+1e-9 {
		t.Fatalf("同分布两半判别准确率 %.2f > 0.5（模拟段对自身不可分辨——拟真度基线被破坏）", accSame)
	}
	t.Logf("T20-G1-01 逻辑面：可分离锚点 acc=%.2f / 不可分辨锚点 acc=%.2f（判别通道自检通过）", accSep, accSame)

	t.Skipf("T20-G1-01 拟真度判别需真实 holdout 对话 ≥50 段 vs 同量级模拟段 5 折交叉" +
		"（configs/gates/T20.yaml min_evidence n:50，suite=holdout）；真实儿童对话采集未授权" +
		"（m2-spec §11 升级项②，founder 决策），M2 无真实语料——判别面保持 debt，" +
		"逻辑面（判别统计通道自检：灵敏性+不可分辨基线双锚点）已真实执行")
}

// simFeatures 生成 n 段模拟旅程的判别特征（平均话轮长+打断频率——旅程级指标面）。
func simFeatures(p Profile, n int, tag string) [][2]float64 {
	out := make([][2]float64, n)
	for i := 0; i < n; i++ {
		m := journeyMetrics(Script(p, int64(i), fmt.Sprintf("%s/%d", tag, i)))
		out[i] = [2]float64{m[1], m[2]}
	}
	return out
}

// centroidAccuracy 最近质心判别（前半训练质心、后半测试）：两集合特征的可分
// 性度量（通道自检用——非拟真度判定本体）。
func centroidAccuracy(a, b [][2]float64) float64 {
	ac, bc := centroid(a[:len(a)/2]), centroid(b[:len(b)/2])
	correct, total := 0, 0
	for _, x := range a[len(a)/2:] {
		total++
		if dist(x, ac) <= dist(x, bc) {
			correct++
		}
	}
	for _, x := range b[len(b)/2:] {
		total++
		if dist(x, bc) < dist(x, ac) {
			correct++
		}
	}
	return float64(correct) / float64(total)
}

func centroid(xs [][2]float64) [2]float64 {
	var c [2]float64
	for _, x := range xs {
		c[0] += x[0]
		c[1] += x[1]
	}
	c[0] /= float64(len(xs))
	c[1] /= float64(len(xs))
	return c
}

func dist(x, y [2]float64) float64 {
	dx, dy := x[0]-y[0], x[1]-y[1]
	return dx*dx + dy*dy
}
