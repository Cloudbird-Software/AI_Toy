// OnnxEmbedder 真实面测试（非门禁）：Go 分词/推理全链对 HF golden、中文检索
// 语义 sanity、单查延迟。模型/库缺失时 Skip（基础设施面，惯例照 T3/T14 真测；
// 目录与库路径见 onnx_embedder.go）。
package memory

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// newRealOnnxEmbedder 模型就位时构造（构造含预热），否则 Skip 并带原因。
func newRealOnnxEmbedder(t *testing.T) *OnnxEmbedder {
	t.Helper()
	dir := DefaultEmbedderModelDir()
	if _, err := os.Stat(filepath.Join(dir, "onnx", "model.onnx")); err != nil {
		t.Skipf("bge ONNX 未就位（基础设施面，目录 %s）: %v", dir, err)
	}
	e, err := NewOnnxEmbedder(dir)
	if err != nil {
		t.Skipf("嵌入器初始化失败（基础设施面）: %v", err)
	}
	t.Cleanup(e.Destroy)
	return e
}

// bgeGolden golden 对拍数据（reports/eval/T10/code/parity_bge.py 生成）。
type bgeGolden struct {
	Model      string  `json:"model"`
	OnnxSHA256 string  `json:"onnx_sha256"`
	Tolerance  float64 `json:"tolerance"`
	Dim        int     `json:"dim"`
	Pooling    string  `json:"pooling"`
	Cases      []struct {
		Text      string    `json:"text"`
		InputIDs  []int     `json:"input_ids"`
		Embedding []float64 `json:"embedding"`
	} `json:"cases"`
}

func loadBgeGolden(t *testing.T) *bgeGolden {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "golden_bge.json"))
	if err != nil {
		t.Fatalf("golden 读取失败: %v", err)
	}
	var g bgeGolden
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("golden 解析失败: %v", err)
	}
	return &g
}

// TestBertWordPieceGoldenIDs Go WordPiece 分词与 HF 全等（ID 序列零容差——
// golden 由 HF BertTokenizer 生成；分词漂移即此红）。
func TestBertWordPieceGoldenIDs(t *testing.T) {
	g := loadBgeGolden(t)
	dir := DefaultEmbedderModelDir()
	if _, err := os.Stat(filepath.Join(dir, "vocab.txt")); err != nil {
		t.Skipf("词表未就位（基础设施面，目录 %s）: %v", dir, err)
	}
	w, err := NewBertWordPiece(filepath.Join(dir, "vocab.txt"))
	if err != nil {
		t.Fatalf("词表加载: %v", err)
	}
	for _, c := range g.Cases {
		got := w.Encode(c.Text)
		if len(got) != len(c.InputIDs) {
			t.Errorf("分词长度漂移 %q: Go %d vs HF %d（Go=%v HF=%v）",
				c.Text, len(got), len(c.InputIDs), got, c.InputIDs)
			continue
		}
		for i := range got {
			if got[i] != c.InputIDs[i] {
				t.Errorf("分词 ID 漂移 %q @%d: Go %d vs HF %d（Go=%v HF=%v）",
					c.Text, i, got[i], c.InputIDs[i], got, c.InputIDs)
				break
			}
		}
	}
	t.Logf("WordPiece 分词对拍：%d 条用例全等（中英混排/标点/数字/大小写）", len(g.Cases))
}

// TestOnnxEmbedderGoldenParity Go 全链（分词→ONNX 推理）对 HF golden embedding
// max_abs ≤ golden 容差（1e-4 口径；对拍面 = Go ORT 1.29 vs Python HF eager）。
func TestOnnxEmbedderGoldenParity(t *testing.T) {
	g := loadBgeGolden(t)
	e := newRealOnnxEmbedder(t)
	worst := 0.0
	for _, c := range g.Cases {
		vec, err := e.Embed(c.Text)
		if err != nil {
			t.Fatalf("Embed(%q): %v", c.Text, err)
		}
		if len(vec) != g.Dim {
			t.Fatalf("维度 %d ≠ golden %d（bge-small-zh-v1.5 契约 512）", len(vec), g.Dim)
		}
		errAbs := 0.0
		for i := range vec {
			if d := math.Abs(vec[i] - c.Embedding[i]); d > errAbs {
				errAbs = d
			}
		}
		if errAbs > worst {
			worst = errAbs
		}
		if errAbs > g.Tolerance {
			t.Errorf("case %q max_abs=%.3e > %.1e", c.Text, errAbs, g.Tolerance)
		}
	}
	t.Logf("Go 全链对拍：%d 条 max_abs=%.3e ≤ %.1e（onnx sha256 %.12s…）",
		len(g.Cases), worst, g.Tolerance, g.OnnxSHA256)
}

// TestOnnxEmbedderNorm 输出恒为单位向量（L2 归一化入图验证——余弦=点积前提）。
func TestOnnxEmbedderNorm(t *testing.T) {
	e := newRealOnnxEmbedder(t)
	for _, text := range []string{"孩子喜欢恐龙", "a", "你好，世界！Hello 123"} {
		vec, err := e.Embed(text)
		if err != nil {
			t.Fatalf("Embed(%q): %v", text, err)
		}
		norm := 0.0
		for _, v := range vec {
			norm += v * v
		}
		norm = math.Sqrt(norm)
		if math.Abs(norm-1) > 1e-3 {
			t.Errorf("%q ‖v‖=%.6f ≠ 1（归一化面漂移）", text, norm)
		}
	}
}

// TestOnnxEmbedderChineseRetrievalSanity 中文检索语义 sanity（非门禁）：10 条
// 记忆+3 查询——「恐龙玩具」应把恐龙记忆排在天气/零食之前，「外婆叫什么名字」
// 应首中外婆条目（排序余量实测见 reports/eval/T10/）。
func TestOnnxEmbedderChineseRetrievalSanity(t *testing.T) {
	e := newRealOnnxEmbedder(t)
	s := mustStore(t, Options{MaxNodes: 20, Embedder: e})
	entries := []struct{ subj, pred, text string }{
		{"孩子", "喜欢", "孩子喜欢恐龙"},
		{"天气", "记录", "今天天气好"},
		{"外婆", "名字", "外婆的名字叫桂花"},
		{"玩具", "最爱", "最爱的玩具是积木"},
		{"幼儿园", "事件", "幼儿园今天举办运动会"},
		{"小狗", "名字", "小狗的名字叫团团"},
		{"博物馆", "事件", "在自然博物馆看到了霸王龙化石"},
		{"零食", "最爱", "最喜欢吃冰淇淋"},
		{"恐龙", "痴迷", "天天画恐龙还说长大要养恐龙"},
		{"绘本", "最爱", "最爱读太空绘本"},
	}
	for i, en := range entries {
		if err := s.Write("child", Node{ID: fmt.Sprintf("m%02d", i),
			Subject: en.subj, Pred: en.pred, Text: en.text, EmoWeight: 0.5}, nil); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	type hit struct {
		id  string
		sc  float64
		txt string
	}
	rank := func(q string, topK int) []hit {
		var out []hit
		for _, n := range s.SearchByEmbedding("child", q, topK, 0) {
			out = append(out, hit{n.ID, 0, n.Text})
		}
		return out
	}
	textOf := func(h []hit, id string) string {
		for _, x := range h {
			if x.id == id {
				return x.txt
			}
		}
		return ""
	}
	// 面①：恐龙查询——恐龙条目须压过天气/零食（语义聚类而非字面碰撞）。
	dino := rank("恐龙玩具", 5)
	if len(dino) < 5 {
		t.Fatalf("恐龙玩具召回 %d < 5", len(dino))
	}
	dinoIds := map[string]bool{}
	for _, x := range dino {
		dinoIds[x.id] = true
	}
	if !dinoIds["m00"] && !dinoIds["m08"] {
		t.Fatalf("恐龙记忆未进 top5：%v", dino)
	}
	// 面②：外婆查询——外婆条目必须第一（人名事实的语义可达性）。
	name := rank("外婆叫什么名字", 3)
	if len(name) == 0 || name[0].id != "m02" {
		t.Fatalf("外婆名字查询 top1 ≠ m02（外婆条目）：%v", name)
	}
	// 面③：成对余弦序——cos(恐龙玩具,孩子喜欢恐龙) > cos(恐龙玩具,今天天气好)
	// 且 > cos(恐龙玩具,最喜欢吃冰淇淋)（Python 预验证 0.76 vs 0.33/0.35 量级）。
	q, err := e.Embed("恐龙玩具")
	if err != nil {
		t.Fatal(err)
	}
	cos := func(text string) float64 {
		v, err := e.Embed(text)
		if err != nil {
			t.Fatalf("Embed(%q): %v", text, err)
		}
		return cosineSimilarity(q, v)
	}
	cDino, cWeather, cFood := cos("孩子喜欢恐龙"), cos("今天天气好"), cos("最喜欢吃冰淇淋")
	if !(cDino > cWeather && cDino > cFood) {
		t.Fatalf("恐龙排序翻转：dino=%.4f weather=%.4f food=%.4f", cDino, cWeather, cFood)
	}
	t.Logf("sanity：恐龙 top5=%v；外婆 top1=%s；cos 恐龙玩具→喜欢恐龙=%.4f > 天气=%.4f/冰淇淋=%.4f",
		keys(dinoIds), textOf(name, "m02"), cDino, cWeather, cFood)
}

// TestOnnxEmbedderQueryLatency 单查延迟面（非门禁，报告用 RTF P50 数据源）：
// 满载 400 节点库上 100 次查询嵌入+语义 top5 检索计时（含分词+推理+余弦全链）。
func TestOnnxEmbedderQueryLatency(t *testing.T) {
	e := newRealOnnxEmbedder(t)
	s, ps := gateRecallStore(t, 200, e) // 满载：200 探针+200 噪声（嵌入预计算在写入面）
	const at = 200 * 3_600_000
	elapsed := make([]float64, 0, len(ps))
	for i := range ps {
		st := time.Now()
		if got := s.SearchByEmbedding("child", ps[i].Subject+" "+ps[i].Pred, 5, at); len(got) == 0 {
			t.Fatalf("探针 %d 语义检索空结果", i)
		}
		elapsed = append(elapsed, float64(time.Since(st).Nanoseconds())/1e6)
	}
	sort.Float64s(elapsed)
	p50, p95 := elapsed[len(elapsed)/2], elapsed[len(elapsed)*95/100]
	t.Logf("语义检索全链延迟 n=%d：P50=%.3fms P95=%.3fms max=%.3fms（预算 150ms 的 RTF P50=%.4f）",
		len(elapsed), p50, p95, elapsed[len(elapsed)-1], p50/150)
}

// keys 便于日志输出（确定性排序）。
func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
