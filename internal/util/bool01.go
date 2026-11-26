package util

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Bool01 JSON 布尔约定：仅接受 0/1（数字或字符串）或 true/false；对外序列化为 0/1。
type Bool01 bool

func (b Bool01) Bool() bool { return bool(b) }

func (b Bool01) MarshalJSON() ([]byte, error) {
	if b {
		return []byte("1"), nil
	}
	return []byte("0"), nil
}

func (b *Bool01) UnmarshalJSON(data []byte) error {
	data = bytesTrim(data)
	s := string(data)
	switch s {
	case "1", "true", `"1"`, `"true"`:
		*b = true
		return nil
	case "0", "false", `"0"`, `"false"`, "null", "":
		*b = false
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err == nil {
		i, err := n.Int64()
		if err == nil {
			*b = i != 0
			return nil
		}
	}
	var bb bool
	if err := json.Unmarshal(data, &bb); err == nil {
		*b = Bool01(bb)
		return nil
	}
	return fmt.Errorf("invalid bool01: %s", s)
}

func bytesTrim(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

// BoolTF JSON 布尔：对外序列化为 true/false；入站兼容 true/false、0/1、CHG/NCH。
type BoolTF bool

func (b BoolTF) Bool() bool { return bool(b) }

func (b BoolTF) MarshalJSON() ([]byte, error) {
	if b {
		return []byte("true"), nil
	}
	return []byte("false"), nil
}

func (b *BoolTF) UnmarshalJSON(data []byte) error {
	data = bytesTrim(data)
	s := string(data)
	switch strings.ToLower(strings.Trim(s, `"`)) {
	case "1", "true", "chg", "charging":
		*b = true
		return nil
	case "0", "false", "nch", "nchg", "null", "":
		*b = false
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err == nil {
		i, err := n.Int64()
		if err == nil {
			*b = i != 0
			return nil
		}
	}
	var bb bool
	if err := json.Unmarshal(data, &bb); err == nil {
		*b = BoolTF(bb)
		return nil
	}
	return fmt.Errorf("invalid booltf: %s", s)
}

// ParseUint32Loose 解析 pid 等字段（数字或字符串）。
func ParseUint32Loose(v interface{}) (uint32, error) {
	switch t := v.(type) {
	case float64:
		return uint32(t), nil
	case json.Number:
		i, err := t.Int64()
		return uint32(i), err
	case string:
		u, err := strconv.ParseUint(t, 10, 32)
		return uint32(u), err
	default:
		return 0, fmt.Errorf("unsupported")
	}
}
