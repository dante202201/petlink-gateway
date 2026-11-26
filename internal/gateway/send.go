package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"pet-translate-gateway/internal/model"
)

// SendCmdToDevice 下行命令：{"type":"cmd","cmd":"...","data":...}；离线入队。
func (m *ConnManager) SendCmdToDevice(sn string, cmd interface{}) error {
	if sn == "" {
		return fmt.Errorf("device sn 为空")
	}

	var body []byte
	var err error
	switch c := cmd.(type) {
	case *model.DeviceCommand:
		normalizeCmd(c)
		if c.Cmd == "" {
			return fmt.Errorf("命令 cmd 为空")
		}
		body, err = json.Marshal(c)
	case model.DeviceCommand:
		normalizeCmd(&c)
		if c.Cmd == "" {
			return fmt.Errorf("命令 cmd 为空")
		}
		body, err = json.Marshal(c)
	default:
		body, err = json.Marshal(cmd)
	}
	if err != nil {
		return fmt.Errorf("指令序列化失败: %w", err)
	}
	m.shadowConfigFromCmd(sn, body)
	err = m.enqueueText(sn, body)
	if err == nil {
		var parsed model.DeviceCommand
		if json.Unmarshal(body, &parsed) == nil {
			m.noteOTAPush(sn, &parsed)
		}
	}
	return err
}

// shadowConfigFromCmd 下行设置类命令写入 Redis 影子配置（设备未上报前 App 可展示）。
// enable_auto_record / enable_translate：仅缓存期望值，等设备成功 ACK 后再写入状态（默认 auto_record=0）。
func (m *ConnManager) shadowConfigFromCmd(sn string, body []byte) {
	var cmd model.DeviceCommand
	if err := json.Unmarshal(body, &cmd); err != nil || cmd.Cmd == "" {
		return
	}
	upd := model.ConfigFromCmd(cmd.Cmd, cmd.Data)
	if upd == nil {
		return
	}
	if model.CmdType(cmd.Cmd) == model.CmdEnableAutoRecord || model.CmdType(cmd.Cmd) == model.CmdEnableTranslate {
		if upd.AutoRecord == nil && upd.TranslateEnabled == nil {
			return
		}
		enabled := false
		if upd.AutoRecord != nil {
			enabled = *upd.AutoRecord
		} else if upd.TranslateEnabled != nil {
			enabled = *upd.TranslateEnabled
		}
		if err := m.redis.SetPendingAutoRecord(context.Background(), sn, enabled); err != nil {
			m.log.Warn("缓存待确认 auto_record 失败", zap.String("sn", sn), zap.String("cmd", cmd.Cmd), zap.Error(err))
		}
		return
	}
	if err := m.redis.ApplyDeviceConfig(context.Background(), sn, upd, false); err != nil {
		m.log.Warn("写入配置影子失败", zap.String("sn", sn), zap.String("cmd", cmd.Cmd), zap.Error(err))
	}
}

func normalizeCmd(c *model.DeviceCommand) {
	if c == nil {
		return
	}
	c.Type = "cmd"
	if c.Data == nil {
		c.Data = map[string]interface{}{}
	}
	normalizeOTAForceInCmd(c)
}

// normalizeOTAForceInCmd 下行 ota_update.data.force 统一为布尔（兼容历史 0/1）。
func normalizeOTAForceInCmd(c *model.DeviceCommand) {
	if c == nil || !strings.EqualFold(strings.TrimSpace(c.Cmd), string(model.CmdOTAUpdate)) {
		return
	}
	switch d := c.Data.(type) {
	case map[string]interface{}:
		d["force"] = parseForceFlag(d["force"])
	case model.OTAUpdateData:
		c.Data = map[string]interface{}{
			"url":     d.URL,
			"version": d.Version,
			"force":   d.Force,
		}
	case *model.OTAUpdateData:
		if d == nil {
			return
		}
		c.Data = map[string]interface{}{
			"url":     d.URL,
			"version": d.Version,
			"force":   d.Force,
		}
	}
}

// SendNotify 下行通知，如 token_expired。
func (m *ConnManager) SendNotify(sn, event string) error {
	body, err := json.Marshal(model.DeviceNotify{Type: "notify", Event: event})
	if err != nil {
		return err
	}
	return m.enqueueText(sn, body)
}

// SendTTSToDevice 下发 TTS：先 Text 元数据，再 Binary（单帧 60ms OPUS）。
func (m *ConnManager) SendTTSToDevice(sn string, opus []byte) error {
	return m.SendTTSToDeviceWithPID(sn, "", opus)
}

// SendTTSToDeviceWithPID 带宠物 pid 的 TTS 下发（走串行队列，受 pause/resume/level 流控）。
func (m *ConnManager) SendTTSToDeviceWithPID(sn string, pid string, opus []byte) error {
	if sn == "" {
		return fmt.Errorf("device sn 为空")
	}
	if len(opus) == 0 {
		return fmt.Errorf("tts opus 为空")
	}
	return m.enqueueTTSAndWait(sn, [][]byte{opus}, pid)
}

// SendTTSFrames 多帧 TTS：入串行队列，按帧 Text+Binary 下发；设备 pause 时停发，resume/level ms=0 后放下一路。
func (m *ConnManager) SendTTSFrames(sn string, frames [][]byte) error {
	return m.SendTTSFramesWithPID(sn, "", frames)
}

// SendTTSFramesWithPID 带宠物 pid 的多帧 TTS 下发。
func (m *ConnManager) SendTTSFramesWithPID(sn string, pid string, frames [][]byte) error {
	if sn == "" {
		return fmt.Errorf("device sn 为空")
	}
	if len(frames) == 0 {
		return fmt.Errorf("tts frames 为空")
	}
	if err := m.enqueueTTSAndWait(sn, frames, pid); err != nil {
		return err
	}
	m.log.Info("TTS 多帧已入队并发送完成", zap.String("sn", sn), zap.Int("frames", len(frames)))
	return nil
}

// BuildCommand 构造 {"type":"cmd","cmd":"...","data":...}。
func BuildCommand(cmdType model.CmdType, data interface{}) *model.DeviceCommand {
	if data == nil {
		data = map[string]interface{}{}
	}
	return &model.DeviceCommand{
		Type: "cmd",
		Cmd:  string(cmdType),
		Data: data,
	}
}

func (m *ConnManager) enqueueText(sn string, body []byte) error {
	dc, ok := m.Get(sn)
	if !ok || dc == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := m.redis.PushOfflineCmd(ctx, sn, string(body)); err != nil {
			return fmt.Errorf("写入离线指令失败: %w", err)
		}
		m.shadowConfigFromCmd(sn, body)
		m.log.Info("设备离线，指令已入队", zap.String("sn", sn), zap.String("cmd", clipJSONForLog(body, maxUplinkLogJSON)))
		return nil
	}
	return m.enqueueToConn(dc, OutboundMsg{MsgType: websocket.TextMessage, Data: body})
}

func (m *ConnManager) enqueueToConn(dc *DeviceConn, msg OutboundMsg) error {
	select {
	case <-dc.CloseCh:
		if msg.MsgType == websocket.TextMessage {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := m.redis.PushOfflineCmd(ctx, dc.SN, string(msg.Data)); err != nil {
				return fmt.Errorf("连接关闭后写入离线指令失败: %w", err)
			}
			return nil
		}
		return fmt.Errorf("连接已关闭 sn=%s", dc.SN)
	case dc.SendCh <- msg:
		if msg.MsgType == websocket.TextMessage {
			m.logDownlinkText(dc.SN, msg.Data)
		}
		return nil
	case <-time.After(m.cfg.WebSocket.WriteTimeout):
		if msg.MsgType == websocket.TextMessage {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := m.redis.PushOfflineCmd(ctx, dc.SN, string(msg.Data)); err != nil {
				return fmt.Errorf("下行超时后写入离线指令失败: %w", err)
			}
			m.log.Warn("下行 Text 写入通道超时，已入离线队列（不踢线）",
				zap.String("sn", dc.SN),
				zap.String("cmd", clipJSONForLog(msg.Data, maxUplinkLogJSON)),
				zap.Duration("timeout", m.cfg.WebSocket.WriteTimeout),
			)
			// 不关连接：关连接没有 Close 帧，设备会报 Connection lost。
			// 离线队列在下次握手时补发。
			return nil
		}
		return fmt.Errorf("下行发送队列超时 sn=%s", dc.SN)
	}
}
