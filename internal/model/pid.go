package model

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// 未绑定宠物档案：空字符串；兼容旧固件数字占位的十进制/十六进制写法。
const PIDUnbound = ""

// PetID 宠物档案 ID（JSON 字符串）。入站兼容数字（旧 uint32），出站一律字符串。
type PetID string

func (p PetID) String() string { return string(p) }

func (p PetID) IsUnbound() bool { return IsUnboundPetID(string(p)) }

func (p *PetID) UnmarshalJSON(data []byte) error {
	data = []byte(strings.TrimSpace(string(data)))
	if len(data) == 0 || string(data) == "null" {
		*p = ""
		return nil
	}
	parseOK := false
	s := ""
	var s2 string
	if err := json.Unmarshal(data, &s2); err == nil {
		s = strings.TrimSpace(s2)
		parseOK = true
	}
	if !parseOK {
		var n json.Number
		if err := json.Unmarshal(data, &n); err == nil {
			s = strings.TrimSpace(n.String())
			parseOK = true
		}
	}
	if !parseOK {
		var i int64
		if err := json.Unmarshal(data, &i); err == nil {
			s = strconv.FormatInt(i, 10)
			parseOK = true
		}
	}
	if !parseOK {
		return fmt.Errorf("invalid pid: %s", string(data))
	}
	if s == "" {
		*p = ""
		return nil
	}
	if IsUnboundPetID(s) {
		*p = ""
		return nil
	}
	hasDigit := false
	for _, r := range s {
		if r >= '0' && r <= '9' {
			hasDigit = true
			continue
		}
		return fmt.Errorf("invalid pid (must be pure numeric string, no letters/prefix): %q", s)
	}
	if !hasDigit {
		return fmt.Errorf("invalid pid (empty or no digits): %q", s)
	}
	*p = PetID(s)
	return nil
}

func (p PetID) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(p))
}

// IsUnboundPetID 未绑定：空 / "0" / 旧全 F 占位。
func IsUnboundPetID(pid string) bool {
	s := strings.TrimSpace(pid)
	switch s {
	case "", "0", "4294967295":
		return true
	}
	return strings.EqualFold(s, "ffffffff")
}
