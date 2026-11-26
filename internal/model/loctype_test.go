package model

import (
	"encoding/json"
	"testing"
)

func TestLocTypeCodeWiFi(t *testing.T) {
	cases := []string{`1`, `"1"`, `"wifi"`, `"WiFi"`}
	for _, raw := range cases {
		var l LocTypeCode
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if l != LocTypeWiFi {
			t.Fatalf("%s -> %q want 1", raw, l)
		}
	}
	out, err := json.Marshal(LocTypeCode("wifi"))
	if err != nil || string(out) != "1" {
		t.Fatalf("marshal=%s err=%v", out, err)
	}
}
