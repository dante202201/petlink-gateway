package model

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// WifiAP 设备上报的周边 WiFi 扫描项（定位用）。
// 兼容字段名：mac / macAddress / bssid；rssi / signalStrength / signal。
type WifiAP struct {
	MAC  string `json:"mac"`
	RSSI int    `json:"rssi"`
	SSID string `json:"ssid,omitempty"`
	Ch   int    `json:"channel,omitempty"`
}

func (a *WifiAP) UnmarshalJSON(b []byte) error {
	b = bytesTrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	// 纯字符串：当作 MAC
	if b[0] == '"' {
		var mac string
		if err := json.Unmarshal(b, &mac); err != nil {
			return err
		}
		a.MAC = normalizeMAC(mac)
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	a.MAC = normalizeMAC(firstString(raw, "mac", "macAddress", "mac_address", "bssid", "BSSID", "bssId"))
	a.SSID = firstString(raw, "ssid", "SSID")
	a.RSSI = firstInt(raw, "rssi", "signalStrength", "signal_strength", "signal", "rssi_dbm")
	a.Ch = firstInt(raw, "channel", "ch", "frequency")
	return nil
}

// WifiScanList WiFi 扫点列表（HTTP / WS 共用）。
type WifiScanList []WifiAP

func (l *WifiScanList) UnmarshalJSON(b []byte) error {
	if string(b) == "null" || len(b) == 0 {
		*l = nil
		return nil
	}
	var aps []WifiAP
	if err := json.Unmarshal(b, &aps); err != nil {
		return err
	}
	*l = WifiScanList(aps)
	return nil
}

// LocMacList 兼容 loc_macs 为 MAC 字符串数组，或 AP 对象数组。
type LocMacList []WifiAP

func (l *LocMacList) UnmarshalJSON(b []byte) error {
	var scan WifiScanList
	if err := scan.UnmarshalJSON(b); err != nil {
		return err
	}
	*l = LocMacList(scan)
	return nil
}

// ExtractWifiScan 从定位请求 JSON 中提取扫点（兼容多种键名）。
func ExtractWifiScan(raw map[string]json.RawMessage) (WifiScanList, error) {
	keys := []string{
		"wifi_scan", "wifiScan", "wifi_list", "wifiList",
		"wifiAccessPoints", "wifi_access_points", "aps", "ap_list",
		"loc_macs", "macs",
	}
	for _, k := range keys {
		v, ok := raw[k]
		if !ok || len(v) == 0 || string(v) == "null" {
			continue
		}
		var scan WifiScanList
		if err := json.Unmarshal(v, &scan); err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		if len(scan) > 0 {
			return scan, nil
		}
	}
	return nil, nil
}

func normalizeMAC(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "-", ":")
	return s
}

func firstString(raw map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		v, ok := raw[k]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(v, &s); err == nil && s != "" {
			return s
		}
	}
	return ""
}

func firstInt(raw map[string]json.RawMessage, keys ...string) int {
	for _, k := range keys {
		v, ok := raw[k]
		if !ok {
			continue
		}
		var n int
		if err := json.Unmarshal(v, &n); err == nil {
			return n
		}
		var f float64
		if err := json.Unmarshal(v, &f); err == nil {
			return int(f)
		}
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			if i, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
				return i
			}
		}
	}
	return 0
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}
