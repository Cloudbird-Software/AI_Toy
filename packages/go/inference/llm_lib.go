// llama.cpp 最小动态装载绑定（ABI 钉死 v0.3.0，commit c1d0e7a004015f23bc0233470b747b596f29b264）。
// 装配惯例与 yalue/onnxruntime_go 一致：运行时 dlopen 共享库（默认
// /usr/local/lib/libllama.so.0.3.0，env T14_LLM_LIB 覆盖），构建期不依赖库文件——
// 库缺失时引擎降级桩（llm.go/llm_stub.go），无库机器上 go build 照常通过。
//
// 结构体 llama_model_params / llama_context_params / llama_batch 逐字段复刻
// include/llama.h v0.3.0（仅所用字段；回调/枚举以等宽占位类型声明，布局不变）。
// 默认参数经 dlsym 的 llama_*_default_params() 返回值在 C 侧填充，Go 侧只调
// 包装函数、不触碰结构体布局；换库版本属 ABI 破坏面，须换文件名
// libllama.so.<ver> 并回归本包测试（ADR-0012）。
//
// log 静音：装载成功后默认安装 no-op 回调（llama.cpp 默认向 stderr 打加载进度），
// env T14_LLM_VERBOSE=1 保留原始日志排障。
package inference

/*
#cgo CFLAGS: -std=c11 -O2
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <stdio.h>
#include <dlfcn.h>

// ---- llama.h v0.3.0 复刻（仅所用类型/结构体） ----
typedef int32_t llama_token;
typedef int32_t llama_pos;
typedef int32_t llama_seq_id;
typedef struct llama_model    llama_model;
typedef struct llama_context  llama_context;
typedef struct llama_vocab    llama_vocab;
typedef struct llama_memory_i * llama_memory_t;

typedef struct llama_batch {
    int32_t        n_tokens;
    llama_token  * token;
    float        * embd;
    llama_pos    * pos;
    int32_t      * n_seq_id;
    llama_seq_id ** seq_id;
    int8_t       * logits;
} llama_batch;

// 回调/嵌套结构以等宽占位声明（本桥一律传 NULL，布局对齐即可）
struct llama_model_params {
    void       * devices;                    // ggml_backend_dev_t *
    const void * tensor_buft_overrides;
    int32_t      n_gpu_layers;
    int32_t      split_mode;                 // enum llama_split_mode
    int32_t      load_mode;                  // enum llama_load_mode
    int32_t      main_gpu;
    const float* tensor_split;
    bool       (*progress_callback)(float, void *);
    void       * progress_callback_user_data;
    const void * kv_overrides;
    bool vocab_only;
    bool check_tensors;
    bool use_extra_bufts;
    bool no_host;
    bool no_alloc;
    bool load_mtp;
};

struct llama_context_params {
    uint32_t n_ctx;
    uint32_t n_batch;
    uint32_t n_ubatch;
    uint32_t n_seq_max;
    uint32_t n_rs_seq;
    uint32_t n_outputs_max;
    uint32_t n_outputs_max_per_seq;
    int32_t  n_threads;
    int32_t  n_threads_batch;
    int32_t  ctx_type;           // enum llama_context_type
    int32_t  rope_scaling_type;  // enum llama_rope_scaling_type
    int32_t  pooling_type;       // enum llama_pooling_type
    int32_t  attention_type;     // enum llama_attention_type
    int32_t  flash_attn_type;    // enum llama_flash_attn_type
    float    rope_freq_base;
    float    rope_freq_scale;
    float    yarn_ext_factor;
    float    yarn_attn_factor;
    float    yarn_beta_fast;
    float    yarn_beta_slow;
    uint32_t yarn_orig_ctx;
    float    defrag_thold;
    bool   (*cb_eval)(void *, bool, void *);   // ggml_backend_sched_eval_callback
    void   * cb_eval_user_data;
    int32_t  type_k;             // enum ggml_type
    int32_t  type_v;             // enum ggml_type
    bool   (*abort_callback)(void *);
    void   * abort_callback_data;
    bool embeddings;
    bool offload_kqv;
    bool no_perf;
    bool op_offload;
    bool swa_full;
    bool kv_unified;
    void * samplers;             // struct llama_sampler_seq_config *
    size_t n_samplers;
    struct llama_context * ctx_other;
};

// ---- dlsym 函数指针（真实签名，结构体按值传递留在 C 内） ----
static struct llama_model_params (*p_model_default_params)(void);
static struct llama_context_params (*p_ctx_default_params)(void);
static struct llama_model * (*p_model_load_from_file)(const char *, struct llama_model_params);
static void (*p_model_free)(struct llama_model *);
static struct llama_context * (*p_init_from_model)(struct llama_model *, struct llama_context_params);
static void (*p_free)(struct llama_context *);
static uint32_t (*p_n_ctx)(const struct llama_context *);
static int32_t (*p_tokenize)(const struct llama_vocab *, const char *, int32_t, llama_token *, int32_t, bool, bool);
static int32_t (*p_token_to_piece)(const struct llama_vocab *, llama_token, char *, int32_t, int32_t, bool);
static int32_t (*p_decode)(struct llama_context *, llama_batch);
static float * (*p_get_logits_ith)(struct llama_context *, int32_t);
static bool (*p_vocab_is_eog)(const struct llama_vocab *, llama_token);
static const struct llama_vocab * (*p_model_get_vocab)(const struct llama_model *);
static int32_t (*p_vocab_n_tokens)(const struct llama_vocab *);
static llama_memory_t (*p_get_memory)(const struct llama_context *);
static void (*p_memory_clear)(llama_memory_t, bool);
static void (*p_log_set)(void (*)(int32_t, const char *, void *), void *);

static void * g_lib = NULL;        // libllama 句柄
static void   g_quiet_cb(int32_t lvl, const char * text, void * ud) { (void)lvl; (void)text; (void)ud; }

static void dlsym_or_fail(void * h, const char * name, void ** out, char * err, int errsz) {
    void * p = dlsym(h, name);
    if (p == NULL) {
        snprintf(err, errsz, "libllama 缺符号 %s（版本不匹配？需 v0.3.0 ABI）", name);
    }
    *out = p;
}

// llama_dyn_init dlopen libllama 并解析全部符号。返回 0 成功；非 0 失败（err 写明原因）。
// ggml 系依赖（libggml/libggml-base/libggml-cpu）经 DT_NEEDED 由系统搜索路径解析
// （安装到 /usr/local/lib 并 ldconfig；ORT 同址惯例）。
// llama_dyn_preload 以 RTLD_GLOBAL 预载依赖库（按文件内 DT_SONAME 挂靠），
// 使后续 dlopen(libllama) 的 DT_NEEDED 在无系统 ld 配置的机器上也能解析。
// 失败静默忽略（ld 配置兜底）。
void llama_dyn_preload(const char * path) {
    if (g_lib != NULL) return;
    dlopen(path, RTLD_NOW | RTLD_GLOBAL); // NOLINT
}

int llama_dyn_init(const char * libllama_path, int quiet, char * err, int errsz) {
    err[0] = 0;
    if (g_lib != NULL) return 0;
    g_lib = dlopen(libllama_path, RTLD_NOW | RTLD_GLOBAL);
    if (g_lib == NULL) {
        snprintf(err, errsz, "dlopen %s 失败: %s", libllama_path, dlerror());
        return 1;
    }
    #define DLSYM(name, var) dlsym_or_fail(g_lib, name, (void **)&var, err, errsz)
    DLSYM("llama_model_default_params", p_model_default_params);
    if (err[0]) return 1;
    DLSYM("llama_context_default_params", p_ctx_default_params);
    DLSYM("llama_model_load_from_file", p_model_load_from_file);
    DLSYM("llama_model_free", p_model_free);
    DLSYM("llama_init_from_model", p_init_from_model);
    DLSYM("llama_free", p_free);
    DLSYM("llama_n_ctx", p_n_ctx);
    DLSYM("llama_tokenize", p_tokenize);
    DLSYM("llama_token_to_piece", p_token_to_piece);
    DLSYM("llama_decode", p_decode);
    DLSYM("llama_get_logits_ith", p_get_logits_ith);
    DLSYM("llama_vocab_is_eog", p_vocab_is_eog);
    DLSYM("llama_model_get_vocab", p_model_get_vocab);
    DLSYM("llama_vocab_n_tokens", p_vocab_n_tokens);
    DLSYM("llama_get_memory", p_get_memory);
    DLSYM("llama_memory_clear", p_memory_clear);
    DLSYM("llama_log_set", p_log_set);
    if (err[0]) return 1;
    if (quiet && p_log_set != NULL) {
        p_log_set(g_quiet_cb, NULL);
    }
    return 0;
}

// llama_dyn_model_load 加载模型（CPU：n_gpu_layers=0），返回句柄（NULL=失败）。
void * llama_dyn_model_load(const char * path) {
    struct llama_model_params mp = p_model_default_params();
    mp.n_gpu_layers = 0;
    return (void *)p_model_load_from_file(path, mp);
}

// llama_dyn_ctx_init 建上下文：n_ctx/n_batch/n_ubatch、线程数、关闭 embeddings。
void * llama_dyn_ctx_init(void * model, int n_ctx, int n_batch, int threads) {
    struct llama_context_params cp = p_ctx_default_params();
    cp.n_ctx = (uint32_t)n_ctx;
    cp.n_batch = (uint32_t)n_batch;
    cp.n_ubatch = (uint32_t)n_batch;
    cp.n_seq_max = 1;
    cp.n_threads = threads;
    cp.n_threads_batch = threads;
    cp.embeddings = false;
    return (void *)p_init_from_model((struct llama_model *)model, cp);
}

int32_t llama_dyn_vocab_n_tokens(void * model) {
    return p_vocab_n_tokens(p_model_get_vocab((struct llama_model *)model));
}

// llama_dyn_tokenize 返回 token 数（<0=需缓冲 -n）；出错返回 INT32_MIN。
int32_t llama_dyn_tokenize(void * model, const char * text, int32_t text_len, int32_t * out, int32_t max_out) {
    const struct llama_vocab * vocab = p_model_get_vocab((struct llama_model *)model);
    return p_tokenize(vocab, text, text_len, out, max_out, false, true);
}

// llama_dyn_decode 按显式 pos 解码一批 token，出侧取最后位置 logits（logits=NULL
// → 仅末 token 输出）。返回 0 成功；>0 为 KV 槽告警（上下文满）；<0 失败。
int32_t llama_dyn_decode(void * model, void * ctx, const int32_t * tokens, const int32_t * pos,
                         int32_t n, float ** logits_out, int32_t * n_logits) {
    llama_batch b;
    memset(&b, 0, sizeof(b));
    b.n_tokens = n;
    b.token = (llama_token *)tokens;
    b.pos = (llama_pos *)pos;
    int32_t rc = p_decode((struct llama_context *)ctx, b);
    if (rc != 0) return rc;
    // get_logits_ith 入参为批内 token 下标（output_ids 映射到行）；
    // 批内仅末 token 标记输出（logits=NULL → 末 token），故取 n-1。
    *logits_out = p_get_logits_ith((struct llama_context *)ctx, n - 1);
    *n_logits = p_vocab_n_tokens(p_model_get_vocab((struct llama_model *)model));
    return rc;
}

// llama_dyn_token_to_piece 单 token 反解（special=true，保 <think> 等特殊串可见）。
int32_t llama_dyn_token_to_piece(void * model, int32_t token, char * buf, int32_t buflen) {
    const struct llama_vocab * vocab = p_model_get_vocab((struct llama_model *)model);
    return p_token_to_piece(vocab, (llama_token)token, buf, buflen, 0, true);
}

bool llama_dyn_is_eog(void * model, int32_t token) {
    return p_vocab_is_eog(p_model_get_vocab((struct llama_model *)model), (llama_token)token);
}

void llama_dyn_memory_clear(void * ctx) {
    p_memory_clear(p_get_memory((struct llama_context *)ctx), true);
}

void llama_dyn_free_ctx(void * ctx) { p_free((struct llama_context *)ctx); }
void llama_dyn_free_model(void * model) { p_model_free((struct llama_model *)model); }
uint32_t llama_dyn_n_ctx(void * ctx) { return p_n_ctx((struct llama_context *)ctx); }
*/
import "C"

import (
	"fmt"
	"os"
	"path/filepath"
	"unsafe"
)

// DefaultLLMLibPath 定位 libllama 共享库：env T14_LLM_LIB → 常见系统路径
// （版本化文件名钉 ABI，同 libonnxruntime.so.1.29.0 惯例）。
func DefaultLLMLibPath() string {
	if p := os.Getenv("T14_LLM_LIB"); p != "" {
		return p
	}
	for _, p := range []string{
		"/usr/local/lib/libllama.so.0.3.0",
		"/usr/lib/libllama.so.0.3.0",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// llamaDynLoad 装载 libllama 并解析符号（进程内幂等；失败返回原因）。
// ggml 依赖先按同目录候选名 RTLD_GLOBAL 预载（按 DT_SONAME 挂靠），
// 无系统 ld 配置的机器上 DT_NEEDED 也能解析。
func llamaDynLoad() error {
	libPath := DefaultLLMLibPath()
	if libPath == "" {
		return fmt.Errorf("inference: llm: 未找到 libllama（T14_LLM_LIB 或 /usr/local/lib/libllama.so.0.3.0）")
	}
	dir := filepath.Dir(libPath)
	for _, dep := range []string{"libggml-base.so.0", "libggml-cpu.so.0", "libggml.so.0"} {
		p := filepath.Join(dir, dep)
		if _, err := os.Stat(p); err == nil {
			cp := C.CString(p)
			C.llama_dyn_preload(cp)
			C.free(unsafe.Pointer(cp))
		}
	}
	cPath := C.CString(libPath)
	defer C.free(unsafe.Pointer(cPath))
	quiet := os.Getenv("T14_LLM_VERBOSE") == ""
	errBuf := make([]C.char, 512)
	if rc := C.llama_dyn_init(cPath, C.int(boolToInt(quiet)), &errBuf[0], C.int(len(errBuf))); rc != 0 {
		return fmt.Errorf("inference: llm: %s", C.GoString(&errBuf[0]))
	}
	return nil
}

func boolToInt(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

// llamaModelLoad 加载 GGUF 模型（CPU，n_gpu_layers=0；NULL=失败）。
func llamaModelLoad(path string) unsafe.Pointer {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	return C.llama_dyn_model_load(cPath)
}

// llamaCtxInit 建推理上下文（n_ctx/n_batch/线程数；NULL=失败）。
func llamaCtxInit(model unsafe.Pointer, nCtx, nBatch, threads int) unsafe.Pointer {
	return C.llama_dyn_ctx_init(model, C.int(nCtx), C.int(nBatch), C.int(threads))
}

// llamaVocabNTokens 词表大小（<=0 异常）。
func llamaVocabNTokens(model unsafe.Pointer) int32 {
	return int32(C.llama_dyn_vocab_n_tokens(model))
}

func llamaMemoryClear(ctx unsafe.Pointer) { C.llama_dyn_memory_clear(ctx) }

func llamaFreeCtx(ctx unsafe.Pointer) { C.llama_dyn_free_ctx(ctx) }

func llamaFreeModel(model unsafe.Pointer) { C.llama_dyn_free_model(model) }

// llamaTokenize 分词（parse_special=true；缓冲不足时按 -need 重配重试）。
// 返回 token 切片与是否 EOG 判定用的裸 id 面（引擎自行处理）。
func llamaTokenize(model unsafe.Pointer, text string) ([]int32, error) {
	cText := C.CString(text)
	defer C.free(unsafe.Pointer(cText))
	buf := make([]C.int32_t, 0, 256)
	for {
		if cap(buf) > 1<<20 {
			return nil, fmt.Errorf("inference: llm: tokenize 缓冲超限")
		}
		buf = buf[:cap(buf)]
		n := C.llama_dyn_tokenize(model, cText, C.int32_t(len(text)), &buf[0], C.int32_t(len(buf)))
		if n >= 0 {
			return int32Slice(buf[:n]), nil
		}
		if n == -2147483648 { // INT32_MIN：溢出
			return nil, fmt.Errorf("inference: llm: tokenize 溢出")
		}
		buf = make([]C.int32_t, 0, -int(n))
	}
}

// llamaDecodeStep 显式 pos 解码一步，返回指向 llama 内部 logits 的只读切片
// （有效期至下一次 decode 前——生成循环内即取即用）。
func llamaDecodeStep(model, ctx unsafe.Pointer, tokens, pos []int32) ([]float32, error) {
	var logitsPtr *C.float
	var nLogits C.int32_t
	rc := C.llama_dyn_decode(model, ctx, (*C.int32_t)(&tokens[0]), (*C.int32_t)(&pos[0]),
		C.int32_t(len(tokens)), &logitsPtr, &nLogits)
	if rc != 0 {
		return nil, fmt.Errorf("inference: llm: decode rc=%d（>0=上下文满，<0=失败）", int32(rc))
	}
	if nLogits <= 0 {
		return nil, fmt.Errorf("inference: llm: decode 返回空 logits")
	}
	return unsafeSliceF32(unsafe.Pointer(logitsPtr), int(nLogits)), nil
}

// llamaTokenPiece 单 token 反解为文本片段（含特殊串）。
func llamaTokenPiece(model unsafe.Pointer, token int32) (string, error) {
	buf := make([]C.char, 256)
	n := C.llama_dyn_token_to_piece(model, C.int32_t(token), &buf[0], C.int32_t(len(buf)))
	if n < 0 {
		if -int(n) > len(buf) {
			buf = make([]C.char, -int(n))
			n = C.llama_dyn_token_to_piece(model, C.int32_t(token), &buf[0], C.int32_t(len(buf)))
		}
		if n < 0 {
			return "", fmt.Errorf("inference: llm: token %d 反解失败", token)
		}
	}
	return C.GoStringN(&buf[0], n), nil
}

func llamaIsEOG(model unsafe.Pointer, token int32) bool {
	return bool(C.llama_dyn_is_eog(model, C.int32_t(token)))
}

func int32Slice(buf []C.int32_t) []int32 {
	out := make([]int32, len(buf))
	for i, v := range buf {
		out[i] = int32(v)
	}
	return out
}

func unsafeSliceF32(p unsafe.Pointer, n int) []float32 {
	return unsafe.Slice((*float32)(p), n)
}
