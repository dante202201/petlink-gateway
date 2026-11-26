package model

import (
	"testing"
)

func TestConfigFromCmdVolume(t *testing.T) {
	u := ConfigFromCmd("set_volume", "65")
	if u == nil || u.Volume == nil || *u.Volume != 65 {
		t.Fatalf("got %+v", u)
	}
}

func TestConfigFromCmdEnableAutoRecord(t *testing.T) {
	u := ConfigFromCmd("enable_auto_record", "true")
	if u == nil || u.TranslateEnabled == nil || !*u.TranslateEnabled {
		t.Fatalf("true: %+v", u)
	}
	if u.AutoRecord == nil || !*u.AutoRecord {
		t.Fatalf("true auto_record: %+v", u)
	}
	u2 := ConfigFromCmd("enable_auto_record", "false")
	if u2 == nil || u2.TranslateEnabled == nil || *u2.TranslateEnabled {
		t.Fatalf("false: %+v", u2)
	}
	if u2.AutoRecord == nil || *u2.AutoRecord {
		t.Fatalf("false auto_record: %+v", u2)
	}
}

func TestConfigFromUplink(t *testing.T) {
	vol, sens, tr := 70, 80, 1
	u := &DeviceUplink{
		Volume:           &vol,
		Sensitivity:      &sens,
		TranslateEnabled: &tr,
		SleepWindow:      "22:6",
	}
	cfg := ConfigFromUplink(u)
	if cfg == nil || cfg.Volume == nil || *cfg.Volume != 70 {
		t.Fatalf("volume %+v", cfg)
	}
	if cfg.SleepWindow == nil || *cfg.SleepWindow != "22:6" {
		t.Fatalf("sleep_window %+v", cfg)
	}
	if cfg.AutoRecord == nil || !*cfg.AutoRecord || cfg.TranslateEnabled == nil || !*cfg.TranslateEnabled {
		t.Fatalf("translate sync %+v", cfg)
	}
}

func TestConfigFromUplinkAutoRecord(t *testing.T) {
	ar := 0
	u := &DeviceUplink{AutoRecord: &ar}
	cfg := ConfigFromUplink(u)
	if cfg == nil || cfg.AutoRecord == nil || *cfg.AutoRecord {
		t.Fatalf("auto_record=0 %+v", cfg)
	}
	if cfg.TranslateEnabled == nil || *cfg.TranslateEnabled {
		t.Fatalf("translate sync from auto_record alone %+v", cfg)
	}
}

func TestConfigFromUplinkDisagree(t *testing.T) {
	ar, tr := 0, 1
	u := &DeviceUplink{AutoRecord: &ar, TranslateEnabled: &tr}
	cfg := ConfigFromUplink(u)
	if cfg == nil || cfg.AutoRecord == nil || *cfg.AutoRecord {
		t.Fatalf("auto_record want false %+v", cfg)
	}
	if cfg.TranslateEnabled == nil || !*cfg.TranslateEnabled {
		t.Fatalf("translate_enabled want true (keep device value) %+v", cfg)
	}
}
