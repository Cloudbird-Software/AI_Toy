// latency-smoke —— loop 组合延迟冒烟（IR #95 / docs/m2-spec.md §9）：
// 唤醒→对话→出声 ×N 轮驱动 loop.Pipeline，采五段延迟样本，落盘 budgets
// 兼容的 reports/nightly/latency.json；组合链=本 cmd 生成 → `budgets check`
// 守恒消费（ΣP95−overlap ≤ 1500，基准 configs/budgets/latency.yaml 不改）。
//
// M2 语义声明（诚实优先——数字如实记录，stub 面如实声明）：
//   - Responder 面板 = 模板 + persona.Apply（M2 桩——cloud_llm 段墙钟为面板
//     真实执行耗时，不代表真实云 LLM 延迟；asr_uplink=M2 桩恒 0：ASR 定稿/
//     上行面未建独立面板）
//   - 合成器 = 即时桩（tts_first 段墙钟为桩耗时，不代表真实 TTS）
//   - transport = 播放启动桩（≈0）
//   - 报告段值 = 逻辑时钟口径（CI 宿主逻辑链路真测——同步组装器段间逻辑
//     推进为构造保证 0，不代表最终产品延迟；墙钟口径随 stdout 摘要输出）
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Cloudbird-Software/AI_Toy/packages/go/kws"
	"github.com/Cloudbird-Software/AI_Toy/packages/go/loop"
	"github.com/Cloudbird-Software/AI_Toy/packages/go/persona"
	"github.com/Cloudbird-Software/AI_Toy/packages/go/tts"
	"github.com/Cloudbird-Software/AI_Toy/packages/go/turntaking"
)

// personaCardYAML 冒烟用人格卡（night-bear——packages/go/persona 测试基准卡
// 同款值；M2 Responder 面板=模板+persona.Apply 的编译输入）。
const personaCardYAML = `
id: night-bear
big5:
  O: 3.5
  C: 4.2
  E: 1.8
  A: 4.6
  N: 1.5
catchphrases:
  - "呼——呼——，月亮升起来啦"
  - "把眼睛交给小熊保管吧"
tone_rules:
  - 句子短，一次只说一件事
  - 少用感叹号，多用轻声词
taboos:
  - 死亡
  - 恐怖
values:
  - 睡前安抚
  - 守时守例
closeness:
  initial: 0.3
  max: 0.9
  warmup_turns: 10
`

const (
	smokeSilenceMs  = 500 // FSM 尾静音门限（tail_silence 逻辑值=此镜像，≤ 段预算 600）
	cloudChunkCount = 3   // 桩流块数
	cloudChunkBytes = 64  // 桩块字节
)

func main() {
	out := flag.String("out", "reports/nightly/latency.json", "延迟报告落盘路径")
	turns := flag.Int("turns", 30, "冒烟轮数（唤醒→对话→出声 ×N）")
	commit := flag.String("commit", "", "产生报告的 commit（缺省取 git HEAD）")
	flag.Parse()
	if *turns < 1 {
		fmt.Fprintln(os.Stderr, "error: -turns 须 ≥ 1")
		os.Exit(2)
	}
	if *commit == "" {
		*commit = headCommit()
	}

	cs, err := persona.Compile(mustCard())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: persona 编译失败: %v\n", err)
		os.Exit(2)
	}

	// Responder 面板（M2=模板+persona Apply 桩）：话轮模板产文+SystemSeg 人格
	// 上下文注入，过词表约束（taboo 残留=0）——面板耗时即 cloud_llm 段墙钟样本来源。
	responder := loop.ResponderFunc(func(t loop.Turn) (string, error) {
		n := strings.TrimPrefix(t.ID, "turn-")
		seg := cs.SystemSeg
		if len(seg) > 24 {
			seg = seg[:24] + "…"
		}
		return cs.Apply("第" + n + "轮陪你说晚安（" + seg + "）"), nil
	})

	synth := newInstantSynth()
	p, err := loop.Wire(loop.Config{
		KWS: kws.Config{FrameMs: 30, ConfirmFrames: 2, RefractoryMs: 500,
			Threshold: 0.5, Infer: kws.ConfidenceFunc(confFromFeats)},
		FSM:  turntaking.Config{SilenceMs: smokeSilenceMs, MaxTurnMs: 20000, BargeInWindow: 300},
		TTS:  tts.RouterConfig{PreSpeak: func(string) error { return nil }, Cloud: synth, Edge: synth},
		Resp: responder,
		Tier: 0, // L0 云档（latency.yaml 预算口径）
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: loop 组装失败: %v\n", err)
		os.Exit(2)
	}

	speakDone := 0
	for i := 0; i < *turns; i++ {
		base := int64(i) * 2000
		evs := append(driveTurn(p, base), drain(p)...)
		for _, e := range evs {
			if e.Kind == loop.EvSpeakDone {
				speakDone++
			}
		}
	}
	if speakDone != *turns {
		fmt.Fprintf(os.Stderr, "error: 冒烟须每轮出声收口：SpeakDone=%d ≠ turns=%d\n", speakDone, *turns)
		os.Exit(1)
	}

	rep := p.LatencyReport()
	rep.Commit, rep.Timestamp = *commit, time.Now().UTC().Format(time.RFC3339)
	rep.Note = "M2 stub 语义（诚实优先，数字如实）：段值=逻辑时钟口径——tail_silence=EvVoiceEnd→ActTurnEnd" +
		"（FSM SilenceMs=500 门限逐轮镜像，真实逻辑值）；asr_uplink=桩恒 0（ASR 定稿/上行面未建独立面板）；" +
		"cloud_llm=Responder 面板（模板+persona.Apply 桩，逻辑口径构造保证 0，墙钟实测见 stdout 摘要）；" +
		"tts_first=即时合成桩（同步泵构造保证逻辑 0）；transport=播放启动桩（同步交付即启动恒 0）；" +
		"overlap_ms=0（保守口径）。不代表最终产品延迟——真实面待 ASR/云 LLM/流式 TTS 接线（M3）。"
	if err := writeReport(*out, rep); err != nil {
		fmt.Fprintf(os.Stderr, "error: 报告落盘失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("loop 组合延迟冒烟（IR #95）：turns=%d tier=L0（云档） FSM SilenceMs=%d\n", *turns, smokeSilenceMs)
	fmt.Println("报告段值=逻辑时钟口径（CI 宿主逻辑链路真测——M2 stub 语义声明见下，不代表最终产品延迟）")
	printSegments("分段（逻辑口径——报告面）", rep.Segments)
	wall := p.LatencyWallReport()
	printSegments("补充观测（CI 宿主墙钟——stub 真实耗时，仅对照）", wall.Segments)
	fmt.Println("M2 语义声明（诚实优先）：asr_uplink=桩（ASR/上行面未建）；cloud_llm=Responder 面板（模板+persona.Apply 桩）；tts_first=即时合成桩；transport=播放启动桩；overlap_ms=0（保守口径）。")
	fmt.Printf("报告已写入 %s（commit=%s）→ 下一步：go run ./tools/budgets/cmd/budgets check --report %s\n", *out, rep.Commit, *out)
}

// driveTurn 单轮驱动：唤醒（两帧超阈）→用户话轮（语音起止+尾静音收口）。
func driveTurn(p *loop.Pipeline, base int64) []loop.Event {
	var out []loop.Event
	out = append(out, p.PushAudioFrame(kws.Frame{TS: base, Feats: []float32{0.9}})...)
	out = append(out, p.PushAudioFrame(kws.Frame{TS: base + 30, Feats: []float32{0.9}})...)
	out = append(out, p.PushVAD(turntaking.VADEvent{Kind: turntaking.EvVoiceStart, AtMs: base + 130})...)
	out = append(out, p.PushVAD(turntaking.VADEvent{Kind: turntaking.EvVoiceEnd, AtMs: base + 430})...)
	out = append(out, p.PushVAD(turntaking.VADEvent{Kind: turntaking.EvNone, AtMs: base + 430 + smokeSilenceMs})...)
	return out
}

// drain 泵到播报收口（防呆上限 8 泵）。
func drain(p *loop.Pipeline) []loop.Event {
	var out []loop.Event
	for i := 0; p.Speaking() && i < 8; i++ {
		out = append(out, p.PumpSpeak()...)
	}
	return out
}

// confFromFeats kws 置信度桩（Feats[0]→置信度——脚本化唤醒注入）。
func confFromFeats(f kws.Frame) float64 {
	if len(f.Feats) > 0 {
		return float64(f.Feats[0])
	}
	return 0
}

// instantSynth 即时合成桩（3 块 ×64B 无延迟——tts_first 墙钟=桩耗时）。
type instantSynth struct{}

func newInstantSynth() *instantSynth { return &instantSynth{} }

func (s *instantSynth) Synthesize(req tts.Request) (tts.AudioStream, error) {
	if strings.TrimSpace(req.Text) == "" {
		return nil, tts.ErrEmptyText
	}
	return &instantStream{}, nil
}

type instantStream struct{ i int }

func (s *instantStream) Next() (tts.Chunk, error) {
	if s.i >= cloudChunkCount {
		return tts.Chunk{}, io.EOF
	}
	s.i++
	return tts.Chunk{Data: bytes.Repeat([]byte{0xAB}, cloudChunkBytes), Seq: s.i, Final: s.i == cloudChunkCount}, nil
}

func (s *instantStream) Cancel() error { return nil }

// mustCard 解析冒烟人格卡。
func mustCard() persona.Card {
	c, err := persona.Load([]byte(personaCardYAML))
	if err != nil {
		panic(fmt.Sprintf("latency-smoke: 内置人格卡非法: %v", err))
	}
	return c
}

// headCommit 当前 HEAD sha（非 git 环境→unknown）。
func headCommit() string {
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	if s := strings.TrimSpace(string(out)); s != "" {
		return s
	}
	return "unknown"
}

// writeReport 报告落盘（目录自建）。
func writeReport(path string, rep loop.LatencyReportDoc) error {
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// printSegments 分段摘要打印（id/p50/p95/ms）。
func printSegments(title string, segs []loop.SegmentStat) {
	fmt.Println(title + ":")
	for _, s := range segs {
		fmt.Printf("  %-13s p50=%-8.3g p95=%-8.3g (ms)\n", s.ID, s.P50, s.P95)
	}
}
