package gateway

import (
	"testing"

	"go.uber.org/zap"

	"pet-translate-gateway/internal/store"
)

func TestAllowAudioByBindingNilStore(t *testing.T) {
	m := &ConnManager{log: zap.NewNop()}
	dc := &DeviceConn{SN: "S1"}
	if !m.allowAudioByBinding(dc) {
		t.Fatal("nil store should allow")
	}
}

func TestAllowAudioByBindingCache(t *testing.T) {
	// store 非 nil 但无 DB：Lookup 返回 nil → 未绑定；随后走缓存
	m := &ConnManager{log: zap.NewNop(), store: &store.Store{}}
	dc := &DeviceConn{SN: "S1"}
	if m.allowAudioByBinding(dc) {
		t.Fatal("no a_emb_sn row should reject")
	}
	if dc.bindCheckedAt.IsZero() || dc.bindOK {
		t.Fatalf("cache not set: checked=%v ok=%v", dc.bindCheckedAt, dc.bindOK)
	}
	// 强制改缓存，确认 TTL 内不再查库
	dc.bindOK = true
	if !m.allowAudioByBinding(dc) {
		t.Fatal("cached bound should allow")
	}
	dc.bindOK = false
	if m.allowAudioByBinding(dc) {
		t.Fatal("cached unbound should reject")
	}
}

func TestEmbHasActiveBindingViaStore(t *testing.T) {
	d := store.EmbDevice{UserID: 1, PetID: "9"}
	if !d.HasActiveBinding() {
		t.Fatal("expected active")
	}
}
