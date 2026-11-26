package gateway

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"pet-translate-gateway/internal/model"
	redisx "pet-translate-gateway/internal/redis"
	"pet-translate-gateway/internal/util"
)

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// rateLimiter 单设备滑动窗口限流。
type rateLimiter struct {
	mu         sync.Mutex
	window     time.Duration
	maxCount   int
	timestamps []time.Time
}

func newRateLimiter(maxQPS int) *rateLimiter {
	return &rateLimiter{
		window:     time.Second,
		maxCount:   maxQPS,
		timestamps: make([]time.Time, 0, maxQPS+1),
	}
}

// Allow 判断是否允许通过。
func (r *rateLimiter) Allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-r.window)
	i := 0
	for ; i < len(r.timestamps); i++ {
		if r.timestamps[i].After(cutoff) {
			break
		}
	}
	r.timestamps = r.timestamps[i:]
	if len(r.timestamps) >= r.maxCount {
		return false
	}
	r.timestamps = append(r.timestamps, now)
	return true
}

// fragmentDeduper 音频分片去重（实时与补发共用指纹）。
type fragmentDeduper struct {
	mu    sync.Mutex
	size  int
	seen  map[string]struct{}
	order []string
}

func newFragmentDeduper(size int) *fragmentDeduper {
	if size <= 0 {
		size = 128
	}
	return &fragmentDeduper{
		size:  size,
		seen:  make(map[string]struct{}, size),
		order: make([]string, 0, size),
	}
}

// SeenOrAdd 若已存在返回 true。
func (d *fragmentDeduper) SeenOrAdd(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.seen[key]; ok {
		return true
	}
	d.seen[key] = struct{}{}
	d.order = append(d.order, key)
	if len(d.order) > d.size {
		old := d.order[0]
		d.order = d.order[1:]
		delete(d.seen, old)
	}
	return false
}

// readPump 读协程：固件双帧协议（JSON 文本 + OPUS 二进制）、限流、去重、转发。
func (m *ConnManager) readPump(dc *DeviceConn) {
	defer m.onDisconnect(dc)

	limiter := newRateLimiter(m.cfg.WebSocket.MaxQPSPerDevice)
	deduper := newFragmentDeduper(m.cfg.WebSocket.DedupWindowSize)

	_ = dc.Conn.SetReadDeadline(time.Now().Add(m.cfg.WebSocket.ReadTimeout))
	dc.Conn.SetPongHandler(func(appData string) error {
		m.touchHeartbeat(dc)
		m.log.Info("收到设备帧",
			zap.String("sn", dc.SN),
			zap.String("frame", "pong"),
			zap.Int("payload_len", len(appData)),
			zap.String("payload", clipRawForLog([]byte(appData), 128)),
			zap.String("payload_hex", hexPreview([]byte(appData), 32)),
		)
		return nil
	})
	dc.Conn.SetPingHandler(func(appData string) error {
		m.touchHeartbeat(dc)
		m.log.Info("收到设备帧",
			zap.String("sn", dc.SN),
			zap.String("frame", "ping"),
			zap.Int("payload_len", len(appData)),
			zap.String("payload", clipRawForLog([]byte(appData), 128)),
			zap.String("payload_hex", hexPreview([]byte(appData), 32)),
		)
		dc.writeMu.Lock()
		err := dc.Conn.WriteControl(
			websocket.PongMessage,
			[]byte(appData),
			time.Now().Add(m.cfg.WebSocket.WriteTimeout),
		)
		dc.writeMu.Unlock()
		if err != nil {
			m.log.Warn("回复设备 Ping 的 Pong 失败", zap.String("sn", dc.SN), zap.Error(err))
			return err
		}
		m.log.Info("已回 WebSocket Pong", zap.String("sn", dc.SN), zap.Int("payload_len", len(appData)))
		return nil
	})

	for {
		msgType, data, err := dc.Conn.ReadMessage()
		if err != nil {
			reason := logCloseReason(err)
			if reason == "unexpected" {
				m.log.Warn("连接异常断开", zap.String("sn", dc.SN), zap.String("reason", reason), zap.Error(err))
			} else {
				m.log.Info("连接断开", zap.String("sn", dc.SN), zap.String("reason", reason), zap.Error(err))
			}
			return
		}

		m.logDeviceIngress(dc.SN, msgType, data)

		// 音频双帧协议：Text 元数据 + Binary Opus 算一对，Binary 不再占 QPS。
		countLimit := !(msgType == websocket.BinaryMessage && dc.hasPendingMeta())
		if countLimit && !limiter.Allow() {
			// 超限：告警仍放行，不断开连接（避免 3s 录音/突发补发中途被踢导致缺帧）
			m.log.Warn("设备消息超限，已放行本帧（不断开）",
				zap.String("sn", dc.SN),
				zap.Int("max_qps", m.cfg.WebSocket.MaxQPSPerDevice),
				zap.Int("opcode", msgType),
				zap.Int("bytes", len(data)),
			)
		}
		m.touchHeartbeat(dc)

		switch msgType {
		case websocket.TextMessage:
			m.handleTextFrame(dc, data)
		case websocket.BinaryMessage:
			m.handleBinaryFrame(dc, data, deduper)
		default:
			m.log.Warn("忽略未知帧类型", zap.String("sn", dc.SN), zap.Int("type", msgType))
		}
	}
}

// logDeviceIngress 打印设备发来的应用数据帧（Text/Binary）；控制帧 Ping/Pong 在 Handler 里打。
func (m *ConnManager) logDeviceIngress(sn string, msgType int, data []byte) {
	fields := []zap.Field{
		zap.String("sn", sn),
		zap.Int("opcode", msgType),
		zap.Int("bytes", len(data)),
	}
	switch msgType {
	case websocket.TextMessage:
		fields = append(fields,
			zap.String("frame", "text"),
			zap.String("payload", clipJSONForLog(data, maxUplinkLogJSON)),
		)
	case websocket.BinaryMessage:
		fields = append(fields,
			zap.String("frame", "binary"),
			zap.String("payload_hex", hexPreview(data, 64)),
		)
	default:
		fields = append(fields,
			zap.String("frame", fmt.Sprintf("other_%d", msgType)),
			zap.String("payload_hex", hexPreview(data, 64)),
		)
	}
	m.log.Info("收到设备帧", fields...)
}

func hexPreview(data []byte, maxBytes int) string {
	if len(data) == 0 {
		return ""
	}
	n := len(data)
	if n > maxBytes {
		n = maxBytes
	}
	s := hex.EncodeToString(data[:n])
	if len(data) > maxBytes {
		return s + "…"
	}
	return s
}

// touchHeartbeat 刷新读超时；Redis 在线 TTL 节流到 5s，避免每包音频都打一轮 Redis。
func (m *ConnManager) touchHeartbeat(dc *DeviceConn) {
	dc.MarkClientSeen()
	_ = dc.Conn.SetReadDeadline(time.Now().Add(m.cfg.WebSocket.ReadTimeout))
	now := time.Now()
	dc.mu.Lock()
	due := dc.lastRedisTouch.IsZero() || now.Sub(dc.lastRedisTouch) >= 5*time.Second
	if due {
		dc.lastRedisTouch = now
	}
	dc.mu.Unlock()
	if !due || m.redis == nil {
		return
	}
	_ = m.redis.RefreshOnline(context.Background(), dc.SN)
	_ = m.redis.RefreshConnMapping(context.Background(), dc.SN, dc.ConnID)
}

// handleTextFrame 处理固件 JSON 文本帧；也兼容 BLE STAT 明文（如 CHARGE:78）。
func (m *ConnManager) handleTextFrame(dc *DeviceConn, data []byte) {
	if len(data) == 0 || len(data) > 64*1024 {
		m.log.Warn("非法文本长度，丢弃", zap.String("sn", dc.SN), zap.Int("len", len(data)))
		return
	}

	// BLE STAT Notify 文本通道：CHARGE:<电量%>
	if bat, ok := model.ParseSTATCharge(string(data)); ok {
		m.log.Info("设备充电状态（STAT）",
			zap.String("sn", dc.SN),
			zap.String("stat", model.FormatSTATCharge(bat)),
			zap.Int("bat", bat),
			zap.Bool("charge", true),
		)
		patch := &redisx.DeviceStatusPatch{
			SetBattery:    true,
			Battery:       bat,
			SetCharge:     true,
			Charge:        true,
			SetLastSeenAt: true,
			LastSeenAt:    time.Now().UnixMilli(),
		}
		_ = m.redis.UpdateDeviceStatus(context.Background(), dc.SN, patch)
		return
	}

	// 应用层心跳（部分固件发 Text 而非 WebSocket Ping 控制帧）
	if m.handleAppHeartbeat(dc, data) {
		return
	}

	var meta model.DeviceUplink
	if err := json.Unmarshal(data, &meta); err != nil {
		m.log.Warn("JSON 解析失败，丢弃",
			zap.String("sn", dc.SN),
			zap.Error(err),
			zap.String("raw", clipRawForLog(data, 256)),
		)
		return
	}
	if meta.SN != "" && meta.SN != dc.SN {
		m.log.Warn("报文 SN 与连接不一致，丢弃",
			zap.String("conn_sn", dc.SN),
			zap.String("msg_sn", meta.SN),
		)
		return
	}
	meta.SN = dc.SN
	m.logUplinkJSON(dc.SN, data, meta.Type)

	switch meta.Type {
	case "ping":
		// 带状态应用层心跳：type=ping 同时携带 bat/charge/rssi/bound/pid/breed/fw/db，
		// 按昨天约定"双通道双通道（固件任选其一）"，等价刷新状态快照。
		m.refreshDeviceStatusFromMeta(dc.SN, &meta, "", "", true, true)
		m.log.Info("收到带状态应用层心跳，已刷新状态快照",
			zap.String("sn", dc.SN),
			zap.Int("bat", meta.Bat),
			zap.Bool("charge", meta.Charge.Bool()),
			zap.Int("rssi", meta.RSSI),
			zap.Int("bound", boolToInt(meta.Bound.Bool())),
			zap.String("pid", meta.PID.String()),
			zap.String("breed", meta.BreedCode),
			zap.String("pet", meta.Pet),
			zap.String("fw", meta.FW),
			zap.Float64("db", meta.DB),
			zap.String("rec_mode", meta.RecMode.String()),
			zap.Bool("rec_continuous", model.RecModeIsContinuous(meta.RecMode.String())),
		)
		return
	case "tts_flow":
		action := strings.ToLower(strings.TrimSpace(meta.Action))
		ms := meta.MS
		if meta.TTSBufMs != nil {
			ms = *meta.TTSBufMs
		}
		m.log.Info("设备 TTS 流控上报",
			zap.String("sn", dc.SN),
			zap.String("action", action),
			zap.Int("ms", ms),
		)
		m.handleTTSFlow(dc.SN, action, ms)
		return
	case "status":
		// 统一状态上报（设备主动事件帧）：按昨天文档 §2.1 双通道的第二通道。
		// audio_start / audio_end：事件流语义边界（与 kind=0 的 seq=0~end=1 成对）；不单独切文件，只打点。
		m.touchHeartbeat(dc)
		hasLoc := meta.HasLocation()
		ev := strings.ToLower(strings.TrimSpace(meta.Event))
		fields := []zap.Field{
			zap.String("sn", dc.SN),
			zap.Int("bat", meta.Bat),
			zap.Bool("charge", meta.Charge.Bool()),
			zap.Int("rssi", meta.RSSI),
			zap.Int("bound", boolToInt(meta.Bound.Bool())),
			zap.String("pid", meta.PID.String()),
			zap.String("breed", meta.BreedCode),
			zap.String("pet", meta.Pet),
			zap.String("fw", meta.FW),
			zap.Float64("db", meta.DB),
			zap.String("event", meta.Event),
			zap.String("state", meta.State),
			zap.Bool("has_location", hasLoc),
			zap.String("rec_mode", meta.RecMode.String()),
			zap.Bool("rec_continuous", model.RecModeIsContinuous(meta.RecMode.String())),
		}
		switch ev {
		case "audio_start":
			m.log.Info(eventSegBeginMark,
				append(fields,
					zap.String("reason", "status.audio_start"),
					zap.String("hint", "随后应出现 kind=0 且 seq=0 的音频帧"),
				)...,
			)
		case "audio_end":
			m.log.Info(eventSegEndMark,
				append(fields,
					zap.String("reason", "status.audio_end"),
					zap.String("hint", "拼装以音频 end=1 为准；本事件仅边界打点"),
				)...,
			)
		default:
			m.log.Info("设备统一状态上报（status）", fields...)
		}
		if hasLoc {
			meta.NormalizeLocation()
			m.dispatchLocation(dc.SN, &meta)
		}
		m.refreshDeviceStatusFromMeta(dc.SN, &meta, meta.Event, meta.State, true, true)
		return
	case "locate", "loc", "geolocate":
		meta.NormalizeLocation()
		m.dispatchLocation(dc.SN, &meta)
		return
	case "batch":
		dc.mu.Lock()
		dc.batchRemain = meta.Count
		dc.mu.Unlock()
		m.log.Info("进入批量上传模式", zap.String("sn", dc.SN), zap.Int("count", meta.Count))
		return
	case "ota_progress":
		m.onOTAProgress(dc.SN, &meta)
		return
	case "notify", "ack":
		fields := []zap.Field{
			zap.String("sn", dc.SN),
			zap.String("type", meta.Type),
			zap.String("event", meta.Event),
			zap.String("cmd", meta.Cmd),
			zap.Int("result", meta.Result),
			zap.String("state", meta.State),
		}
		if meta.Type == "ack" && meta.Result != 0 {
			// ota_update result=1 = 忙/仍在执行，属预期，不打 WARN
			if strings.EqualFold(strings.TrimSpace(meta.Cmd), string(model.CmdOTAUpdate)) && meta.Result == 1 {
				m.log.Info("设备事件/回执", fields...)
			} else {
				m.log.Warn("设备命令回执失败", fields...)
			}
		} else {
			m.log.Info("设备事件/回执", fields...)
		}
		if meta.Type == "ack" {
			if isAutoRecordAckCmd(meta.Cmd) {
				if isDeviceCmdAckSuccess(&meta) {
					m.applyAutoRecordOnAck(dc.SN, &meta)
				} else if m.redis != nil {
					// 失败 ACK：丢掉待确认期望值，避免后续误写入
					_, _, _ = m.redis.TakePendingAutoRecord(context.Background(), dc.SN)
				}
			}
			if strings.EqualFold(strings.TrimSpace(meta.Cmd), string(model.CmdOTAUpdate)) {
				m.onOTAAck(dc.SN, meta.Result)
			}
			// enter_calib_ble 成功：标记主动离线（calib_ble），与意外掉线区分
			if strings.EqualFold(strings.TrimSpace(meta.Cmd), string(model.CmdEnterCalibBLE)) &&
				isDeviceCmdAckSuccess(&meta) {
				m.markCalibBLEOffline(dc)
			}
		}
		m.refreshDeviceStatusFromMeta(dc.SN, &meta, meta.Event, meta.State, false, false)
		return
	}

	if meta.ExpectsBinary() {
		if meta.IsRetransmit() {
			m.enrichRetransmitMeta(dc, &meta)
		}
		if meta.Codec == "" {
			m.log.Warn("音频元数据缺少 codec，丢弃", zap.String("sn", dc.SN), zap.Int("seq", meta.Seq))
			return
		}
		if prev := dc.takePendingMeta(); prev != nil {
			m.log.Warn("分片丢包：上一条 JSON 未收到 Binary",
				zap.String("sn", dc.SN),
				zap.Int("seq", prev.Seq),
			)
		}
		// 段首绝对 ts：按 kind 分桶写入（对齐固件 _segmentBaseTs[2]）。
		// 补发不写 live 基点：重连后未收尾段由 Binary 路径 Resolve 恢复。
		kind := model.ResolveAudioStreamKind(meta.Kind, meta.DB)
		dc.mu.Lock()
		st := dc.kindState(kind)
		if meta.Seq == 0 && meta.TS > 0 && !meta.IsRetransmit() {
			st.baseTS = meta.TS
		}
		dc.mu.Unlock()
		cp := meta
		dc.setPendingMeta(&cp)
		m.log.Debug("音频元数据已收，等待 Binary",
			zap.String("sn", dc.SN),
			zap.String("type", meta.Type),
			zap.String("codec", string(meta.Codec)),
			zap.Int("kind", kind),
			zap.String("kind_label", model.AudioStreamKindLabel(kind)),
			zap.Int("seq", meta.Seq),
			zap.Int("total", meta.Total),
			zap.Bool("end", meta.End.Bool()),
			zap.Bool("retransmit", meta.IsRetransmit()),
			zap.String("pid", meta.PID.String()),
			zap.Int64("ts", meta.TS),
			zap.Int("bat", meta.Bat),
			zap.Bool("charge", meta.Charge.Bool()),
			zap.Int("rssi", meta.RSSI),
			zap.Float64("db", meta.DB),
			zap.Int("imu", len(meta.IMU)),
		)
		return
	}

	if meta.HasLocation() {
		meta.NormalizeLocation()
		m.dispatchLocation(dc.SN, &meta)
		m.refreshDeviceStatusFromMeta(dc.SN, &meta, "", "", true, true)
		return
	}

	m.log.Warn("未识别的上行 JSON，已忽略",
		zap.String("sn", dc.SN),
		zap.String("type", meta.Type),
		zap.String("json", clipJSONForLog(data, maxUplinkLogJSON)),
	)
}

// handleBinaryFrame 处理紧随 JSON 的音频二进制帧（Opus 或 MP3 裸帧）。
// 约定：本条 Binary = 一个完整音频包，单独发送，勿多包拼接。
func (m *ConnManager) handleBinaryFrame(dc *DeviceConn, data []byte, deduper *fragmentDeduper) {
	meta := dc.takePendingMeta()
	if meta == nil {
		m.log.Warn("收到孤立 Binary，无前置 JSON，丢弃",
			zap.String("sn", dc.SN),
			zap.Int("len", len(data)),
		)
		return
	}
	if len(data) == 0 || len(data) > 256*1024 {
		m.log.Warn("非法音频 Binary 长度，丢弃", zap.String("sn", dc.SN), zap.Int("len", len(data)))
		return
	}

	kind := model.ResolveAudioStreamKind(meta.Kind, meta.DB)
	isEvent := kind == model.AudioKindEvent

	if meta.CRC == 0 {
		m.log.Warn("音频帧缺少 crc（固件应每帧携带），仍放行",
			zap.String("sn", dc.SN),
			zap.Int("kind", kind),
			zap.Int("seq", meta.Seq),
			zap.Int("opus_len", len(data)),
		)
	} else {
		got := util.CRC32IEEE(data)
		if got != meta.CRC {
			if isEvent {
				dc.mu.Lock()
				dc.kindState(kind).segDropCRC++
				dc.mu.Unlock()
			}
			m.log.Warn("【事件流丢帧-网关滤】CRC 校验失败，不拼入文件",
				zap.String("sn", dc.SN),
				zap.Int("kind", kind),
				zap.Uint32("want", meta.CRC),
				zap.Uint32("got", got),
				zap.Int("seq", meta.Seq),
				zap.Int("total", meta.Total),
				zap.Int("opus_len", len(data)),
				zap.String("drop_reason", "crc_fail"),
			)
			dc.mu.Lock()
			if dc.batchRemain > 0 {
				dc.batchRemain--
			}
			dc.mu.Unlock()
			return
		}
	}

	if m.cfg != nil && !m.cfg.Audio.AcceptsKind(kind) {
		if isEvent {
			dc.mu.Lock()
			dc.kindState(kind).segDropAccept++
			dc.mu.Unlock()
		}
		m.log.Info("【事件流丢帧-网关滤】通路未启用，丢弃分片",
			zap.String("sn", dc.SN),
			zap.Int("kind", kind),
			zap.String("kind_label", model.AudioStreamKindLabel(kind)),
			zap.String("accept", m.cfg.Audio.Accept),
			zap.Int("seq", meta.Seq),
			zap.Int("total", meta.Total),
			zap.Bool("end", meta.End.Bool()),
			zap.Float64("db", meta.DB),
			zap.Int("opus_len", len(data)),
			zap.String("drop_reason", "accept_filter"),
		)
		dc.mu.Lock()
		if dc.batchRemain > 0 {
			dc.batchRemain--
		}
		dc.mu.Unlock()
		return
	}

	if !m.allowAudioByBinding(dc) {
		if isEvent {
			dc.mu.Lock()
			dc.kindState(kind).segDropAccept++
			dc.mu.Unlock()
		}
		m.log.Info("设备未绑定用户/宠物，丢弃录音上传",
			zap.String("sn", dc.SN),
			zap.Int("kind", kind),
			zap.String("kind_label", model.AudioStreamKindLabel(kind)),
			zap.Int("seq", meta.Seq),
			zap.Int("total", meta.Total),
			zap.Bool("end", meta.End.Bool()),
			zap.Int("opus_len", len(data)),
			zap.String("drop_reason", "unbound"),
		)
		dc.mu.Lock()
		if dc.batchRemain > 0 {
			dc.batchRemain--
		}
		dc.mu.Unlock()
		return
	}

	dc.mu.Lock()
	base := dc.kindState(kind).baseTS
	dc.mu.Unlock()
	// 重连后 live 相对 ts 需要先恢复未收尾段基点，否则 AbsoluteTS(base=0) 会把 60000 当成绝对时间。
	if base <= 0 && !meta.IsRetransmit() && meta.Seq > 0 && !model.IsAbsoluteUnixMicros(meta.TS) && m.store != nil {
		rctx, rcancel := context.WithTimeout(context.Background(), 2*time.Second)
		resolved, rerr := m.store.ResolveSessionDeviceTS(rctx, dc.SN, kind, meta.Seq, 0)
		rcancel()
		if rerr == nil && resolved > 0 {
			dc.mu.Lock()
			if dc.kindState(kind).baseTS <= 0 {
				dc.kindState(kind).baseTS = resolved
			}
			base = dc.kindState(kind).baseTS
			dc.mu.Unlock()
		}
	}
	absTS := meta.AbsoluteTS(base)
	offset := int64(0)
	if !meta.IsRetransmit() && meta.Seq > 0 {
		offset = meta.TS
	}

	// 事件流开段：seq=0 打分段开始标记并清零计数；同时通知拼接窗口（间隙已超窗则立刻落地上一轮）
	if isEvent && meta.Seq == 0 && !meta.IsRetransmit() {
		dc.mu.Lock()
		st := dc.kindState(kind)
		st.resetSegDiag()
		if meta.Total > 0 {
			st.segDeviceTotal = meta.Total
		}
		baseTS := st.baseTS
		if baseTS <= 0 {
			baseTS = absTS
		}
		dc.mu.Unlock()
		m.logEventSegBegin(dc.SN, "audio.seq=0", meta, baseTS)
		if m.pipeline != nil {
			firstTS := absTS
			if firstTS <= 0 {
				firstTS = baseTS
			}
			m.pipeline.NoteEventSegBegin(dc.SN, firstTS)
		}
	}

	// 去重：键 = sn+kind+seq+设备绝对 ts[+crc]。total 是累计值，禁止进键。
	dc.mu.Lock()
	liveBase := dc.kindState(kind).baseTS
	dc.mu.Unlock()
	dedupTS := canonicalDedupAbsTS(meta, liveBase, absTS, kind)
	fp := audioFragmentDedupKey(dc.SN, kind, meta.Seq, dedupTS, meta.CRC)
	if deduper.SeenOrAdd(fp) {
		if isEvent {
			dc.mu.Lock()
			dc.kindState(kind).segDropDedup++
			dc.mu.Unlock()
		}
		m.log.Info("【事件流丢帧-网关滤】重复分片跳过",
			zap.String("sn", dc.SN),
			zap.Int("kind", kind),
			zap.Int("seq", meta.Seq),
			zap.Int("total", meta.Total),
			zap.Bool("retransmit", meta.IsRetransmit()),
			zap.Int64("abs_ts", absTS),
			zap.Int64("dedup_ts", dedupTS),
			zap.String("dedup_key", fp),
			zap.String("drop_reason", "dedup"),
		)
		return
	}

	// 首传 seq 跳号：固件转缓存后补发属正常，先记 hole，等补发窗口再决定是否 gap_warn
	if !meta.IsRetransmit() {
		dc.mu.Lock()
		st := dc.kindState(kind)
		if meta.Seq == 0 {
			st.sawSeq = true
			st.lastSeq = 0
			st.firstPassHole = false
		} else if st.sawSeq && meta.Seq != st.lastSeq+1 {
			st.firstPassHole = true
			m.log.Info("首传 seq 不连续，等待补发窗口",
				zap.String("sn", dc.SN),
				zap.Int("kind", kind),
				zap.Int("prev_seq", st.lastSeq),
				zap.Int("seq", meta.Seq),
				zap.Int("total", meta.Total),
				zap.Int64("abs_ts", absTS),
				zap.Duration("retransmit_wait", retransmitGapWait),
				zap.String("hint", "固件内存紧时先缓存后补发，勿立刻当跳号告警"),
			)
			st.lastSeq = meta.Seq
		} else {
			st.sawSeq = true
			st.lastSeq = meta.Seq
		}
		dc.mu.Unlock()
	}

	if isEvent {
		dc.mu.Lock()
		dc.kindState(kind).noteSegRecv(meta.Seq, meta.Total)
		dc.mu.Unlock()
	}

	m.enqueueAudioPersist(dc, meta, data, absTS, offset, kind)
	dc.rememberFragment(meta, absTS, kind)

	if meta.HasLocation() {
		meta.NormalizeLocation()
		m.dispatchLocation(dc.SN, meta)
	}

	dc.mu.Lock()
	if dc.batchRemain > 0 {
		dc.batchRemain--
	}
	dc.mu.Unlock()
}

// enqueueAudioPersist 把已校验分片交给 persistPump；队列满时同步落库以免丢帧。
func (m *ConnManager) enqueueAudioPersist(dc *DeviceConn, meta *model.DeviceUplink, data []byte, absTS, offset int64, kind int) {
	opus := append([]byte(nil), data...)
	job := audioPersistJob{meta: meta, opus: opus, absTS: absTS, offset: offset, kind: kind}
	if dc.persistCh == nil {
		m.dispatchAudio(dc, meta, opus, absTS, offset, kind)
		return
	}
	select {
	case <-dc.CloseCh:
		return
	case dc.persistCh <- job:
		return
	default:
		m.log.Warn("音频持久化队列满，同步落库（可能拖住读循环）",
			zap.String("sn", dc.SN),
			zap.Int("kind", kind),
			zap.Int("seq", meta.Seq),
			zap.Int("queued", cap(dc.persistCh)),
		)
		m.dispatchAudio(dc, meta, opus, absTS, offset, kind)
	}
}

// persistPump 单连接串行入库，保序；与读循环解耦，避免 TCP 反压打回设备编码任务。
func (m *ConnManager) persistPump(dc *DeviceConn) {
	for {
		select {
		case <-dc.CloseCh:
			for {
				select {
				case job := <-dc.persistCh:
					m.dispatchAudio(dc, job.meta, job.opus, job.absTS, job.offset, job.kind)
				default:
					return
				}
			}
		case job := <-dc.persistCh:
			m.dispatchAudio(dc, job.meta, job.opus, job.absTS, job.offset, job.kind)
		}
	}
}

// applyAutoRecordOnAck：enable_auto_record / enable_translate 成功 ACK 后，把下发期望值写入状态；之后设备 ping/status 上报覆盖。
func (m *ConnManager) applyAutoRecordOnAck(sn string, meta *model.DeviceUplink) {
	if m.redis == nil || meta == nil || !isAutoRecordAckCmd(meta.Cmd) || !isDeviceCmdAckSuccess(meta) {
		return
	}
	enabled, ok, err := m.redis.TakePendingAutoRecord(context.Background(), sn)
	if err != nil {
		m.log.Warn("读取待确认 auto_record 失败", zap.String("sn", sn), zap.Error(err))
		return
	}
	if !ok {
		m.log.Info("auto_record ACK 成功但无待确认期望值，跳过状态写入",
			zap.String("sn", sn), zap.String("cmd", meta.Cmd))
		return
	}
	upd := &model.DeviceConfigUpdate{AutoRecord: &enabled, TranslateEnabled: &enabled}
	if err := m.redis.ApplyDeviceConfig(context.Background(), sn, upd, false); err != nil {
		m.log.Warn("ACK 后写入 auto_record 失败", zap.String("sn", sn), zap.Bool("auto_record", enabled), zap.Error(err))
		return
	}
	m.log.Info("ACK 确认 auto_record 已写入状态",
		zap.String("sn", sn),
		zap.String("cmd", meta.Cmd),
		zap.Bool("auto_record", enabled),
	)
}

func isAutoRecordAckCmd(cmd string) bool {
	switch model.CmdType(strings.TrimSpace(cmd)) {
	case model.CmdEnableAutoRecord, model.CmdEnableTranslate:
		return true
	default:
		return false
	}
}

// isDeviceCmdAckSuccess：result=0 为成功；兼容 event/state 为 success（失败 event 仍以 result≠0 为准）。
func isDeviceCmdAckSuccess(meta *model.DeviceUplink) bool {
	if meta == nil {
		return false
	}
	if meta.Result != 0 {
		return false
	}
	ev := strings.ToLower(strings.TrimSpace(meta.Event))
	switch ev {
	case "fail", "failed", "error":
		return false
	}
	return true
}

// writePump 写协程：文本指令 + 二进制 TTS。
func (m *ConnManager) writePump(dc *DeviceConn) {
	defer dc.Close()

	for {
		select {
		case <-dc.CloseCh:
			return
		case msg, ok := <-dc.SendCh:
			if !ok {
				return
			}
			dc.writeMu.Lock()
			_ = dc.Conn.SetWriteDeadline(time.Now().Add(m.cfg.WebSocket.WriteTimeout))
			err := dc.Conn.WriteMessage(msg.MsgType, msg.Data)
			dc.writeMu.Unlock()
			if err != nil {
				m.log.Warn("下行写入失败", zap.String("sn", dc.SN), zap.Error(err))
				return
			}
		}
	}
}

func (m *ConnManager) onDisconnect(dc *DeviceConn) {
	dc.Close()
	m.closeTTS(dc.SN, dc.ConnID)
	removed := m.Unregister(dc.SN, dc.ConnID)
	if removed {
		_ = m.redis.SetOfflineIfConn(context.Background(), dc.SN, dc.ConnID)
	}
	dur := time.Duration(0)
	if !dc.ConnectedAt.IsZero() {
		dur = time.Since(dc.ConnectedAt)
	}
	dc.mu.Lock()
	activeCalib := dc.calibBLEExpectOffline
	dc.mu.Unlock()
	fields := []zap.Field{
		zap.String("sn", dc.SN),
		zap.String("conn_id", dc.ConnID),
		zap.Bool("cleared_online", removed),
		zap.Bool("client_seen", dc.ClientSeen()),
		zap.Duration("online_for", dur),
	}
	if activeCalib {
		fields = append(fields,
			zap.String("offline_reason", redisx.OfflineReasonCalibBLE),
			zap.Bool("active_offline", true),
		)
		m.log.Info("设备主动离线(BLE标定)", fields...)
	} else {
		m.log.Info("设备下线", fields...)
	}
	m.onOTADisconnect(dc.SN)
}

// markCalibBLEOffline enter_calib_ble ack 成功：写入 offline_reason=calib_ble，供状态接口与下线日志区分主动离线。
func (m *ConnManager) markCalibBLEOffline(dc *DeviceConn) {
	if dc == nil {
		return
	}
	dc.mu.Lock()
	dc.calibBLEExpectOffline = true
	dc.mu.Unlock()
	if m.redis == nil {
		return
	}
	_ = m.redis.UpdateDeviceStatus(context.Background(), dc.SN, &redisx.DeviceStatusPatch{
		SetOfflineReason: true,
		OfflineReason:    redisx.OfflineReasonCalibBLE,
		SetLastEvent:     true,
		LastEvent:        "calib_ble_enter",
	})
	m.log.Info("设备将主动离线进入 BLE 标定",
		zap.String("sn", dc.SN),
		zap.String("offline_reason", redisx.OfflineReasonCalibBLE),
		zap.Bool("active_offline", true),
	)
}

// handleAppHeartbeat 识别 Text 心跳并回 Text pong；是心跳返回 true。
func (m *ConnManager) handleAppHeartbeat(dc *DeviceConn, data []byte) bool {
	s := strings.TrimSpace(string(data))
	if s == "" {
		return false
	}
	low := strings.ToLower(s)
	isHB := low == "ping" || low == "heartbeat" || low == `{"type":"ping"}` || low == `{"type":"heartbeat"}`
	if !isHB {
		var peek struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &peek) == nil {
			t := strings.ToLower(strings.TrimSpace(peek.Type))
			isHB = t == "ping" || t == "heartbeat"
		}
	}
	if !isHB {
		return false
	}
	m.touchHeartbeat(dc)
	var meta model.DeviceUplink
	if json.Unmarshal(data, &meta) == nil {
		t := strings.ToLower(strings.TrimSpace(meta.Type))
		if t == "ping" || t == "heartbeat" {
			if meta.SN == "" || meta.SN == dc.SN {
				meta.SN = dc.SN
				m.refreshDeviceStatusFromMeta(dc.SN, &meta, "", "", true, true)
				if t == "ping" {
					m.onOTAPing(dc.SN, meta.Boot.Bool(), meta.FW)
				}
			}
		}
	}
	pong := []byte(`{"type":"pong"}`)
	select {
	case <-dc.CloseCh:
		return true
	case dc.SendCh <- OutboundMsg{MsgType: websocket.TextMessage, Data: pong}:
		fields := []zap.Field{zap.String("sn", dc.SN), zap.String("raw", clipRawForLog(data, 128))}
		if meta.Boot.Bool() {
			fields = append(fields, zap.Bool("boot", true), zap.String("fw", meta.FW))
		}
		m.log.Info("收到应用层心跳，已回 Text pong", fields...)
	case <-time.After(m.cfg.WebSocket.WriteTimeout):
		m.log.Warn("应用层心跳回 pong 入队超时", zap.String("sn", dc.SN))
	}
	return true
}

// refreshDeviceStatusFromMeta 从上报元数据增量刷新 Redis 设备统一状态 key。
// hasExplicitBat / hasExplicitBound：
//   - true 表示该字段是从 meta 里解到的"显式上报值"，即使是 0/false 也要覆盖快照（避免 charge:false/bound:0 被跳过）
//   - false 表示调用方无法确认该字段是否上报（如部分 notify/ack），保持快照旧值不动
func (m *ConnManager) refreshDeviceStatusFromMeta(sn string, meta *model.DeviceUplink, event, state string, hasExplicitBat, hasExplicitBound bool) {
	if sn == "" || meta == nil {
		return
	}
	now := util.NowUTCUnixMilli()
	patch := &redisx.DeviceStatusPatch{
		SetLastReportAt: true,
		LastReportAt:    now,
		SetLastSeenAt:   true,
		LastSeenAt:      now,
	}
	if hasExplicitBat {
		patch.SetBattery = true
		patch.Battery = meta.Bat
		patch.SetCharge = true
		patch.Charge = meta.Charge.Bool()
	}
	// RSSI：正常信号都是负数，0 一般是占位；但 hasExplicitBound=true（type=ping/status/locate/audio 显式上报）时允许 0 覆盖，
	// 否则 0 当"没上报"处理，避免覆盖旧有效 RSSI。
	if meta.RSSI != 0 || hasExplicitBound {
		patch.SetRSSI = true
		patch.RSSI = meta.RSSI
	}
	// pid：显式状态帧允许写空（解绑）；ack/notify 等无档案字段时不要用空串冲掉快照
	pid := meta.PID.String()
	if hasExplicitBound || pid != "" {
		patch.SetPID = true
		patch.PID = pid
	}
	if meta.Pet != "" {
		// 新协议：Pet 字段（dog/cat/other）优先，覆盖 breed_code 旧占位
		patch.SetBreed = true
		patch.Breed = meta.Pet
	} else if meta.BreedCode != "" {
		patch.SetBreed = true
		patch.Breed = meta.BreedCode
	}
	// DB：正常 db 都是负数（声音强度），0 一般占位；显式上报帧 hasExplicitBound=true 时允许 0 覆盖。
	if meta.DB != 0 || hasExplicitBound {
		patch.SetDB = true
		patch.DB = meta.DB
	}
	if meta.FW != "" {
		patch.SetFWVer = true
		patch.FWVer = meta.FW
	}
	if meta.Ch != 0 || hasExplicitBound {
		patch.SetCh = true
		patch.Ch = meta.Ch
	}
	if meta.ProtoVer != 0 {
		patch.SetProtoVer = true
		patch.ProtoVer = meta.ProtoVer
	}
	if event != "" {
		patch.SetLastEvent = true
		patch.LastEvent = event
	}
	if state != "" {
		patch.SetLastState = true
		patch.LastState = state
	}
	if m := model.NormalizeMotion(meta.Motion); m != "" {
		patch.SetMotion = true
		patch.Motion = m
	}
	if t := model.NormalizeTier(meta.Tier); t != "" {
		patch.SetTier = true
		patch.Tier = t
	}
	if len(meta.IMU) > 0 {
		if b, err := json.Marshal(meta.IMU); err == nil {
			patch.SetIMU = true
			patch.IMU = b
		}
	}
	// 定位：Lat/Lng 同时为 0 表示无效（无 wifi 或 wifi 未解析），不覆盖旧有效坐标。
	if meta.LocLat != 0 || meta.LocLng != 0 {
		patch.SetLastLoc = true
		patch.LastLocLat = meta.LocLat
		patch.LastLocLng = meta.LocLng
		patch.LastLocAcc = meta.LocAcc
		patch.LastLocTS = meta.LocTS
	}
	if meta.LocSpeed != 0 || meta.LocType != "" {
		patch.SetLocExtra = true
		patch.LocSpeed = meta.LocSpeed
		if meta.LocType != "" {
			patch.LocType = meta.LocType.String()
		}
	}
	// 配置与状态同一 patch 写入，避免「先 Update 再 ApplyConfig」被并发 ping/status 冲掉 translate_enabled/auto_record
	if cfg := model.ConfigFromUplink(meta); cfg != nil {
		mergeConfigIntoStatusPatch(patch, cfg, true)
	}
	if err := m.redis.UpdateDeviceStatus(context.Background(), sn, patch); err != nil {
		m.log.Warn("刷新设备状态失败", zap.String("sn", sn), zap.Error(err))
	}
}

func mergeConfigIntoStatusPatch(patch *redisx.DeviceStatusPatch, upd *model.DeviceConfigUpdate, fromDevice bool) {
	if patch == nil || upd == nil {
		return
	}
	if upd.Volume != nil {
		patch.SetVolume = true
		patch.Volume = *upd.Volume
	}
	if upd.Sensitivity != nil {
		patch.SetSensitivity = true
		patch.Sensitivity = *upd.Sensitivity
	}
	if upd.TranslateEnabled != nil {
		patch.SetTranslateEnabled = true
		patch.TranslateEnabled = *upd.TranslateEnabled
		if upd.AutoRecord == nil {
			patch.SetAutoRecord = true
			patch.AutoRecord = *upd.TranslateEnabled
		}
	}
	if upd.AutoRecord != nil {
		patch.SetAutoRecord = true
		patch.AutoRecord = *upd.AutoRecord
		if upd.TranslateEnabled == nil {
			patch.SetTranslateEnabled = true
			patch.TranslateEnabled = *upd.AutoRecord
		}
	}
	if upd.SleepEnabled != nil {
		patch.SetSleepEnabled = true
		patch.SleepEnabled = *upd.SleepEnabled
	}
	if upd.SleepWindow != nil {
		patch.SetSleepWindow = true
		patch.SleepWindow = *upd.SleepWindow
	}
	if upd.LocIntervalMs != nil {
		patch.SetLocIntervalMs = true
		patch.LocIntervalMs = *upd.LocIntervalMs
	}
	patch.SetConfigSource = true
	if fromDevice {
		patch.ConfigSource = "device"
	} else {
		patch.ConfigSource = "shadow"
	}
	patch.SetConfigUpdatedAt = true
	patch.ConfigUpdatedAt = util.NowUTCUnixMilli()
}
