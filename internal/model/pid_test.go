package model

import (
	"encoding/json"
	"testing"
)

func TestPetIDStringAndNumber(t *testing.T) {
	var a PetID
	if err := json.Unmarshal([]byte(`"123"`), &a); err != nil || a != "123" {
		t.Fatalf("str: %q err=%v", a, err)
	}
	var b PetID
	if err := json.Unmarshal([]byte(`10086`), &b); err != nil || b != "10086" {
		t.Fatalf("num: %q err=%v", b, err)
	}
	out, _ := json.Marshal(PetID("123"))
	if string(out) != `"123"` {
		t.Fatalf("marshal=%s", out)
	}
	if !IsUnboundPetID("") || !IsUnboundPetID("4294967295") || !IsUnboundPetID("FFFFFFFF") || !IsUnboundPetID("0") {
		t.Fatal("unbound checks should pass for \"\", 0, 4294967295, FFFFFFFF")
	}
	if IsUnboundPetID("123") {
		t.Fatal("123 should be bound")
	}
}

func TestPetIDRejectNonNumeric(t *testing.T) {
	cases := []string{
		`"pet_10086"`,
		`"pet_abc"`,
		`"abc"`,
		`"PID-123"`,
		`"12-34"`,
		`"1.23"`,
	}
	for i, c := range cases {
		var p PetID
		err := json.Unmarshal([]byte(c), &p)
		if err == nil {
			t.Fatalf("case %d %s should reject, got=%q", i, c, p)
		}
	}
}

func TestPetIDCompatibilityBounds(t *testing.T) {
	cases := []struct {
		in  string
		out PetID
	}{
		{`"FFFFFFFF"`, ""},
		{`"ffffffff"`, ""},
		{`"0"`, ""},
		{`"4294967295"`, ""},
		{`0`, ""},
		{`4294967295`, ""},
		{`"1"`, "1"},
		{`"4294967294"`, "4294967294"},
	}
	for i, c := range cases {
		var p PetID
		if err := json.Unmarshal([]byte(c.in), &p); err != nil {
			t.Fatalf("case %d in=%s err=%v", i, c.in, err)
		}
		if p != c.out {
			t.Fatalf("case %d in=%s want=%q got=%q", i, c.in, c.out, p)
		}
	}
}
