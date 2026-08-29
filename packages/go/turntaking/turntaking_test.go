// 表驱动单测（m1-spec §6 三件套之一）：转移表 7 行穷举 + 未列组合自转移矩阵 +
// NewFSM 配置校验 + 错误语义（AtMs 非单调丢弃、任意调用序不 panic）+ 尾静音
// 边界（±1ms）。转移表口径唯一来源：docs/m1-spec.md §3「转移表」。
package turntaking

import "testing"

// baseCfg 单测基线配置（门限取值仅测试面使用——阈值执法唯一来源是
// configs/gates/T3.yaml，本文件不复制阈值语义）。
func baseCfg() Config {
	return Config{SilenceMs: 800, MaxTurnMs: 10000, BargeInWindow: 300}
}

// fsmCall 一次 FSM 输入（闭包形态，表驱动可读）。
type fsmCall func(f *FSM) []Action

func wake(at int64) fsmCall { return func(f *FSM) []Action { return f.OnWake(at) } }

func speakStart(at int64) fsmCall { return func(f *FSM) []Action { return f.OnSpeakStart(at) } }

func speakDone(at int64) fsmCall { return func(f *FSM) []Action { return f.OnSpeakDone(at) } }

func vad(k VADEventKind, at int64) fsmCall {
	return func(f *FSM) []Action { return f.OnVAD(VADEvent{Kind: k, AtMs: at}) }
}

// containsKind 判定动作序列是否含 k。
func containsKind(act []Action, k ActionKind) bool {
	for _, a := range act {
		if a.Kind == k {
			return true
		}
	}
	return false
}

// actsEqual 逐项比较两动作序列（Kind+AtMs；Action 为可比较类型）。
func actsEqual(a, b []Action) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNewFSMConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		ok   bool
	}{
		{"合法配置", Config{SilenceMs: 800, MaxTurnMs: 10000, BargeInWindow: 300}, true},
		{"BargeInWindow=1 下边界", Config{SilenceMs: 800, MaxTurnMs: 10000, BargeInWindow: 1}, true},
		{"SilenceMs=0", Config{SilenceMs: 0, MaxTurnMs: 10000, BargeInWindow: 300}, false},
		{"SilenceMs<0", Config{SilenceMs: -1, MaxTurnMs: 10000, BargeInWindow: 300}, false},
		{"MaxTurnMs=0", Config{SilenceMs: 800, MaxTurnMs: 0, BargeInWindow: 300}, false},
		{"BargeInWindow=0", Config{SilenceMs: 800, MaxTurnMs: 10000, BargeInWindow: 0}, false},
		{"BargeInWindow=301 越打断契约上限", Config{SilenceMs: 800, MaxTurnMs: 10000, BargeInWindow: 301}, false},
	}
	for _, tc := range cases {
		fsm, err := NewFSM(tc.cfg)
		if tc.ok {
			if err != nil {
				t.Errorf("%s：合法配置被拒：%v", tc.name, err)
				continue
			}
			if fsm.State() != StIdle {
				t.Errorf("%s：初始态须 StIdle（零值），got %d", tc.name, fsm.State())
			}
			if fsm.BargeInLatencyMs() != noBargeIn {
				t.Errorf("%s：未发生打断时 BargeInLatencyMs 须 %d，got %d", tc.name, noBargeIn, fsm.BargeInLatencyMs())
			}
			continue
		}
		if err == nil {
			t.Errorf("%s：非法配置未报错", tc.name)
		}
	}
}

// TestTransferTable 转移表 7 行全覆盖（spec §3；行内双触发逐行列出）。
func TestTransferTable(t *testing.T) {
	cfg := baseCfg()
	cases := []struct {
		name        string
		pre         []fsmCall
		call        fsmCall
		want        State
		wantActions []Action
	}{
		{
			name:        "第1行 Idle+OnWake→Listening+OpenMic",
			call:        wake(1000),
			want:        StListening,
			wantActions: []Action{{Kind: ActOpenMic, AtMs: 1000}},
		},
		{
			name:        "第1行 Idle+EvVoiceStart→Listening+OpenMic",
			call:        vad(EvVoiceStart, 1000),
			want:        StListening,
			wantActions: []Action{{Kind: ActOpenMic, AtMs: 1000}},
		},
		{
			name: "第2行 Listening+EvVoiceStart→Listening（清尾静音计时）",
			pre:  []fsmCall{wake(0), vad(EvVoiceStart, 100), vad(EvVoiceEnd, 200)},
			call: vad(EvVoiceStart, 300),
			want: StListening,
		},
		{
			name:        "第3行 Listening 尾静音≥SilenceMs→Idle+TurnEnd+CloseMic",
			pre:         []fsmCall{wake(0), vad(EvVoiceStart, 100), vad(EvVoiceEnd, 200)},
			call:        vad(EvNone, 1000), // 静音 800 ≥ SilenceMs(800)
			want:        StIdle,
			wantActions: []Action{{Kind: ActTurnEnd, AtMs: 1000}, {Kind: ActCloseMic, AtMs: 1000}},
		},
		{
			name:        "第4行 Listening 累计≥MaxTurnMs→Idle+TurnEnd+CloseMic（防挂起）",
			pre:         []fsmCall{wake(0), vad(EvVoiceStart, 100), vad(EvVoiceEnd, 200), vad(EvVoiceStart, 300), vad(EvVoiceEnd, 400)},
			call:        vad(EvNone, 10000), // 累计 10000 ≥ MaxTurnMs(10000)
			want:        StIdle,
			wantActions: []Action{{Kind: ActTurnEnd, AtMs: 10000}, {Kind: ActCloseMic, AtMs: 10000}},
		},
		{
			name:        "第4行变体 VAD 永不静音（持续翻转）→累计达 MaxTurnMs 照样截断",
			pre:         []fsmCall{wake(0), vad(EvVoiceStart, 100), vad(EvVoiceEnd, 200), vad(EvVoiceStart, 300), vad(EvVoiceEnd, 400)},
			call:        vad(EvVoiceStart, 10001),
			want:        StIdle,
			wantActions: []Action{{Kind: ActTurnEnd, AtMs: 10001}, {Kind: ActCloseMic, AtMs: 10001}},
		},
		{
			name: "第5行 Listening+OnSpeakStart→Speaking",
			pre:  []fsmCall{wake(0)},
			call: speakStart(100),
			want: StSpeaking,
		},
		{
			name:        "第6行 Speaking+EvVoiceStart→Listening+StopTTS+OpenMic（打断）",
			pre:         []fsmCall{wake(0), speakStart(100)},
			call:        vad(EvVoiceStart, 500),
			want:        StListening,
			wantActions: []Action{{Kind: ActStopTTS, AtMs: 500}, {Kind: ActOpenMic, AtMs: 500}},
		},
		{
			name: "第7行 Speaking+EvVoiceEnd→Speaking（无动作）",
			pre:  []fsmCall{wake(0), speakStart(100)},
			call: vad(EvVoiceEnd, 500),
			want: StSpeaking,
		},
		{
			name: "第7行 Speaking+OnSpeakDone→Idle（播完）",
			pre:  []fsmCall{wake(0), speakStart(100)},
			call: speakDone(900),
			want: StIdle,
		},
		{
			// 路径选择（实现卡 PR 记录）：转移表第 5 行只列 Listening+OnSpeakStart；
			// spec §1 架构闭环要求 ActTurnEnd（→Idle）后驱动注入 SpeakRequest 开播入
			// Speaking——不放行则集成流中 Speaking/打断链不可达（BI-3.2 破坏）。
			name: "Idle+OnSpeakStart→Speaking（spec §1 闭环：TurnEnd 后驱动开播）",
			pre:  []fsmCall{wake(0), vad(EvVoiceStart, 100), vad(EvVoiceEnd, 200), vad(EvNone, 1000)},
			call: speakStart(1100),
			want: StSpeaking,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fsm, err := NewFSM(cfg)
			if err != nil {
				t.Fatalf("NewFSM: %v", err)
			}
			for _, p := range tc.pre {
				p(fsm)
			}
			got := tc.call(fsm)
			if fsm.State() != tc.want {
				t.Errorf("后继态：want %d got %d（actions=%v）", tc.want, fsm.State(), got)
			}
			if !actsEqual(got, tc.wantActions) {
				t.Errorf("动作：want %v got %v", tc.wantActions, got)
			}
		})
	}
}

// TestUnlistedCombosSelfTransition 未列组合＝自转移（穷举无死角）。列内组合
// （第 1/2/5/6/7 行与闭环放行）已在 TestTransferTable 覆盖，本表穷举其余全部
// 状态×输入格：状态不变、零动作。
func TestUnlistedCombosSelfTransition(t *testing.T) {
	cfg := baseCfg()
	cases := []struct {
		name string
		pre  []fsmCall
		call fsmCall
		want State
	}{
		// Idle（OnWake/EvVoiceStart/OnSpeakStart 为列内，见 TestTransferTable）。
		{name: "Idle+EvVoiceEnd", call: vad(EvVoiceEnd, 1000), want: StIdle},
		{name: "Idle+EvNone", call: vad(EvNone, 1000), want: StIdle},
		{name: "Idle+OnSpeakDone", call: speakDone(1000), want: StIdle},
		// Listening（wake(0) 后 50ms 的良性时刻：静音/累计均未达门限）。
		{name: "Listening+OnWake", pre: []fsmCall{wake(0)}, call: wake(50), want: StListening},
		{name: "Listening+EvVoiceEnd（第3行前缀：静音未累计）", pre: []fsmCall{wake(0)}, call: vad(EvVoiceEnd, 50), want: StListening},
		{name: "Listening+EvNone（静音 50<800）", pre: []fsmCall{wake(0)}, call: vad(EvNone, 50), want: StListening},
		{name: "Listening+OnSpeakDone", pre: []fsmCall{wake(0)}, call: speakDone(50), want: StListening},
		// Speaking（wake(0)+speakStart(10) 后 20ms）。
		{name: "Speaking+OnWake", pre: []fsmCall{wake(0), speakStart(10)}, call: wake(20), want: StSpeaking},
		{name: "Speaking+EvNone", pre: []fsmCall{wake(0), speakStart(10)}, call: vad(EvNone, 20), want: StSpeaking},
		{name: "Speaking+OnSpeakStart", pre: []fsmCall{wake(0), speakStart(10)}, call: speakStart(20), want: StSpeaking},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fsm, err := NewFSM(cfg)
			if err != nil {
				t.Fatalf("NewFSM: %v", err)
			}
			for _, p := range tc.pre {
				p(fsm)
			}
			got := tc.call(fsm)
			if fsm.State() != tc.want {
				t.Errorf("自转移破坏：want %d got %d", tc.want, fsm.State())
			}
			if len(got) != 0 {
				t.Errorf("自转移须零动作，got %v", got)
			}
		})
	}
}

// TestSilenceBoundary 尾静音门限边界（±1ms 表用例；转移表口径「静音≥SilenceMs」）
// ＋第 2 行「清尾静音计时」可观测语义（停顿内复讲后，静音自复讲后新段终点重算）。
func TestSilenceBoundary(t *testing.T) {
	cfg := baseCfg()
	cases := []struct {
		name    string
		silence int64
		wantEnd bool
	}{
		{"静音 SilenceMs-1 → 不终点", 799, false},
		{"静音 SilenceMs → 终点（口径：静音≥SilenceMs）", 800, true},
		{"静音 SilenceMs+1 → 终点", 801, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fsm, err := NewFSM(cfg)
			if err != nil {
				t.Fatalf("NewFSM: %v", err)
			}
			wake(0)(fsm)
			vad(EvVoiceStart, 100)(fsm)
			vad(EvVoiceEnd, 200)(fsm)
			got := vad(EvNone, 200+tc.silence)(fsm)
			if containsKind(got, ActTurnEnd) != tc.wantEnd {
				t.Errorf("静音 %dms：wantEnd=%v got actions=%v", tc.silence, tc.wantEnd, got)
			}
			wantState := StListening
			if tc.wantEnd {
				wantState = StIdle
			}
			if fsm.State() != wantState {
				t.Errorf("静音 %dms：态须 %d got %d", tc.silence, wantState, fsm.State())
			}
		})
	}
	t.Run("复讲重置尾静音基准（清尾静音计时）", func(t *testing.T) {
		fsm, err := NewFSM(cfg)
		if err != nil {
			t.Fatalf("NewFSM: %v", err)
		}
		wake(0)(fsm)
		vad(EvVoiceStart, 100)(fsm)
		vad(EvVoiceEnd, 200)(fsm)
		vad(EvVoiceStart, 600)(fsm) // 400ms 停顿内复讲（<SilenceMs，未终点）
		if fsm.State() != StListening {
			t.Fatalf("复讲须保持 Listening，got %d", fsm.State())
		}
		vad(EvVoiceEnd, 700)(fsm)
		// 静音自复讲后 VoiceEnd(700) 重算：1499 处 799ms 不得终点（若未清计时，
		// 自 200 起算 1299ms 早已终点——本用例即区分）。
		if got := vad(EvNone, 1499)(fsm); containsKind(got, ActTurnEnd) || fsm.State() != StListening {
			t.Errorf("静音 799ms（自 700 起算）不得终点：actions=%v state=%d", got, fsm.State())
		}
		got := vad(EvNone, 1500)(fsm)
		if !containsKind(got, ActTurnEnd) || fsm.State() != StIdle {
			t.Errorf("静音 800ms（自 700 起算）须终点：actions=%v state=%d", got, fsm.State())
		}
	})
}

// TestAtMsNonMonotonicDropped 错误语义：AtMs 早于已见最大值 → 整事件丢弃
// （迟到帧不回放，不拨回内部基准）；等值不算非单调（须处理）。
func TestAtMsNonMonotonicDropped(t *testing.T) {
	cfg := baseCfg()
	t.Run("迟到 EvVoiceEnd 不拨回尾静音基准", func(t *testing.T) {
		fsm, err := NewFSM(cfg)
		if err != nil {
			t.Fatalf("NewFSM: %v", err)
		}
		wake(1000)(fsm)
		vad(EvVoiceStart, 1500)(fsm)
		vad(EvVoiceEnd, 2000)(fsm)
		if got := vad(EvVoiceEnd, 1200)(fsm); len(got) != 0 {
			t.Errorf("迟到帧须零动作，got %v", got)
		}
		// 若迟到 VoiceEnd(1200) 被回放，静音基准拨回 1200 → EvNone(2000) 即静音
		// 800 终点；正确语义：基准仍 2000 → 静音 0，不得终点。
		if got := vad(EvNone, 2000)(fsm); containsKind(got, ActTurnEnd) {
			t.Errorf("迟到帧回放泄漏：静音基准被拨回（actions=%v）", got)
		}
		if fsm.State() != StListening {
			t.Errorf("state 须 Listening，got %d", fsm.State())
		}
	})
	t.Run("迟到 EvVoiceStart 不触发打断", func(t *testing.T) {
		fsm, err := NewFSM(cfg)
		if err != nil {
			t.Fatalf("NewFSM: %v", err)
		}
		wake(1000)(fsm)
		speakStart(1100)(fsm)
		if got := vad(EvVoiceStart, 1050)(fsm); len(got) != 0 {
			t.Errorf("迟到帧须零动作，got %v", got)
		}
		if fsm.State() != StSpeaking {
			t.Errorf("迟到打断帧不得改变状态，got %d", fsm.State())
		}
		if fsm.BargeInLatencyMs() != noBargeIn {
			t.Errorf("迟到帧不得记打断元数据，got %d", fsm.BargeInLatencyMs())
		}
	})
	t.Run("迟到 OnWake/OnSpeakDone 不改变状态", func(t *testing.T) {
		fsm, err := NewFSM(cfg)
		if err != nil {
			t.Fatalf("NewFSM: %v", err)
		}
		wake(1000)(fsm)
		if got := wake(500)(fsm); len(got) != 0 || fsm.State() != StListening {
			t.Errorf("迟到 OnWake 不得重开麦：actions=%v state=%d", got, fsm.State())
		}
		speakStart(1100)(fsm)
		if got := speakDone(900)(fsm); len(got) != 0 || fsm.State() != StSpeaking {
			t.Errorf("迟到 OnSpeakDone 不得关播：actions=%v state=%d", got, fsm.State())
		}
	})
	t.Run("等值 AtMs 不算非单调（须处理）", func(t *testing.T) {
		fsm, err := NewFSM(cfg)
		if err != nil {
			t.Fatalf("NewFSM: %v", err)
		}
		wake(500)(fsm)
		vad(EvVoiceStart, 600)(fsm)
		vad(EvVoiceEnd, 600)(fsm) // 与 VoiceStart 同刻：≥已见最大值，须被处理
		// 若被丢弃，voiceActive 仍为 true → 静音判定永不触发；正确语义：
		// VoiceEnd(600) 已处理，静音自 600 起算，1400 处 800ms 终点。
		got := vad(EvNone, 1400)(fsm)
		if !containsKind(got, ActTurnEnd) || fsm.State() != StIdle {
			t.Errorf("等值 AtMs 的 EvVoiceEnd 须被处理（静音 800 须终点）：actions=%v state=%d", got, fsm.State())
		}
	})
}

// TestBargeInLatencyMetadata 打断响应契约元数据：VoiceStart 输入→StopTTS 输出
// 为同步单步（Action.AtMs=事件时刻、逻辑延迟 0 ≤ BargeInWindow ≤ 300ms；
// 链路实测延迟 M2 硬件计时）。
func TestBargeInLatencyMetadata(t *testing.T) {
	cfg := baseCfg()
	fsm, err := NewFSM(cfg)
	if err != nil {
		t.Fatalf("NewFSM: %v", err)
	}
	if got := fsm.BargeInLatencyMs(); got != noBargeIn {
		t.Errorf("未发生打断时元数据须 %d，got %d", noBargeIn, got)
	}
	wake(0)(fsm)
	speakStart(100)(fsm)
	got := vad(EvVoiceStart, 500)(fsm)
	if len(got) < 2 || got[0].Kind != ActStopTTS {
		t.Fatalf("打断须首出 ActStopTTS，got %v", got)
	}
	if got[0].AtMs != 500 {
		t.Errorf("StopTTS 动作时刻须=VoiceStart 输入时刻（同步单步），got %d", got[0].AtMs)
	}
	if l := fsm.BargeInLatencyMs(); l != 0 {
		t.Errorf("BargeInLatencyMs 须 0（同步单步，≤BargeInWindow=%d），got %d", cfg.BargeInWindow, l)
	}
}
