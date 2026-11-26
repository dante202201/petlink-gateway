package gateway

import (
	"context"
	"time"

	"go.uber.org/zap"

	"pet-translate-gateway/internal/model"
)

const fragCacheMax = 256

func (dc *DeviceConn) kindState(kind int) *audioKindConnState {
	if kind != model.AudioKindFile {
		kind = model.AudioKindEvent
	}
	return &dc.kindAudio[kind]
}

// rememberFragment 缓存本连接成功处理的音频元数据，供精简补发回填（按 kind 分桶）。
func (dc *DeviceConn) rememberFragment(meta *model.DeviceUplink, absTS int64, kind int) {
	if dc == nil || meta == nil {
		return
	}
	dc.mu.Lock()
	defer dc.mu.Unlock()
	st := dc.kindState(kind)
	if st.fragCache == nil {
		st.fragCache = make(map[int]fragCacheEntry, 32)
	}
	if meta.Seq == 0 && !meta.IsRetransmit() {
		// 仅首传 seq=0 开新段。补发对齐固件：retransmit 不写 _segmentBaseTs，
		// 否则旧段 seq=0 会覆盖当前 live 基点，后续相对 ts 全折错，seq 连续性也被清掉。
		st.fragCache = make(map[int]fragCacheEntry, 32)
		st.sawSeq = false
		st.lastSeq = 0
		if absTS > 0 {
			st.lastBaseTS = absTS
			st.baseTS = absTS
		}
	}
	if meta.Codec != "" {
		st.lastCodec = string(meta.Codec)
	}
	if meta.Total > 0 {
		st.lastTotal = meta.Total
	}
	if !meta.PID.IsUnbound() {
		st.lastPID = meta.PID.String()
	}
	st.fragCache[meta.Seq] = fragCacheEntry{
		Codec: string(meta.Codec),
		Total: meta.Total,
		TS:    absTS,
		PID:   meta.PID.String(),
	}
	if len(st.fragCache) > fragCacheMax {
		st.fragCache = map[int]fragCacheEntry{
			meta.Seq: st.fragCache[meta.Seq],
		}
	}
}

// fillFromSessionCache 用同连接、同 kind 缓存补全缺失字段。
func (dc *DeviceConn) fillFromSessionCache(meta *model.DeviceUplink, kind int) (filled bool) {
	if dc == nil || meta == nil {
		return false
	}
	dc.mu.Lock()
	defer dc.mu.Unlock()
	st := dc.kindState(kind)
	if e, ok := st.fragCache[meta.Seq]; ok {
		if meta.Codec == "" && e.Codec != "" {
			meta.Codec = model.AudioCodec(model.NormalizeAudioCodec(e.Codec))
			filled = true
		}
		if meta.Total == 0 && e.Total > 0 {
			meta.Total = e.Total
			filled = true
		}
		if meta.TS == 0 && e.TS > 0 {
			meta.TS = e.TS
			filled = true
		}
		if meta.PID.IsUnbound() && e.PID != "" {
			meta.PID = model.PetID(e.PID)
			filled = true
		}
	}
	if meta.Codec == "" && st.lastCodec != "" {
		meta.Codec = model.AudioCodec(model.NormalizeAudioCodec(st.lastCodec))
		filled = true
	}
	if meta.Total == 0 && st.lastTotal > 0 {
		meta.Total = st.lastTotal
		filled = true
	}
	if meta.PID.IsUnbound() && st.lastPID != "" {
		meta.PID = model.PetID(st.lastPID)
		filled = true
	}
	if meta.TS == 0 && st.lastBaseTS > 0 {
		meta.TS = st.lastBaseTS + int64(meta.Seq)*model.FrameDurationUS(kind)
		filled = true
	}
	return filled
}

// enrichRetransmitMeta 精简补发补全 codec/total/ts/pid。
func (m *ConnManager) enrichRetransmitMeta(dc *DeviceConn, meta *model.DeviceUplink) {
	if meta == nil || !meta.IsRetransmit() {
		return
	}
	kind := model.ResolveAudioStreamKind(meta.Kind, meta.DB)
	before := *meta
	filled := dc.fillFromSessionCache(meta, kind)

	if meta.Codec == "" || meta.Total == 0 || meta.TS == 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if m.store != nil {
			if row, err := m.store.LookupAudioMetaByKind(ctx, dc.SN, meta.Seq, meta.CRC, kind); err == nil && row != nil {
				if meta.Codec == "" && row.Codec != "" {
					meta.Codec = model.AudioCodec(model.NormalizeAudioCodec(row.Codec))
					filled = true
				}
				if meta.Total == 0 && row.Total > 0 {
					meta.Total = row.Total
					filled = true
				}
				if meta.TS == 0 && row.TS > 0 {
					meta.TS = row.TS
					filled = true
				}
				if meta.PID.IsUnbound() && row.PID != "" {
					meta.PID = model.PetID(row.PID)
					filled = true
				}
			}
		}
	}

	if filled {
		m.log.Info("补发元数据已补全",
			zap.String("sn", dc.SN),
			zap.Int("kind", kind),
			zap.Int("seq", meta.Seq),
			zap.String("codec_before", string(before.Codec)),
			zap.String("codec_after", string(meta.Codec)),
			zap.Int("total_before", before.Total),
			zap.Int("total_after", meta.Total),
			zap.Int64("ts_before", before.TS),
			zap.Int64("ts_after", meta.TS),
		)
	}
}
