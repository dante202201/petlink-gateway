package pipeline

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"

	"pet-translate-gateway/internal/model"
)

// EventStitchSeg 一次设备关段（kind=0），等待实测窗口内的后续段拼成一轮。
type EventStitchSeg struct {
	DeviceTS       int64
	FirstTS        int64
	LastTS         int64
	PetHint        string
	PID            string
	FullTranslate  bool
	DeviceSilence  int // 设备 Sound ended silence，毫秒；0=未报
	DeviceSegGap   int // 设备 seg_gap，毫秒；0=未报
}

type stitchGroup struct {
	segs  []EventStitchSeg
	timer *time.Timer
}

// StitchDecision 相邻两段实测间隙是否落入拼接窗口。
// 真实沉默 ≈ 实测间隙 + 设备尾巴数字静音（默认 800ms，误差约 100ms）。
func StitchDecision(measuredGapUs int64, windowMs, deviceSilenceMs int) (inWindow bool, measuredGapMs, realSilenceEstMs int) {
	if windowMs <= 0 {
		windowMs = 5000
	}
	if deviceSilenceMs < 0 {
		deviceSilenceMs = 0
	}
	if measuredGapUs < 0 {
		measuredGapUs = 0
	}
	measuredGapMs = int(measuredGapUs / 1000)
	realSilenceEstMs = measuredGapMs + deviceSilenceMs
	inWindow = measuredGapMs <= windowMs
	return
}

func (s *Service) stitchWindow() time.Duration {
	ms := s.stitchWindowMs
	if ms <= 0 {
		ms = 5000
	}
	return time.Duration(ms) * time.Millisecond
}

func (s *Service) effectiveDeviceSegSilenceMs() int {
	if s.deviceSegSilenceMs > 0 {
		return s.deviceSegSilenceMs
	}
	return 800
}

// StitchEnabled 事件流是否走拼接窗口（录音文件不走）。
func (s *Service) StitchEnabled() bool {
	return s != nil && s.stitchOn
}

// NoteEventSegBegin 新事件段 seq=0：若与上一轮尾巴间隙已超窗口，立刻落地上一轮。
func (s *Service) NoteEventSegBegin(sn string, firstTS int64) {
	if s == nil || !s.stitchOn || sn == "" || firstTS <= 0 {
		return
	}
	s.stitchMu.Lock()
	g := s.stitchWait[sn]
	if g == nil || len(g.segs) == 0 {
		s.stitchMu.Unlock()
		return
	}
	prev := g.segs[len(g.segs)-1]
	gapUs := firstTS - prev.LastTS
	inWindow, measuredMs, realEst := StitchDecision(gapUs, s.stitchWindowMs, s.effectiveDeviceSegSilenceMs())
	s.log.Info("事件流相邻段拼接口径",
		zap.String("sn", sn),
		zap.Int64("prev_device_ts", prev.DeviceTS),
		zap.Int64("prev_last_ts", prev.LastTS),
		zap.Int64("next_first_ts", firstTS),
		zap.Int("measured_gap_ms", measuredMs),
		zap.Int("device_seg_silence_ms", s.effectiveDeviceSegSilenceMs()),
		zap.Int("real_silence_est_ms", realEst),
		zap.Int("stitch_window_ms", s.stitchWindowMs),
		zap.Bool("in_window", inWindow),
		zap.Int("device_silence_ms", prev.DeviceSilence),
		zap.Int("device_seg_gap_ms", prev.DeviceSegGap),
		zap.String("hint", "真实沉默≈实测间隙+800ms；5s窗口≈5.9s真实沉默；固件关段阈值若改2500，窗口必须同步"),
	)
	if inWindow {
		s.stitchMu.Unlock()
		return
	}
	g.timer.Stop()
	delete(s.stitchWait, sn)
	segs := append([]EventStitchSeg(nil), g.segs...)
	s.stitchMu.Unlock()
	s.log.Info("事件流拼接窗口已超，先落地上一轮",
		zap.String("sn", sn),
		zap.Int("segs", len(segs)),
		zap.Int("measured_gap_ms", measuredMs),
	)
	s.flushEventStitch(sn, segs)
}

// ScheduleEventStitch 事件流 end=true：进入实测窗口，超时或超窗后拼一轮再翻译。
func (s *Service) ScheduleEventStitch(sn string, seg EventStitchSeg) {
	if s == nil || !s.Enabled() {
		return
	}
	if !s.stitchOn {
		s.ProcessSessionAsync(sn, seg.DeviceTS, seg.PetHint, seg.PID, model.AudioKindEvent, seg.FullTranslate)
		return
	}
	if seg.DeviceTS <= 0 {
		s.ProcessSessionAsync(sn, seg.DeviceTS, seg.PetHint, seg.PID, model.AudioKindEvent, seg.FullTranslate)
		return
	}
	s.stitchMu.Lock()
	defer s.stitchMu.Unlock()
	if s.stitchWait == nil {
		s.stitchWait = make(map[string]*stitchGroup)
	}
	g := s.stitchWait[sn]
	if g != nil && len(g.segs) > 0 {
		prev := g.segs[len(g.segs)-1]
		if prev.DeviceTS == seg.DeviceTS {
			g.segs[len(g.segs)-1] = seg
			s.resetStitchTimerLocked(sn, g)
			return
		}
		gapUs := seg.FirstTS - prev.LastTS
		if seg.FirstTS <= 0 || prev.LastTS <= 0 {
			gapUs = 0
		}
		inWindow, measuredMs, realEst := StitchDecision(gapUs, s.stitchWindowMs, s.effectiveDeviceSegSilenceMs())
		s.log.Info("事件流关段拼接口径",
			zap.String("sn", sn),
			zap.Int64("prev_device_ts", prev.DeviceTS),
			zap.Int64("this_device_ts", seg.DeviceTS),
			zap.Int("measured_gap_ms", measuredMs),
			zap.Int("device_seg_silence_ms", s.effectiveDeviceSegSilenceMs()),
			zap.Int("real_silence_est_ms", realEst),
			zap.Int("stitch_window_ms", s.stitchWindowMs),
			zap.Bool("in_window", inWindow),
			zap.Int("device_silence_ms", seg.DeviceSilence),
			zap.Int("device_seg_gap_ms", seg.DeviceSegGap),
		)
		if !inWindow {
			g.timer.Stop()
			old := append([]EventStitchSeg(nil), g.segs...)
			s.stitchWait[sn] = &stitchGroup{segs: []EventStitchSeg{seg}}
			s.resetStitchTimerLocked(sn, s.stitchWait[sn])
			go s.flushEventStitch(sn, old)
			return
		}
		g.segs = append(g.segs, seg)
		s.resetStitchTimerLocked(sn, g)
		return
	}
	s.stitchWait[sn] = &stitchGroup{segs: []EventStitchSeg{seg}}
	s.resetStitchTimerLocked(sn, s.stitchWait[sn])
	s.log.Info("事件流进入拼接窗口",
		zap.String("sn", sn),
		zap.Int64("device_ts", seg.DeviceTS),
		zap.Int64("first_ts", seg.FirstTS),
		zap.Int64("last_ts", seg.LastTS),
		zap.Int("stitch_window_ms", s.stitchWindowMs),
		zap.Int("device_seg_silence_ms", s.effectiveDeviceSegSilenceMs()),
		zap.String("timeline", "device: base+seq*60ms（不用到达墙钟）"),
	)
}

func (s *Service) resetStitchTimerLocked(sn string, g *stitchGroup) {
	if g.timer != nil {
		g.timer.Stop()
	}
	g.timer = time.AfterFunc(s.stitchWindow(), func() {
		s.stitchMu.Lock()
		cur := s.stitchWait[sn]
		if cur != g {
			s.stitchMu.Unlock()
			return
		}
		delete(s.stitchWait, sn)
		segs := append([]EventStitchSeg(nil), cur.segs...)
		s.stitchMu.Unlock()
		s.flushEventStitch(sn, segs)
	})
}

func (s *Service) flushEventStitch(sn string, segs []EventStitchSeg) {
	if len(segs) == 0 {
		return
	}
	primary := segs[0]
	extra := make([]int64, 0, len(segs)-1)
	for i := 1; i < len(segs); i++ {
		if segs[i].DeviceTS > 0 && segs[i].DeviceTS != primary.DeviceTS {
			extra = append(extra, segs[i].DeviceTS)
		}
	}
	s.log.Info("事件流拼接窗口到期，开始翻译",
		zap.String("sn", sn),
		zap.Int("segs", len(segs)),
		zap.Int64("primary_device_ts", primary.DeviceTS),
		zap.Int64s("stitch_device_ts", extra),
		zap.Bool("full_translate", primary.FullTranslate),
	)
	s.processSessionsAsync(sn, primary.DeviceTS, extra, primary.PetHint, primary.PID, model.AudioKindEvent, primary.FullTranslate)
}

func (s *Service) processSessionsAsync(sn string, deviceTS int64, extra []int64, petHint, pid string, audioKind int, fullTranslate bool) {
	if !s.Enabled() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		unlock := s.lockSessionKeys(sn, audioKind, append([]int64{deviceTS}, extra...))
		defer unlock()
		if err := s.process(ctx, sn, deviceTS, petHint, pid, audioKind, fullTranslate, extra); err != nil {
			s.log.Error("音频会话处理失败",
				zap.String("sn", sn),
				zap.Int64("device_ts", deviceTS),
				zap.Int64s("stitch_device_ts", extra),
				zap.Int("audio_kind", audioKind),
				zap.Bool("full_translate", fullTranslate),
				zap.Error(err),
			)
		}
	}()
}

func (s *Service) lockSessionKeys(sn string, audioKind int, tss []int64) func() {
	uniq := uniquePositiveTS(tss)
	mus := make([]*sync.Mutex, 0, len(uniq))
	for _, ts := range uniq {
		mus = append(mus, s.sessionLock(sessionKey(sn, ts, audioKind)))
	}
	for _, mu := range mus {
		mu.Lock()
	}
	return func() {
		for i := len(mus) - 1; i >= 0; i-- {
			mus[i].Unlock()
		}
	}
}

func uniquePositiveTS(in []int64) []int64 {
	seen := make(map[int64]struct{}, len(in))
	out := make([]int64, 0, len(in))
	for _, ts := range in {
		if ts <= 0 {
			continue
		}
		if _, ok := seen[ts]; ok {
			continue
		}
		seen[ts] = struct{}{}
		out = append(out, ts)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (s *Service) appendStitchSegments(
	ctx context.Context, sn string, audioKind int,
	frames [][]byte, absTSUs []int64, opus []byte,
	chunks, gapFilled int, petFromDB, codecName string,
	extra []int64,
) ([][]byte, []int64, []byte, int, int, string, string) {
	for _, ts := range extra {
		if ts <= 0 {
			continue
		}
		o2, f2, t2, g2, pet2, c2, n2, err := s.store.LoadSessionOpusDetailed(ctx, sn, ts, audioKind)
		if err != nil {
			s.log.Warn("拼接段加载失败，跳过该段",
				zap.String("sn", sn),
				zap.Int64("device_ts", ts),
				zap.Error(err),
			)
			continue
		}
		_ = o2
		gapUs := int64(0)
		if len(absTSUs) > 0 && len(t2) > 0 && absTSUs[len(absTSUs)-1] > 0 && t2[0] > absTSUs[len(absTSUs)-1] {
			gapUs = t2[0] - absTSUs[len(absTSUs)-1]
		}
		inWindow, measuredMs, realEst := StitchDecision(gapUs, s.stitchWindowMs, s.effectiveDeviceSegSilenceMs())
		s.log.Info("事件流拼接装入下一段",
			zap.String("sn", sn),
			zap.Int64("device_ts", ts),
			zap.Int("extra_chunks", n2),
			zap.Int("measured_gap_ms", measuredMs),
			zap.Int("real_silence_est_ms", realEst),
			zap.Bool("in_window", inWindow),
		)
		// 只拼真实包：段间 PLC 空包会把解码器带乱，OSS 听感变成「扑一声 + 长静音」
		frames, absTSUs = concatFrames(frames, absTSUs, f2, t2)
		chunks += n2
		gapFilled += g2
		if petFromDB == "" && pet2 != "" {
			petFromDB = pet2
		}
		if codecName == "" && c2 != "" {
			codecName = c2
		}
	}
	var buf []byte
	for _, f := range frames {
		buf = append(buf, f...)
	}
	return frames, absTSUs, buf, chunks, gapFilled, petFromDB, codecName
}

func concatFrames(a [][]byte, aTS []int64, b [][]byte, bTS []int64) ([][]byte, []int64) {
	if len(b) == 0 {
		return a, aTS
	}
	if len(a) == 0 {
		return b, bTS
	}
	out := make([][]byte, 0, len(a)+len(b))
	outTS := make([]int64, 0, len(a)+len(b))
	out = append(out, a...)
	outTS = append(outTS, aTS...)
	out = append(out, b...)
	outTS = append(outTS, bTS...)
	return out, outTS
}

func formatStitchHint(n int) string {
	return fmt.Sprintf("stitched_segs=%d", n)
}
