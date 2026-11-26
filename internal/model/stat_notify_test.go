package model

import "testing"

func TestParseSTATCharge(t *testing.T) {
	cases := []struct {
		in   string
		bat  int
		ok   bool
	}{
		{"CHARGE:78", 78, true},
		{"CHARGE:78%", 78, true},
		{"charge:5", 5, true},
		{"CHARGE: 100", 100, true},
		{"CHARGE:0", 0, true},
		{"CHARGE:101", 0, false},
		{"CHG:78", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		bat, ok := ParseSTATCharge(c.in)
		if ok != c.ok || (ok && bat != c.bat) {
			t.Fatalf("%q => bat=%d ok=%v want bat=%d ok=%v", c.in, bat, ok, c.bat, c.ok)
		}
	}
}

func TestFormatSTATCharge(t *testing.T) {
	if got := FormatSTATCharge(78); got != "CHARGE:78" {
		t.Fatal(got)
	}
}
