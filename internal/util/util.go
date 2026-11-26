// Package util 提供时间、CRC、编解码等轻量工具。
package util

import (
	"encoding/binary"
	"hash/crc32"
	"time"
)

// NowUTCUnix 返回当前 UTC 秒级时间戳。
func NowUTCUnix() int64 {
	return time.Now().UTC().Unix()
}

// NowUTCUnixMilli 返回当前 UTC 毫秒级时间戳（网关自身 received_at 等）。
func NowUTCUnixMilli() int64 {
	return time.Now().UTC().UnixMilli()
}

// NowUTCUnixMicro 返回当前 UTC 微秒级时间戳（与设备方案 A 的 ts 同量级，仅演示客户端用）。
func NowUTCUnixMicro() int64 {
	return time.Now().UTC().UnixMicro()
}

// CRC32IEEE 计算 IEEE CRC32 校验值。
func CRC32IEEE(data []byte) uint32 {
	return crc32.ChecksumIEEE(data)
}

// AppendCRC32 在数据末尾追加 4 字节大端 CRC32。
func AppendCRC32(data []byte) []byte {
	sum := CRC32IEEE(data)
	buf := make([]byte, len(data)+4)
	copy(buf, data)
	binary.BigEndian.PutUint32(buf[len(data):], sum)
	return buf
}

// VerifyAndStripCRC32 校验并剥离末尾 4 字节 CRC32。
// 返回去掉 CRC 后的有效载荷；校验失败返回 false。
func VerifyAndStripCRC32(data []byte) ([]byte, bool) {
	if len(data) < 4 {
		return nil, false
	}
	payload := data[:len(data)-4]
	want := binary.BigEndian.Uint32(data[len(data)-4:])
	got := CRC32IEEE(payload)
	if want != got {
		return nil, false
	}
	return payload, true
}

// PutUint16BE / GetUint16BE 大端 16 位读写辅助。
func PutUint16BE(b []byte, v uint16) {
	binary.BigEndian.PutUint16(b, v)
}

// GetUint16BE 读取大端 uint16。
func GetUint16BE(b []byte) uint16 {
	return binary.BigEndian.Uint16(b)
}
