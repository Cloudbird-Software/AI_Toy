// OnnxEmbedder —— bge-small-zh-v1.5 ONNX 真推理嵌入器（M2-T10，issue #134 收口；
// 实现 Embedder 接口；StubEmbedder 保留作模型缺失 fallback）。
//
// 装配惯例与 T3 vap / T14 vad 同源（yalue/onnxruntime_go + libonnxruntime.so.
// 1.29.0，进程内全局初始化一次；intra-op=2，RTF 口径可比）。本包核心存储
// （memory.go）保持纯 Go/标准库白名单；本文件是可选基础设施零件，随模型目录
// 在位与否生效。
//
// 导出契约（reports/eval/T10/，opset 17，Python 对拍 max_abs<1e-4）：
//   - input_ids [B,T] int64 / attention_mask [B,T] int64（batch=1 单流）；
//   - 输出 sentence_embedding [B,512]：CLS 池化 + L2 归一化已入图（口径以
//     模型随附 1_Pooling/config.json 为准 cls_token=true——BGE 官方用法；
//     bge-small-zh-v1.5 hidden=512，M1 注释「384 维」系 bge-small-en 笔误）。
//
// 模型/词表缺失时构造返回 error，由调用方降级 StubEmbedder（门禁测试侧
// 照 T3 engineOrSkip 基础设施 debt 惯例 Skip）。
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// bgeEmbedDim 输出维度契约（bge-small-zh-v1.5 hidden_size=512，构造时实测校验）。
const bgeEmbedDim = 512

// DefaultEmbedderModelDir 定位 bge-small-zh-v1.5 模型目录（含 onnx/model.onnx
// 与 vocab.txt）：env T10_BGE_MODEL_DIR → 数据集落盘路径（与 T14_*_MODEL 惯例同）。
func DefaultEmbedderModelDir() string {
	if p := os.Getenv("T10_BGE_MODEL_DIR"); p != "" {
		return p
	}
	return "/root/workspace/datasets/models/bge-small-zh-v1.5"
}

// defaultEmbedderLibraryPath 定位 libonnxruntime（与 inference 包同一覆盖口
// T14_ORT_LIB/T3_VAP_ORT_LIB 与系统路径——两包不互 import，惯例复制一份）。
func defaultEmbedderLibraryPath() string {
	for _, k := range []string{"T14_ORT_LIB", "T3_VAP_ORT_LIB"} {
		if p := os.Getenv(k); p != "" {
			return p
		}
	}
	for _, p := range []string{
		"/usr/local/lib/libonnxruntime.so.1.29.0",
		"/usr/lib/libonnxruntime.so.1.29.0",
		"/usr/local/lib/libonnxruntime.so",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

var (
	embedORTOnce sync.Once
	embedORTErr  error
)

func initEmbedORT() error {
	embedORTOnce.Do(func() {
		if p := defaultEmbedderLibraryPath(); p != "" {
			ort.SetSharedLibraryPath(p)
		}
		embedORTErr = ort.InitializeEnvironment()
	})
	return embedORTErr
}

// OnnxEmbedder bge-small-zh-v1.5 嵌入器（单流使用，与 Store 同定性不加锁）。
type OnnxEmbedder struct {
	tok  *BertWordPiece
	sess *ort.DynamicAdvancedSession
	out  *ort.Tensor[float32] // sentence_embedding [1,512]（形状固定复用）
}

// NewOnnxEmbedder 构造（modelDir 为 HF 模型目录，含 vocab.txt 与 onnx/model.onnx）。
// 构造即校验：词表加载、ORT 初始化、会话装配、输出维度实测（含一次预热推理，
// 顺带消除首查冷启动对延迟口径的干扰）。
func NewOnnxEmbedder(modelDir string) (*OnnxEmbedder, error) {
	tok, err := NewBertWordPiece(filepath.Join(modelDir, "vocab.txt"))
	if err != nil {
		return nil, err
	}
	if err := initEmbedORT(); err != nil {
		return nil, fmt.Errorf("memory: onnxruntime 初始化失败: %w", err)
	}
	opts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("memory: SessionOptions: %w", err)
	}
	defer opts.Destroy()
	// intra-op=2 与 T3 vap/T14 口径一致（4 核低功耗机 RTF 可比；嵌入查询延迟同量级）。
	if err := opts.SetIntraOpNumThreads(2); err != nil {
		return nil, fmt.Errorf("memory: SetIntraOpNumThreads: %w", err)
	}
	e := &OnnxEmbedder{tok: tok}
	e.sess, err = ort.NewDynamicAdvancedSession(filepath.Join(modelDir, "onnx", "model.onnx"),
		[]string{"input_ids", "attention_mask"},
		[]string{"sentence_embedding"}, opts)
	if err != nil {
		return nil, fmt.Errorf("memory: 加载嵌入模型 %s: %w", modelDir, err)
	}
	e.out, err = ort.NewTensor[float32](ort.Shape{1, bgeEmbedDim}, make([]float32, bgeEmbedDim))
	if err != nil {
		e.Destroy()
		return nil, fmt.Errorf("memory: 输出张量: %w", err)
	}
	// 维度契约实测 + 预热（shape 不符即失败——模型换版早暴露）。
	if _, err := e.Embed("初始化预热"); err != nil {
		e.Destroy()
		return nil, fmt.Errorf("memory: 嵌入器预热失败: %w", err)
	}
	return e, nil
}

// Embed 返回 text 的 512 维 L2 归一化稠密向量（池化已在图内，Go 侧零后处理）。
// nil 接收者防护：typed-nil 经接口装入 Options.Embedder 时按错误降级而非 panic
// （调用方合同=Embed error 非 nil 视为本次预计算/检索降级）。
func (e *OnnxEmbedder) Embed(text string) ([]float64, error) {
	if e == nil || e.sess == nil {
		return nil, fmt.Errorf("memory: 嵌入器未初始化（模型缺失降级面）")
	}
	ids := e.tok.Encode(text)
	shape := ort.Shape{1, int64(len(ids))}
	idData := make([]int64, len(ids))
	for i, v := range ids {
		idData[i] = int64(v)
	}
	in, err := ort.NewTensor(shape, idData)
	if err != nil {
		return nil, fmt.Errorf("memory: input_ids 张量: %w", err)
	}
	defer in.Destroy()
	mask := make([]int64, len(ids)) // 单句无 padding：全 1（make 零值是 0，须显式置 1）
	for i := range mask {
		mask[i] = 1
	}
	mt, err := ort.NewTensor(shape, mask)
	if err != nil {
		return nil, fmt.Errorf("memory: attention_mask 张量: %w", err)
	}
	defer mt.Destroy()
	if err := e.sess.Run([]ort.Value{in, mt}, []ort.Value{e.out}); err != nil {
		return nil, fmt.Errorf("memory: 嵌入推理: %w", err)
	}
	vec := make([]float64, bgeEmbedDim)
	for i, v := range e.out.GetData() {
		vec[i] = float64(v)
	}
	return vec, nil
}

// Destroy 释放会话与张量（进程内一次；调用方持有生命周期）。
func (e *OnnxEmbedder) Destroy() {
	if e.out != nil {
		e.out.Destroy()
		e.out = nil
	}
	if e.sess != nil {
		e.sess.Destroy()
		e.sess = nil
	}
}
