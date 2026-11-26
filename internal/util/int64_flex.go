package util

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Int64Flex 接受 JSON number / JSON string / form 数字串，便于 App 传 "id":676 或 "id":"676"。
type Int64Flex int64

func (v Int64Flex) Int64() int64 { return int64(v) }

func (v *Int64Flex) UnmarshalJSON(b []byte) error {
	b = bytesTrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*v = 0
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return fmt.Errorf("int64 string: %w", err)
		}
		*v = Int64Flex(n)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		i, err := n.Int64()
		if err != nil {
			f, err2 := n.Float64()
			if err2 != nil {
				return err
			}
			*v = Int64Flex(int64(f))
			return nil
		}
		*v = Int64Flex(i)
		return nil
	}
	var i int64
	if err := json.Unmarshal(b, &i); err != nil {
		return err
	}
	*v = Int64Flex(i)
	return nil
}

func bytesTrimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\t' || b[j-1] == '\n' || b[j-1] == '\r') {
		j--
	}
	return b[i:j]
}
