package model

import "testing"

func TestBoundFromProfile(t *testing.T) {
	cases := []struct {
		pid, breed string
		want       int
	}{
		{"623", "cat", 1},
		{"623", "dog", 1},
		{"", "cat", 0},
		{"623", "", 0},
		{"4294967295", "cat", 0},
		{"623", "0", 0},
		{"0", "cat", 0},
	}
	for _, tc := range cases {
		got := BoundFromProfile(tc.pid, tc.breed)
		if got != tc.want {
			t.Fatalf("BoundFromProfile(%q,%q)=%d want %d", tc.pid, tc.breed, got, tc.want)
		}
	}
}
