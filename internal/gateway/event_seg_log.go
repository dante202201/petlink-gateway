package gateway

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"pet-translate-gateway/internal/model"
	redisx "pet-translate-gateway/internal/redis"
)

const (
	eventSegBeginMark = "=======事件音频开始======="
	eventSegEndMark   = "=======事件音频结束======="
)

func (st *audioKindConnState) resetSegDiag() {
	st.segRecvOK = 0
	st.segDropCRC = 0
	st.segDropDedup = 0
	st.segDropAccept = 0
	st.segDropOther = 0
	st.segGapWarn = 0
	st.segDeviceTotal = 0
	st.firstPassHole = false
	st.segSeqSeen = make(map[int]struct{}, 64)
	st.segStartedAt = time.Now()
}

func (st *audioKindConnState) noteSegRecv(seq, total int) {
	if st.segSeqSeen == nil {
		st.segSeqSeen = make(map[int]struct{}, 64)
	}
	st.segSeqSeen[seq] = struct{}{}
	st.segRecvOK++
	// 取更大的 total，避免滚动中间值覆盖 end 最终 total（旧 80/44 假缺失）
	if total > st.segDeviceTotal {
		st.segDeviceTotal = total
	}
}

func (st *audioKindConnState) missingSeqs() []int {
	total := st.segDeviceTotal
	if total <= 0 || st.segSeqSeen == nil {
		return nil
	}
	var miss []int
	for i := 0; i < total; i++ {
		if _, ok := st.segSeqSeen[i]; !ok {
			miss = append(miss, i)
		}
	}
	return miss
}

func formatSeqList(seqs []int, maxShow int) string {
	if len(seqs) == 0 {
		return ""
	}
	sort.Ints(seqs)
	if maxShow <= 0 || len(seqs) <= maxShow {
		parts := make([]string, len(seqs))
		for i, s := range seqs {
			parts[i] = fmt.Sprintf("%d", s)
		}
		return strings.Join(parts, ",")
	}
	parts := make([]string, maxShow)
	for i := 0; i < maxShow; i++ {
		parts[i] = fmt.Sprintf("%d", seqs[i])
	}
	return fmt.Sprintf("%s,...(+%d)", strings.Join(parts, ","), len(seqs)-maxShow)
}

func (m *ConnManager) logEventSegBegin(sn string, reason string, meta *model.DeviceUplink, sessionBaseTS int64) {
	if meta == nil {
		return
	}
	m.log.Info(eventSegBeginMark,
		zap.String("sn", sn),
		zap.String("reason", reason),
		zap.Int("kind", model.AudioKindEvent),
		zap.Int("seq", meta.Seq),
		zap.Int("total", meta.Total),
		zap.Bool("end", meta.End.Bool()),
		zap.Bool("retransmit", meta.IsRetransmit()),
		zap.Int64("session_device_ts", sessionBaseTS),
		zap.Int64("ts", meta.TS),
		zap.Float64("db", meta.DB),
		zap.Uint32("crc", meta.CRC),
		zap.String("codec", string(meta.Codec)),
		zap.String("pid", meta.PID.String()),
	)
}

func (m *ConnManager) logEventSegEnd(sn string, meta *model.AudioMessage, sessionBaseTS int64, st *audioKindConnState, storeChunks, storeTotal int, storeReady, storeIncomplete bool) {
	miss := st.missingSeqs()
	dropGateway := st.segDropCRC + st.segDropDedup + st.segDropAccept + st.segDropOther
	deviceTotal := meta.Total
	if deviceTotal <= 0 {
		deviceTotal = st.segDeviceTotal
	}
	if storeTotal > 0 {
		deviceTotal = storeTotal
	}
	uniqueOK := len(st.segSeqSeen)
	missingN := 0
	if deviceTotal > 0 {
		missingN = deviceTotal - uniqueOK
		if missingN < 0 {
			missingN = 0
		}
	}
	dur := time.Duration(0)
	if !st.segStartedAt.IsZero() {
		dur = time.Since(st.segStartedAt)
	}
	pendingRT := missingN > 0 || storeIncomplete
	verdict := "完整"
	if pendingRT {
		// end 瞬间的缺帧是补发前中间快照，勿当最终缺失
		verdict = "待补发(中间快照)"
	}
	if dropGateway > 0 && uniqueOK >= deviceTotal && deviceTotal > 0 {
		verdict = "完整(过程中有网关过滤但最终帧数够)"
		pendingRT = false
	}

	m.log.Info(eventSegEndMark,
		zap.String("sn", sn),
		zap.Int("kind", model.AudioKindEvent),
		zap.Int64("session_device_ts", sessionBaseTS),
		zap.Int("device_total", deviceTotal),
		zap.Int("conn_unique_ok", uniqueOK),
		zap.Int("conn_recv_ok_hits", st.segRecvOK),
		zap.Int("store_chunks", storeChunks),
		zap.Int("missing_count", missingN),
		zap.String("missing_seqs", formatSeqList(miss, 40)),
		zap.Bool("pending_retransmit", pendingRT),
		zap.Bool("store_ready", storeReady),
		zap.Bool("store_incomplete", storeIncomplete),
		zap.Int("drop_crc", st.segDropCRC),
		zap.Int("drop_dedup", st.segDropDedup),
		zap.Int("drop_accept", st.segDropAccept),
		zap.Int("drop_other", st.segDropOther),
		zap.Int("drop_gateway_total", dropGateway),
		zap.Int("gap_warn", st.segGapWarn),
		zap.Int("end_seq", meta.Seq),
		zap.Bool("retransmit", meta.Retransmit),
		zap.Duration("seg_wall_dur", dur),
		zap.String("verdict", verdict),
		zap.String("hint", "pending_retransmit=true→补发前中间快照勿当最终缺失; 拼装后 remain>0 才是真缺; drop_crc→网关CRC滤; drop_dedup→重复帧"),
	)

	m.saveEventCycleToRedis(sn, meta, sessionBaseTS, st, storeChunks, deviceTotal, storeReady, storeIncomplete, miss, missingN, verdict, dur, pendingRT)
	if pendingRT || st.firstPassHole {
		// 等补发窗口后再用最终 total / 入库帧数判定是否真跳号
		m.scheduleRetransmitGapCheck(sn, sessionBaseTS, model.AudioKindEvent, deviceTotal, st.firstPassHole)
	}
}

// scheduleRetransmitGapCheck end 后等待补发；仍缺齐才记 gap_warn / 最终缺失。
func (m *ConnManager) scheduleRetransmitGapCheck(sn string, deviceTS int64, kind, expectTotal int, hadFirstPassHole bool) {
	if m == nil || sn == "" || deviceTS <= 0 {
		return
	}
	go func() {
		timer := time.NewTimer(retransmitGapWait)
		defer timer.Stop()
		<-timer.C
		if m.store == nil || m.redis == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		st, err := m.store.GetSessionChunkStats(ctx, sn, deviceTS, kind)
		if err != nil {
			return
		}
		finalTotal := expectTotal
		if st.Total > finalTotal {
			finalTotal = st.Total
		}
		if st.ExpectedFrames() > finalTotal {
			finalTotal = st.ExpectedFrames()
		}
		remain := 0
		if finalTotal > st.Chunks {
			remain = finalTotal - st.Chunks
		}
		if remain <= 0 && !st.Incomplete() {
			m.log.Info("补发窗口已过，段已齐，不记跳号告警",
				zap.String("sn", sn),
				zap.Int64("device_ts", deviceTS),
				zap.Int("chunks", st.Chunks),
				zap.Int("final_total", finalTotal),
				zap.Bool("had_first_pass_hole", hadFirstPassHole),
			)
			_ = m.redis.PatchAudioCycleRetransmitSettled(ctx, sn, deviceTS, kind, st.Chunks, finalTotal, 0, false)
			return
		}
		gapWarn := 0
		if hadFirstPassHole || remain > 0 {
			gapWarn = 1
		}
		m.log.Warn("补发窗口已过仍未齐，记最终缺失/跳号",
			zap.String("sn", sn),
			zap.Int64("device_ts", deviceTS),
			zap.Int("chunks", st.Chunks),
			zap.Int("final_total", finalTotal),
			zap.Int("remain", remain),
			zap.Bool("had_first_pass_hole", hadFirstPassHole),
		)
		_ = m.redis.PatchAudioCycleRetransmitSettled(ctx, sn, deviceTS, kind, st.Chunks, finalTotal, gapWarn, true)
	}()
}

func (m *ConnManager) saveEventCycleToRedis(
	sn string, meta *model.AudioMessage, sessionBaseTS int64, st *audioKindConnState,
	storeChunks, deviceTotal int, storeReady, storeIncomplete bool, miss []int, missingN int,
	verdict string, dur time.Duration, pendingRT bool,
) {
	if m.redis == nil || meta == nil {
		return
	}
	now := time.Now().Unix()
	started := now
	if !st.segStartedAt.IsZero() {
		started = st.segStartedAt.Unix()
	}
	problems := buildCycleProblems(deviceTotal, storeChunks, missingN, miss, st, pendingRT)
	events := []redisx.AudioCycleEvent{
		{At: started, Phase: "begin", Detail: "事件流开段"},
	}
	if st.segDropCRC > 0 {
		events = append(events, redisx.AudioCycleEvent{At: now, Phase: "filter", Reason: "crc_fail", Detail: fmt.Sprintf("drop_crc=%d", st.segDropCRC)})
	}
	if st.firstPassHole {
		events = append(events, redisx.AudioCycleEvent{At: now, Phase: "gap_pending", Reason: "first_pass_hole",
			Detail: "首传有跳号，补发窗口内不告警"})
	}
	if !pendingRT && st.segGapWarn > 0 {
		events = append(events, redisx.AudioCycleEvent{At: now, Phase: "gap", Reason: "seq_gap", Detail: fmt.Sprintf("gap_warn=%d", st.segGapWarn)})
	}
	endDetail := fmt.Sprintf("total=%d store=%d missing=%d pending_retransmit=%v verdict=%s wall=%s（wall≠音频时长；看后续 timing 的 toc/span）",
		deviceTotal, storeChunks, missingN, pendingRT, verdict, dur)
	events = append(events, redisx.AudioCycleEvent{
		At: now, Phase: map[bool]string{true: "end_pending_rt", false: "end"}[pendingRT],
		Seq: meta.Seq, Detail: endDetail,
	})
	wallMs := int(dur.Milliseconds())
	cycle := &redisx.AudioSessionCycle{
		SN: sn, DeviceTS: sessionBaseTS, AudioKind: model.AudioKindEvent, KindLabel: "event",
		StartedAt: started, EndedAt: now, WallMs: wallMs,
		DeviceTotal: deviceTotal, ConnUniqueOK: len(st.segSeqSeen), ConnRecvHits: st.segRecvOK,
		StoreChunks: storeChunks, MissingCount: missingN, MissingSeqs: miss,
		DropCRC: st.segDropCRC, DropDedup: st.segDropDedup, DropAccept: st.segDropAccept, DropOther: st.segDropOther,
		GapWarn: st.segGapWarn, StoreReady: storeReady, StoreIncomplete: storeIncomplete,
		PendingRetransmit: pendingRT, MissingFinal: false,
		Verdict: verdict, Problems: problems, Events: events,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.redis.SaveAudioCycle(ctx, cycle); err != nil {
		m.log.Warn("写入音频诊断到 Redis 失败", zap.String("sn", sn), zap.Int64("device_ts", sessionBaseTS), zap.Error(err))
	}
}

func buildCycleProblems(deviceTotal, storeChunks, missingN int, miss []int, st *audioKindConnState, pendingRT bool) []string {
	var problems []string
	// 待补发中间快照：缺帧只记说明，不记为最终问题
	if pendingRT {
		if missingN > 0 || (deviceTotal > 0 && storeChunks < deviceTotal) {
			problems = append(problems, fmt.Sprintf("待补发(中间快照): device_total=%d store=%d missing=%d seqs=[%s]",
				deviceTotal, storeChunks, missingN, formatSeqList(miss, 20)))
		}
	} else {
		if deviceTotal > 0 && storeChunks < deviceTotal {
			problems = append(problems, fmt.Sprintf("最终缺失: device_total=%d store_chunks=%d missing=%d", deviceTotal, storeChunks, deviceTotal-storeChunks))
		}
		if missingN > 0 {
			problems = append(problems, fmt.Sprintf("连接侧缺 seq: count=%d seqs=[%s]", missingN, formatSeqList(miss, 20)))
		}
	}
	if st.segDropCRC >= 3 {
		problems = append(problems, fmt.Sprintf("大量CRC舍弃: drop_crc=%d（坏包未拼进文件）", st.segDropCRC))
	} else if st.segDropCRC > 0 {
		problems = append(problems, fmt.Sprintf("CRC舍弃: drop_crc=%d", st.segDropCRC))
	}
	if !pendingRT && st.segGapWarn > 0 {
		problems = append(problems, fmt.Sprintf("补发后仍跳号/缺失: gap_warn=%d", st.segGapWarn))
	}
	if st.segDropDedup >= 5 {
		problems = append(problems, fmt.Sprintf("大量去重跳过: drop_dedup=%d", st.segDropDedup))
	}
	if st.segDropAccept > 0 {
		problems = append(problems, fmt.Sprintf("通路未启用过滤: drop_accept=%d", st.segDropAccept))
	}
	if len(problems) == 0 {
		return nil
	}
	return problems
}

// snapshotEventSegDiag 在持锁下拷贝事件流段诊断，避免打日志时并发改 map。
func (st *audioKindConnState) snapshotEventSegDiag() audioKindConnState {
	out := audioKindConnState{
		segRecvOK:      st.segRecvOK,
		segDropCRC:     st.segDropCRC,
		segDropDedup:   st.segDropDedup,
		segDropAccept:  st.segDropAccept,
		segDropOther:   st.segDropOther,
		segGapWarn:     st.segGapWarn,
		segDeviceTotal: st.segDeviceTotal,
		segStartedAt:   st.segStartedAt,
	}
	if len(st.segSeqSeen) > 0 {
		out.segSeqSeen = make(map[int]struct{}, len(st.segSeqSeen))
		for k := range st.segSeqSeen {
			out.segSeqSeen[k] = struct{}{}
		}
	}
	return out
}
