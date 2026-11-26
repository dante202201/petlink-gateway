package util

import (
	"encoding/json"
	"testing"
)

func TestBoolTF(t *testing.T) {
	var b BoolTF
	for _, raw := range []string{`true`, `1`, `"true"`, `"CHG"`, `"chg"`} {
		if err := json.Unmarshal([]byte(raw), &b); err != nil || !b.Bool() {
			t.Fatalf("%s => %v err=%v", raw, b, err)
		}
	}
	for _, raw := range []string{`false`, `0`, `"false"`, `"NCH"`, `"nch"`} {
		if err := json.Unmarshal([]byte(raw), &b); err != nil || b.Bool() {
			t.Fatalf("%s => %v err=%v", raw, b, err)
		}
	}
	out, _ := json.Marshal(BoolTF(true))
	if string(out) != "true" {
		t.Fatalf("marshal=%s", out)
	}
	out, _ = json.Marshal(BoolTF(false))
	if string(out) != "false" {
		t.Fatalf("marshal=%s", out)
	}
}
