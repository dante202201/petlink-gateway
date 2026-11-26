package model

import (
	"fmt"
	"strconv"
	"strings"
)

// STATChargePrefix BLE STAT Notify 充电状态固定前缀。
const STATChargePrefix = "CHARGE:"

// ParseSTATCharge 解析 BLE STAT 文本：CHARGE:<电量%> ，如 CHARGE:78 / CHARGE:78%。
func ParseSTATCharge(line string) (bat int, ok bool) {
	s := strings.TrimSpace(line)
	s = strings.TrimRight(s, "\x00")
	s = strings.TrimSpace(s)
	if len(s) < len(STATChargePrefix)+1 {
		return 0, false
	}
	if !strings.EqualFold(s[:len(STATChargePrefix)], STATChargePrefix) {
		return 0, false
	}
	rest := strings.TrimSpace(s[len(STATChargePrefix):])
	rest = strings.TrimSuffix(rest, "%")
	rest = strings.TrimSpace(rest)
	n, err := strconv.Atoi(rest)
	if err != nil || n < 0 || n > 100 {
		return 0, false
	}
	return n, true
}

// FormatSTATCharge 生成 BLE STAT 充电上报文本（固定格式，无空格、无百分号后缀）。
func FormatSTATCharge(bat int) string {
	if bat < 0 {
		bat = 0
	}
	if bat > 100 {
		bat = 100
	}
	return fmt.Sprintf("%s%d", STATChargePrefix, bat)
}
