package model

import (
	"fmt"
	"strconv"
	"strings"
)

// DeviceConfigUpdate 设备配置项（上行真值或下行影子写入 Redis）。
type DeviceConfigUpdate struct {
	Volume           *int
	Sensitivity      *int
	TranslateEnabled *bool
	AutoRecord       *bool // 与 TranslateEnabled 同语义；优先用设备 auto_record 字段
	SleepEnabled     *bool
	SleepWindow      *string
	LocIntervalMs    *int
}

// ConfigFromUplink 从 status/ping 等上行解析配置（仅非 nil 字段会写入）。
func ConfigFromUplink(u *DeviceUplink) *DeviceConfigUpdate {
	if u == nil {
		return nil
	}
	out := &DeviceConfigUpdate{}
	has := false
	if u.Volume != nil {
		v := clampPercent(*u.Volume)
		out.Volume = &v
		has = true
	}
	if u.Sensitivity != nil {
		v := clampPercent(*u.Sensitivity)
		out.Sensitivity = &v
		has = true
	}
	// auto_record / translate_enabled：两边都报时各自落库（固件可能 auto_record=0 关文件录音，同时 translate_enabled=1）；
	// 只报其中一个时，互相同步以兼容旧固件。
	if u.AutoRecord != nil {
		v := *u.AutoRecord != 0
		out.AutoRecord = &v
		has = true
	}
	if u.TranslateEnabled != nil {
		v := *u.TranslateEnabled != 0
		out.TranslateEnabled = &v
		has = true
	}
	if out.AutoRecord != nil && out.TranslateEnabled == nil {
		v := *out.AutoRecord
		out.TranslateEnabled = &v
	} else if out.TranslateEnabled != nil && out.AutoRecord == nil {
		v := *out.TranslateEnabled
		out.AutoRecord = &v
	}
	if u.SleepEnabled != nil {
		v := *u.SleepEnabled != 0
		out.SleepEnabled = &v
		has = true
	}
	if s := strings.TrimSpace(u.SleepWindow); s != "" {
		out.SleepWindow = &s
		has = true
	}
	if u.LocIntervalMs != nil && *u.LocIntervalMs > 0 {
		v := *u.LocIntervalMs
		out.LocIntervalMs = &v
		has = true
	}
	if !has {
		return nil
	}
	return out
}

// ConfigFromCmd 从下行 cmd 解析影子配置（App 改设置后、设备未上报前的兜底值）。
func ConfigFromCmd(cmd string, data interface{}) *DeviceConfigUpdate {
	switch CmdType(strings.TrimSpace(cmd)) {
	case CmdSetVolume:
		if v, ok := parsePercentData(data); ok {
			return &DeviceConfigUpdate{Volume: &v}
		}
	case CmdSetSensitivity:
		if v, ok := parsePercentData(data); ok {
			return &DeviceConfigUpdate{Sensitivity: &v}
		}
	case CmdEnableTranslate, CmdEnableAutoRecord:
		if v, ok := parseBoolData(data); ok {
			return &DeviceConfigUpdate{TranslateEnabled: &v, AutoRecord: &v}
		}
	case CmdEnableSleep:
		if v, ok := parseBoolData(data); ok {
			return &DeviceConfigUpdate{SleepEnabled: &v}
		}
	case CmdSetSleepWindow:
		if s, ok := parseStringData(data); ok {
			return &DeviceConfigUpdate{SleepWindow: &s}
		}
	case CmdSetLocInterval:
		if v, ok := parseIntData(data); ok && v > 0 {
			return &DeviceConfigUpdate{LocIntervalMs: &v}
		}
	}
	return nil
}

func clampPercent(n int) int {
	if n < 0 {
		return 0
	}
	if n > 100 {
		return 100
	}
	return n
}

func parsePercentData(data interface{}) (int, bool) {
	n, ok := parseIntData(data)
	if !ok {
		return 0, false
	}
	return clampPercent(n), true
}

func parseIntData(data interface{}) (int, bool) {
	switch v := data.(type) {
	case nil:
		return 0, false
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case float32:
		return int(v), true
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return 0, false
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

func parseBoolData(data interface{}) (bool, bool) {
	switch v := data.(type) {
	case nil:
		return false, false
	case bool:
		return v, true
	case int:
		return v != 0, true
	case int64:
		return v != 0, true
	case float64:
		return v != 0, true
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		switch s {
		case "1", "true", "on", "yes":
			return true, true
		case "0", "false", "off", "no":
			return false, true
		default:
			return false, false
		}
	default:
		return false, false
	}
}

func parseStringData(data interface{}) (string, bool) {
	switch v := data.(type) {
	case nil:
		return "", false
	case string:
		s := strings.TrimSpace(v)
		return s, s != ""
	default:
		s := strings.TrimSpace(fmt.Sprint(v))
		return s, s != ""
	}
}
