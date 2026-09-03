// Package memory embedding — T11 向量底座嵌入接口（M1 桩；M2 接 bge-small-zh ONNX）。
//
// 设计纪律（对齐 memory.go 合同）：
//   - 纯 Go、无 IO、无随机、无墙钟：StubEmbedder 仅用确定性哈希；
//   - Embedder 由调用方注入（Options.Embedder），nil 时语义检索降级；
//   - 预计算发生在 Write/Update 路径，与 keyword index 同级；
//   - purge 五通道 + embeddings 六通道全清（delete 零残留）。
package memory

import (
	"math"
	"strings"
)

// Embedder 文本嵌入注入面（TODO-M2：替换为 bge-small-zh-v1.5 ONNX 真推理）。
//
// 输入表面：node.Subject + " " + node.Pred + " " + node.Text。
// 维度合同：bge-small-zh-v1.5 = 384 维。
type Embedder interface {
	// Embed 返回 text 的稠密向量；error 非 nil 时调用方视为本次预计算/检索降级。
	Embed(text string) ([]float64, error)
}

// StubEmbedder 确定性桩嵌入器（M1 验收/回放用；M2 废弃）。
//
// 每个维度 d 由 FNV-1a 64-bit 哈希(text + ":" + strconv.Itoa(d)) 的
// 低位 53 位映射到 [-1,1]（保留符号位+小数点），保证：
//   - 同 text 同 dim 始终同值（确定性）；
//   - 无 random、无 IO、无浮点随机源。
type StubEmbedder struct{}

// Embed 返回 384 维确定性伪向量。
func (StubEmbedder) Embed(text string) ([]float64, error) {
	const dim = 384
	out := make([]float64, dim)
	h := fnvNew()
	for i := 0; i < dim; i++ {
		h.Write([]byte(text))
		h.Write([]byte{':'})
		h.Write([]byte{byte(i), byte(i >> 8)})
		v := h.Sum64()
		h.Sum64() // 推进内部状态（避免相邻 dim 线性相关）
		// 取 53 位映射到 [-1,1)
		f := float64(int64(v&((1<<53)-1)))/(1<<53)*2 - 1
		out[i] = f
		h.Reset()
	}
	return out, nil
}

// fnvNew 返回 FNV-1a 64-bit 初始值（offset basis）。
func fnvNew() fnv64a {
	return fnv64a(offset64)
}

type fnv64a uint64

const offset64 = 14695981039346656037

func (f *fnv64a) Write(p []byte) {
	for _, b := range p {
		*f ^= fnv64a(b)
		*f *= prime64
	}
}

func (f *fnv64a) Sum64() uint64 { return uint64(*f) }

func (f *fnv64a) Reset() { *f = offset64 }

const prime64 = 1099511628211

// cosineSimilarity 计算两个等长向量的余弦相似度（未归一化前可视为点积；
// 调用方保证等长且非 nil）。
func cosineSimilarity(a, b []float64) float64 {
	var aa, bb, ab float64
	for i := range a {
		aa += a[i] * a[i]
		bb += b[i] * b[i]
		ab += a[i] * b[i]
	}
	den := math.Sqrt(aa*bb + 1e-9)
	return ab / den
}

// textSurface 返回节点嵌入输入表面（与预计算口径一致）。
func textSurface(n *Node) string {
	var sb strings.Builder
	if n.Subject != "" {
		sb.WriteString(n.Subject)
		sb.WriteByte(' ')
	}
	if n.Pred != "" {
		sb.WriteString(n.Pred)
		sb.WriteByte(' ')
	}
	sb.WriteString(n.Text)
	return sb.String()
}
