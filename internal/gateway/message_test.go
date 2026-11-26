package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"go.uber.org/zap"

	"pet-translate-gateway/internal/model"
	"pet-translate-gateway/internal/util"
)

func TestDeviceUplinkFields(t *testing.T) {
	raw := []byte(`{
		"sn":"SN001","type":"audio","codec":"opus","seq":1,"total":3,"end":0,
		"pid":"123","db":-45,"bat":80,"charge":false,"rssi":-50,"ts":20000,"crc":12345,
		"loc_type":1,"loc_macs":["AA:BB:CC:DD:EE:01","AA:BB:CC:DD:EE:02"]
	}`)
	var meta model.DeviceUplink
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if !meta.ExpectsBinary() {
		t.Fatal("audio meta should expect binary")
	}
	if !meta.HasLocation() {
		t.Fatal("expected location")
	}
	if meta.Total != 3 || meta.PID != "123" || len(meta.LocMacs) != 2 {
		t.Fatalf("unexpected meta: %+v", meta)
	}
	if meta.LocType != model.LocTypeWiFi {
		t.Fatalf("loc_type=%q want 1", meta.LocType)
	}
	if abs := meta.AbsoluteTS(1720000000000000); abs != 1720000000020000 {
		t.Fatalf("absolute ts=%d", abs)
	}
}

func TestLocateNoBinary(t *testing.T) {
	raw := []byte(`{"type":"locate","sn":"SN001","loc_type":1,"loc_macs":["aa:bb"],"loc_ts":1}`)
	var meta model.DeviceUplink
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.ExpectsBinary() {
		t.Fatal("locate should not expect binary")
	}
}

func TestRetransmitExpectsBinary(t *testing.T) {
	raw := []byte(`{"type":"retransmit","seq":5,"total":12,"ts":1720000000100000,"crc":1,"codec":"opus","pid":"456"}`)
	var meta model.DeviceUplink
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if !meta.ExpectsBinary() {
		t.Fatal("retransmit should expect binary")
	}
	if abs := meta.AbsoluteTS(999); abs != 1720000000100000 {
		t.Fatalf("retransmit ts should stay absolute, got %d", abs)
	}
}

func TestSlimRetransmitFlag(t *testing.T) {
	raw := []byte(`{"sn":"S1","type":"retransmit","seq":3,"retransmit":1,"crc":99}`)
	var meta model.DeviceUplink
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if !meta.IsRetransmit() || !meta.ExpectsBinary() {
		t.Fatalf("slim retransmit not recognized: %+v", meta)
	}
}

func TestFillFromSessionCache(t *testing.T) {
	base := int64(1_720_000_000_000_000)
	dc := &DeviceConn{SN: "S1"}
	dc.kindAudio[model.AudioKindEvent] = audioKindConnState{
		fragCache: map[int]fragCacheEntry{
			3: {Codec: "opus", Total: 12, TS: base + 60_000, PID: "123"},
		},
		lastCodec:  "opus",
		lastTotal:  12,
		lastBaseTS: base,
	}
	meta := &model.DeviceUplink{Type: "retransmit", Seq: 3, Retransmit: true}
	if !dc.fillFromSessionCache(meta, model.AudioKindEvent) {
		t.Fatal("expected fill")
	}
	if meta.Codec != "opus" || meta.Total != 12 || meta.TS != base+60_000 || meta.PID != "123" {
		t.Fatalf("filled=%+v", meta)
	}
}

func TestRememberFragmentRetransmitDoesNotClobberLiveBase(t *testing.T) {
	live := int64(1_720_000_000_000_000)
	dc := &DeviceConn{SN: "S1"}
	dc.kindAudio[model.AudioKindEvent] = audioKindConnState{
		baseTS: live, lastBaseTS: live, sawSeq: true, lastSeq: 10,
		fragCache: map[int]fragCacheEntry{10: {TS: live + 600_000}},
	}
	old := live - 5_000_000
	meta := &model.DeviceUplink{Type: "retransmit", Seq: 0, Retransmit: true, TS: old}
	dc.rememberFragment(meta, old, model.AudioKindEvent)
	st := dc.kindState(model.AudioKindEvent)
	if st.baseTS != live || st.lastBaseTS != live || st.lastSeq != 10 || !st.sawSeq {
		t.Fatalf("live state clobbered: %+v", st)
	}
	if _, ok := st.fragCache[10]; !ok {
		t.Fatal("live fragCache cleared by retransmit seq=0")
	}
}

func TestRememberFragmentLiveSeq0ResetsBase(t *testing.T) {
	old := int64(1_720_000_000_000_000)
	live := old + 8_000_000
	dc := &DeviceConn{SN: "S1"}
	dc.kindAudio[model.AudioKindEvent] = audioKindConnState{baseTS: old, sawSeq: true, lastSeq: 40}
	meta := &model.DeviceUplink{Type: "audio", Seq: 0, TS: live}
	dc.rememberFragment(meta, live, model.AudioKindEvent)
	st := dc.kindState(model.AudioKindEvent)
	if st.baseTS != live || st.sawSeq || st.lastSeq != 0 {
		t.Fatalf("live seq0 should open new segment: %+v", st)
	}
}

func TestPickAudioSessionBaseRetransmitKeepsLiveBase(t *testing.T) {
	live := int64(1_720_000_000_000_000)
	old := live - 5_000_000
	dc := &DeviceConn{SN: "S1"}
	dc.kindAudio[model.AudioKindEvent] = audioKindConnState{baseTS: live}
	m := &ConnManager{log: zap.NewNop()}
	meta := &model.DeviceUplink{Type: "retransmit", Seq: 0, Retransmit: true, TS: old}
	got := m.pickAudioSessionBase(dc, meta, old, model.AudioKindEvent)
	if got != old {
		t.Fatalf("session=%d want old %d", got, old)
	}
	if dc.kindState(model.AudioKindEvent).baseTS != live {
		t.Fatalf("live base overwritten: %d", dc.kindState(model.AudioKindEvent).baseTS)
	}
}

func TestPickAudioSessionBaseFirmwareBugNewSegment(t *testing.T) {
	live := int64(1_720_000_000_000_000)
	newer := live + 2_000_000
	dc := &DeviceConn{SN: "S1"}
	dc.kindAudio[model.AudioKindEvent] = audioKindConnState{baseTS: live}
	m := &ConnManager{log: zap.NewNop()}
	meta := &model.DeviceUplink{Type: "audio", Seq: 0, Retransmit: true, TS: newer}
	got := m.pickAudioSessionBase(dc, meta, newer, model.AudioKindEvent)
	if got != newer || meta.IsRetransmit() {
		t.Fatalf("firmware bug path: session=%d retransmit=%v", got, meta.IsRetransmit())
	}
	if dc.kindState(model.AudioKindEvent).baseTS != newer {
		t.Fatalf("want new live base %d got %d", newer, dc.kindState(model.AudioKindEvent).baseTS)
	}
}

func TestUnboundPID(t *testing.T) {
	u := model.DeviceUplink{PID: ""}
	if !u.IsUnboundPID() {
		t.Fatal("expected unbound")
	}
	u.PID = "4294967295"
	if !u.IsUnboundPID() {
		t.Fatal("legacy full-F should be unbound")
	}
}

func TestCommandFirmwareFormat(t *testing.T) {
	cmd := BuildCommand(model.CmdReboot, nil)
	b, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["type"] != "cmd" {
		t.Fatalf("type=%v want cmd", got["type"])
	}
	if got["cmd"] != "reboot" {
		t.Fatalf("cmd=%v", got["cmd"])
	}
	if _, ok := got["data"]; !ok {
		t.Fatal("missing data field")
	}
}

func TestCRCRoundTrip(t *testing.T) {
	opus := []byte("FAKE_OPUS_CHUNK_0")
	sum := util.CRC32IEEE(opus)
	if sum == 0 {
		t.Fatal("unexpected zero crc")
	}
	if util.CRC32IEEE(opus) != sum {
		t.Fatal("crc not stable")
	}
}

func TestBool01(t *testing.T) {
	var b util.Bool01
	if err := json.Unmarshal([]byte(`1`), &b); err != nil || !b.Bool() {
		t.Fatalf("1 -> %v err=%v", b, err)
	}
	if err := json.Unmarshal([]byte(`0`), &b); err != nil || b.Bool() {
		t.Fatalf("0 -> %v err=%v", b, err)
	}
}

func TestClipJSONForLogRedactsToken(t *testing.T) {
	raw := []byte(`{"sn":"S1","token":"secret","type":"audio"}`)
	got := clipJSONForLog(raw, 200)
	if strings.Contains(got, "secret") {
		t.Fatalf("token not redacted: %s", got)
	}
	if !strings.Contains(got, `"token":"***"`) {
		t.Fatalf("missing redacted token: %s", got)
	}
}

func TestRateLimiterAllow(t *testing.T) {
	r := newRateLimiter(3)
	if !r.Allow() || !r.Allow() || !r.Allow() {
		t.Fatal("first 3 should allow")
	}
	if r.Allow() {
		t.Fatal("4th should deny within window")
	}
}

func TestDeviceConnPendingMetaPair(t *testing.T) {
	dc := &DeviceConn{}
	if dc.hasPendingMeta() {
		t.Fatal("empty")
	}
	dc.setPendingMeta(&model.DeviceUplink{Type: "audio", Seq: 1})
	if !dc.hasPendingMeta() {
		t.Fatal("want pending")
	}
	got := dc.takePendingMeta()
	if got == nil || got.Seq != 1 || dc.hasPendingMeta() {
		t.Fatalf("take failed: %+v pending=%v", got, dc.hasPendingMeta())
	}
}

func TestAutoRecordAckHelpers(t *testing.T) {
	if !isAutoRecordAckCmd("enable_auto_record") || !isAutoRecordAckCmd("enable_translate") {
		t.Fatal("want auto_record cmds")
	}
	if isAutoRecordAckCmd("set_volume") {
		t.Fatal("set_volume should not apply auto_record")
	}
	ok := &model.DeviceUplink{Type: "ack", Cmd: "enable_auto_record", Result: 0, Event: "success"}
	if !isDeviceCmdAckSuccess(ok) {
		t.Fatal("result=0 / event=success should succeed")
	}
	fail := &model.DeviceUplink{Type: "ack", Cmd: "enable_auto_record", Result: 1}
	if isDeviceCmdAckSuccess(fail) {
		t.Fatal("result=1 should fail")
	}
}
