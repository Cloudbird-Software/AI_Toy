// ORT 运行时装配（与 T3 vap 包同一绑定、同一惯例：yalue/onnxruntime_go +
// libonnxruntime.so.1.29.0，进程内全局初始化一次）。本包不引入第二个 ORT 绑定库。
package inference

import (
	"fmt"
	"os"
	"strconv"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// defaultIntraOpThreads 与 T3 vap 一致：4 核低功耗机上 intra-op=2（RTF 口径可比）。
const defaultIntraOpThreads = 2

var (
	ortOnce sync.Once
	ortErr  error
)

// initORT 全局一次初始化 ONNX Runtime（库路径首次生效）。
func initORT() error {
	ortOnce.Do(func() {
		if p := DefaultLibraryPath(); p != "" {
			ort.SetSharedLibraryPath(p)
		}
		ortErr = ort.InitializeEnvironment()
	})
	return ortErr
}

// DefaultLibraryPath 定位 libonnxruntime 共享库：
// env T14_ORT_LIB → env T3_VAP_ORT_LIB（与 T3 共用覆盖口）→ 常见系统路径。
func DefaultLibraryPath() string {
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
	return "" // 让 ort 用其默认查找（或由调用方显式配置）
}

// newSessionOpts 构造会话选项（intra-op 线程数：env T14_INTRA_OP_THREADS 覆盖
// → 入参 → defaultIntraOpThreads；覆盖口供 RTF 校准与延迟预算实测用）。
func newSessionOpts(threads int) (*ort.SessionOptions, error) {
	opts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("inference: SessionOptions: %w", err)
	}
	if err := opts.SetIntraOpNumThreads(effectiveIntraOpThreads(threads)); err != nil {
		opts.Destroy()
		return nil, fmt.Errorf("inference: SetIntraOpNumThreads: %w", err)
	}
	return opts, nil
}

// effectiveIntraOpThreads 线程数解析（newSessionOpts 与测试日志共用口径）。
func effectiveIntraOpThreads(requested int) int {
	if requested <= 0 {
		if n := os.Getenv("T14_INTRA_OP_THREADS"); n != "" {
			if v, err := strconv.Atoi(n); err == nil && v > 0 {
				return v
			}
		}
	}
	if requested <= 0 {
		return defaultIntraOpThreads
	}
	return requested
}

// dimOf 形状总元素数。
func dimOf(shape []int64) int {
	n := 1
	for _, d := range shape {
		n *= int(d)
	}
	return n
}

// DefaultVADModelPath 定位 Silero VAD ONNX：env T14_VAD_MODEL → 数据集落盘路径。
func DefaultVADModelPath() string {
	if p := os.Getenv("T14_VAD_MODEL"); p != "" {
		return p
	}
	return "/root/workspace/datasets/models/silero-vad/silero_vad.onnx"
}

// DefaultASRModelDir 定位 FireRedASR2 模型目录（encoder.int8.onnx /
// decoder.int8.onnx / tokens.txt / test_wavs/）：env T14_ASR_MODEL_DIR → 数据集落盘路径。
func DefaultASRModelDir() string {
	if p := os.Getenv("T14_ASR_MODEL_DIR"); p != "" {
		return p
	}
	return "/root/workspace/datasets/models/fireredasr2-sherpa-onnx"
}
