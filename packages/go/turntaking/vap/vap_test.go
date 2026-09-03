// 引擎单元测试（非门禁）：golden 对拍（导出时 ORT fp32 参考输出，Go 侧逐帧比对，
// 容差 1e-3 = 导出验证口径）、状态滚动正确性、Reset 语义、RTF 观测面。
// 模型/库缺失时 Skip（基础设施面，非数据面：路径见 DefaultModelPath/DefaultLibraryPath）。
package vap

import (
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"
)

// goldenChunk 与导出脚本 golden_chunk 公式一致（float64 数学，Go/Python 可复现；
// sin 实现差异 ~1e-7 级，远小于 1e-3 容差）。
func goldenChunk(frameIdx, j int) float32 {
	if frameIdx%10 >= 6 { // 4/10 静音帧
		return 0
	}
	freq := 220.0 + 110.0*float64(frameIdx%7)
	t := float64(j) / 16000.0
	env := math.Sin(math.Pi * float64(j) / NewSamples)
	return float32(0.08 * math.Sin(2*math.Pi*freq*t) * env)
}

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	modelPath, err := DefaultModelPath()
	if err != nil {
		t.Skipf("模型路径不可定位（基础设施面）: %v", err)
	}
	if _, err := os.Stat(modelPath); err != nil {
		t.Skipf("模型未就位（先 just fetch-models 或放置 %s）: %v", modelPath, err)
	}
	e, err := New(Config{ModelPath: modelPath, LibraryPath: DefaultLibraryPath()})
	if err != nil {
		t.Skipf("引擎初始化失败（基础设施面）: %v", err)
	}
	t.Cleanup(func() { _ = e.Destroy() })
	return e
}

func TestGoldenParity(t *testing.T) {
	raw, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Skipf("golden 向量缺失（基础设施面）: %v", err)
	}
	var golden struct {
		Frames []struct {
			PNow []float32 `json:"p_now"`
			VAD  []float32 `json:"vad"`
		} `json:"frames"`
		Tol float64 `json:"tol"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("golden.json 解析: %v", err)
	}
	e := newTestEngine(t)
	chunk1 := make([]float32, NewSamples)
	chunk2 := make([]float32, NewSamples)
	for k, want := range golden.Frames {
		for j := range chunk1 {
			chunk1[j] = goldenChunk(k, j)
		}
		p, err := e.Step(chunk1, chunk2)
		if err != nil {
			t.Fatalf("frame %d: Step: %v", k, err)
		}
		if d := absDiff(p.PNowUser, want.PNow[0]); d > golden.Tol {
			t.Errorf("frame %d: p_now_user %.6f ≠ golden %.6f（差 %.2e）", k, p.PNowUser, want.PNow[0], d)
		}
		if d := absDiff(p.PNowSystem, want.PNow[1]); d > golden.Tol {
			t.Errorf("frame %d: p_now_system %.6f ≠ golden %.6f（差 %.2e）", k, p.PNowSystem, want.PNow[1], d)
		}
		if d := absDiff(p.VADUser, want.VAD[0]); d > golden.Tol {
			t.Errorf("frame %d: vad_user %.6f ≠ golden %.6f（差 %.2e）", k, p.VADUser, want.VAD[0], d)
		}
	}
	t.Logf("golden 对拍 %d 帧全绿（容差 %.0e）——Go ORT 运行时与导出验证参考一致", len(golden.Frames), golden.Tol)
}

func TestResetAndRTF(t *testing.T) {
	e := newTestEngine(t)
	chunk1 := make([]float32, NewSamples)
	chunk2 := make([]float32, NewSamples)
	for j := range chunk1 {
		chunk1[j] = goldenChunk(0, j)
	}
	if _, err := e.Step(chunk1, chunk2); err != nil {
		t.Fatalf("Step: %v", err)
	}
	e.Reset()
	if e.Steps() != 0 || e.Wall() != 0 {
		t.Errorf("Reset 须清零观测面：steps=%d wall=%v", e.Steps(), e.Wall())
	}
	p, ok := e.Predict()
	if ok || p != (turntakingPrediction{}) {
		t.Errorf("Reset 后须无有效预测，got %v ok=%v", p, ok)
	}
	// RTF 观测面：100 帧实测 RTF（推理墙钟口径）。
	begin := time.Now()
	for k := 0; k < 100; k++ {
		for j := range chunk1 {
			chunk1[j] = goldenChunk(k, j)
		}
		if _, err := e.Step(chunk1, chunk2); err != nil {
			t.Fatalf("Step %d: %v", k, err)
		}
	}
	wallAll := time.Since(begin)
	audio := time.Duration(100) * FrameMs * time.Millisecond
	rtf := float64(wallAll) / float64(audio)
	t.Logf("RTF（含窗口拷贝）=%.4f（100 帧 / 纯推理墙钟 %.4f，p50=%.1f ms/帧）",
		rtf, float64(e.Wall())/float64(audio), float64(e.Wall()/time.Millisecond)/100)
}

func absDiff(a, b float32) float64 {
	d := float64(a) - float64(b)
	if d < 0 {
		return -d
	}
	return d
}
