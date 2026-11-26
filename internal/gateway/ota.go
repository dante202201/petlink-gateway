package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"pet-translate-gateway/internal/model"
	"pet-translate-gateway/internal/store"
)

const (
	otaConfirmTimeout = 10 * time.Minute
	// 三种回执：result=0 正常继续；result=1 仍在跑再等 10s；30s 无回执当丢重推。
	otaAckTimeout    = 30 * time.Second
	otaBusyWait      = 10 * time.Second
	otaFailedRepush  = 10 * time.Second // stage=result note=failed
	otaStalledRepush = 10 * time.Second // 本轮 done 未涨
	otaIntervalPush  = 40 * time.Second // 无事件时的背靠背兜底节奏
	otaWatchInterval = 5 * time.Second
)

// otaSession 单设备 OTA 进度与重推调度（内存；任务落定后删除）。
type otaSession struct {
	lastDone       int64
	lastTotal      int64
	lastStage      string
	lastNote       string
	lastProgressAt time.Time
	doneAtPush     int64 // 最近一次推送时的 done 快照
	lastPushAt     time.Time
	ackPending     bool
	lastAckResult  int // -1=尚未收到
	lastAckAt      time.Time
	nextPushAt     time.Time
	nextPushReason string
	pushCount      int
	lastQuietLogAt time.Time // 校验/刷写跳过日志限频
}

// normalizeFW 去掉空白与可选 v 前缀，便于比对。
func normalizeFW(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")
	return s
}

func fwEqual(a, b string) bool {
	na, nb := normalizeFW(a), normalizeFW(b)
	return na != "" && na == nb
}

// settleOTAFromBoot boot=1 时：fw==目标 → 成功；否则回滚失败。
func settleOTAFromBoot(target, reported string) (ok bool, reason string) {
	if fwEqual(target, reported) {
		return true, ""
	}
	return false, store.OTAReasonRollback
}

// settleOTATimeout 十分钟窗口：从未断线 → 静默跳过；断过线但无 boot=1 → 未执行/下载失败。
func settleOTATimeout(disconnected bool) string {
	if disconnected {
		return store.OTAReasonTimeoutNoReboot
	}
	return store.OTAReasonSkipped
}

func asOTAString(v interface{}) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case json.Number:
		return strings.TrimSpace(t.String())
	case json.RawMessage:
		s := strings.TrimSpace(string(t))
		return strings.Trim(s, `"`)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return strings.TrimSpace(fmt.Sprint(t))
		}
		s := strings.TrimSpace(string(b))
		return strings.Trim(s, `"`)
	}
}

func parseOTAPayload(data interface{}) (url, version string, force bool) {
	if data == nil {
		return "", "", false
	}
	switch d := data.(type) {
	case model.OTAUpdateData:
		return strings.TrimSpace(d.URL), strings.TrimSpace(d.Version), d.Force
	case *model.OTAUpdateData:
		if d == nil {
			return "", "", false
		}
		return strings.TrimSpace(d.URL), strings.TrimSpace(d.Version), d.Force
	case map[string]interface{}:
		return asOTAString(d["url"]), asOTAString(d["version"]), parseForceFlag(d["force"])
	case json.RawMessage:
		var inner interface{}
		if json.Unmarshal(d, &inner) == nil {
			return parseOTAPayload(inner)
		}
	case string:
		s := strings.TrimSpace(d)
		if s == "" {
			return "", "", false
		}
		var inner interface{}
		if json.Unmarshal([]byte(s), &inner) == nil {
			return parseOTAPayload(inner)
		}
	}
	return "", "", false
}

// parseForceFlag 兼容 true/false 与历史 0/1。
func parseForceFlag(v interface{}) bool {
	switch t := v.(type) {
	case bool:
		return t
	case int:
		return t != 0
	case int64:
		return t != 0
	case float64:
		return t != 0
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i != 0
		}
	case string:
		s := strings.TrimSpace(strings.ToLower(t))
		if s == "1" || s == "true" {
			return true
		}
		if n, err := strconv.Atoi(s); err == nil && n != 0 {
			return true
		}
	}
	return false
}

func strPtr(s string) *string { return &s }
func boolPtr(v bool) *bool    { return &v }
func i64Ptr(v int64) *int64   { return &v }

func isOTAUpdateCmd(cmd *model.DeviceCommand) bool {
	if cmd == nil {
		return false
	}
	name := strings.TrimSpace(cmd.Cmd)
	if name == "" || strings.EqualFold(name, "cmd") {
		name = strings.TrimSpace(cmd.Type)
	}
	return strings.EqualFold(name, string(model.CmdOTAUpdate))
}

func (m *ConnManager) fillOTAFromCurrentFW(url, version string) (string, string) {
	if url != "" && version != "" {
		return url, version
	}
	if m != nil && m.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		pkg, err := m.store.GetCurrentFirmwarePackage(ctx)
		cancel()
		if err == nil && pkg != nil {
			if url == "" {
				url = strings.TrimSpace(pkg.URL)
			}
			if version == "" {
				version = strings.TrimSpace(pkg.Version)
			}
		}
	}
	if m != nil && m.cfg != nil {
		if url == "" {
			url = strings.TrimSpace(m.cfg.Firmware.URL)
		}
		if version == "" {
			version = strings.TrimSpace(m.cfg.Firmware.Version)
		}
	}
	return url, version
}

func (m *ConnManager) noteOTAPush(sn string, cmd *model.DeviceCommand) {
	m.noteOTAPushOpt(sn, cmd, true)
}

// noteOTAPushOpt markSession=true 表示主动下发（管理端/API）；false 表示仅下行日志观察（自动重推写出时）。
func (m *ConnManager) noteOTAPushOpt(sn string, cmd *model.DeviceCommand, markSession bool) {
	if m == nil || !isOTAUpdateCmd(cmd) {
		return
	}
	sn = strings.TrimSpace(sn)
	if sn == "" {
		return
	}
	url, version, force := parseOTAPayload(cmd.Data)
	url, version = m.fillOTAFromCurrentFW(url, version)
	fromFW := m.currentDeviceFW(sn)
	now := time.Now().UnixMilli()

	if m.store == nil {
		if markSession {
			m.log.Info("OTA 已下发（无库，不记升级日志）",
				zap.String("sn", sn),
				zap.String("from_fw", fromFW),
				zap.String("target_fw", version),
			)
			m.markOTASessionPushed(sn)
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if prev, err := m.store.GetPendingOTABySN(ctx, sn); err == nil && prev != nil {
		sameTarget := version == "" || prev.TargetFW == "" || fwEqual(prev.TargetFW, version)
		if sameTarget {
			// 同目标进行中：绝不新建第二条 pending（否则 ListPending 会刷出重复 ack_lost）。
			if markSession {
				_ = m.store.UpdateOTALog(ctx, prev.ID, store.OTALogPatch{
					PushedAt: i64Ptr(now),
					Status:   strPtr(store.OTAStatusPushed),
				})
				m.markOTASessionPushed(sn)
			}
			return
		}
		_ = m.store.UpdateOTALog(ctx, prev.ID, store.OTALogPatch{
			Status:     strPtr(store.OTAStatusFail),
			FailReason: strPtr(store.OTAReasonSuperseded),
			ResultAt:   i64Ptr(now),
		})
	}
	if !markSession {
		// 下行日志看到的重推：没有 pending 时不擅自建单
		return
	}
	rec := &store.OTALog{
		SN:       sn,
		FromFW:   fromFW,
		TargetFW: version,
		URL:      url,
		Force:    force,
		Status:   store.OTAStatusPushed,
		PushedAt: now,
	}
	id, err := m.store.InsertOTALog(ctx, rec)
	if err != nil {
		m.log.Warn("写入 OTA 升级日志失败", zap.String("sn", sn), zap.Error(err))
		return
	}
	m.log.Info("OTA 已下发，记录升级日志",
		zap.Int64("id", id),
		zap.String("sn", sn),
		zap.String("from_fw", fromFW),
		zap.String("target_fw", version),
		zap.String("url", url),
		zap.Bool("force", force),
	)
	m.markOTASessionPushed(sn)
}

func (m *ConnManager) noteOTAFromRaw(sn string, body []byte) {
	if m == nil || len(body) == 0 {
		return
	}
	var parsed model.DeviceCommand
	if err := json.Unmarshal(body, &parsed); err != nil {
		return
	}
	if !isOTAUpdateCmd(&parsed) {
		return
	}
	parsed.Cmd = string(model.CmdOTAUpdate)
	// 写出路径观察：不刷新会话、不新建日志（避免与自动重推叠加成风暴）
	m.noteOTAPushOpt(sn, &parsed, false)
}

func (m *ConnManager) currentDeviceFW(sn string) string {
	if m == nil || m.redis == nil || sn == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ds, err := m.redis.GetDeviceStatus(ctx, sn)
	if err != nil || ds == nil {
		return ""
	}
	return strings.TrimSpace(ds.FWVer)
}

func (m *ConnManager) onOTAAck(sn string, result int) {
	sn = strings.TrimSpace(sn)
	if sn == "" {
		return
	}
	now := time.Now()
	m.otaMu.Lock()
	s := m.ensureOTASessionLocked(sn)
	s.ackPending = false
	s.lastAckResult = result
	s.lastAckAt = now
	m.otaMu.Unlock()

	if m.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rec, err := m.store.GetPendingOTABySN(ctx, sn)
	if err != nil || rec == nil {
		return
	}
	nowMs := now.UnixMilli()
	if result == 1 {
		// 仍在跑：不落定失败。verify/flash 禁止打扰；download 才预约一次 10s 重推。
		if m.otaStageQuiet(sn) {
			m.cancelOTARepush(sn)
			m.logOTAQuietSkip(sn, "ack_busy")
			return
		}
		scheduled := m.scheduleOTARepushOnce(sn, otaBusyWait, "ack_busy")
		if scheduled {
			m.log.Info("OTA ack result=1，设备仍在下载，稍后重推",
				zap.Int64("id", rec.ID),
				zap.String("sn", sn),
				zap.Duration("retry_in", otaBusyWait),
			)
		}
		return
	}
	if result != 0 {
		_ = m.store.UpdateOTALog(ctx, rec.ID, store.OTALogPatch{
			Status:     strPtr(store.OTAStatusFail),
			FailReason: strPtr(store.OTAReasonAckRejected),
			AckAt:      i64Ptr(nowMs),
			ResultAt:   i64Ptr(nowMs),
		})
		m.clearOTASession(sn)
		m.log.Warn("OTA ack 失败，判定升级失败",
			zap.Int64("id", rec.ID),
			zap.String("sn", sn),
			zap.Int("result", result),
		)
		return
	}
	_ = m.store.UpdateOTALog(ctx, rec.ID, store.OTALogPatch{
		Status: strPtr(store.OTAStatusConfirming),
		AckAt:  i64Ptr(nowMs),
	})
	m.log.Info("OTA ack result=0，进入待确认（进度驱动重推 / 等待 boot=1）",
		zap.Int64("id", rec.ID),
		zap.String("sn", sn),
		zap.String("target_fw", rec.TargetFW),
		zap.String("from_fw", rec.FromFW),
	)
}

func (m *ConnManager) onOTADisconnect(sn string) {
	m.patchPendingOTA(sn, func(rec *store.OTALog) *store.OTALogPatch {
		if rec.Status != store.OTAStatusConfirming && rec.Status != store.OTAStatusPushed {
			return nil
		}
		if rec.Disconnected {
			return nil
		}
		now := time.Now().UnixMilli()
		return &store.OTALogPatch{
			Disconnected: boolPtr(true),
			DisconnectAt: i64Ptr(now),
		}
	})
}

func (m *ConnManager) onOTAReconnect(sn string) {
	m.patchPendingOTA(sn, func(rec *store.OTALog) *store.OTALogPatch {
		if rec.Status != store.OTAStatusConfirming && rec.Status != store.OTAStatusPushed {
			return nil
		}
		if rec.Reconnected {
			return nil
		}
		now := time.Now().UnixMilli()
		return &store.OTALogPatch{
			Reconnected: boolPtr(true),
			ReconnectAt: i64Ptr(now),
		}
	})
}

func (m *ConnManager) onOTAPing(sn string, boot bool, fw string) {
	if m == nil || m.store == nil {
		return
	}
	sn = strings.TrimSpace(sn)
	fw = strings.TrimSpace(fw)
	if sn == "" || fw == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rec, err := m.store.GetPendingOTABySN(ctx, sn)
	if err != nil || rec == nil {
		return
	}
	if rec.Status != store.OTAStatusConfirming && rec.Status != store.OTAStatusPushed {
		return
	}
	now := time.Now().UnixMilli()

	// 已到目标版本：刷写重启后固件常不带 boot=1，按 fw 比对即可落定成功并清会话。
	if fwEqual(rec.TargetFW, fw) {
		_ = m.store.UpdateOTALog(ctx, rec.ID, store.OTALogPatch{
			Status:   strPtr(store.OTAStatusSuccess),
			ResultFW: strPtr(fw),
			BootAt:   i64Ptr(now),
			ResultAt: i64Ptr(now),
		})
		m.clearOTASession(sn)
		m.log.Info("OTA 已按版本落定成功",
			zap.Int64("id", rec.ID),
			zap.String("sn", sn),
			zap.Bool("boot", boot),
			zap.String("target_fw", rec.TargetFW),
			zap.String("result_fw", fw),
		)
		return
	}
	if !boot {
		return
	}
	// boot=1 但版本未变 → 回滚失败
	ok, reason := settleOTAFromBoot(rec.TargetFW, fw)
	status := store.OTAStatusSuccess
	if !ok {
		status = store.OTAStatusFail
	}
	patch := store.OTALogPatch{
		Status:   strPtr(status),
		ResultFW: strPtr(fw),
		BootAt:   i64Ptr(now),
		ResultAt: i64Ptr(now),
	}
	if !ok {
		patch.FailReason = strPtr(reason)
	}
	if err := m.store.UpdateOTALog(ctx, rec.ID, patch); err != nil {
		m.log.Warn("OTA boot ping 落定失败", zap.Int64("id", rec.ID), zap.Error(err))
		return
	}
	m.clearOTASession(sn)
	m.log.Info("OTA 已按 boot ping 落定",
		zap.Int64("id", rec.ID),
		zap.String("sn", sn),
		zap.String("status", status),
		zap.String("reason", reason),
		zap.String("target_fw", rec.TargetFW),
		zap.String("result_fw", fw),
		zap.String("from_fw", rec.FromFW),
	)
}

func (m *ConnManager) patchPendingOTA(sn string, fn func(*store.OTALog) *store.OTALogPatch) {
	if m == nil || m.store == nil || fn == nil {
		return
	}
	sn = strings.TrimSpace(sn)
	if sn == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rec, err := m.store.GetPendingOTABySN(ctx, sn)
	if err != nil || rec == nil {
		return
	}
	p := fn(rec)
	if p == nil {
		return
	}
	_ = m.store.UpdateOTALog(ctx, rec.ID, *p)
}

func (m *ConnManager) watchOTATimeouts() {
	if m == nil || m.store == nil {
		return
	}
	ticker := time.NewTicker(otaWatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.otaStop:
			return
		case <-ticker.C:
			m.settleOTATimeouts()
		}
	}
}

func (m *ConnManager) settleOTATimeouts() {
	if m == nil || m.store == nil {
		return
	}
	m.driveOTARepush()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	list, err := m.store.ListPendingOTALogs(ctx)
	if err != nil {
		m.log.Warn("扫描 OTA 待确认记录失败", zap.Error(err))
		return
	}
	now := time.Now()
	nowMs := now.UnixMilli()
	for i := range list {
		rec := list[i]
		if rec.Status != store.OTAStatusConfirming {
			continue
		}
		base := rec.AckAt
		if base <= 0 {
			base = rec.PushedAt
		}
		if base <= 0 || now.Sub(time.UnixMilli(base)) < otaConfirmTimeout {
			continue
		}
		reason := settleOTATimeout(rec.Disconnected)
		_ = m.store.UpdateOTALog(ctx, rec.ID, store.OTALogPatch{
			Status:     strPtr(store.OTAStatusFail),
			FailReason: strPtr(reason),
			ResultAt:   i64Ptr(nowMs),
		})
		m.clearOTASession(rec.SN)
		m.log.Info("OTA 超时判定失败",
			zap.Int64("id", rec.ID),
			zap.String("sn", rec.SN),
			zap.String("reason", reason),
			zap.Bool("disconnected", rec.Disconnected),
			zap.String("target_fw", rec.TargetFW),
		)
	}
}

// otaStageQuiet verify/flash 期间禁止重推（刷写中绝不能打扰）。
func otaStageQuiet(stage string) bool {
	switch strings.ToLower(strings.TrimSpace(stage)) {
	case "verify", "flash":
		return true
	default:
		return false
	}
}

func (m *ConnManager) otaLastStage(sn string) string {
	m.otaMu.Lock()
	defer m.otaMu.Unlock()
	if s := m.otaSessions[sn]; s != nil {
		return s.lastStage
	}
	return ""
}

func (m *ConnManager) otaStageQuiet(sn string) bool {
	return otaStageQuiet(m.otaLastStage(sn))
}

func (m *ConnManager) cancelOTARepush(sn string) {
	sn = strings.TrimSpace(sn)
	if m == nil || sn == "" {
		return
	}
	m.otaMu.Lock()
	defer m.otaMu.Unlock()
	if s := m.otaSessions[sn]; s != nil {
		s.nextPushAt = time.Time{}
		s.nextPushReason = ""
	}
}

// onOTAProgress 处理设备 ota_progress；failed 与 download 期 done 停滞触发重推。
func (m *ConnManager) onOTAProgress(sn string, meta *model.DeviceUplink) {
	if m == nil || meta == nil {
		return
	}
	sn = strings.TrimSpace(sn)
	if sn == "" {
		return
	}
	stage := strings.ToLower(strings.TrimSpace(meta.Stage))
	note := strings.ToLower(strings.TrimSpace(meta.Note))
	done := meta.Done
	total := int64(meta.Total)

	m.otaMu.Lock()
	s := m.ensureOTASessionLocked(sn)
	s.lastStage = stage
	s.lastNote = note
	s.lastProgressAt = time.Now()
	// 只在 download 期单调递增 done；verify/flash 的 done=0 是阶段计数器，勿清零
	if stage == "download" && note != "retry" && done > s.lastDone {
		s.lastDone = done
	}
	if total > 0 {
		s.lastTotal = total
	}
	curDone, doneAtPush := s.lastDone, s.doneAtPush
	lastPush := s.lastPushAt
	quiet := otaStageQuiet(stage)
	if quiet {
		s.nextPushAt = time.Time{}
		s.nextPushReason = ""
	}
	m.otaMu.Unlock()

	m.log.Info("OTA 进度",
		zap.String("sn", sn),
		zap.String("stage", stage),
		zap.String("note", note),
		zap.Int64("done", done),
		zap.Int64("total", total),
		zap.Int64("tracked_done", curDone),
	)

	if stage == "result" && note == "failed" {
		m.execOTARepush(sn, "progress_failed")
		return
	}
	if quiet {
		// 校验/刷写中：取消一切重推，等 boot 或 result=failed
		return
	}
	// 仅 download：本轮 done 未涨 → 调度重推
	if stage == "download" && !lastPush.IsZero() &&
		time.Since(lastPush) >= otaStalledRepush && curDone <= doneAtPush {
		m.scheduleOTARepush(sn, 0, "done_stalled")
	}
}

func (m *ConnManager) ensureOTASessionLocked(sn string) *otaSession {
	if m.otaSessions == nil {
		m.otaSessions = make(map[string]*otaSession)
	}
	s := m.otaSessions[sn]
	if s == nil {
		s = &otaSession{lastAckResult: -1}
		m.otaSessions[sn] = s
	}
	return s
}

func (m *ConnManager) markOTASessionPushed(sn string) {
	sn = strings.TrimSpace(sn)
	if m == nil || sn == "" {
		return
	}
	m.otaMu.Lock()
	defer m.otaMu.Unlock()
	s := m.ensureOTASessionLocked(sn)
	s.lastPushAt = time.Now()
	s.doneAtPush = s.lastDone
	s.ackPending = true
	s.lastAckResult = -1
	s.nextPushAt = time.Time{}
	s.nextPushReason = ""
	s.pushCount++
}

func (m *ConnManager) scheduleOTARepush(sn string, delay time.Duration, reason string) {
	_ = m.scheduleOTARepushOnce(sn, delay, reason)
}

// scheduleOTARepushOnce 预约重推；若已有更早或相同预约则返回 false（用于日志去重）。
func (m *ConnManager) scheduleOTARepushOnce(sn string, delay time.Duration, reason string) bool {
	sn = strings.TrimSpace(sn)
	if m == nil || sn == "" {
		return false
	}
	at := time.Now().Add(delay)
	m.otaMu.Lock()
	defer m.otaMu.Unlock()
	s := m.ensureOTASessionLocked(sn)
	if otaStageQuiet(s.lastStage) && reason != "progress_failed" {
		s.nextPushAt = time.Time{}
		s.nextPushReason = ""
		return false
	}
	if !s.nextPushAt.IsZero() && !at.Before(s.nextPushAt) {
		return false
	}
	s.nextPushAt = at
	s.nextPushReason = reason
	return true
}

func (m *ConnManager) logOTAQuietSkip(sn, reason string) {
	sn = strings.TrimSpace(sn)
	if m == nil || sn == "" {
		return
	}
	m.otaMu.Lock()
	s := m.ensureOTASessionLocked(sn)
	stage := s.lastStage
	if !s.lastQuietLogAt.IsZero() && time.Since(s.lastQuietLogAt) < 30*time.Second {
		m.otaMu.Unlock()
		return
	}
	s.lastQuietLogAt = time.Now()
	m.otaMu.Unlock()
	m.log.Info("OTA 校验/刷写中，跳过重推",
		zap.String("sn", sn),
		zap.String("reason", reason),
		zap.String("stage", stage),
	)
}

func (m *ConnManager) clearOTASession(sn string) {
	sn = strings.TrimSpace(sn)
	if m == nil || sn == "" {
		return
	}
	m.otaMu.Lock()
	defer m.otaMu.Unlock()
	delete(m.otaSessions, sn)
}

// driveOTARepush 扫描会话：到期调度 / ack 丢失 / 40s 兜底 / done 停滞。
func (m *ConnManager) driveOTARepush() {
	if m == nil || m.store == nil {
		return
	}
	now := time.Now()
	type job struct {
		sn     string
		reason string
	}
	var jobs []job

	seen := make(map[string]bool)
	m.otaMu.Lock()
	for sn, s := range m.otaSessions {
		if s == nil {
			continue
		}
		// verify/flash：静默等待；标为 seen，避免 ListPending 再刷 ack_lost
		if otaStageQuiet(s.lastStage) {
			s.nextPushAt = time.Time{}
			s.nextPushReason = ""
			seen[sn] = true
			continue
		}
		reason := ""
		switch {
		case !s.nextPushAt.IsZero() && !now.Before(s.nextPushAt):
			reason = s.nextPushReason
			if reason == "" {
				reason = "scheduled"
			}
		case s.ackPending && !s.lastPushAt.IsZero() && now.Sub(s.lastPushAt) >= otaAckTimeout:
			reason = "ack_lost"
		case !s.lastPushAt.IsZero() && !s.ackPending &&
			now.Sub(s.lastPushAt) >= otaIntervalPush && s.lastDone <= s.doneAtPush:
			reason = "interval_40s"
		case !s.lastPushAt.IsZero() && !s.ackPending &&
			now.Sub(s.lastPushAt) >= otaStalledRepush &&
			s.lastDone <= s.doneAtPush &&
			s.lastProgressAt.After(s.lastPushAt) &&
			s.lastStage == "download":
			reason = "done_stalled"
		}
		if reason != "" {
			jobs = append(jobs, job{sn: sn, reason: reason})
			s.nextPushAt = time.Time{}
			s.nextPushReason = ""
			seen[sn] = true
		}
	}
	m.otaMu.Unlock()

	// 无会话的 pending：ack 超时补发（每 SN 最多一条）
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	list, err := m.store.ListPendingOTALogs(ctx)
	if err == nil {
		for i := range list {
			rec := list[i]
			if seen[rec.SN] {
				continue
			}
			if rec.Status != store.OTAStatusPushed {
				continue
			}
			if rec.PushedAt <= 0 || now.Sub(time.UnixMilli(rec.PushedAt)) < otaAckTimeout {
				continue
			}
			jobs = append(jobs, job{sn: rec.SN, reason: "ack_lost"})
			seen[rec.SN] = true
		}
	}

	for _, j := range jobs {
		m.execOTARepush(j.sn, j.reason)
	}
}

// execOTARepush 对仍有 pending 日志且在线的设备重发 ota_update。
func (m *ConnManager) execOTARepush(sn, reason string) {
	sn = strings.TrimSpace(sn)
	if m == nil || sn == "" {
		return
	}
	if reason != "progress_failed" && m.otaStageQuiet(sn) {
		m.cancelOTARepush(sn)
		m.logOTAQuietSkip(sn, reason)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rec, err := m.store.GetPendingOTABySN(ctx, sn)
	if err != nil || rec == nil {
		m.clearOTASession(sn)
		return
	}
	if !m.retryOTAPush(rec) {
		// 离线则稍后再试
		m.scheduleOTARepush(sn, otaFailedRepush, reason)
		return
	}
	nowMs := time.Now().UnixMilli()
	_ = m.store.UpdateOTALog(ctx, rec.ID, store.OTALogPatch{
		FailReason: strPtr(store.OTAReasonRetried),
		PushedAt:   i64Ptr(nowMs),
		Status:     strPtr(store.OTAStatusPushed), // 重推后重新等 ack
	})
	m.markOTASessionPushed(sn)
	m.log.Info("OTA 已自动重推 ota_update",
		zap.Int64("id", rec.ID),
		zap.String("sn", sn),
		zap.String("reason", reason),
		zap.String("target_fw", rec.TargetFW),
	)
}

// retryOTAPush 对仍在线设备重发 ota_update（不写新日志，避免 superseded）。
func (m *ConnManager) retryOTAPush(rec *store.OTALog) bool {
	if m == nil || rec == nil || strings.TrimSpace(rec.SN) == "" {
		return false
	}
	if _, ok := m.Get(rec.SN); !ok {
		return false
	}
	url, version := m.fillOTAFromCurrentFW(rec.URL, rec.TargetFW)
	if url == "" || version == "" {
		return false
	}
	data := map[string]interface{}{
		"url":           url,
		"version":       version,
		"force":         rec.Force,
		"accept_ranges": true,
	}
	if m.cfg != nil {
		if m.cfg.Firmware.Size > 0 {
			data["size"] = m.cfg.Firmware.Size
		}
		if md5 := strings.TrimSpace(m.cfg.Firmware.MD5); md5 != "" {
			data["md5"] = md5
		}
	}
	if m.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		pkg, err := m.store.GetCurrentFirmwarePackage(ctx)
		cancel()
		if err == nil && pkg != nil {
			if pkg.Size > 0 {
				data["size"] = pkg.Size
			}
			if md5 := strings.TrimSpace(pkg.MD5); md5 != "" {
				data["md5"] = md5
			}
		}
	}
	cmd := &model.DeviceCommand{Type: "cmd", Cmd: string(model.CmdOTAUpdate), Data: data}
	body, err := json.Marshal(cmd)
	if err != nil {
		return false
	}
	if err := m.enqueueText(rec.SN, body); err != nil {
		m.log.Warn("OTA 补发失败", zap.String("sn", rec.SN), zap.Error(err))
		return false
	}
	return true
}
