package gateway

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

const maxUplinkLogJSON = 1024

type outboundPeek struct {
	Type  string `json:"type"`
	Cmd   string `json:"cmd"`
	Event string `json:"event"`
	Seq   int    `json:"seq"`
	Total int    `json:"total"`
	End   int    `json:"end"`
	CRC   uint32 `json:"crc"`
	PID   string `json:"pid"`
	Codec string `json:"codec"`
}

func clipRawForLog(data []byte, max int) string {
	if max <= 0 {
		max = maxUplinkLogJSON
	}
	if len(data) <= max {
		return string(data)
	}
	// 避免截断到 UTF-8 中间
	n := max
	for n > 0 && !utf8.RuneStart(data[n]) {
		n--
	}
	return string(data[:n]) + "…"
}

func clipJSONForLog(data []byte, max int) string {
	if max <= 0 {
		max = maxUplinkLogJSON
	}
	src := data
	var obj map[string]interface{}
	if json.Unmarshal(data, &obj) == nil {
		if _, ok := obj["token"]; ok {
			obj["token"] = "***"
		}
		if b, err := json.Marshal(obj); err == nil {
			src = b
		}
	}
	return clipRawForLog(src, max)
}

func (m *ConnManager) logUplinkJSON(sn string, data []byte, metaType string) {
	m.log.Info("设备上行",
		zap.String("sn", sn),
		zap.String("type", metaType),
		zap.Int("bytes", len(data)),
		zap.String("json", clipJSONForLog(data, maxUplinkLogJSON)),
	)
}

func (m *ConnManager) logDownlinkText(sn string, data []byte) {
	var peek outboundPeek
	_ = json.Unmarshal(data, &peek)
	kind := "文本"
	frame := "text"
	switch peek.Type {
	case "cmd":
		kind = "指令"
	case "notify":
		kind = "通知"
	case "tts":
		kind = "TTS双帧①元数据"
		frame = "text"
	}
	fields := []zap.Field{
		zap.String("sn", sn),
		zap.String("kind", kind),
		zap.String("frame", frame),
		zap.String("type", peek.Type),
		zap.String("cmd", peek.Cmd),
		zap.String("event", peek.Event),
		zap.Int("seq", peek.Seq),
		zap.Int("total", peek.Total),
		zap.String("json", clipJSONForLog(data, maxUplinkLogJSON)),
	}
	if peek.Type == "tts" {
		fields = append(fields,
			zap.Int("end", peek.End),
			zap.Uint32("crc", peek.CRC),
			zap.String("pid", peek.PID),
			zap.String("codec", peek.Codec),
		)
	}
	m.log.Info("设备下行", fields...)
	m.noteOTAFromRaw(sn, data)
}

// logDownlinkTTSBinary 记录 TTS 双帧的 Binary（一条 = 一个完整 Opus 包）。
func (m *ConnManager) logDownlinkTTSBinary(sn string, seq, total, end int, crc uint32, opus []byte) {
	m.log.Info("设备下行",
		zap.String("sn", sn),
		zap.String("kind", "TTS双帧②音频"),
		zap.String("frame", "binary"),
		zap.String("type", "tts"),
		zap.Int("seq", seq),
		zap.Int("total", total),
		zap.Int("end", end),
		zap.Uint32("crc", crc),
		zap.Int("opus_bytes", len(opus)),
		zap.String("payload_hex", hexPreview(opus, 32)),
	)
}

func logCloseReason(err error) string {
	if err == nil {
		return "eof"
	}
	if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		return "normal"
	}
	if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
		return "unexpected"
	}
	return "error"
}
