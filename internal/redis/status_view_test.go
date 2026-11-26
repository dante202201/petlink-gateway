package redisx

import (
	"encoding/json"
	"testing"
)

func TestDeviceStatusToViewBound(t *testing.T) {
	ds := &DeviceStatus{
		SN: "S1", PID: "623", Breed: "cat", Battery: 61, FWVer: "2.0.0",
	}
	v := ds.ToView(2)
	if v.Bound != 1 {
		t.Fatalf("bound=%d want 1", v.Bound)
	}
	if v.Battery != 61 || v.FWVer != "2.0.0" || v.Breed != "cat" {
		t.Fatalf("b/fw_ver/breed: b=%d fw=%q breed=%q", v.Battery, v.FWVer, v.Breed)
	}
	if v.OfflineCmdPending != 2 {
		t.Fatalf("offline=%d", v.OfflineCmdPending)
	}
	ds2 := &DeviceStatus{SN: "S2", PID: "623", Breed: ""}
	if ds2.ToView(0).Bound != 0 {
		t.Fatal("empty breed should be unbound")
	}
}

func TestDeviceStatusToViewNoDupAliases(t *testing.T) {
	vol, sens := 65, 80
	slp := true
	ds := &DeviceStatus{
		SN: "S1", Battery: 50, FWVer: "1.0.0", Breed: "dog",
		Volume: &vol, Sensitivity: &sens, SleepEnabled: &slp, SleepWindow: "0:6",
		ConfigSource: "device",
	}
	b, err := json.Marshal(ds.ToView(0))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, dup := range []string{"bat", "fw", "pet"} {
		if _, ok := m[dup]; ok {
			t.Fatalf("unexpected duplicate field %q in response", dup)
		}
	}
	if m["b"].(float64) != 50 {
		t.Fatalf("b=%v", m["b"])
	}
	if m["volume"].(float64) != 65 || m["sensitivity"].(float64) != 80 {
		t.Fatalf("volume/sensitivity missing: %v", m)
	}
	if m["sleep_window"].(string) != "0:6" {
		t.Fatalf("sleep_window=%v", m["sleep_window"])
	}
}

func TestDeviceStatusToViewSleepState(t *testing.T) {
	ds := &DeviceStatus{SN: "S1", LastState: "sleep", LastEvent: "idle", Tier: "normal"}
	v := ds.ToView(0)
	if !v.InSleep {
		t.Fatal("last_state=sleep should set in_sleep")
	}
	if v.LastState != "sleep" || v.StateLabel != "睡眠" {
		t.Fatalf("state=%q label=%q", v.LastState, v.StateLabel)
	}
	ds2 := &DeviceStatus{SN: "S2", LastState: "idle", Tier: "normal"}
	if ds2.ToView(0).InSleep {
		t.Fatal("idle+normal should not be in_sleep")
	}
}

func TestDeviceStatusToViewCalibBLEOffline(t *testing.T) {
	ds := &DeviceStatus{SN: "S1", OfflineReason: OfflineReasonCalibBLE, LastEvent: "calib_ble_enter"}
	v := ds.ToView(0)
	if v.OfflineReason != OfflineReasonCalibBLE || !v.ActiveOffline {
		t.Fatalf("offline_reason=%q active_offline=%v", v.OfflineReason, v.ActiveOffline)
	}
	ds2 := &DeviceStatus{SN: "S2", OfflineReason: ""}
	v2 := ds2.ToView(0)
	if v2.OfflineReason != "" || v2.ActiveOffline {
		t.Fatalf("normal offline should not be active: reason=%q active=%v", v2.OfflineReason, v2.ActiveOffline)
	}
}

func TestDeviceStatusToViewMotionTier(t *testing.T) {
	ds := &DeviceStatus{
		SN: "S1", Motion: "walk", Tier: "light",
		IMU: json.RawMessage(`[{"ts":1,"acc":[0,0,1],"gyro":[0,0,0]}]`),
	}
	v := ds.ToView(0)
	if v.Motion != "walk" || v.MotionLabel != "走" {
		t.Fatalf("motion=%q label=%q", v.Motion, v.MotionLabel)
	}
	if v.Tier != "light" || v.TierLabel != "浅睡" {
		t.Fatalf("tier=%q label=%q", v.Tier, v.TierLabel)
	}
	if !v.InSleep {
		t.Fatal("tier=light should set in_sleep")
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	arr, ok := m["imu"].([]interface{})
	if !ok || len(arr) != 1 {
		t.Fatalf("imu=%v", m["imu"])
	}
	if m["tier"].(string) != "light" || m["tier_label"].(string) != "浅睡" {
		t.Fatalf("tier fields=%v %v", m["tier"], m["tier_label"])
	}
}

func TestDeviceStatusToViewConfigAlwaysPresent(t *testing.T) {
	b, _ := json.Marshal((&DeviceStatus{SN: "S1"}).ToView(0))
	var m map[string]interface{}
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"volume", "sensitivity", "auto_record", "translate_enabled", "sleep_enabled", "sleep_window", "loc_interval_ms", "config_source"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("config key %q missing from JSON", k)
		}
	}
	if m["volume"] != nil || m["sensitivity"] != nil {
		t.Fatalf("unset config should be null, got volume=%v sensitivity=%v", m["volume"], m["sensitivity"])
	}
	if m["auto_record"] != false || m["translate_enabled"] != false {
		t.Fatalf("default auto_record/translate want false, got %v %v", m["auto_record"], m["translate_enabled"])
	}
	if m["sleep_window"] != "" {
		t.Fatalf("unset sleep_window want empty string, got %v", m["sleep_window"])
	}
}

func TestDeviceStatusAutoRecordOff(t *testing.T) {
	off := false
	on := true
	if !(&DeviceStatus{}).AutoRecordOff() {
		t.Fatal("default unset should drop (auto_record default 0)")
	}
	if !(&DeviceStatus{AutoRecord: &off}).AutoRecordOff() {
		t.Fatal("auto_record=false should drop")
	}
	if (&DeviceStatus{AutoRecord: &on}).AutoRecordOff() {
		t.Fatal("auto_record=true should not drop")
	}
	// 仅旧字段 translate_enabled=false 也视为关
	if !(&DeviceStatus{TranslateEnabled: &off}).AutoRecordOff() {
		t.Fatal("translate_enabled=false fallback should drop")
	}
	v := (&DeviceStatus{TranslateEnabled: &on}).ToView(0)
	if v.AutoRecord == nil || !*v.AutoRecord {
		t.Fatalf("view auto_record fallback %+v", v.AutoRecord)
	}
	v0 := (&DeviceStatus{SN: "S1"}).ToView(0)
	if v0.AutoRecord == nil || *v0.AutoRecord {
		t.Fatalf("view default auto_record want false, got %+v", v0.AutoRecord)
	}
	// 不一致：auto_record=false 但 translate_enabled=true → 接口各自原样
	vMixed := (&DeviceStatus{AutoRecord: &off, TranslateEnabled: &on}).ToView(0)
	if vMixed.TranslateEnabled == nil || !*vMixed.TranslateEnabled {
		t.Fatalf("mixed translate want true: %v", vMixed.TranslateEnabled)
	}
	if vMixed.AutoRecord == nil || *vMixed.AutoRecord {
		t.Fatalf("mixed auto_record want false: %v", vMixed.AutoRecord)
	}
}
