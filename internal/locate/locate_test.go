package locate

import (
	"testing"

	"pet-translate-gateway/internal/model"
)

func TestFingerprintStable(t *testing.T) {
	a := []model.WifiAP{
		{MAC: "AA:BB:CC:01", RSSI: -40},
		{MAC: "aa:bb:cc:02", RSSI: -50},
	}
	b := []model.WifiAP{
		{MAC: "aa:bb:cc:02", RSSI: -10},
		{MAC: "aa-bb-cc-01", RSSI: -90},
	}
	fa := Fingerprint(a)
	fb := Fingerprint(b)
	if fa != fb {
		t.Fatalf("fingerprint mismatch: %s vs %s", fa, fb)
	}
	if len(fa) != 40 {
		t.Fatalf("unexpected sha1 hex len: %d", len(fa))
	}
}
