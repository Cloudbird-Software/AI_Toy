// stream —— 流实现（spec §4 降级行为表的执行面）：
//
//	passStream     透传流（cache/cloud/edge 正常路径；云路径首包已预拉）
//	degradedStream 降级流（静默占位≤SilenceCapMs → Edge 全新补偿重合成）
//
// 共同不变量（CI-4 对齐）：所有路径有界返回（绝不 hang）；err 后流终止固化
// （重试=重播半句风险，禁止——不重播半句、已播 Seq 不回退）；Cancel 幂等。
package tts

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// firstPacket 预拉首包（at=chunk 到达时刻——静默上限判定锚点）。
type firstPacket struct {
	c   Chunk
	err error
	at  time.Time
}

func elapsedMs(start time.Time) int64 { return time.Since(start).Milliseconds() }

// passStream 透传流。终止态固化：EOF/中途 err/Cancel 之后 Next 恒返回同一
// 结局（不重试、不重播半句——已播 Seq 不回退）；流中途 err 记 partial 语义。
type passStream struct {
	router  *Router
	req     Request
	inner   AudioStream
	pending *firstPacket // 云路径预拉首包（nil=无预拉）
	start   time.Time
	rec     *metricRec

	mu       sync.Mutex
	terminal error // nil=运行中；io.EOF / err / ErrCanceled
	seq      int
}

func newPassStream(r *Router, req Request, inner AudioStream, fp *firstPacket, start time.Time, rec *metricRec) *passStream {
	return &passStream{router: r, req: req, inner: inner, pending: fp, start: start, rec: rec}
}

func (s *passStream) Next() (Chunk, error) {
	s.mu.Lock()
	if s.terminal != nil {
		s.mu.Unlock()
		return Chunk{}, s.terminal
	}
	var c Chunk
	var err error
	if s.pending != nil {
		c, err = s.pending.c, s.pending.err
		s.pending = nil
	} else {
		c, err = s.inner.Next()
	}
	switch {
	case err == nil:
		if c.Seq > s.seq {
			s.seq = c.Seq
		}
		s.rec.setFirstPacket(elapsedMs(s.start))
		s.mu.Unlock()
		return c, nil
	case errors.Is(err, io.EOF):
		s.terminateLocked(io.EOF)
		s.mu.Unlock()
		s.router.unregister(s.req.TurnID, s)
		return Chunk{}, io.EOF
	default:
		// 中途 err：终止固化（不重播半句）+ partial 语义（已播 Seq 快照）
		s.terminateLocked(err)
		s.mu.Unlock()
		s.router.unregister(s.req.TurnID, s)
		return Chunk{}, err
	}
}

func (s *passStream) Cancel() error {
	s.mu.Lock()
	if s.terminal != nil { // 已终止（含已 Cancel）：幂等无操作
		s.mu.Unlock()
		return nil
	}
	s.terminal = ErrCanceled
	s.rec.finish(false, s.seq) // 打断非故障：不记 partial（已播快照保留在 LastSeq）
	inner := s.inner
	s.mu.Unlock()
	err := inner.Cancel()
	s.router.unregister(s.req.TurnID, s)
	return err
}

// terminateLocked 流终止态固化（须持 s.mu 调用）。
func (s *passStream) terminateLocked(err error) {
	if s.terminal != nil {
		return
	}
	s.terminal = err
	partial := err != nil && !errors.Is(err, ErrCanceled)
	s.rec.finish(partial, s.seq)
}

// degradedStream 云首包失败/超时的降级流（spec §4 降级表「云首包超时」行）：
//
//	首个 Next() 立即返回静默占位 chunk（Data=nil 0 字节——音频面恢复锚点）；
//	随后等待 Edge 补偿首包（构造即启动并行预拉），到达时刻须 ≤start+SilenceCapMs
//	（静默 ≤2s 纪律），超限→ErrTimeout；Edge 流中途 err→终止固化（不重播半句）；
//	Cancel→立即终止（幂等），同 TurnID 不续播。
type degradedStream struct {
	router   *Router
	req      Request
	edge     AudioStream
	start    time.Time // Synthesize 进入时刻（静默计时锚点：含云首包等待期）
	deadline time.Time // start+SilenceCapMs（chaos CH-02：注入即静默开始）
	rec      *metricRec
	fpCh     chan firstPacket
	cancelCh chan struct{}
	once     sync.Once

	mu              sync.Mutex
	terminal        error
	placeholderDone bool
	fp              *firstPacket // 已收未交付的 Edge 首包
	fpDelivered     bool
	seq             int
}

func newDegradedStream(r *Router, req Request, edge AudioStream, start time.Time, rec *metricRec, silenceCap time.Duration) *degradedStream {
	s := &degradedStream{
		router:   r,
		req:      req,
		edge:     edge,
		start:    start,
		deadline: start.Add(silenceCap),
		rec:      rec,
		fpCh:     make(chan firstPacket, 1),
		cancelCh: make(chan struct{}),
	}
	go func() { // Edge 补偿首包并行预拉（静默占位期间已在途）
		c, err := edge.Next()
		s.fpCh <- firstPacket{c: c, err: err, at: time.Now()}
	}()
	return s
}

func (s *degradedStream) Next() (Chunk, error) {
	s.mu.Lock()
	if s.terminal != nil {
		s.mu.Unlock()
		return Chunk{}, s.terminal
	}
	if !s.placeholderDone {
		// 静默占位：立即出 0 字节 chunk（占位开始——静默 ≤SilenceCapMs 内 Edge 出声）
		s.placeholderDone = true
		s.seq = 0
		s.rec.setFirstPacket(elapsedMs(s.start))
		s.mu.Unlock()
		return Chunk{Seq: 0}, nil
	}
	// Edge 首包仅等待一次（fpDelivered）：交付后后续 chunk 直读 Edge 流，
	// 不得重复进入首包等待（fpCh 已消费——重复等待=静默占满 SilenceCapMs）。
	if !s.fpDelivered {
		s.mu.Unlock()
		if err := s.awaitEdgeFirst(); err != nil {
			return Chunk{}, err
		}
		s.mu.Lock()
		if s.terminal != nil { // 等待期间被 Cancel：终止态优先
			t := s.terminal
			s.mu.Unlock()
			return Chunk{}, t
		}
		s.fpDelivered = true
		s.mu.Unlock()
		return s.nextEdge()
	}
	s.mu.Unlock() // 后续 chunk 路径：解锁后交 nextEdge（内部自持锁）
	return s.nextEdge()
}

// awaitEdgeFirst 等待 Edge 补偿首包（锁外）：静默上限内到达→缓存到 s.fp；
// 超限/Edge 失败→ErrTimeout 终止（静默无补偿，上层转文字/动作）；
// Cancel→ErrCanceled。
func (s *degradedStream) awaitEdgeFirst() error {
	d := time.Until(s.deadline)
	if d <= 0 {
		return s.failTimeout(fmt.Errorf("%w: silence cap exceeded before edge first packet", ErrTimeout))
	}
	select {
	case f := <-s.fpCh:
		if f.err != nil && !errors.Is(f.err, io.EOF) {
			return s.failTimeout(fmt.Errorf("%w: edge fallback first packet: %v", ErrTimeout, f.err))
		}
		if f.at.After(s.deadline) {
			return s.failTimeout(fmt.Errorf("%w: edge first packet %v after silence cap", ErrTimeout, f.at.Sub(s.start)))
		}
		s.mu.Lock()
		if s.terminal != nil {
			s.mu.Unlock()
			return s.terminal
		}
		s.fp = &f
		s.mu.Unlock()
		return nil
	case <-time.After(d):
		return s.failTimeout(fmt.Errorf("%w: silence cap %s exceeded", ErrTimeout, time.Since(s.start).Round(time.Millisecond)))
	case <-s.cancelCh:
		return ErrCanceled
	}
}

// nextEdge 交付 Edge 首包（重编号续占位 Seq）或透传后续 chunk。
func (s *degradedStream) nextEdge() (Chunk, error) {
	s.mu.Lock()
	if s.terminal != nil {
		s.mu.Unlock()
		return Chunk{}, s.terminal
	}
	if s.fp != nil {
		f := *s.fp
		s.fp = nil
		if f.err != nil && errors.Is(f.err, io.EOF) { // Edge 空流：占位后流尽
			s.terminateLocked(io.EOF)
			s.mu.Unlock()
			s.router.unregister(s.req.TurnID, s)
			return Chunk{}, io.EOF
		}
		s.seq++
		c := f.c
		c.Seq = s.seq // 占位 Seq=0 → Edge 续 1,2,...（单调，已播 Seq 不回退）
		s.mu.Unlock()
		return c, nil
	}
	c, err := s.edge.Next()
	switch {
	case err == nil:
		s.seq++
		c.Seq = s.seq
		s.mu.Unlock()
		return c, nil
	case errors.Is(err, io.EOF):
		s.terminateLocked(io.EOF)
		s.mu.Unlock()
		s.router.unregister(s.req.TurnID, s)
		return Chunk{}, io.EOF
	default: // 中途 err：终止固化（不重播半句）+ partial 语义
		s.terminateLocked(err)
		s.mu.Unlock()
		s.router.unregister(s.req.TurnID, s)
		return Chunk{}, err
	}
}

func (s *degradedStream) Cancel() error {
	s.mu.Lock()
	if s.terminal != nil { // 已终止：幂等无操作
		s.mu.Unlock()
		return nil
	}
	s.terminal = ErrCanceled
	s.rec.finish(false, s.seq)
	edge := s.edge
	s.mu.Unlock()
	s.once.Do(func() { close(s.cancelCh) }) // 唤醒等待 Edge 首包的 Next
	err := edge.Cancel()
	s.router.unregister(s.req.TurnID, s)
	return err
}

// failTimeout 静默超限：终止流（ErrTimeout 语义）并取消 Edge 流。
func (s *degradedStream) failTimeout(err error) error {
	s.mu.Lock()
	if s.terminal != nil {
		t := s.terminal
		s.mu.Unlock()
		return t
	}
	s.terminal = err
	s.rec.finish(true, s.seq)
	edge := s.edge
	s.mu.Unlock()
	_ = edge.Cancel()
	s.router.unregister(s.req.TurnID, s)
	return err
}

// terminateLocked 流终止态固化（须持 s.mu 调用）。
func (s *degradedStream) terminateLocked(err error) {
	if s.terminal != nil {
		return
	}
	s.terminal = err
	partial := err != nil && !errors.Is(err, ErrCanceled)
	s.rec.finish(partial, s.seq)
}
