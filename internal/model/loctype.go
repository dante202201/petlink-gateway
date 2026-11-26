package model

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// 定位类型定稿：数字枚举。1 = wifi。
const (
	LocTypeWiFi = "1"
	// 预留（入站若仍发字符串会规范成数字）
	LocTypeGPS    = "2"
	LocTypeBeacon = "3"
)

// LocTypeCode JSON 中可为数字或字符串；对外序列化为数字（如 1）。
// 定稿：1=wifi；入站兼容 "wifi" / 1 / "1"。
type LocTypeCode string

func (l LocTypeCode) String() string { return string(l) }

func (l LocTypeCode) IsWiFi() bool {
	return NormalizeLocType(string(l)) == LocTypeWiFi
}

func (l *LocTypeCode) UnmarshalJSON(data []byte) error {
	data = trimJSONSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*l = ""
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err == nil {
		*l = LocTypeCode(NormalizeLocType(n.String()))
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*l = LocTypeCode(NormalizeLocType(s))
		return nil
	}
	return fmt.Errorf("invalid loc_type: %s", string(data))
}

func (l LocTypeCode) MarshalJSON() ([]byte, error) {
	s := NormalizeLocType(string(l))
	if s == "" {
		return []byte("null"), nil
	}
	if _, err := strconv.Atoi(s); err == nil {
		return []byte(s), nil
	}
	return json.Marshal(s)
}

// NormalizeLocType 统一为数字字符串；未知原样返回（小写 trim）。
func NormalizeLocType(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "", "0":
		return ""
	case "1", "wifi":
		return LocTypeWiFi
	case "2", "gps":
		return LocTypeGPS
	case "3", "beacon":
		return LocTypeBeacon
	default:
		return s
	}
}

func trimJSONSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}
