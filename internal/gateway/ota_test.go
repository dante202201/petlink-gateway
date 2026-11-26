package gateway

import (
	"encoding/json"
	"testing"
	"time"

	"go.uber.org/zap"

	"pet-translate-gateway/internal/model"
	"pet-translate-gateway/internal/store"
)

func TestNormalizeFW(t *testing.T) {
	if normalizeFW("  v2.1.0 ") != "2.1.0" {
		t.Fatalf("normalize %q", normalizeFW("  v2.1.0 "))
	}
	if !fwEqual("v2.1.0", "2.1.0") {
		t.Fatal("expected equal")
	}
	if fwEqual("2.1.0", "2.2.0") {
		t.Fatal("expected not equal")
	}
	if fwEqual("", "") {
		t.Fatal("empty should not equal")
	}
}

func TestSettleOTAFromBoot(t *testing.T) {
	ok, reason := settleOTAFromBoot("2.2.0", "2.2.0")
	if !ok || reason != "" {
		t.Fatalf("success: ok=%v reason=%s", ok, reason)
	}
	ok, reason = settleOTAFromBoot("2.2.0", "2.1.0")
	if ok || reason != store.OTAReasonRollback {
		t.Fatalf("rollback: ok=%v reason=%s", ok, reason)
	}
}

func TestSettleOTATimeout(t *testing.T) {
	if got := settleOTATimeout(false); got != store.OTAReasonSkipped {
		t.Fatalf("skip=%s", got)
	}
	if got := settleOTATimeout(true); got != store.OTAReasonTimeoutNoReboot {
		t.Fatalf("no reboot=%s", got)
	}
}

func TestParseOTAPayload(t *testing.T) {
	u, v, f := parseOTAPayload(map[string]interface{}{
		"url": "https://x/fw.bin", "version": "2.2.0", "force": 1.0,
	})
	if u != "https://x/fw.bin" || v != "2.2.0" || !f {
		t.Fatalf("map: url=%s ver=%s force=%v", u, v, f)
	}
	u, v, f = parseOTAPayload(`{"url":"https://y.bin","version":"2.3.0","force":true}`)
	if u != "https://y.bin" || v != "2.3.0" || !f {
		t.Fatalf("json string: url=%s ver=%s force=%v", u, v, f)
	}
	if !isOTAUpdateCmd(&model.DeviceCommand{Type: "cmd", Cmd: "ota_update"}) {
		t.Fatal("cmd ota_update")
	}
	if !isOTAUpdateCmd(&model.DeviceCommand{Type: "ota_update"}) {
		t.Fatal("type ota_update")
	}
}

func TestOTAProgressExpectsBinaryFalse(t *testing.T) {
	meta := &model.DeviceUplink{Type: "ota_progress", Done: 1411356, Total: 1967616, Stage: "download"}
	if meta.ExpectsBinary() {
		t.Fatal("ota_progress must not expect binary even when total>0")
	}
}

func TestOTAProgressParseFields(t *testing.T) {
	raw := []byte(`{"done":1411356,"note":"interrupted","stage":"download","total":1967616,"type":"ota_progress"}`)
	var meta model.DeviceUplink
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Type != "ota_progress" || meta.Stage != "download" || meta.Note != "interrupted" {
		t.Fatalf("meta=%+v", meta)
	}
	if meta.Done != 1411356 || meta.Total != 1967616 {
		t.Fatalf("done/total=%d/%d", meta.Done, meta.Total)
	}
	raw2 := []byte(`{"done":0,"note":"failed","stage":"result","total":0,"type":"ota_progress"}`)
	var fail model.DeviceUplink
	if err := json.Unmarshal(raw2, &fail); err != nil {
		t.Fatal(err)
	}
	if fail.Stage != "result" || fail.Note != "failed" {
		t.Fatalf("fail=%+v", fail)
	}
}

func TestOTAStageQuiet(t *testing.T) {
	if !otaStageQuiet("verify") || !otaStageQuiet("flash") {
		t.Fatal("verify/flash should be quiet")
	}
	if otaStageQuiet("download") || otaStageQuiet("result") {
		t.Fatal("download/result should not be quiet")
	}
}

func TestOTAProgressDownloadDoneMonotonic(t *testing.T) {
	m := &ConnManager{otaSessions: make(map[string]*otaSession), log: zap.NewNop()}
	m.onOTAProgress("S1", &model.DeviceUplink{Type: "ota_progress", Stage: "download", Done: 1000, Total: 2000})
	m.onOTAProgress("S1", &model.DeviceUplink{Type: "ota_progress", Stage: "flash", Done: 0, Total: 2000})
	m.otaMu.Lock()
	got := m.otaSessions["S1"].lastDone
	stage := m.otaSessions["S1"].lastStage
	m.otaMu.Unlock()
	if got != 1000 {
		t.Fatalf("flash done=0 must not reset tracked_done, got %d", got)
	}
	if stage != "flash" {
		t.Fatalf("stage=%s", stage)
	}
	if !m.otaStageQuiet("S1") {
		t.Fatal("flash should be quiet")
	}
}

func TestScheduleOTARepushTakesEarliest(t *testing.T) {
	m := &ConnManager{otaSessions: make(map[string]*otaSession), log: zap.NewNop()}
	m.scheduleOTARepush("S1", 10*time.Second, "later")
	m.scheduleOTARepush("S1", 2*time.Second, "sooner")
	m.otaMu.Lock()
	s := m.otaSessions["S1"]
	reason := s.nextPushReason
	at := s.nextPushAt
	m.otaMu.Unlock()
	if reason != "sooner" {
		t.Fatalf("reason=%s", reason)
	}
	if time.Until(at) > 3*time.Second {
		t.Fatalf("expected ~2s schedule, at=%v", at)
	}
}

func TestScheduleOTARepushOnceDedup(t *testing.T) {
	m := &ConnManager{otaSessions: make(map[string]*otaSession), log: zap.NewNop()}
	if !m.scheduleOTARepushOnce("S1", 10*time.Second, "ack_busy") {
		t.Fatal("first schedule should succeed")
	}
	if m.scheduleOTARepushOnce("S1", 10*time.Second, "ack_busy") {
		t.Fatal("duplicate schedule should be false")
	}
}

func TestDeviceUplinkBootField(t *testing.T) {
	raw := []byte(`{"type":"ping","sn":"S1","fw":"2.2.0","boot":1,"bat":80}`)
	var meta model.DeviceUplink
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if !meta.Boot.Bool() || meta.FW != "2.2.0" || meta.Type != "ping" {
		t.Fatalf("meta=%+v", meta)
	}
	raw2 := []byte(`{"type":"ping","fw":"2.1.0","unknown_field":true}`)
	var meta2 model.DeviceUplink
	if err := json.Unmarshal(raw2, &meta2); err != nil {
		t.Fatal(err)
	}
	if meta2.Boot.Bool() {
		t.Fatal("boot should default false")
	}
}
