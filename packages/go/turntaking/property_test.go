// 属性测试（m1-spec §6 三件套之二，testing/quick）：P1 无永久挂起（含 MaxTurnMs
// 兜底——任意事件序列后 ≤1 步可达 Idle）；P2 打断确定性（Speaking 任意时刻
// EvVoiceStart→首 Action=ActStopTTS、逻辑延迟 0≤BargeInWindow）；P3 单调性
// （迟到事件丢弃且不改变状态）；P4 确定性回放（同事件序列→同态轨迹+同动作序列）。
// 命名与 AGENTS.md「本地命令」的 `-run Property` 匹配。
package turntaking

import (
	"math"
	"reflect"
	"testing"
	"testing/quick"
)

// propCfg 属性测试基线配置（时间参数收窄使 quick 随机时刻（normAt ≤ 1e6）足以
// 覆盖 Silence/MaxTurn 边界；门限语义唯一来源仍是 configs/gates/T3.yaml）。
func propCfg() Config {
	return Config{SilenceMs: 50, MaxTurnMs: 1000, BargeInWindow: 300}
}

// rawEvent 属性测试随机事件原料：Op 选入口（0=OnVAD 1=OnWake 2=OnSpeakStart
// 3=OnSpeakDone）、Kind 为 VAD 类别原料（归一到 0..2）、At 为时刻原料。
// 字段须导出（quick 经 reflect 填充，非导出字段不可 Set）。
type rawEvent struct {
	Op   int8
	Kind int8
	At   int64
}

// eventSeq 固定 8 事件的随机序列（quick 不支持 slice 形参，用导出字段结构体承载）。
type eventSeq struct {
	E0, E1, E2, E3, E4, E5, E6, E7 rawEvent
}

func seqEvents(s eventSeq) [8]rawEvent {
	return [8]rawEvent{s.E0, s.E1, s.E2, s.E3, s.E4, s.E5, s.E6, s.E7}
}

// normAt 把随机 int64 归一到 [0,1e6)（负数经 uint64 模运算均匀落段；有界段内
// 无加减溢出，MaxTurn 兜底步可安全外推）。
func normAt(v int64) int64 { return int64(uint64(v) % 1_000_000) }

func normMod8(v int8) int8 { return (v%8 + 8) % 8 }

func normKind(k int8) VADEventKind { return VADEventKind((k%3 + 3) % 3) }

// applyEvent 把原料归一后喂给 FSM（四个入口全覆盖，AtMs 经 normAt 有界）。
func applyEvent(f *FSM, e rawEvent) []Action {
	at := normAt(e.At)
	switch normMod8(e.Op) % 4 {
	case 0:
		return f.OnVAD(VADEvent{Kind: normKind(e.Kind), AtMs: at})
	case 1:
		return f.OnWake(at)
	case 2:
		return f.OnSpeakStart(at)
	default:
		return f.OnSpeakDone(at)
	}
}

// mirrorLast 与实现同步的已见最大 AtMs 镜像（仅被接受的事件推进）。
func mirrorLast(last, at int64) int64 {
	if at >= last {
		return at
	}
	return last
}

// TestPropertyNoPermanentHang P1 无永久挂起（资产卡活性 + m1-spec §3 P3 MaxTurnMs
// 兜底）：任意 8 事件随机序列后，Idle 0 步即达；Listening 1 个 VAD 事件（任意类别，
// 含「VAD 永不静音」的 VoiceStart）即触发累计截断回 Idle；Speaking 1 个 OnSpeakDone
// 回 Idle。即任意可达状态 ≤1 步可达 Idle。
func TestPropertyNoPermanentHang(t *testing.T) {
	cfg := propCfg()
	f := func(seq eventSeq) bool {
		fsm, err := NewFSM(cfg)
		if err != nil {
			t.Errorf("NewFSM 基线配置被拒：%v", err)
			return false
		}
		last := int64(math.MinInt64) // 镜像实现的 lastMs 初值
		for _, e := range seqEvents(seq) {
			at := normAt(e.At)
			applyEvent(fsm, e)
			last = mirrorLast(last, at)
		}
		switch fsm.State() {
		case StIdle:
			return true // 0 步已达
		case StListening:
			// MaxTurnMs 兜底：last ≥ turnStart ⇒ 该时刻累计 ≥ MaxTurnMs，
			// 行4 截断先于行2 自转移（VAD 永不静音也照样截断）。
			at := last + int64(cfg.MaxTurnMs) + 1
			act := fsm.OnVAD(VADEvent{Kind: EvVoiceStart, AtMs: at})
			return fsm.State() == StIdle &&
				containsKind(act, ActTurnEnd) && containsKind(act, ActCloseMic)
		case StSpeaking:
			fsm.OnSpeakDone(last + 1)
			return fsm.State() == StIdle
		}
		return false
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 300}); err != nil {
		t.Errorf("P1 无永久挂起被违反：%v", err)
	}
}

// TestPropertyBargeInDeterministic P2 打断确定性/即时性：Speaking 态任意时刻
// （含与 SpeakStart 同刻、开播后极早、任意延迟）注入用户 EvVoiceStart →
// 首动作 ActStopTTS、次动作 ActOpenMic、态转 Listening、StopTTS.AtMs=事件时刻
// （同步单步：逻辑延迟 0 ≤ BargeInWindow ≤ 300ms 契约；链路实测延迟 M2 硬件计时）。
func TestPropertyBargeInDeterministic(t *testing.T) {
	cfg := propCfg()
	f := func(wakeAt, speakGap, bargGap int64) bool {
		t0 := normAt(wakeAt)
		t1 := t0 + normAt(speakGap)%100 // 开播时刻 ≥ 唤醒
		t2 := t1 + normAt(bargGap)%9973 // 打断时刻 ≥ 开播（含同刻 0）
		fsm, err := NewFSM(cfg)
		if err != nil {
			t.Errorf("NewFSM 基线配置被拒：%v", err)
			return false
		}
		fsm.OnWake(t0)
		fsm.OnSpeakStart(t1)
		if fsm.State() != StSpeaking {
			return false
		}
		act := fsm.OnVAD(VADEvent{Kind: EvVoiceStart, AtMs: t2})
		if len(act) != 2 || act[0].Kind != ActStopTTS || act[1].Kind != ActOpenMic {
			return false
		}
		if act[0].AtMs != t2 || act[1].AtMs != t2 {
			return false
		}
		if fsm.State() != StListening {
			return false
		}
		lat := fsm.BargeInLatencyMs()
		return lat == 0 && lat <= int64(cfg.BargeInWindow)
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 300}); err != nil {
		t.Errorf("P2 打断确定性被违反：%v", err)
	}
}

// TestPropertyMonotonicAtMs P3 单调性：AtMs 早于已见最大值的事件整体丢弃——
// 零动作且状态不变（迟到帧不回放、不拨回内部基准）；已见最大值只增不减。
func TestPropertyMonotonicAtMs(t *testing.T) {
	cfg := propCfg()
	f := func(seq eventSeq) bool {
		fsm, err := NewFSM(cfg)
		if err != nil {
			t.Errorf("NewFSM 基线配置被拒：%v", err)
			return false
		}
		last := int64(math.MinInt64)
		for _, e := range seqEvents(seq) {
			at := normAt(e.At)
			before := fsm.State()
			act := applyEvent(fsm, e)
			if at < last {
				if len(act) != 0 || fsm.State() != before {
					t.Logf("迟到事件（AtMs=%d < 已见最大 %d）未被整丢：actions=%v state %d→%d",
						at, last, act, before, fsm.State())
					return false
				}
				continue
			}
			last = mirrorLast(last, at)
		}
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 300}); err != nil {
		t.Errorf("P3 单调性被违反：%v", err)
	}
}

// TestPropertyDeterministicReplay P4 确定性回放：同事件序列在两个独立 FSM 实例
// 重放 → 状态轨迹、动作序列、打断延迟元数据全等（无隐藏状态/时钟依赖）。
func TestPropertyDeterministicReplay(t *testing.T) {
	cfg := propCfg()
	type trace struct {
		States  []State
		Actions []Action
		Latency int64
	}
	f := func(seq eventSeq) bool {
		run := func() trace {
			fsm, err := NewFSM(cfg)
			if err != nil {
				t.Errorf("NewFSM 基线配置被拒：%v", err)
				return trace{}
			}
			var tr trace
			for _, e := range seqEvents(seq) {
				tr.Actions = append(tr.Actions, applyEvent(fsm, e)...)
				tr.States = append(tr.States, fsm.State())
			}
			tr.Latency = fsm.BargeInLatencyMs()
			return tr
		}
		a, b := run(), run()
		return reflect.DeepEqual(a, b)
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("P4 确定性回放被违反：%v", err)
	}
}
