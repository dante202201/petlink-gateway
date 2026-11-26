package model

import "testing"

func TestNormalizeMotion(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"walk", MotionWalk},
		{"WALK", MotionWalk},
		{"走", MotionWalk},
		{"run", MotionRun},
		{"跑", MotionRun},
		{"prone", MotionProne},
		{"趴", MotionProne},
		{"lie", MotionLie},
		{"躺", MotionLie},
		{"lying", MotionLie},
		{"stand", MotionStand},
		{"站", MotionStand},
		{"", ""},
		{"unknown", ""},
	}
	for _, tc := range cases {
		if got := NormalizeMotion(tc.in); got != tc.want {
			t.Fatalf("NormalizeMotion(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestMotionLabelCN(t *testing.T) {
	if MotionLabelCN("walk") != "走" {
		t.Fatal(MotionLabelCN("walk"))
	}
	if MotionLabelCN("stand") != "站" {
		t.Fatal(MotionLabelCN("stand"))
	}
}
