package model

import (
	"encoding/json"
	"testing"
)

func TestListDeviceCommands(t *testing.T) {
	catalog := ListDeviceCommands()
	if catalog.Total != len(catalog.List) {
		t.Fatalf("total=%d list=%d", catalog.Total, len(catalog.List))
	}
	if catalog.SendPath != "/api/v1/device/cmd" {
		t.Fatalf("send_path=%q", catalog.SendPath)
	}
	byCmd := map[string]DeviceCmdSpec{}
	for _, spec := range catalog.List {
		if spec.Cmd == "" {
			t.Fatal("empty cmd")
		}
		if spec.DataType == "" || spec.Category == "" {
			t.Fatalf("cmd=%s missing data_type or category", spec.Cmd)
		}
		if spec.ExampleReq == nil || spec.ExampleReq["cmd"] != spec.Cmd {
			t.Fatalf("cmd=%s bad example_request", spec.Cmd)
		}
		byCmd[spec.Cmd] = spec
	}
	if _, ok := byCmd[string(CmdRecordNow)]; !ok {
		t.Fatal("missing record_now")
	}
	calib := byCmd[string(CmdEnterCalibBLE)]
	if calib.Cmd == "" || calib.BLECode != "0x0D" {
		t.Fatalf("enter_calib_ble missing or ble_code=%q", calib.BLECode)
	}
	autoRec := byCmd[string(CmdEnableAutoRecord)]
	if len(autoRec.DataOptions) != 2 || autoRec.DataOptions[0].Value != "true" {
		t.Fatalf("enable_auto_record options=%v", autoRec.DataOptions)
	}
	bitrate := byCmd[string(CmdSetBitrate)]
	if len(bitrate.DataOptions) != 3 {
		t.Fatalf("set_bitrate options=%d want 3", len(bitrate.DataOptions))
	}
	if bitrate.DataOptions[0].Value != "0" || bitrate.DataOptions[1].Value != "1" || bitrate.DataOptions[2].Value != "2" {
		t.Fatalf("set_bitrate options=%v", bitrate.DataOptions)
	}
	playCry := byCmd[string(CmdPlayCry)]
	if len(playCry.DataOptions) != 17 {
		t.Fatalf("play_cry options=%d want 17", len(playCry.DataOptions))
	}
	b, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < 500 {
		t.Fatalf("catalog json too short: %d", len(b))
	}
}

func TestListDeviceCommandsUniqueCmds(t *testing.T) {
	seen := map[string]bool{}
	for _, spec := range ListDeviceCommands().List {
		if seen[spec.Cmd] {
			t.Fatalf("duplicate cmd %q", spec.Cmd)
		}
		seen[spec.Cmd] = true
	}
}
