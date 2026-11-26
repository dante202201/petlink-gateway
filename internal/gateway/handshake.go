package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	redisx "pet-translate-gateway/internal/redis"
)

// 握手鉴权：固件优先用 URL Query；同时兼容 Header（便于调试）。
const (
	HeaderDeviceSN    = "X-Device-SN"
	HeaderDeviceToken = "X-Device-Token"
	QueryDeviceSN     = "sn"
	QueryDeviceToken  = "token"
)

// extractAuth 从 Query 或 Header 提取 SN/Token（Query 优先，对齐固件 /ws?sn=&token=）。
func extractAuth(r *http.Request) (sn, token string) {
	q := r.URL.Query()
	sn = q.Get(QueryDeviceSN)
	token = q.Get(QueryDeviceToken)
	if sn == "" {
		sn = r.Header.Get(HeaderDeviceSN)
	}
	if token == "" {
		token = r.Header.Get(HeaderDeviceToken)
	}
	return sn, token
}

func (m *ConnManager) wsUpgrader() websocket.Upgrader {
	return websocket.Upgrader{
		ReadBufferSize:  m.cfg.WebSocket.ReadBufferSize,
		WriteBufferSize: m.cfg.WebSocket.WriteBufferSize,
		// 硬件无浏览器 Origin；握手尽快返回，避免设备 Header response timeout
		HandshakeTimeout: 10 * time.Second,
		CheckOrigin:      func(r *http.Request) bool { return true },
	}
}

// HandleWS HTTP Upgrade 入口：鉴权 -> 升级 -> 踢旧连 -> 启动读写协程。
//
// 注意：Upgrade 成功后必须尽快结束 HTTP handler，否则 Nginx 等反代可能一直缓冲
// 101 响应头，设备侧就会报 Header response timeout（时好时坏、重启后更常见）。
func (m *ConnManager) HandleWS(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	sn, token := extractAuth(r)

	if sn == "" || token == "" {
		m.log.Warn("握手鉴权失败：缺少 sn/token",
			zap.String("remote", r.RemoteAddr),
			zap.String("path", r.URL.Path),
		)
		http.Error(w, "missing sn or token", http.StatusForbidden)
		return
	}

	m.log.Info("握手开始",
		zap.String("sn", sn),
		zap.String("remote", r.RemoteAddr),
	)

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	ok, err := m.redis.ValidateDeviceToken(ctx, sn, token)
	authDur := time.Since(started)
	cancel()
	if err != nil {
		m.log.Error("握手鉴权 Redis 异常",
			zap.String("sn", sn),
			zap.Duration("auth_dur", authDur),
			zap.Error(err),
		)
		http.Error(w, "auth service unavailable", http.StatusServiceUnavailable)
		return
	}
	if !ok {
		m.log.Warn("握手鉴权失败：非法 token 或未绑定",
			zap.String("sn", sn),
			zap.String("remote", r.RemoteAddr),
			zap.Duration("auth_dur", authDur),
		)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	upgradeAt := time.Now()
	upgrader := m.wsUpgrader()
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		m.log.Error("WebSocket 升级失败",
			zap.String("sn", sn),
			zap.Duration("auth_dur", authDur),
			zap.Duration("upgrade_dur", time.Since(upgradeAt)),
			zap.Error(err),
		)
		return
	}

	// Hijack 后清掉可能残留的读写 deadline，避免影响后续长连接。
	_ = conn.SetReadDeadline(time.Time{})
	_ = conn.SetWriteDeadline(time.Time{})

	dc := &DeviceConn{
		SN:          sn,
		ConnID:      newConnID(),
		Conn:        conn,
		SendCh:      make(chan OutboundMsg, 256),
		CloseCh:     make(chan struct{}),
		persistCh:   make(chan audioPersistJob, audioPersistQueueSize),
		LastPong:    time.Now(),
		ConnectedAt: time.Now(),
	}

	old := m.Register(dc)

	// 先起写协程，保证后续欢迎帧能立刻写出
	go m.writePump(dc)
	go m.readPump(dc)
	go m.persistPump(dc)
	go m.heartbeatWatch(dc)

	m.log.Info("设备上线",
		zap.String("sn", sn),
		zap.String("conn_id", dc.ConnID),
		zap.String("remote", r.RemoteAddr),
		zap.Duration("auth_dur", authDur),
		zap.Duration("upgrade_dur", time.Since(upgradeAt)),
		zap.Duration("handshake_dur", time.Since(started)),
		zap.Bool("kicked_old", old != nil),
	)

	// 踢旧连 / Redis 在线标记 / 欢迎帧 / 离线指令：全部异步，尽快让 HTTP handler 返回，
	// 促使反代把 101 Switching Protocols 刷给设备。
	go m.afterUpgrade(dc, old)
}

// afterUpgrade Upgrade 成功后的收尾（不得阻塞 HandleWS）。
func (m *ConnManager) afterUpgrade(dc *DeviceConn, old *DeviceConn) {
	if old != nil {
		m.log.Warn("设备重复连接，踢掉旧连接",
			zap.String("sn", dc.SN),
			zap.String("old_conn", old.ConnID),
			zap.String("new_conn", dc.ConnID),
		)
		m.closeTTS(old.SN, old.ConnID)
		old.Close()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_ = m.redis.SetOnline(ctx, dc.SN)
	_ = m.redis.SetConnMapping(ctx, dc.SN, dc.ConnID)
	cancel()

	patch := &redisx.DeviceStatusPatch{
		SetConnID:        true,
		ConnID:           dc.ConnID,
		SetConnectedAt:   true,
		ConnectedAt:      dc.ConnectedAt.UnixMilli(),
		SetLastSeenAt:    true,
		LastSeenAt:       dc.ConnectedAt.UnixMilli(),
		SetLastEvent:     true,
		LastEvent:        "connected",
		SetOfflineReason: true,
		OfflineReason:    "", // 重连清除主动离线标记（如 calib_ble）
	}
	_ = m.redis.UpdateDeviceStatus(context.Background(), dc.SN, patch)

	m.sendConnectedNotify(dc)
	m.flushOfflineCmds(dc)
	m.onOTAReconnect(dc.SN)
}

// sendConnectedNotify 握手成功后立即下行 Text，证明应用层双向通路。
func (m *ConnManager) sendConnectedNotify(dc *DeviceConn) {
	body := []byte(`{"type":"notify","event":"connected"}`)
	select {
	case <-dc.CloseCh:
		return
	case dc.SendCh <- OutboundMsg{MsgType: websocket.TextMessage, Data: body}:
		m.log.Info("已下发 connected 通知", zap.String("sn", dc.SN))
	case <-time.After(2 * time.Second):
		m.log.Warn("下发 connected 通知超时", zap.String("sn", dc.SN))
	}
}

// flushOfflineCmds 上线后批量下发离线指令并清空 Redis List。
// 中途下发失败：剩余指令 rollback LPUSH 回 Redis，避免 POP 出来丢了。
func (m *ConnManager) flushOfflineCmds(dc *DeviceConn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmds, err := m.redis.PopAllOfflineCmds(ctx, dc.SN)
	if err != nil {
		m.log.Error("拉取离线指令失败", zap.String("sn", dc.SN), zap.Error(err))
		return
	}
	if len(cmds) == 0 {
		return
	}

	m.log.Info("开始下发离线指令", zap.String("sn", dc.SN), zap.Int("count", len(cmds)))
	failedAt := -1
	for i, raw := range cmds {
		msg := OutboundMsg{MsgType: websocket.TextMessage, Data: []byte(raw)}
		select {
		case <-dc.CloseCh:
			failedAt = i
			break
		case dc.SendCh <- msg:
			m.log.Info("离线指令已下发", zap.String("sn", dc.SN), zap.Int("index", i+1))
			continue
		case <-time.After(3 * time.Second):
			m.log.Warn("离线指令写入发送队列超时，回滚剩余指令", zap.String("sn", dc.SN), zap.Int("index", i+1))
			failedAt = i
			break
		}
		if failedAt >= 0 {
			break
		}
	}
	if failedAt >= 0 && failedAt < len(cmds) {
		remaining := cmds[failedAt:]
		rctx, rcancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer rcancel()
		if rerr := m.redis.LPushOfflineCmds(rctx, dc.SN, remaining...); rerr != nil {
			m.log.Error("回滚剩余离线指令失败，指令已丢失", zap.String("sn", dc.SN),
				zap.Int("rollback_count", len(remaining)), zap.Error(rerr))
			for _, raw := range remaining {
				m.log.Warn("丢失的离线指令", zap.String("sn", dc.SN),
					zap.String("cmd", clipJSONForLog([]byte(raw), maxUplinkLogJSON)))
			}
			return
		}
		m.log.Warn("剩余离线指令已回滚到 Redis 队列", zap.String("sn", dc.SN),
			zap.Int("rollback_count", len(remaining)))
	}
}

func newConnID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
