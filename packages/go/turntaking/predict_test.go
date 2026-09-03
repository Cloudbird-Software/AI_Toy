// 预测提前量单测（predict.go 语义）：不变量①语音中绝不提前；②提前只缩短等待、
// 其余转移零影响、打断链不变；③零值/负门限=关闭；④非单调丢弃；⑤提前终点动作
// 与 endTurn 同形。预测源用确定性 fake（真引擎在 packages/go/turntaking/vap）。
package turntaking

import "testing"

// fakePred 固定值预测源。
type fakePred struct {
	p  Prediction
	ok bool
}

func (f fakePred) Predict() (Prediction, bool) { return f.p, f.ok }

func TestVAPLeadDisabled(t *testing.T) {
	fsm, err := NewFSM(Config{SilenceMs: 800, MaxTurnMs: 10000, BargeInWindow: 300})
	if err != nil {
		t.Fatalf("NewFSM: %v", err)
	}
	wake(0)(fsm)
	vad(EvVoiceStart, 100)(fsm)
	vad(EvVoiceEnd, 200)(fsm)
	// 未配置提前量（零值）：即便模型高置信也不动作。
	if got := fsm.PredictLead(fakePred{p: Prediction{PNowSystem: 0.99}, ok: true}, 300); len(got) != 0 {
		t.Errorf("零门限须关闭提前量，got %v", got)
	}
	if fsm.State() != StListening {
		t.Errorf("关闭提前量不得改状态，got %d", fsm.State())
	}
	// 负门限配置被拒。
	if _, err := NewFSM(Config{SilenceMs: 800, MaxTurnMs: 10000, BargeInWindow: 300, VAPLeadPNow: -0.1}); err == nil {
		t.Errorf("负门限须被配置校验拒绝")
	}
	if _, err := NewFSM(Config{SilenceMs: 800, MaxTurnMs: 10000, BargeInWindow: 300, VAPLeadPNow: 1.1}); err == nil {
		t.Errorf(">1 门限须被配置校验拒绝")
	}
}

func TestVAPLeadFiresEarly(t *testing.T) {
	cfg := Config{SilenceMs: 800, MaxTurnMs: 10000, BargeInWindow: 300, VAPLeadPNow: 0.7}
	fsm, err := NewFSM(cfg)
	if err != nil {
		t.Fatalf("NewFSM: %v", err)
	}
	wake(0)(fsm)
	vad(EvVoiceStart, 100)(fsm)
	vad(EvVoiceEnd, 200)(fsm)
	// 门限未达：不动作。
	if got := fsm.PredictLead(fakePred{p: Prediction{PNowSystem: 0.69}, ok: true}, 400); len(got) != 0 {
		t.Errorf("PNowSystem 0.69<0.7 不得提前，got %v", got)
	}
	// 达门限：话轮提前收口（200→400 仅 200ms 静音，VAD 路径须 1000ms）。
	got := fsm.PredictLead(fakePred{p: Prediction{PNowSystem: 0.7}, ok: true}, 400)
	if !actsEqual(got, []Action{{Kind: ActTurnEnd, AtMs: 400}, {Kind: ActCloseMic, AtMs: 400}}) {
		t.Fatalf("提前终点动作须与 endTurn 同形，got %v", got)
	}
	if fsm.State() != StIdle {
		t.Errorf("提前终点后须 Idle，got %d", fsm.State())
	}
}

func TestVAPLeadSafetyInvariants(t *testing.T) {
	cfg := Config{SilenceMs: 800, MaxTurnMs: 10000, BargeInWindow: 300, VAPLeadPNow: 0.5}
	hi := fakePred{p: Prediction{PNowSystem: 0.9}, ok: true}

	t.Run("不变量①语音进行中绝不提前", func(t *testing.T) {
		fsm, _ := NewFSM(cfg)
		wake(0)(fsm)
		vad(EvVoiceStart, 100)(fsm)
		if got := fsm.PredictLead(hi, 200); len(got) != 0 || fsm.State() != StListening {
			t.Errorf("语音中提前=误截断：actions=%v state=%d", got, fsm.State())
		}
		// 唤醒后未开口语境（行1 入口：silenceRunning=true，与行3 收口语义一致）：
		// 模型预测系统接话 → 提前收口合法（唤醒话轮无用户内容可误截；玩具接话
		// 正是模型语义），断言动作同形而非不动作。
		fsm2, _ := NewFSM(cfg)
		wake(0)(fsm2)
		got := fsm2.PredictLead(hi, 50)
		if !actsEqual(got, []Action{{Kind: ActTurnEnd, AtMs: 50}, {Kind: ActCloseMic, AtMs: 50}}) {
			t.Errorf("唤醒静默话轮提前收口须 endTurn 同形，got %v", got)
		}
	})

	t.Run("不变量②打断链零影响", func(t *testing.T) {
		fsm, _ := NewFSM(cfg)
		wake(0)(fsm)
		speakStart(100)(fsm)
		// Speaking 态预测不动作（打断仍由 EvVoiceStart 驱动，G0-01 层不变）。
		if got := fsm.PredictLead(hi, 200); len(got) != 0 {
			t.Errorf("Speaking 态不得预测动作，got %v", got)
		}
		got := vad(EvVoiceStart, 300)(fsm)
		if len(got) < 2 || got[0].Kind != ActStopTTS || fsm.BargeInLatencyMs() != 0 {
			t.Errorf("预测接缝不得影响打断链：actions=%v lat=%d", got, fsm.BargeInLatencyMs())
		}
	})

	t.Run("不变量④非单调丢弃", func(t *testing.T) {
		fsm, _ := NewFSM(cfg)
		wake(1000)(fsm)
		vad(EvVoiceStart, 1100)(fsm)
		vad(EvVoiceEnd, 1200)(fsm)
		if got := fsm.PredictLead(hi, 1100); len(got) != 0 {
			t.Errorf("迟到预测不得回放，got %v", got)
		}
	})

	t.Run("ok=false 预测不动作", func(t *testing.T) {
		fsm, _ := NewFSM(cfg)
		wake(0)(fsm)
		vad(EvVoiceStart, 100)(fsm)
		vad(EvVoiceEnd, 200)(fsm)
		if got := fsm.PredictLead(fakePred{}, 300); len(got) != 0 {
			t.Errorf("无有效帧不得动作，got %v", got)
		}
	})
}
