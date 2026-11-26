package model

// 时间戳定稿（方案 A）：禁止裸 32 位毫秒（约 49.7 天回绕）。
//
// 设备用 NTP 墙钟锚定 + 上电以来单调微秒拼成一个 int64：
//
//	unix_us = ntp_unix_us_at_sync + (uptime_us_now - uptime_us_at_sync)
//
// 线上 ts 含义：
//   - 音频 seq=0 / 补发 / 定位 loc_ts / notify.ts / imu[].ts：绝对 Unix 微秒
//   - 音频 seq>0：相对本段第一块的偏移微秒（便于省字节）
// 网关入库与 Kafka 的 AudioMessage.TS 一律归一成绝对 Unix 微秒，供波形对齐。
const (
	// UnixMicrosThreshold 大于此值视为绝对 Unix 微秒（约 2001-09 之后）。
	UnixMicrosThreshold int64 = 1_000_000_000_000_000 // 1e15
	// EventFrameDurationUS 事件流（kind=0）定稿帧长 60ms。
	EventFrameDurationUS int64 = 60_000
	// FileFrameDurationUS 录音文件（kind=1）定稿帧长 20ms。
	FileFrameDurationUS int64 = 20_000
	// DefaultFrameDurationUS 兼容旧名：等同事件流 60ms。
	DefaultFrameDurationUS int64 = EventFrameDurationUS
)

// FrameDurationUS 按音频通路返回缺省帧长（微秒）：kind=1→20ms，其余→60ms。
func FrameDurationUS(kind int) int64 {
	if kind == AudioKindFile {
		return FileFrameDurationUS
	}
	return EventFrameDurationUS
}

// FrameDurationSecForKind 按通路返回缺省帧长（秒），无 TOC 时估时长用。
func FrameDurationSecForKind(kind int) float64 {
	return float64(FrameDurationUS(kind)) / 1_000_000.0
}

// AbsoluteTS 将相对偏移还原为绝对 Unix 微秒（base 为段内第一块绝对 ts）。
func (u *DeviceUplink) AbsoluteTS(base int64) int64 {
	if u.IsRetransmit() || u.Seq == 0 || base == 0 {
		return u.TS
	}
	// 固件若后续块误发绝对微秒，直接采用
	if u.TS >= UnixMicrosThreshold {
		return u.TS
	}
	return base + u.TS
}

// IsAbsoluteUnixMicros 判断是否像绝对 Unix 微秒。
func IsAbsoluteUnixMicros(ts int64) bool {
	return ts >= UnixMicrosThreshold
}
