package model

import "strings"

// 运行态枚举（与 motion / tier 正交）。
const (
	StateIdle      = "idle"
	StateRecording = "recording"
	StatePlaying   = "playing"
	StateLocating  = "locating"
	StateOTA       = "ota"
	StateSleep     = "sleep"
	StateSleeping  = "sleeping"
)

// NormalizeState 将固件上报的 state 规范为小写枚举；空串原样返回。
func NormalizeState(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// StateInSleep 是否视为夜间休眠运行态。
func StateInSleep(state string) bool {
	switch NormalizeState(state) {
	case StateSleep, StateSleeping, "night_sleep", "deep_sleep":
		return true
	}
	return false
}

// StateLabelCN 运行态 → 中文标签（App 展示用）。
func StateLabelCN(state string) string {
	switch NormalizeState(state) {
	case StateIdle, "awake":
		return "空闲"
	case StateRecording:
		return "录音中"
	case StatePlaying:
		return "播放中"
	case StateLocating:
		return "定位中"
	case StateOTA:
		return "升级中"
	case StateSleep, StateSleeping, "night_sleep", "deep_sleep":
		return "睡眠"
	default:
		return ""
	}
}
