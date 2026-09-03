// VAP 话轮预测接缝（W3 接入 IR #129，路径 C=混合式：VAD 保底挂 G0 层 + 预测模型
// 负延迟提前量，可独立回滚）。本文件保持包内零依赖（import 白名单=标准库，
// ADR-0004）：Prediction 是 MaAI VAP 输出的语义镜像，真引擎在
// packages/go/turntaking/vap（ONNX Runtime），与本包互不 import——由驱动层
// （loop/测试 harness）结构性对接 Predictor 接口。
//
// 提前量语义（PredictLead）：仅在 Listening 且尾静音计时进行中（含唤醒后未开口
// 的静默话轮——行1 入口 silenceRunning=true 与行3 收口语义一致，该话轮无用户
// 内容可误截）时，若模型预测系统将在 0–600ms 内接话（PNowSystem ≥ 门限）判定用户
// 话轮语义完结，提前发 ActTurnEnd/ActCloseMic——比等满 SilenceMs 提前（负延迟）。
// 安全不变量（predict_test.go 断言）：
//
//	① 用户语音进行中（voiceActive）绝不提前终点（误截断保护，G1-01/G1-04 不劣化）；
//	② 提前量只缩短等待、不改变状态机其余转移；打断链（G0-01）零影响；
//	③ 提前阈值 ≤0 视为关闭（零值安全：不配置即纯 VAD 行为，与既有门禁兼容）。
package turntaking

// Prediction VAP 单帧预测（MaAI MC-VAP 语义：双说话人 [user, system]，值域 [0,1]）。
// user=话筒（孩子），system=玩具本机。
type Prediction struct {
	PNowUser      float32 // 0–600ms 内 user 语音活动概率
	PNowSystem    float32 // 0–600ms 内 system 接话概率（提前量主信号）
	PFutureUser   float32 // 600–2000ms 内 user 活动概率
	PFutureSystem float32 // 600–2000ms 内 system 活动概率
	VADUser       float32 // user 当前帧 VAD
	VADSystem     float32 // system 当前帧 VAD
}

// Predictor 预测源窄接口（真引擎结构性实现，包间零 import）。
type Predictor interface {
	// Predict 返回最近一帧预测；ok=false 表示尚无有效帧（引擎未热身/已失效）。
	Predict() (p Prediction, ok bool)
}

// PredictLead 模型提前量：Listening 态、尾静音计时中、且模型预测系统马上接话时
// 提前收口话轮。atMs 单调门与 OnVAD 一致（非单调丢弃）。返回动作与 endTurn 同形
// （ActTurnEnd+ActCloseMic）。p==nil 或阈值关闭时为零动作。
func (f *FSM) PredictLead(p Predictor, atMs int64) []Action {
	if f.leadThreshold <= 0 {
		return nil
	}
	if !f.accept(atMs) {
		return nil
	}
	if f.state != StListening || !f.silenceRunning || f.voiceActive {
		return nil // 不变量①：语音中/非收口语境绝不提前
	}
	pred, ok := p.Predict()
	if !ok || pred.PNowSystem < f.leadThreshold {
		return nil
	}
	return f.endTurn(atMs)
}
