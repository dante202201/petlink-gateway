package gateway

import (
	"testing"

	"pet-translate-gateway/internal/model"
)

func TestAudioFragmentDedupKeyFirstVsRetransmit(t *testing.T) {
	base := int64(1_720_000_000_000_000)
	abs := base + 60_000
	k1 := audioFragmentDedupKey("SN", model.AudioKindEvent, 1, abs, 0xabc)
	k2 := audioFragmentDedupKey("SN", model.AudioKindEvent, 1, abs, 0xabc)
	if k1 != k2 {
		t.Fatalf("want same key, got %q vs %q", k1, k2)
	}
	// total 不得进入键：累计 total 不同时仍应同一键（同一 seq+absTS）
	want := "SN:k0:1:1720000000060000:2748"
	if k1 != want {
		t.Fatalf("got %q want %q", k1, want)
	}
}

func TestAudioFragmentDedupKeyNoTotal(t *testing.T) {
	ts := int64(1_720_000_000_000_000)
	// 无 crc：sn+kind+seq+absTS
	a := audioFragmentDedupKey("SN", 0, 3, ts, 0)
	if a != "SN:k0:3:1720000000000000" {
		t.Fatalf("got %q", a)
	}
}

func TestCanonicalDedupAbsTS(t *testing.T) {
	base := int64(1_720_000_000_000_000)
	live := &model.DeviceUplink{Seq: 5}
	// 实时：统一到 base+seq*60ms
	got := canonicalDedupAbsTS(live, base, base+12345, model.AudioKindEvent)
	want := base + 5*model.EventFrameDurationUS
	if got != want {
		t.Fatalf("live got %d want %d", got, want)
	}
	rt := &model.DeviceUplink{Seq: 5, Retransmit: true, TS: base + 5*model.EventFrameDurationUS}
	got2 := canonicalDedupAbsTS(rt, base, rt.TS, model.AudioKindEvent)
	if got2 != want {
		t.Fatalf("retransmit same session got %d want %d", got2, want)
	}
	oldAbs := base - 5_000_000
	rtOld := &model.DeviceUplink{Seq: 2, Retransmit: true, TS: oldAbs}
	got3 := canonicalDedupAbsTS(rtOld, base, oldAbs, model.AudioKindEvent)
	if got3 != oldAbs {
		t.Fatalf("old segment retransmit should keep abs, got %d", got3)
	}
}

func TestAudioFragmentDedupKeyKindSeparated(t *testing.T) {
	ts := int64(1_720_000_000_000_000)
	a := audioFragmentDedupKey("SN", model.AudioKindEvent, 3, ts, 0)
	b := audioFragmentDedupKey("SN", model.AudioKindFile, 3, ts, 0)
	if a == b {
		t.Fatal("event/file must not share dedup bucket")
	}
}

func TestSessionBaseForDedup(t *testing.T) {
	meta := &model.DeviceUplink{Seq: 0}
	if g := sessionBaseForDedup(0, meta, 100); g != 100 {
		t.Fatalf("seq0 abs as base: %d", g)
	}
	if g := sessionBaseForDedup(50, meta, 100); g != 50 {
		t.Fatalf("prefer conn base: %d", g)
	}
}

func TestSessionBaseForDedupRetransmitOtherSegment(t *testing.T) {
	live := int64(1_720_000_000_000_000)
	old := live - 5_000_000
	meta := &model.DeviceUplink{Seq: 0, Retransmit: true}
	if g := sessionBaseForDedup(live, meta, old); g != old {
		t.Fatalf("old retransmit seq0 want own abs, got %d", g)
	}
	meta2 := &model.DeviceUplink{Seq: 3, Retransmit: true}
	if g := sessionBaseForDedup(live, meta2, live+60_000); g != live {
		t.Fatalf("same-session retransmit want live base, got %d", g)
	}
	if g := sessionBaseForDedup(live, meta2, old+60_000); g != 0 {
		t.Fatalf("other-segment seq>0 should fallback, got %d", g)
	}
}

func TestAbsInLiveSession(t *testing.T) {
	live := int64(1_720_000_000_000_000)
	if absInLiveSession(0, live) || !absInLiveSession(live, live+60_000) {
		t.Fatal("in-window")
	}
	if absInLiveSession(live, live-1) {
		t.Fatal("older chunk is not current live session")
	}
}
