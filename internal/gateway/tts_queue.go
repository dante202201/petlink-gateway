package gateway

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"pet-translate-gateway/internal/model"
	"pet-translate-gateway/internal/util"
)

const (
	defaultTTSQueueCap   = 2
	defaultTTSInterGap   = 500 * time.Millisecond
	defaultTTSFramePace  = 55 * time.Millisecond
	defaultTTSJobTimeout = 3 * time.Minute
)

// ttsJob 一路 TTS（新一路 seq 从 0 编号，设备靠 seq 认句子边界）。
type ttsJob struct {
	frames [][]byte
	pid    string
	done   chan error
}

type ttsSender struct {
	sn     string
	connID string
	m      *ConnManager

	mu          sync.Mutex
	cond        *sync.Cond
	pending     []*ttsJob
	cur         *ttsJob
	curIdx      int
	paused      bool
	drained     bool // 首路 true；发完一路后等 resume / level ms=0
	lastFrameAt time.Time
	bufferMs    int
	closed      bool
}

func (m *ConnManager) ttsQueueCap() int {
	if m != nil && m.cfg != nil && m.cfg.Translate.TTSQueueCap > 0 {
		return m.cfg.Translate.TTSQueueCap
	}
	return defaultTTSQueueCap
}

func (m *ConnManager) ttsInterGap() time.Duration {
	if m != nil && m.cfg != nil && m.cfg.Translate.TTSInterGapMs > 0 {
		return time.Duration(m.cfg.Translate.TTSInterGapMs) * time.Millisecond
	}
	return defaultTTSInterGap
}

func (m *ConnManager) getOrCreateTTS(dc *DeviceConn) *ttsSender {
	m.ttsMu.Lock()
	defer m.ttsMu.Unlock()
	if m.tts == nil {
		m.tts = make(map[string]*ttsSender)
	}
	if s := m.tts[dc.SN]; s != nil && s.connID == dc.ConnID && !s.closed {
		return s
	}
	s := &ttsSender{sn: dc.SN, connID: dc.ConnID, m: m, drained: true}
	s.cond = sync.NewCond(&s.mu)
	m.tts[dc.SN] = s
	go s.loop()
	return s
}

func (m *ConnManager) closeTTS(sn, connID string) {
	m.ttsMu.Lock()
	s := m.tts[sn]
	if s != nil && s.connID == connID {
		delete(m.tts, sn)
		s.close()
	}
	m.ttsMu.Unlock()
}

func (m *ConnManager) handleTTSFlow(sn string, action string, ms int) {
	m.ttsMu.Lock()
	s := m.tts[sn]
	m.ttsMu.Unlock()
	if s == nil {
		m.log.Info("收到 tts_flow 但本设备无在途 TTS",
			zap.String("sn", sn),
			zap.String("action", action),
			zap.Int("ms", ms),
		)
		return
	}
	s.onFlow(action, ms)
}

func (s *ttsSender) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	fail := fmt.Errorf("设备断开，TTS 队列取消 sn=%s", s.sn)
	if s.cur != nil {
		select {
		case s.cur.done <- fail:
		default:
		}
		s.cur = nil
	}
	for _, j := range s.pending {
		select {
		case j.done <- fail:
		default:
		}
	}
	s.pending = nil
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *ttsSender) onFlow(action string, ms int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch action {
	case "pause":
		s.paused = true
		s.m.log.Info("TTS 流控 pause：停发，剩余帧留在队列",
			zap.String("sn", s.sn),
			zap.Int("cur_idx", s.curIdx),
			zap.Int("pending", len(s.pending)),
		)
	case "resume":
		s.paused = false
		s.drained = true
		s.bufferMs = 0
		s.m.log.Info("TTS 流控 resume：可续发/放下一路",
			zap.String("sn", s.sn),
			zap.Int("cur_idx", s.curIdx),
			zap.Int("pending", len(s.pending)),
		)
	case "level":
		s.bufferMs = ms
		if ms == 0 {
			s.drained = true
			s.paused = false
		}
		s.m.log.Info("TTS 水位 level",
			zap.String("sn", s.sn),
			zap.Int("ms", ms),
			zap.Bool("drained", s.drained),
		)
	default:
		s.m.log.Info("未知 tts_flow action", zap.String("sn", s.sn), zap.String("action", action), zap.Int("ms", ms))
	}
	s.cond.Broadcast()
}

func (s *ttsSender) enqueue(job *ttsJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("TTS 队列已关闭 sn=%s", s.sn)
	}
	capN := s.m.ttsQueueCap()
	if capN < 1 {
		capN = defaultTTSQueueCap
	}
	// 封顶=在途+等待；再多丢掉最老的等待路，保新
	for s.queueLenLocked() >= capN && len(s.pending) > 0 {
		old := s.pending[0]
		s.pending = s.pending[1:]
		select {
		case old.done <- fmt.Errorf("TTS 队列满，丢弃旧路 sn=%s", s.sn):
		default:
		}
		s.m.log.Warn("TTS 队列满，丢掉旧路保新",
			zap.String("sn", s.sn),
			zap.Int("cap", capN),
			zap.Int("dropped_frames", len(old.frames)),
		)
	}
	s.pending = append(s.pending, job)
	s.cond.Signal()
	return nil
}

func (s *ttsSender) queueLenLocked() int {
	n := len(s.pending)
	if s.cur != nil {
		n++
	}
	return n
}

func (s *ttsSender) loop() {
	for {
		s.mu.Lock()
		for !s.closed && (s.paused || s.cur == nil && (len(s.pending) == 0 || !s.canStartLocked())) {
			if s.cur == nil && len(s.pending) > 0 && !s.canStartLocked() && !s.paused {
				wait := s.remainInterGapLocked()
				if wait > 0 {
					s.waitTimeoutLocked(wait)
					continue
				}
			}
			s.cond.Wait()
		}
		if s.closed {
			s.mu.Unlock()
			return
		}
		if s.cur == nil && len(s.pending) > 0 && s.canStartLocked() {
			s.cur = s.pending[0]
			s.pending = s.pending[1:]
			s.curIdx = 0
			s.m.log.Info("TTS 串行放行下一路",
				zap.String("sn", s.sn),
				zap.Int("frames", len(s.cur.frames)),
				zap.Int("pending", len(s.pending)),
			)
		}
		job := s.cur
		idx := s.curIdx
		s.mu.Unlock()
		if job == nil {
			continue
		}
		if idx >= len(job.frames) {
			s.finishCurrent(nil)
			continue
		}
		if err := s.m.sendTTSFrame(s.sn, job.pid, idx, len(job.frames), job.frames[idx]); err != nil {
			s.finishCurrent(err)
			continue
		}
		s.mu.Lock()
		s.curIdx = idx + 1
		s.lastFrameAt = time.Now()
		done := s.curIdx >= len(job.frames)
		s.mu.Unlock()
		if done {
			s.finishCurrent(nil)
			continue
		}
		time.Sleep(defaultTTSFramePace)
	}
}

func (s *ttsSender) canStartLocked() bool {
	if s.paused {
		return false
	}
	if s.lastFrameAt.IsZero() {
		return s.drained
	}
	if !s.drained {
		return false
	}
	return time.Since(s.lastFrameAt) >= s.m.ttsInterGap()
}

func (s *ttsSender) remainInterGapLocked() time.Duration {
	need := s.m.ttsInterGap()
	if s.lastFrameAt.IsZero() {
		return 0
	}
	elapsed := time.Since(s.lastFrameAt)
	if elapsed >= need {
		return 0
	}
	return need - elapsed
}

func (s *ttsSender) waitTimeoutLocked(d time.Duration) {
	timer := time.AfterFunc(d, func() {
		s.mu.Lock()
		s.cond.Broadcast()
		s.mu.Unlock()
	})
	s.cond.Wait()
	timer.Stop()
}

func (s *ttsSender) finishCurrent(err error) {
	s.mu.Lock()
	job := s.cur
	s.cur = nil
	s.curIdx = 0
	if job != nil && err == nil {
		s.drained = false // 下一路必须等 resume / level ms=0
	}
	s.mu.Unlock()
	if job == nil {
		return
	}
	if err != nil {
		s.m.log.Warn("TTS 一路发送失败", zap.String("sn", s.sn), zap.Error(err))
	} else {
		s.m.log.Info("TTS 一路帧已发完，等待设备 resume 后放行下一路",
			zap.String("sn", s.sn),
			zap.Int("frames", len(job.frames)),
		)
	}
	select {
	case job.done <- err:
	default:
	}
}

func (m *ConnManager) enqueueTTSAndWait(sn string, frames [][]byte, pid string) error {
	if len(frames) == 0 {
		return fmt.Errorf("tts frames 为空")
	}
	dc, ok := m.Get(sn)
	if !ok || dc == nil {
		return fmt.Errorf("设备离线，无法下发 TTS sn=%s", sn)
	}
	job := &ttsJob{frames: frames, pid: pid, done: make(chan error, 1)}
	sender := m.getOrCreateTTS(dc)
	if err := sender.enqueue(job); err != nil {
		return err
	}
	timer := time.NewTimer(defaultTTSJobTimeout)
	defer timer.Stop()
	select {
	case err := <-job.done:
		return err
	case <-timer.C:
		return fmt.Errorf("TTS 下发超时 sn=%s frames=%d", sn, len(frames))
	}
}

func (m *ConnManager) sendTTSFrame(sn, pid string, seq, total int, opus []byte) error {
	dc, ok := m.Get(sn)
	if !ok || dc == nil {
		return fmt.Errorf("设备离线，无法下发 TTS sn=%s", sn)
	}
	if len(opus) == 0 {
		return fmt.Errorf("tts opus 为空")
	}
	end := 0
	if seq == total-1 {
		end = 1
	}
	crc := util.CRC32IEEE(opus)
	metaBody, err := json.Marshal(model.TTSMeta{
		Type:  "tts",
		Seq:   seq,
		Total: total,
		End:   end,
		Codec: "opus",
		PID:   pid,
		CRC:   crc,
	})
	if err != nil {
		return err
	}
	if err := m.enqueueToConn(dc, OutboundMsg{MsgType: websocket.TextMessage, Data: metaBody}); err != nil {
		return fmt.Errorf("第 %d/%d 元数据: %w", seq+1, total, err)
	}
	if err := m.enqueueToConn(dc, OutboundMsg{MsgType: websocket.BinaryMessage, Data: opus}); err != nil {
		return fmt.Errorf("第 %d/%d 音频: %w", seq+1, total, err)
	}
	m.logDownlinkTTSBinary(sn, seq, total, end, crc, opus)
	return nil
}
