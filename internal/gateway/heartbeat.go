package gateway

import (
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// heartbeatWatch 心跳监控协程。
// 默认不主动向设备发 Ping：当前联调固件收到服务端 Ping 会直接 1006 断连。
// 保活依赖设备 Ping（服务端回 Pong）+ 空闲超时；需要服务端探活时再开 enable_server_ping。
func (m *ConnManager) heartbeatWatch(dc *DeviceConn) {
	pongTimeout := m.cfg.WebSocket.PongTimeout
	if pongTimeout <= 0 {
		pongTimeout = 20 * time.Second
	}
	pingInterval := m.cfg.WebSocket.DevicePingInterval
	if pingInterval <= 0 {
		pingInterval = 25 * time.Second
	}
	enableServerPing := m.cfg.WebSocket.EnableServerPing

	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	checkTicker := time.NewTicker(time.Second)
	defer checkTicker.Stop()

	var pendingPingAt time.Time
	sendPing := func() bool {
		if !enableServerPing {
			return true
		}
		if !dc.ClientSeen() {
			m.log.Debug("等待设备首包后再发服务端 Ping", zap.String("sn", dc.SN))
			return true
		}
		deadline := time.Now().Add(m.cfg.WebSocket.WriteTimeout)
		dc.writeMu.Lock()
		err := dc.Conn.WriteControl(websocket.PingMessage, []byte("ping"), deadline)
		dc.writeMu.Unlock()
		if err != nil {
			m.log.Warn("服务端 Ping 失败，关闭连接", zap.String("sn", dc.SN), zap.Error(err))
			dc.Close()
			return false
		}
		pendingPingAt = time.Now()
		m.log.Info("服务端已发 Ping", zap.String("sn", dc.SN))
		return true
	}

	if !enableServerPing {
		m.log.Info("服务端主动 Ping 已关闭（仅回设备 Ping + 空闲检测）", zap.String("sn", dc.SN))
	}

	// 连接后短暂窗口仍无任何上行：多半是设备未发 Ping，或 Nginx 未把控制帧转到网关
	warnSilent := time.NewTimer(3 * time.Second)
	defer warnSilent.Stop()

	for {
		select {
		case <-dc.CloseCh:
			return

		case <-warnSilent.C:
			if !dc.ClientSeen() {
				m.log.Warn("上线 3s 仍无设备上行（含 Ping）；若随后约 5s 断连，请查固件是否发了 WS Ping、Nginx 是否转发控制帧",
					zap.String("sn", dc.SN),
				)
			}

		case <-ticker.C:
			if !sendPing() {
				return
			}

		case <-checkTicker.C:
			last := dc.GetLastPong()

			if enableServerPing && !pendingPingAt.IsZero() && time.Since(pendingPingAt) > pongTimeout {
				if last.Before(pendingPingAt) {
					m.log.Warn("Pong 超时，主动关闭连接",
						zap.String("sn", dc.SN),
						zap.Duration("pong_timeout", pongTimeout),
						zap.Duration("since_last_activity", time.Since(last)),
					)
					dc.Close()
					return
				}
				pendingPingAt = time.Time{}
			}

			maxIdle := 2*pingInterval + pongTimeout
			if time.Since(last) > maxIdle {
				m.log.Warn("心跳空闲超时，主动关闭连接",
					zap.String("sn", dc.SN),
					zap.Duration("idle", time.Since(last)),
					zap.Duration("max_idle", maxIdle),
					zap.Bool("client_seen", dc.ClientSeen()),
				)
				dc.Close()
				return
			}
		}
	}
}
