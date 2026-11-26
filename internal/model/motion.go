package model

import "strings"

// 运动姿态枚举（宠物肢体活动态；与运行态 state=idle/recording 正交）。
const (
	MotionWalk  = "walk"  // 走
	MotionRun   = "run"   // 跑
	MotionStand = "stand" // 站
	MotionProne = "prone" // 趴
	MotionLie   = "lie"   // 躺
)

// NormalizeMotion 将固件上报的 motion 规范为 walk/run/stand/prone/lie；无法识别返回 ""。
// 兼容中文：走/跑/站/趴/躺。
func NormalizeMotion(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case MotionWalk, "walking", "走":
		return MotionWalk
	case MotionRun, "running", "跑":
		return MotionRun
	case MotionStand, "standing", "站", "站立":
		return MotionStand
	case MotionProne, "趴", "趴下":
		return MotionProne
	case MotionLie, "lying", "lie_down", "躺", "躺下":
		return MotionLie
	default:
		return ""
	}
}

// MotionLabelCN 英文枚举 → 中文标签（App 展示用）。
func MotionLabelCN(motion string) string {
	switch NormalizeMotion(motion) {
	case MotionWalk:
		return "走"
	case MotionRun:
		return "跑"
	case MotionStand:
		return "站"
	case MotionProne:
		return "趴"
	case MotionLie:
		return "躺"
	default:
		return ""
	}
}
