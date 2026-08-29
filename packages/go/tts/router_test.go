// 表驱动单测（spec §6 三件套之一）：路由三分支（缓存命中/穿透/超时降级）、
// 降级行为 ∈ 预定义集（CI-4 语义：有界返回、不重播半句、静默≤上限）、
// fail-closed（PreSpeak 拒 → 读出=0）、确定性、Cancel 幂等、首包预算记录
// （M1 只记不判）、配置校验。
package tts

import (
	"errors"
	"fmt"
	"io"
	"testing"
	"time"
)

// newTestRouter 组装测试 Router（默认快路径：云 3 chunk、端 2 chunk）。
// 参数为接口类型：显式传 nil 字面量才是 nil 引擎（防 typed-nil 检查穿透）。
func newTestRouter(t *testing.T, cloud, edge Synthesizer, cache PhraseCache, pre PreSpeakFunc, mut func(*RouterConfig)) *Router {
	t.Helper()
	if pre == nil {
		pre = allowAll
	}
	cfg := RouterConfig{
		PreSpeak:             pre,
		Cloud:                cloud,
		Edge:                 edge,
		Cache:                cache,
		FirstPacketTimeoutMs: 30,
		SilenceCapMs:         500, // 宽松默认（静默上限负例在各用例 mut 收紧）
	}
	if mut != nil {
		mut(&cfg)
	}
	r, err := NewRouter(cfg)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return r
}

// drain 收集流全部 chunk（返回 Data 序列）；err 非 EOF/nil 时记录于返回错误。
func drain(s AudioStream) ([][]byte, error) {
	var out [][]byte
	for {
		c, err := s.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return out, err
		}
		out = append(out, c.Data)
	}
}

// dataEq 字节序列等价（nil 与空串视为等长 0 字节）。
func dataEq(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if string(a[i]) != string(b[i]) {
			return false
		}
	}
	return true
}

func byteChunks(ss ...string) [][]byte {
	out := make([][]byte, len(ss))
	for i, s := range ss {
		out[i] = []byte(s)
	}
	return out
}

// TestRouterRouteBranches 路由三分支表驱动（spec §6「Router 命中·穿透·超时」）：
// 命中→零合成延迟出流；穿透→按档选通道；超时→降级行为（静默占位+Edge 补偿）。
func TestRouterRouteBranches(t *testing.T) {
	cases := []struct {
		name        string
		text        string // 请求文本（须与 cachePreset 命中项一致才走缓存分支）
		tier        int
		cachePreset []string
		cloudDelay  int // 云流首包延迟 ms（0=无延迟）
		edgeDelay   int // Edge 流首包延迟 ms
		mut         func(*RouterConfig)
		wantErr     error
		wantChannel string
		wantCloud   int
		wantEdge    int
		wantData    [][]byte // 期望读出的数据序列（nil 元素=静默占位）
	}{
		{
			name:        "缓存命中：零合成延迟直返（云/端零调用）",
			text:        "你好呀",
			tier:        0,
			cachePreset: []string{"你好呀"},
			wantChannel: "cache",
			wantData:    byteChunks("cached-1", "cached-2"),
		},
		{
			name:        "缓存穿透：L0 走云通道",
			text:        "长内容走云",
			tier:        0,
			wantChannel: "cloud",
			wantCloud:   1,
			wantData:    byteChunks("cloud-1", "cloud-2", "cloud-3"),
		},
		{
			name:        "缓存穿透：L1 走云通道",
			text:        "长内容走云",
			tier:        1,
			wantChannel: "cloud",
			wantCloud:   1,
			wantData:    byteChunks("cloud-1", "cloud-2", "cloud-3"),
		},
		{
			name:        "缓存穿透：L2 走端侧通道",
			text:        "长内容走端",
			tier:        2,
			wantChannel: "edge",
			wantEdge:    1,
			wantData:    byteChunks("edge-1", "edge-2"),
		},
		{
			name:        "缓存穿透：L3 未命中无通道",
			text:        "长内容",
			tier:        3,
			wantErr:     ErrNoChannel,
			wantChannel: "",
		},
		{
			name:        "云首包超时：静默占位→Edge 全新补偿（不重播半句）",
			text:        "长内容走云",
			tier:        0,
			cloudDelay:  80, // > FirstPacketTimeoutMs(30)
			edgeDelay:   0,
			wantChannel: "degraded",
			wantCloud:   1,
			wantEdge:    1,
			wantData:    [][]byte{nil, []byte("edge-1"), []byte("edge-2")}, // 首 chunk=静默占位（0 字节）
		},
		{
			name:       "云首包超时且 Edge=nil：ErrTimeout（上层转文字/动作）",
			text:       "长内容走云",
			tier:       0,
			cloudDelay: 80,
			mut: func(c *RouterConfig) {
				c.Edge = nil
			},
			wantErr: ErrTimeout,
		},
		{
			name:        "PreSpeak 拒绝：fail-closed 读出=0（穷举缓存/云通道前置）",
			text:        "拦截我",
			tier:        0,
			cachePreset: []string{"拦截我"},
			wantErr:     ErrIntercepted,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cloud := newStubSynth([]byte("cloud-1"), []byte("cloud-2"), []byte("cloud-3"))
			cloud.firstDelayMs = tc.cloudDelay
			edge := newStubSynth([]byte("edge-1"), []byte("edge-2"))
			edge.firstDelayMs = tc.edgeDelay
			cache := newStubCache()
			for _, p := range tc.cachePreset {
				cache.Put(p, "", &replayStream{chunks: []Chunk{
					{Data: []byte("cached-1"), Seq: 1},
					{Data: []byte("cached-2"), Seq: 2, Final: true},
				}})
			}
			var pre PreSpeakFunc = allowAll
			if tc.wantErr != nil && errors.Is(tc.wantErr, ErrIntercepted) {
				pre = func(string) error { return errors.New("blocked by T9") }
			}
			r := newTestRouter(t, cloud, edge, cache, pre, tc.mut)

			st, err := r.Synthesize(Request{Text: tc.text, Tier: tc.tier, TurnID: "turn-1"})
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("预期 %v，got %v", tc.wantErr, err)
				}
				if st != nil {
					t.Fatalf("拒绝路径不得返回流（读出须=0）")
				}
				return
			}
			if err != nil {
				t.Fatalf("Synthesize: %v", err)
			}
			got, derr := drain(st)
			if derr != nil {
				t.Fatalf("读流失败: %v", derr)
			}
			if !dataEq(got, tc.wantData) {
				t.Fatalf("数据序列不等：got %v（%q）want %v", got, got, tc.wantData)
			}
			calls, _ := cloud.stats()
			ecalls, _ := edge.stats()
			if calls != tc.wantCloud {
				t.Fatalf("云调用 %d 次，want %d", calls, tc.wantCloud)
			}
			if ecalls != tc.wantEdge {
				t.Fatalf("端调用 %d 次，want %d", ecalls, tc.wantEdge)
			}
			if tc.wantChannel != "" {
				ms := r.Metrics()
				if len(ms) == 0 || ms[len(ms)-1].Channel != tc.wantChannel {
					t.Fatalf("路由通道记录=%+v，want channel=%s", ms, tc.wantChannel)
				}
			}
			// 云首包超时：云流必须被终止（Cancel 计数>0）——不留悬挂输出面
			if tc.cloudDelay > 0 && tc.wantErr == nil {
				waitFor(t, time.Second, func() bool {
					_, cancels := cloud.stats()
					return cancels > 0
				}, "云首包超时后云流未被 Cancel")
			}
		})
	}
}

// TestRouterCloudSynthFailDegrades 云 Synthesize 即失败（首包失败，0 字节已出）：
// 同超时降级路径——静默占位 + Edge 全新补偿重合成。
func TestRouterCloudSynthFailDegrades(t *testing.T) {
	cloud := newStubSynth([]byte("cloud-1"))
	cloud.synthErr = errors.New("cloud down")
	edge := newStubSynth([]byte("edge-1"), []byte("edge-2"))
	r := newTestRouter(t, cloud, edge, newStubCache(), nil, nil)

	st, err := r.Synthesize(Request{Text: "讲个故事", Tier: 0, TurnID: "t1"})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	got, derr := drain(st)
	if derr != nil {
		t.Fatalf("读流: %v", derr)
	}
	want := [][]byte{nil, []byte("edge-1"), []byte("edge-2")}
	if !dataEq(got, want) {
		t.Fatalf("降级输出=%v want %v（首块须为静默占位）", got, want)
	}
	if req := edge.lastReq(); req.Text != "讲个故事" {
		t.Fatalf("Edge 补偿须全新完整重合成（got req.Text=%q）", req.Text)
	}
	if ms := r.Metrics(); len(ms) == 0 || ms[len(ms)-1].Channel != "degraded" {
		t.Fatalf("通道记录须为 degraded：%+v", ms)
	}
}

// TestRouterDegradeSilenceBounded 静默补偿有界：Edge 首包超 SilenceCapMs →
// ErrTimeout（用户静默 ≤SilenceCapMs，绝不无限等待）。
func TestRouterDegradeSilenceBounded(t *testing.T) {
	cloud := newStubSynth([]byte("cloud-1"))
	cloud.synthErr = errors.New("cloud down") // 立即降级（t≈0，静默计时干净）
	edge := newStubSynth([]byte("edge-1"))
	edge.firstDelayMs = 200 // > SilenceCapMs(50)
	r := newTestRouter(t, cloud, edge, newStubCache(), nil, func(c *RouterConfig) {
		c.SilenceCapMs = 50
	})

	st, err := r.Synthesize(Request{Text: "你好", Tier: 0, TurnID: "t1"})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	start := time.Now()
	c, err := st.Next() // 静默占位：立即返回
	if err != nil {
		t.Fatalf("占位 chunk: %v", err)
	}
	if len(c.Data) != 0 {
		t.Fatalf("静默占位须 0 字节，got %d", len(c.Data))
	}
	_, err = st.Next() // 等 Edge 首包：超 SilenceCapMs → ErrTimeout
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("静默超上限须 ErrTimeout，got %v", err)
	}
	if d := time.Since(start); d > 400*time.Millisecond {
		t.Fatalf("超时判定耗时 %v（静默上限 50ms）：路径须有界", d)
	}
	_, err = st.Next() // 终止后固化：不复活
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("终止后 Next 须持续 ErrTimeout，got %v", err)
	}
}

// TestRouterNoReplayHalfSentence 云流中途 err：已播 Seq 不回退、不重播半句
// （终止固化——err 后流不可再出任何 chunk）；partial 语义记入 metrics。
func TestRouterNoReplayHalfSentence(t *testing.T) {
	cloud := newStubSynth([]byte("half-1"), []byte("half-2"), []byte("half-3"), []byte("half-4"))
	cloud.midErrAfter = 2 // 交付 2 chunk 后流中途故障
	r := newTestRouter(t, cloud, newStubSynth(), newStubCache(), nil, nil)

	st, err := r.Synthesize(Request{Text: "今天讲个小熊的故事", Tier: 0, TurnID: "t1"})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	var got [][]byte
	for i := 0; i < 2; i++ { // 已播半句（2 chunks）
		c, err := st.Next()
		if err != nil {
			t.Fatalf("前 %d chunk 须正常：got %v", i+1, err)
		}
		got = append(got, c.Data)
	}
	if _, err := st.Next(); err == nil {
		t.Fatal("流中途故障须返回 err")
	}
	// 不重播半句：终止后反复 Next 不得再吐任何已播/未播 chunk
	for i := 0; i < 5; i++ {
		c, err := st.Next()
		if err == nil {
			t.Fatalf("终止流第 %d 次 Next 返回 chunk %q：重播半句违规", i, c.Data)
		}
		if c.Data != nil {
			t.Fatalf("终止流不得携带数据")
		}
	}
	// 下轮回云档：每请求独立尝试云（降级粘性禁止）
	st2, err := r.Synthesize(Request{Text: "再来一个", Tier: 0, TurnID: "t2"})
	if err != nil {
		t.Fatalf("下轮 Synthesize: %v", err)
	}
	if _, err := st2.Next(); err != nil {
		t.Fatalf("下轮须可出声（故障无跨请求粘性）：%v", err)
	}
	calls, _ := cloud.stats()
	if calls != 2 {
		t.Fatalf("下轮须重试云（每请求独立决策）：云调用 %d want 2", calls)
	}
	ms := r.Metrics()
	if len(ms) != 2 {
		t.Fatalf("metrics 记录 %d 条 want 2", len(ms))
	}
	if !ms[0].Partial || ms[0].LastSeq != 2 {
		t.Fatalf("partial 语义缺失：got %+v（须 partial=true lastSeq=2）", ms[0])
	}
	if ms[1].Partial {
		t.Fatalf("下轮正常流不应记 partial：%+v", ms[1])
	}
}

// TestRouterDegradeBehaviorsBounded 降级行为 ∈ 预定义集（CI-4 对齐）：任何故障
// 路径有界返回——Synthesize 面结局只能是 {正常流, 哨兵 err}；流面（Next）对
// 引擎中途 err 按原样传播（降级表「终止流」行的设计行为，不重播半句——
// 专门断言见 TestRouterNoReplayHalfSentence），此处仅断言有界。
func TestRouterDegradeBehaviorsBounded(t *testing.T) {
	type scene struct {
		name      string
		streamErr bool // true=流面错误（引擎原始 err 传播，非降级决策面）
		run       func(t *testing.T) error
	}
	scenes := []scene{
		{"云首包超时→Edge 补偿", false, func(t *testing.T) error {
			cloud := newStubSynth([]byte("c1"))
			cloud.firstDelayMs = 80
			r := newTestRouter(t, cloud, newStubSynth([]byte("e1")), newStubCache(), nil, nil)
			st, err := r.Synthesize(Request{Text: "hi", Tier: 0})
			if err != nil {
				return err
			}
			_, err = drain(st)
			return err
		}},
		{"云首包超时+Edge nil→ErrTimeout", false, func(t *testing.T) error {
			cloud := newStubSynth([]byte("c1"))
			cloud.firstDelayMs = 80
			r := newTestRouter(t, cloud, nil, newStubCache(), nil, func(c *RouterConfig) { c.Edge = nil })
			_, err := r.Synthesize(Request{Text: "hi", Tier: 0})
			return err
		}},
		{"云 Synthesize err→Edge 补偿", false, func(t *testing.T) error {
			cloud := newStubSynth([]byte("c1"))
			cloud.synthErr = errors.New("cloud 500")
			r := newTestRouter(t, cloud, newStubSynth([]byte("e1")), newStubCache(), nil, nil)
			st, err := r.Synthesize(Request{Text: "hi", Tier: 0})
			if err != nil {
				return err
			}
			_, err = drain(st)
			return err
		}},
		{"云流中途 err→终止不重播（原始 err 传播）", true, func(t *testing.T) error {
			cloud := newStubSynth([]byte("a"), []byte("b"), []byte("c"))
			cloud.midErrAfter = 1
			r := newTestRouter(t, cloud, newStubSynth([]byte("e1")), newStubCache(), nil, nil)
			st, err := r.Synthesize(Request{Text: "hi", Tier: 0})
			if err != nil {
				return err
			}
			_, err = drain(st)
			return err
		}},
		{"L3 无缓存→ErrNoChannel", false, func(t *testing.T) error {
			r := newTestRouter(t, newStubSynth([]byte("c1")), newStubSynth([]byte("e1")), newStubCache(), nil, nil)
			_, err := r.Synthesize(Request{Text: "hi", Tier: 3})
			return err
		}},
		{"Edge 首包超静默上限→ErrTimeout", false, func(t *testing.T) error {
			cloud := newStubSynth([]byte("c1"))
			cloud.synthErr = errors.New("cloud down") // 立即降级
			edge := newStubSynth([]byte("e1"))
			edge.firstDelayMs = 300 // > SilenceCapMs(100)
			r := newTestRouter(t, cloud, edge, newStubCache(), nil, func(c *RouterConfig) { c.SilenceCapMs = 100 })
			st, err := r.Synthesize(Request{Text: "hi", Tier: 0})
			if err != nil {
				return err
			}
			if _, err := st.Next(); err != nil {
				return err // 占位块正常
			}
			_, err = st.Next()
			return err
		}},
		{"打断 Cancel→流立即终止", false, func(t *testing.T) error {
			r := newTestRouter(t, newStubSynth([]byte("c1"), []byte("c2")), newStubSynth(), newStubCache(), nil, nil)
			st, err := r.Synthesize(Request{Text: "hi", Tier: 0, TurnID: "tk"})
			if err != nil {
				return err
			}
			if err := r.Cancel("tk"); err != nil {
				return err
			}
			_, err = st.Next()
			return err
		}},
	}
	for _, sc := range scenes {
		t.Run(sc.name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() { done <- sc.run(t) }()
			select {
			case err := <-done:
				if err == nil {
					return // 正常流
				}
				if sc.streamErr {
					return // 流面原始 err 传播（终止固化语义另有专测）；有界即过
				}
				if !errors.Is(err, ErrTimeout) && !errors.Is(err, ErrNoChannel) &&
					!errors.Is(err, ErrCanceled) && !errors.Is(err, io.EOF) && !errors.Is(err, ErrIntercepted) {
					t.Fatalf("降级结局 %v 不在预定义集（{正常流, ErrTimeout, ErrNoChannel, ErrCanceled, io.EOF}）", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("路径无界返回（CI-4 绝不 hang 违规）：2s 内未结局")
			}
		})
	}
}

// TestRouterCancelIdempotent Cancel 幂等：多次调用全 nil、流立即终止、
// 已播 chunk 不回退（终止后无新数据）、未知话轮无操作。
func TestRouterCancelIdempotent(t *testing.T) {
	cloud := newStubSynth([]byte("c1"), []byte("c2"), []byte("c3"))
	r := newTestRouter(t, cloud, nil, newStubCache(), nil, nil)
	if err := r.Cancel("nobody"); err != nil { // 未知话轮：幂等无操作
		t.Fatalf("未知 TurnID Cancel 须 nil，got %v", err)
	}
	st, err := r.Synthesize(Request{Text: "晚安", Tier: 0, TurnID: "bedtime"})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	c, err := st.Next() // 播 1 chunk
	if err != nil {
		t.Fatalf("首 chunk: %v", err)
	}
	if string(c.Data) != "c1" {
		t.Fatalf("首 chunk 数据=%q", c.Data)
	}
	for i := 0; i < 3; i++ { // 幂等：三次 Cancel 全 nil
		if err := r.Cancel("bedtime"); err != nil {
			t.Fatalf("第 %d 次 Cancel: %v", i+1, err)
		}
	}
	if _, err := st.Next(); !errors.Is(err, ErrCanceled) {
		t.Fatalf("Cancel 后 Next 须 ErrCanceled，got %v", err)
	}
	if _, err := st.Next(); !errors.Is(err, ErrCanceled) { // 终止固化：不续播
		t.Fatalf("终止后 Next 须持续 ErrCanceled，got %v", err)
	}
}

// TestRouterPreSpeakFailClosed fail-closed 穷举：PreSpeak 拒绝时无论通道配置
// （云/端/缓存全开）与档位，读出恒=0（ErrIntercepted 且零流）。
func TestRouterPreSpeakFailClosed(t *testing.T) {
	cloud := newStubSynth([]byte("c1"))
	edge := newStubSynth()
	cache := newStubCache()
	cache.Put("坏句子", "", &replayStream{chunks: []Chunk{{Data: []byte("poison"), Seq: 1, Final: true}}})
	pre := func(string) error { return errors.New("T9 rejected") }
	r := newTestRouter(t, cloud, edge, cache, pre, nil)

	for tier := 0; tier < 4; tier++ {
		st, err := r.Synthesize(Request{Text: "坏句子", Tier: tier, TurnID: fmt.Sprintf("fc-%d", tier)})
		if !errors.Is(err, ErrIntercepted) {
			t.Fatalf("tier %d：预期 ErrIntercepted，got err=%v st=%v", tier, err, st)
		}
		if st != nil {
			t.Fatalf("tier %d：拒绝路径返回流（读出风险）", tier)
		}
	}
	calls, _ := cloud.stats()
	ecalls, _ := edge.stats()
	gets, _, hits := cache.stats()
	if calls+ecalls+hits != 0 || gets != 0 {
		t.Fatalf("fail-closed 破损：cloud=%d edge=%d cache.gets=%d hits=%d", calls, ecalls, gets, hits)
	}
}

// TestRouterDeterministicRouting 确定性：同配置 Router 对同请求序列的路由决策
// （通道选择+音频输出+metrics 预算字段）逐次一致。
func TestRouterDeterministicRouting(t *testing.T) {
	run := func() (channels []string, out []string) {
		cloud := newStubSynth([]byte("c-1"), []byte("c-2"))
		edge := newStubSynth()
		cache := newStubCache()
		cache.Put("口癖", "", &replayStream{chunks: []Chunk{{Data: []byte("k"), Seq: 1, Final: true}}})
		r := newTestRouter(t, cloud, edge, cache, nil, nil)
		for _, req := range []Request{
			{Text: "口癖", Tier: 0, TurnID: "a"},
			{Text: "长内容走云", Tier: 0, TurnID: "b"},
			{Text: "长内容走端", Tier: 2, TurnID: "c"},
			{Text: "口癖", Tier: 3, TurnID: "d"},
		} {
			st, err := r.Synthesize(req)
			if err != nil {
				t.Fatalf("Synthesize %q: %v", req.Text, err)
			}
			data, _ := drain(st)
			for _, d := range data {
				out = append(out, string(d))
			}
			channels = append(channels, r.Metrics()[len(r.Metrics())-1].Channel)
		}
		return channels, out
	}
	ch1, out1 := run()
	ch2, out2 := run()
	if len(ch1) != 4 || len(out1) == 0 {
		t.Fatalf("决策序列长度异常：%d/%d", len(ch1), len(out1))
	}
	for i := range ch1 {
		if ch1[i] != ch2[i] {
			t.Fatalf("第 %d 次路由决策漂移：%s vs %s（确定性违规）", i, ch1[i], ch2[i])
		}
	}
	for i := range out1 {
		if out1[i] != out2[i] {
			t.Fatalf("第 %d 块音频输出漂移（确定性违规）", i)
		}
	}
}

// TestRouterFirstPacketBudgetRecorded 首包预算契约（M1 只记不判）：Request 带
// DeadlineMs（budgetMs），路由记录 FirstPacketMs 元数据供 budgets 消费
// （configs/budgets tts_first P95 判定归 M2 真机）。
func TestRouterFirstPacketBudgetRecorded(t *testing.T) {
	cloud := newStubSynth([]byte("c-1"))
	cache := newStubCache()
	cache.Put("口癖", "", &replayStream{chunks: []Chunk{{Data: []byte("k"), Seq: 1, Final: true}}})
	r := newTestRouter(t, cloud, newStubSynth(), cache, nil, nil)

	// 缓存命中：零合成延迟——FirstPacketMs 记录且远小于请求预算
	st, err := r.Synthesize(Request{Text: "口癖", Tier: 0, TurnID: "k1", DeadlineMs: 150})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if _, err := drain(st); err != nil {
		t.Fatalf("drain: %v", err)
	}
	m := r.Metrics()
	if len(m) != 1 {
		t.Fatalf("metrics %d 条 want 1", len(m))
	}
	if m[0].Channel != "cache" || m[0].DeadlineMs != 150 || m[0].FirstPacketMs < 0 {
		t.Fatalf("缓存命中 metric 异常：%+v", m[0])
	}
	// 云正常：首包耗时被记录（>0 或 =0 快机器可接受），不因超预算判错
	st, err = r.Synthesize(Request{Text: "云内容", Tier: 0, TurnID: "k2", DeadlineMs: 150})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if _, err := drain(st); err != nil {
		t.Fatalf("drain: %v", err)
	}
	m = r.Metrics()
	if m[1].Channel != "cloud" || m[1].DeadlineMs != 150 {
		t.Fatalf("云路径 metric 异常：%+v", m[1])
	}
	// 超预算不判错（M1 只记不判）：云首包延迟 50ms > 预算 10ms 仍正常出流
	//（首包超时线 FirstPacketTimeoutMs 放宽至 200ms，隔离「预算记录」与「超时降级」两面）
	slowCloud := newStubSynth([]byte("s-1"))
	slowCloud.firstDelayMs = 50
	r2 := newTestRouter(t, slowCloud, newStubSynth(), newStubCache(), nil, func(c *RouterConfig) {
		c.FirstPacketTimeoutMs = 200
	})
	st, err = r2.Synthesize(Request{Text: "慢云", Tier: 0, TurnID: "k3", DeadlineMs: 10})
	if err != nil {
		t.Fatalf("超预算请求不应判错（M1 只记不判）：%v", err)
	}
	if _, err := drain(st); err != nil {
		t.Fatalf("drain: %v", err)
	}
}

// TestRouterValidation 配置与请求校验：PreSpeak nil→NewRouter error（生产禁
// 裸奔）；Tier 越界/空文本→error 哨兵。
func TestRouterValidation(t *testing.T) {
	if _, err := NewRouter(RouterConfig{}); err == nil {
		t.Fatal("PreSpeak=nil 须 NewRouter error（fail-closed：T9 钩子必接）")
	}
	r := newTestRouter(t, newStubSynth(), nil, nil, nil, nil)
	if _, err := r.Synthesize(Request{Text: "x", Tier: -1}); err == nil {
		t.Fatal("Tier=-1 须 error")
	}
	if _, err := r.Synthesize(Request{Text: "x", Tier: 4}); err == nil {
		t.Fatal("Tier=4 须 error")
	}
	if _, err := r.Synthesize(Request{Text: "", Tier: 0}); !errors.Is(err, ErrEmptyText) {
		t.Fatalf("空文本须 ErrEmptyText，got %v", err)
	}
}

// TestRouterDefaultCaps 默认档位镜像（configs/runtime/tiers.yaml）：
// L0/L1 云、L2 端、L3 仅缓存。
func TestRouterDefaultCaps(t *testing.T) {
	r := newTestRouter(t, newStubSynth(), newStubSynth(), newStubCache(), nil, nil)
	// L0/L1→cloud：Edge 存在但云优先
	for _, tier := range []int{0, 1} {
		if _, err := r.Synthesize(Request{Text: "x", Tier: tier}); err != nil {
			t.Fatalf("tier %d: %v", tier, err)
		}
	}
	// L2→edge：见下方 r2 验证（云缺引擎时 L0/L1 报 ErrNoChannel，L2 正常）
	// L3→仅缓存：未命中 ErrNoChannel
	if _, err := r.Synthesize(Request{Text: "x", Tier: 3}); !errors.Is(err, ErrNoChannel) {
		t.Fatalf("L3 未命中须 ErrNoChannel，got %v", err)
	}
	// L2 走端：配一个新 Router（云 nil）验证端通道
	r2 := newTestRouter(t, nil, newStubSynth([]byte("e1")), newStubCache(), nil, nil)
	st, err := r2.Synthesize(Request{Text: "x", Tier: 2})
	if err != nil {
		t.Fatalf("L2 须走端侧：%v", err)
	}
	if _, err := drain(st); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if _, err := r2.Synthesize(Request{Text: "x", Tier: 0}); !errors.Is(err, ErrNoChannel) {
		t.Fatalf("云缺引擎且档位要云须 ErrNoChannel，got %v", err)
	}
}

// waitFor 轮询条件满足（带超时，async 桩计数上报用）。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal(msg)
}
