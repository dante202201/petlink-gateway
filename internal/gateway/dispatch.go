package gateway

import (
	"context"
	"strings"
	"time"

	"go.uber.org/zap"

	"pet-translate-gateway/internal/model"
	"pet-translate-gateway/internal/pipeline"
	"pet-translate-gateway/internal/util"
)

// dispatchAudio 组装音频消息投递 Kafka / 入库。
// 关键：段级 sessionBaseTS 取自对应 kind 的段首绝对微秒；kind=0/1 分桶，补发并回原段。
func (m *ConnManager) dispatchAudio(dc *DeviceConn, meta *model.DeviceUplink, opus []byte, absTS, offset int64, kind int) {
	if meta.IsUnboundPID() {
		m.log.Info("设备音频 pid 空/占位，入库后按 a_emb_sn 补 user_id/emb_id/pid",
			zap.String("sn", dc.SN),
			zap.String("pid", meta.PID.String()),
			zap.Int("seq", meta.Seq),
			zap.Int("kind", kind),
		)
	}

	sessionBaseTS := m.pickAudioSessionBase(dc, meta, absTS, kind)
	if sessionBaseTS <= 0 {
		sessionBaseTS = absTS
	}

	msg := buildAudioMsg(dc.SN, meta, opus, absTS, offset, kind)
	if msg.Codec == "" {
		m.log.Warn("codec 为空，拒绝转发", zap.String("sn", dc.SN), zap.Int("kind", kind))
		return
	}

	kctx, kcancel := context.WithTimeout(context.Background(), m.cfg.Kafka.Timeout)
	defer kcancel()
	kafkaOK := true
	if err := m.kafka.SendAudio(kctx, msg); err != nil {
		kafkaOK = false
		m.log.Error("音频消息投递 Kafka 失败", zap.String("sn", dc.SN), zap.Int("seq", msg.Seq), zap.Int("kind", kind), zap.Error(err))
	}

	var storeID int64
	pipelineTriggered := false
	fullTranslate := false
	if m.store != nil {
		sctx, scancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer scancel()
		id, err := m.store.SaveAudio(sctx, msg, sessionBaseTS)
		if err != nil {
			m.log.Error("音频入库失败", zap.String("sn", dc.SN), zap.Int("seq", msg.Seq), zap.Int("kind", kind), zap.Error(err))
			return
		}
		storeID = id
		// end=true：本段完整（含补发并回原段后的收尾）。按配置决定翻译 / 仅归档。
		if msg.End {
			storeChunks, storeTotal := 0, msg.Total
			storeReady, storeIncomplete := false, false
			if st, serr := m.store.GetSessionChunkStats(sctx, dc.SN, sessionBaseTS, kind); serr == nil {
				storeChunks, storeTotal = st.Chunks, st.Total
				if storeTotal <= 0 {
					storeTotal = msg.Total
				}
				storeReady, storeIncomplete = st.Ready(), st.Incomplete()
				m.log.Info("音频段 end=true：total 与入库帧数",
					zap.String("sn", dc.SN),
					zap.Int("kind", kind),
					zap.Int("device_total", storeTotal),
					zap.Int("chunks", storeChunks),
					zap.Bool("ready", storeReady),
					zap.Bool("incomplete", storeIncomplete),
				)
			}
			if kind == model.AudioKindEvent {
				dc.mu.Lock()
				segSt := dc.kindState(kind).snapshotEventSegDiag()
				dc.mu.Unlock()
				m.logEventSegEnd(dc.SN, msg, sessionBaseTS, &segSt, storeChunks, storeTotal, storeReady, storeIncomplete)
			}
			wantTranslate := m.cfg != nil && m.cfg.Audio.TranslatesKind(kind)
			wantArchive := m.cfg != nil && m.cfg.Audio.ArchivesKind(kind)
			var skipReasons []string
			if !wantTranslate && !wantArchive {
				skipReasons = append(skipReasons, "audio.accept/translate 未启用该通路的会话处理")
			} else if m.pipeline == nil {
				skipReasons = append(skipReasons, "pipeline=nil")
			} else if !m.pipeline.Enabled() {
				skipReasons = append(skipReasons, "pipeline.enabled=false")
			} else if !m.pipeline.UploadOriginEnabled() {
				skipReasons = append(skipReasons, "pipeline.upload_oss_origin=false")
			}
			if len(skipReasons) == 0 {
				pipelineTriggered = true
				fullTranslate = wantTranslate && m.pipeline.FullTranslateEnabled()
				if kind == model.AudioKindEvent && m.redis != nil {
					_ = m.redis.PatchAudioCycleFlags(context.Background(), dc.SN, sessionBaseTS, kind, true, fullTranslate)
				}
				petHint := strings.TrimSpace(meta.Pet)
				isPlaceholderPetStr := func(p string) bool {
					switch strings.ToLower(p) {
					case "", "0", "1", "4294967295", "ffffffff", "-1":
						return true
					}
					return false
				}
				if isPlaceholderPetStr(petHint) {
					petHint = strings.TrimSpace(meta.BreedCode)
				}
				petFinal := petHint
				if isPlaceholderPetStr(petFinal) {
					petFinal = ""
				}
				m.log.Info("音频段结束 end=true，触发会话处理",
					zap.String("sn", dc.SN),
					zap.Int("kind", kind),
					zap.String("kind_label", model.AudioStreamKindLabel(kind)),
					zap.Int("seq", msg.Seq),
					zap.Int("total", msg.Total),
					zap.Int64("session_device_ts", sessionBaseTS),
					zap.Int64("last_chunk_abs_ts", msg.TS),
					zap.Bool("retransmit_raw", meta.Retransmit.Bool()),
					zap.String("pet_final", petFinal),
					zap.Bool("full_translate", fullTranslate),
					zap.Bool("archive_only", wantArchive && !fullTranslate),
				)
				if kind == model.AudioKindEvent {
					// 原帧 zip：与 stitch/可播转码并行，不 PLC、不多段拼接
					m.pipeline.UploadEventRawPackAsync(dc.SN, sessionBaseTS)
					// 设备时间轴：段首绝对 ts + seq×60ms；不用到达墙钟，避免欠采集段被误拆
					firstTS := sessionBaseTS
					lastTS := sessionBaseTS + int64(msg.Seq)*model.FrameDurationUS(kind)
					if firstTS <= 0 && msg.TS > 0 {
						firstTS = msg.TS
						lastTS = msg.TS
					}
					m.pipeline.ScheduleEventStitch(dc.SN, pipeline.EventStitchSeg{
						DeviceTS:      sessionBaseTS,
						FirstTS:       firstTS,
						LastTS:        lastTS,
						PetHint:       petFinal,
						PID:           msg.PID,
						FullTranslate: fullTranslate,
						DeviceSilence: meta.Silence,
						DeviceSegGap:  meta.SegGap,
					})
				} else {
					m.pipeline.ProcessSessionAsync(dc.SN, sessionBaseTS, petFinal, msg.PID, kind, fullTranslate)
				}
			} else {
				m.log.Info("音频段结束 end=true，跳过会话处理",
					zap.String("sn", dc.SN),
					zap.Int("kind", kind),
					zap.String("kind_label", model.AudioStreamKindLabel(kind)),
					zap.Int("seq", msg.Seq),
					zap.Int64("session_device_ts", sessionBaseTS),
					zap.Strings("skip_reasons", skipReasons),
				)
			}
		} else if msg.Retransmit && m.pipeline != nil && m.pipeline.Enabled() && m.pipeline.UploadOriginEnabled() {
			// 迟到补发：段若已 end，防抖后重拼覆盖 OSS（首跑未落库则跳过，由首跑吃新帧）
			if st, serr := m.store.GetSessionChunkStats(sctx, dc.SN, sessionBaseTS, kind); serr == nil && st.HasEnd {
				petHint := strings.TrimSpace(meta.Pet)
				m.pipeline.ScheduleOriginRebuild(dc.SN, sessionBaseTS, petHint, msg.PID, kind)
				if kind == model.AudioKindEvent {
					m.pipeline.UploadEventRawPackAsync(dc.SN, sessionBaseTS)
				}
				m.log.Info("迟到补发：已调度原音重拼",
					zap.String("sn", dc.SN),
					zap.Int("kind", kind),
					zap.Int("seq", msg.Seq),
					zap.Int64("session_device_ts", sessionBaseTS),
					zap.Int("chunks", st.Chunks),
					zap.Int("total", st.Total),
				)
			}
		}
	}

	m.log.Info("音频分片已处理",
		zap.String("sn", dc.SN),
		zap.String("type", msg.Type),
		zap.String("codec", msg.Codec),
		zap.Int("kind", kind),
		zap.String("kind_label", model.AudioStreamKindLabel(kind)),
		zap.Int("seq", msg.Seq),
		zap.Int("total", msg.Total),
		zap.Bool("end", msg.End),
		zap.Bool("retransmit", msg.Retransmit),
		zap.String("pid", msg.PID),
		zap.Int64("ts", msg.TS),
		zap.Int("opus_len", len(msg.OpusData)),
		zap.Int("bat", msg.Bat),
		zap.Bool("charge", msg.Charge),
		zap.Int("rssi", msg.RSSI),
		zap.Float64("db", msg.DB),
		zap.Int("imu", len(msg.IMU)),
		zap.String("motion", msg.Motion),
		zap.Int64("store_id", storeID),
		zap.Bool("kafka_ok", kafkaOK),
		zap.Bool("pipeline", pipelineTriggered),
		zap.Bool("full_translate", fullTranslate),
	)
}

// pickAudioSessionBase 入库段锚。对齐固件：补发不写 live _segmentBaseTs。
// 旧段补发并回原段；仅「新段误标 retransmit 且 ts 明显晚于当前基点」才开新 live 基点。
func (m *ConnManager) pickAudioSessionBase(dc *DeviceConn, meta *model.DeviceUplink, absTS int64, kind int) int64 {
	dc.mu.Lock()
	liveBase := dc.kindState(kind).baseTS
	dc.mu.Unlock()

	if meta.IsRetransmit() {
		// 旧固件：新录音段首忘记清 retransmit。必须先于 live 窗判断：
		// 新段 absTS 仍可能落在上一档 2 分钟窗内。
		if (meta.Seq == 0 || meta.Seq == 1) && liveBase > 0 && absTS-liveBase > 500_000 {
			meta.Retransmit = util.Bool01(false)
			dc.mu.Lock()
			dc.kindState(kind).baseTS = absTS
			dc.mu.Unlock()
			m.log.Warn("固件补发标记未清零修复：新录音段首强制 Retransmit=false",
				zap.String("sn", dc.SN),
				zap.Int("kind", kind),
				zap.Int("seq", meta.Seq),
				zap.Int64("abs_ts", absTS),
				zap.Int64("session_base_ts", absTS),
				zap.Int64("diff_us", absTS-liveBase),
			)
			return absTS
		}
		if absInLiveSession(liveBase, absTS) {
			return liveBase
		}
		if m.store != nil {
			rctx, rcancel := context.WithTimeout(context.Background(), 2*time.Second)
			resolved, rerr := m.store.ResolveSessionDeviceTS(rctx, dc.SN, kind, meta.Seq, absTS)
			rcancel()
			if rerr != nil {
				m.log.Warn("补发并回原段：查询 session device_ts 失败",
					zap.String("sn", dc.SN), zap.Int("kind", kind), zap.Int("seq", meta.Seq), zap.Error(rerr))
			} else if resolved > 0 {
				m.log.Info("补发并回原段：已恢复 session device_ts",
					zap.String("sn", dc.SN),
					zap.Int("kind", kind),
					zap.Int("seq", meta.Seq),
					zap.Int64("session_device_ts", resolved),
					zap.Int64("abs_ts", absTS),
					zap.Bool("wrote_live_base", false),
				)
				return resolved
			}
		}
		if absTS > 0 {
			return absTS
		}
		return liveBase
	}

	if liveBase <= 0 && m.store != nil && meta.Seq > 0 {
		rctx, rcancel := context.WithTimeout(context.Background(), 2*time.Second)
		resolved, rerr := m.store.ResolveSessionDeviceTS(rctx, dc.SN, kind, meta.Seq, absTS)
		rcancel()
		if rerr != nil {
			m.log.Warn("补发并回原段：查询 session device_ts 失败",
				zap.String("sn", dc.SN), zap.Int("kind", kind), zap.Int("seq", meta.Seq), zap.Error(rerr))
		} else if resolved > 0 {
			dc.mu.Lock()
			if dc.kindState(kind).baseTS <= 0 {
				dc.kindState(kind).baseTS = resolved
			}
			liveBase = dc.kindState(kind).baseTS
			dc.mu.Unlock()
			m.log.Info("补发并回原段：已恢复 session device_ts",
				zap.String("sn", dc.SN),
				zap.Int("kind", kind),
				zap.Int("seq", meta.Seq),
				zap.Int64("session_device_ts", liveBase),
				zap.Int64("abs_ts", absTS),
			)
		}
	}
	if liveBase > 0 {
		return liveBase
	}
	return absTS
}

func buildAudioMsg(sn string, meta *model.DeviceUplink, opus []byte, absTS, offset int64, kind int) *model.AudioMessage {
	return &model.AudioMessage{
		SN: sn, Token: meta.Token, Type: meta.Type, ProtoVer: meta.ProtoVer,
		Codec: meta.Codec.String(), Seq: meta.Seq, Total: meta.Total, End: meta.End.Bool(),
		Retransmit: meta.IsRetransmit(), Kind: kind, PID: meta.PID.String(),
		Pet:   meta.Pet,
		Breed: meta.BreedCode,
		DB: meta.DB, Bat: meta.Bat, Charge: meta.Charge.Bool(), RSSI: meta.RSSI, Ch: meta.Ch,
		FW: meta.FW, Bound: meta.Bound.Bool(), TS: absTS, TSOffset: offset, CRC: meta.CRC,
		OpusData: opus, ReceivedAt: util.NowUTCUnixMilli(), IMU: meta.IMU,
		Motion:  model.NormalizeMotion(meta.Motion),
		LocType: meta.LocType.String(), LocLat: meta.LocLat, LocLng: meta.LocLng, LocAcc: meta.LocAcc,
		LocSpeed: meta.LocSpeed, LocMacs: meta.LocMacs, LocTS: meta.LocTS,
	}
}

// dispatchLocation 定位透传 Kafka。
func (m *ConnManager) dispatchLocation(sn string, meta *model.DeviceUplink) {
	msg := &model.LocationMessage{
		SN: sn, Type: meta.Type, LocType: meta.LocType.String(), LocLat: meta.LocLat, LocLng: meta.LocLng,
		LocAcc: meta.LocAcc, LocSpeed: meta.LocSpeed, LocMacs: meta.LocMacs, LocTS: meta.LocTS,
		Bat: meta.Bat, Charge: meta.Charge.Bool(), RSSI: meta.RSSI, TS: meta.TS, ReceivedAt: util.NowUTCUnixMilli(),
	}
	if msg.Type == "" || msg.Type == "loc" || msg.Type == "geolocate" {
		msg.Type = "locate"
	}
	if msg.TS == 0 {
		msg.TS = meta.LocTS
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.cfg.Kafka.Timeout)
	defer cancel()
	if err := m.kafka.SendLocation(ctx, msg); err != nil {
		m.log.Error("定位消息投递 Kafka 失败", zap.String("sn", sn), zap.Error(err))
		return
	}
	m.log.Info("定位上报已转发",
		zap.String("sn", sn), zap.String("loc_type", msg.LocType),
		zap.Float64("lat", msg.LocLat), zap.Float64("lng", msg.LocLng), zap.Int("macs", len(msg.LocMacs)),
	)
}
