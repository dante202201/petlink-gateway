package model

import "strings"

// 功耗/休眠档位（与运行态 state、运动姿态 motion 正交）。
const (
	TierNormal = "normal" // 正常工作态
	TierLight  = "light"  // 浅睡：静音 ≥5min；有网保 WiFi / 无网关射频保 BLE
	TierDeep   = "deep"   // 深睡：浅睡后再静音约 5min；充电/配网态阻断
)

// NormalizeTier 将固件上报的 tier 规范为 normal/light/deep；无法识别返回 ""。
func NormalizeTier(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case TierNormal, "work", "awake", "active", "正常", "工作":
		return TierNormal
	case TierLight, "light_sleep", "shallow", "浅睡", "浅休眠":
		return TierLight
	case TierDeep, "deep_sleep", "深睡", "深休眠":
		return TierDeep
	default:
		return ""
	}
}

// TierLabelCN 英文枚举 → 中文标签（App 展示用）。
func TierLabelCN(tier string) string {
	switch NormalizeTier(tier) {
	case TierNormal:
		return "正常"
	case TierLight:
		return "浅睡"
	case TierDeep:
		return "深睡"
	default:
		return ""
	}
}

// TierInSleep light/deep 视为休眠中（供 App in_sleep 推导）。
func TierInSleep(tier string) bool {
	switch NormalizeTier(tier) {
	case TierLight, TierDeep:
		return true
	default:
		return false
	}
}
