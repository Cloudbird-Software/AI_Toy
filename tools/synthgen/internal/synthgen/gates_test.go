package synthgen

// T2 门禁测试（m2-spec §10 Mark 接线策略表，IR #90）：G0-01/G1-01 真实——canonical
// 负样本批（datasets/synth/batches，与 T4 门禁帧流同标签种子）manifest 实测：eval-only
// 拓扑（TrainN=0+purpose+训练管道零引用）与源分布（≥4 源类型、单源占比 ≤0.30）；
// G0-02/G1-02 debt——真实回流管线未建（每条先真实执行逻辑面/统计通道，失败即红，
// 再对数据面 t.Skipf 写明缺失物与消解路径；dispatchGate 按顶层整测 SKIP 判 debt，
// IR #76/ADR-0002）。口径与样本量声明唯一来源：configs/gates/T2.yaml（本文件只落
// 断言本体）。

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

// canonicalNegSpec canonical 负样本批冻结约定：与 T4 门禁帧流同 (generator@version,
// seed-label, duration)——T4-G0-01 消费 gen-tneg 6h、T4-G0-02 消费 gen-kwsadv 30min；
// manifest 侧（T2 消费）与帧流侧（T4 消费）对齐同一批（可审计：注册表+manifest 落
// 库，PCM 不入 git 由参数确定性重建——.gitignore frames.jsonl）。
type canonicalNegSpec struct {
	gen        Generator
	seedLabel  string
	durationMs int
}

var canonicalNeg = [...]canonicalNegSpec{
	{TNegGen(), "T4-G0-01", 6 * 3600 * 1000},
	{KWSAdvGen(), "T4-G0-02", 30 * 60 * 1000},
}

// repoRoot 定位仓库根（本包位于 <root>/tools/synthgen/internal/synthgen）。
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("未定位到仓库根: %v", err)
	}
	return root
}

// readCanonicalNeg 读 canonical 负样本批 manifest（统一入口 ReadNegBatch；返回批
// 须恰为 canonical 约定批——被其它同生成器批遮蔽即红，注册表卫生问题显式暴露）。
func readCanonicalNeg(t *testing.T, c canonicalNegSpec) (NegBatch, string) {
	t.Helper()
	b, dir, err := ReadNegBatch(filepath.Join(repoRoot(t), BatchesDir), c.gen.ID)
	if err != nil {
		t.Fatalf("%s canonical 批未落库（先跑 CLI generate-neg --seed-label %s --duration-ms %d）: %v",
			c.gen.ID, c.seedLabel, c.durationMs, err)
	}
	wantID := fmt.Sprintf("%s-%s-seed%d-d%d", c.gen.ID, c.gen.Version, NegSeed(c.seedLabel), c.durationMs)
	if b.ID != wantID {
		t.Fatalf("%s canonical 批被遮蔽：got %s, want %s（datasets/synth/batches 目录序首个须为 canonical 批）",
			c.gen.ID, b.ID, wantID)
	}
	return b, dir
}

// TestT2HoldoutZeroContamination T2-G0-01（BI-2.2/G0）真实：holdout 零污染
// （holdout_contamination_count ==0，M2 面=负样本批拓扑，m2-spec §10）：(1) 批
// 切分记录 TrainN=0/HoldoutN=0、全量入 eval 池（EvalN=N）；(2) purpose=eval-only
// 拒绝训练用途；(3) 训练管道产物（synth-train/synth-holdout 切分文件）在批目录
// 缺席；(4) packages/ 生产代码（非 _test.go）零引用 datasets/synth/synth-train
// ——负样本路径在训练管道拓扑不可达（管道 fitness 断言的 M2 面）。minhash 全量
// 比对待真实训练管线 M3+ 追加（spec §10）。
func TestT2HoldoutZeroContamination(t *testing.T) {
	gaterunner.Mark(t, "T2", "BI-2.2", "T2-G0-01", "G0")
	for _, c := range canonicalNeg {
		b, dir := readCanonicalNeg(t, c)
		if b.GeneratorID != c.gen.ID || b.GeneratorVersion != c.gen.Version ||
			b.Seed != NegSeed(c.seedLabel) || b.DurationMs != c.durationMs {
			t.Fatalf("%s canonical 批重建参数与冻结约定不符: %+v", c.gen.ID, b)
		}
		if b.TrainN != 0 || b.HoldoutN != 0 || b.EvalN != b.N || b.N != c.durationMs/NegFrameMs {
			t.Fatalf("%s 批切分记录 = train %d/holdout %d/eval %d/n %d（eval-only 须全量入池、永不进训练管道）",
				c.gen.ID, b.TrainN, b.HoldoutN, b.EvalN, b.N)
		}
		if b.Purpose != NegPurpose {
			t.Fatalf("%s 批 purpose = %q, want %q（负样本批用途声明）", c.gen.ID, b.Purpose, NegPurpose)
		}
		for _, forbidden := range []string{"synth-train.jsonl", "synth-holdout.jsonl"} {
			if _, err := os.Stat(filepath.Join(dir, forbidden)); !os.IsNotExist(err) {
				t.Fatalf("%s 批含 %s（训练管道拓扑：负样本永不切训练/holdout 文件）", c.gen.ID, forbidden)
			}
		}
	}
	// 训练引用拓扑扫描：packages/ 生产代码（Go，非 _test.go）零引用负样本批/训练
	// 切分路径——数据飞轮训练面（packages/go/data-flywheel）落地后任何引用即红。
	refs := scanProductionRefs(t, filepath.Join(repoRoot(t), "packages"))
	if len(refs) > 0 {
		t.Fatalf("holdout_contamination_count 红：生产代码引用负样本/训练切分路径 %v（拓扑：负样本路径在训练管道不可达）", refs)
	}
}

// scanProductionRefs 扫 packages/ 下生产代码（.go 非 _test.go）对负样本批/训练切分
// 路径的引用（datasets/synth、synth-train、synth-holdout——synthgen 工具侧与测试
// 侧合法消费，生产代码零引用）。
func scanProductionRefs(t *testing.T, pkgDir string) []string {
	t.Helper()
	var hits []string
	needles := [...]string{"datasets/synth", "synth-train", "synth-holdout"}
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

// piiProbes PII 探针模式（T2-G0-02 口径：姓名/地址/电话/学校残留=0——资产卡四类
// 探针面）。手机号词边界锚定防长数字字段（seed/ts）内部子串误报；小数点侧
// \b 锚不住（rms 等浮点小数段恰 11 位时形如电话号），由 piiResidualCount 显式
// 剔除紧邻 '.' 的命中。真实回流面=每批 200 条注入探针过管线，M2 无回流（见
// Skipf）；本面为残留计数通道对合成产物的实测。
var piiProbes = []*regexp.Regexp{
	regexp.MustCompile(`\b1[3-9]\d{9}\b`), // 大陆手机号
	regexp.MustCompile(`姓名|住址|地址|电话|学校`),  // PII 字段名（载荷自由文本面）
}

// piiResidualCount 计数 PII 探针命中（残留计数通道）。手机号命中剔除紧邻小数点
// 的浮点小数段（数值字段非电话文本）。
func piiResidualCount(data []byte) int {
	n := len(piiProbes[1].FindAll(data, -1))
	for _, loc := range piiProbes[0].FindAllIndex(data, -1) {
		if loc[0] > 0 && data[loc[0]-1] == '.' {
			continue
		}
		if loc[1] < len(data) && data[loc[1]] == '.' {
			continue
		}
		n++
	}
	return n
}

// TestT2PIIResidual T2-G0-02（BI-2.2/G0）debt：脱敏召回（pii_residual_count ==0，
// min_evidence n:200——每批 200 条注入已知 PII 探针过管线）。逻辑面：残留计数通道
// 真实执行——阳性对照（探针样本文本须命中 ≥2，通道非空转）+ canonical 批 manifest
// 与小批 frames.jsonl 重建记录实测扫描断言 0 残留（合成产物无自由文本载荷）。
// 数据面：真实回流管线（授权采集+探针注入+脱敏+声纹不可逆验证）M2 未建。
func TestT2PIIResidual(t *testing.T) {
	gaterunner.Mark(t, "T2", "BI-2.2", "T2-G0-02", "G0")
	// 阳性对照：探针样本文本（含手机号/字段名）须被计数通道命中——扫描器非空转。
	probe := []byte(`{"text": "联系人电话 13812345678，学校：阳光小学，住址xx路"}`)
	if got := piiResidualCount(probe); got < 2 {
		t.Fatalf("pii_residual_count 逻辑面红：探针样本文本命中 %d <2（残留计数通道空转）", got)
	}
	// 实测面：canonical 批 manifest + 小批重建帧记录（temp 生成——frames.jsonl 不入
	// git，由 (generator@version, seed, duration) 确定性重建）零 PII 残留。
	_, dir := readCanonicalNeg(t, canonicalNeg[0])
	manifest, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("canonical 批 manifest 读取失败: %v", err)
	}
	_, tmpDir, err := GenerateBatchNeg(TNegGen(), t.TempDir(), 60000, NegSeed("pii-probe"))
	if err != nil {
		t.Fatalf("探针批生成失败: %v", err)
	}
	frames, err := os.ReadFile(filepath.Join(tmpDir, "frames.jsonl"))
	if err != nil {
		t.Fatalf("探针批 frames.jsonl 读取失败: %v", err)
	}
	if residual := piiResidualCount(manifest) + piiResidualCount(frames); residual != 0 {
		t.Fatalf("pii_residual_count 红：合成产物 PII 残留 %d 处（manifest+重建帧记录）", residual)
	}
	t.Skipf("T2-G0-02 debt：脱敏召回需真实回流管线——授权采集+每批 200 条注入已知 PII 探针过管线（min_evidence n:200，姓名/地址/电话/学校残留=0；声纹再识别 ≤3%% 不可逆验证见资产卡），M2 无回流（m2-spec §10）。当前仅残留计数通道对合成产物实测 0 残留（合成产物无 PII 载荷，非脱敏召回证据）。回流管线就位后以真实探针批替换并去掉本 Skip。")
}

// TestT2SynthDiversity T2-G1-01（BI-2.1/G1）真实：合成多样性（single_source_share
// ≤0.30）——canonical 负样本批 manifest 实测（m2-spec §10）：源类型 ≥4 类
// （gen-tneg: speech_like/tv_noise/burst/mixed；gen-kwsadv: xiaoai/tianmao/xiaodu/
// nearconf——冻结表），等额轮转调度下单源占比恒 1/4（尾轮截断 ≤1 块余量）。vs
// 真实参考集分布距离面待真实语料回流（资产卡：分维度报告）。
func TestT2SynthDiversity(t *testing.T) {
	gaterunner.Mark(t, "T2", "BI-2.1", "T2-G1-01", "G1")
	const singleSourceLimit = 0.30 // configs/gates/T2.yaml T2-G1-01 threshold
	for _, c := range canonicalNeg {
		b, _ := readCanonicalNeg(t, c)
		if len(b.SourceShares) < 4 {
			t.Fatalf("%s 批源类型数 = %d < 4（m2-spec §2：源类型 ≥4 类）", c.gen.ID, len(b.SourceShares))
		}
		for src, share := range b.SourceShares {
			if share > singleSourceLimit {
				t.Fatalf("single_source_share 红：%s 批源 %s 占比 %.3f > %.2f（等额轮转须 ≤门槛）",
					c.gen.ID, src, share, singleSourceLimit)
			}
		}
	}
}

// TestT2FlywheelSpeed T2-G1-02（BI-2.3/G1）debt：飞轮转速（flywheel_improved_cycle_rate
// ≥0.50——≥50% 回流周期在 ≥1 核心指标统计显著提升，bootstrap CI 不含 0）。逻辑面：
// 判定通道真实执行——注入两个已知答案占位周期（A 两指标均显著提升/B 均不显著）×
// 各 4 点 before/after 对，配对 bootstrap（evalkit，固定种子确定复现）走通显著性
// 判定与周期占比计算，断言通道判定与注入期望一致；数据面：≥2 个真实回流周期
// 报告未建（M2 无回流，m2-spec §10）。
func TestT2FlywheelSpeed(t *testing.T) {
	gaterunner.Mark(t, "T2", "BI-2.3", "T2-G1-02", "G1")
	const flywheelGate = 0.50 // configs/gates/T2.yaml T2-G1-02 threshold
	// 占位周期报告（注入已知答案）：A=两指标配对差恒 +0.12/+0.08（CI 不含 0）；
	// B=两指标配对差均值≈0 且有散布（CI 含 0）。
	type metricPair struct{ before, after []float64 }
	cycles := [][]metricPair{
		{
			{before: []float64{0.50, 0.52, 0.48, 0.51}, after: []float64{0.62, 0.64, 0.60, 0.63}},
			{before: []float64{0.44, 0.46, 0.42, 0.45}, after: []float64{0.52, 0.54, 0.50, 0.53}},
		},
		{
			{before: []float64{0.50, 0.52, 0.48, 0.51}, after: []float64{0.510, 0.515, 0.484, 0.502}},
			{before: []float64{0.52, 0.50, 0.54, 0.51}, after: []float64{0.514, 0.504, 0.537, 0.515}},
		},
	}
	improved := 0
	for ci, cycle := range cycles {
		significant := false
		for mi, pair := range cycle {
			_, lo, hi := evalkit.PairedBootstrap(pair.after, pair.before, 1000, int64(1000+ci*100+mi))
			if lo > 0 || hi < 0 { // 95% CI 不含 0 → 该核心指标统计显著提升
				significant = true
			}
		}
		if want := ci == 0; significant != want { // 注入期望：A 显著、B 不显著
			t.Fatalf("flywheel_improved_cycle_rate 逻辑面红：占位周期 %d 显著性=%v 与注入期望不符（bootstrap CI 判定通道失效）", ci, significant)
		}
		if significant {
			improved++
		}
	}
	if rate := float64(improved) / float64(len(cycles)); rate < flywheelGate {
		t.Fatalf("flywheel_improved_cycle_rate 逻辑面红：%.2f < %.2f（通道判定与注入期望不符）", rate, flywheelGate)
	}
	t.Skipf("T2-G1-02 debt：飞轮转速需 ≥2 个真实回流周期报告（每周期核心指标 before/after 配对 bootstrap 95%%CI 不含 0 即显著提升；≥50%% 周期 ≥1 核心指标显著提升），M2 无真实回流（m2-spec §10）。当前仅注入占位周期对走通显著性判定与占比计算通道（非飞轮证据）。回流周期报告就位后以真实指标替换并去掉本 Skip。")
}
