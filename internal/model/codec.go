package model

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// 音频编码：与固件约定 0=opus，1=mp3；入站兼容字符串。
const (
	CodecOpus = "opus"
	CodecMP3  = "mp3"
)

// AudioCodec JSON 可为数字或字符串；规范后为 "opus" / "mp3"。
// 0 / "0" / "opus" → opus；1 / "1" / "mp3" → mp3。
type AudioCodec string

func (c AudioCodec) String() string { return string(c) }

func (c AudioCodec) IsMP3() bool  { return NormalizeAudioCodec(string(c)) == CodecMP3 }
func (c AudioCodec) IsOpus() bool { return NormalizeAudioCodec(string(c)) == CodecOpus }

func (c *AudioCodec) UnmarshalJSON(data []byte) error {
	data = trimJSONSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*c = ""
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err == nil {
		*c = AudioCodec(NormalizeAudioCodec(n.String()))
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*c = AudioCodec(NormalizeAudioCodec(s))
		return nil
	}
	return fmt.Errorf("invalid codec: %s", string(data))
}

func (c AudioCodec) MarshalJSON() ([]byte, error) {
	switch NormalizeAudioCodec(string(c)) {
	case "":
		return []byte("null"), nil
	case CodecMP3:
		return []byte("1"), nil
	case CodecOpus:
		return []byte("0"), nil
	default:
		return json.Marshal(string(c))
	}
}

// NormalizeAudioCodec 统一为 opus/mp3；空保持空（表示未带 codec）。
func NormalizeAudioCodec(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "":
		return ""
	case "0", "opus":
		return CodecOpus
	case "1", "mp3", "mpeg", "mpeg3", "audio/mpeg", "audio/mp3":
		return CodecMP3
	default:
		// 兼容误传数字字符串以外的值
		if _, err := strconv.Atoi(s); err == nil {
			if s == "0" {
				return CodecOpus
			}
			if s == "1" {
				return CodecMP3
			}
		}
		return s
	}
}

// FrameDurationSec 单帧时长（秒），无 TOC 时可估时长；默认按事件流 60ms。
// 按通路请用 FrameDurationSecForKind；有裸 Opus 包时以 TOC 为准。
// 下行 TTS 仍用 ffmpeg -frame_duration 60，与本函数无关。
func (c AudioCodec) FrameDurationSec() float64 {
	return FrameDurationSecForKind(AudioKindEvent)
}

// OSSContentType 原音频上传 Content-Type。
func (c AudioCodec) OSSContentType() string {
	if c.IsMP3() {
		return "audio/mpeg"
	}
	return "audio/opus"
}

// FileExt 原音频对象后缀。
func (c AudioCodec) FileExt() string {
	if c.IsMP3() {
		return "mp3"
	}
	return "opus"
}
