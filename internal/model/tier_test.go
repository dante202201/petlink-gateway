package model

import "testing"

func TestNormalizeTier(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"normal", TierNormal},
		{"NORMAL", TierNormal},
		{"work", TierNormal},
		{"正常", TierNormal},
		{"light", TierLight},
		{"light_sleep", TierLight},
		{"浅睡", TierLight},
		{"deep", TierDeep},
		{"deep_sleep", TierDeep},
		{"深睡", TierDeep},
		{"", ""},
		{"unknown", ""},
	}
	for _, tc := range cases {
		if got := NormalizeTier(tc.in); got != tc.want {
			t.Fatalf("NormalizeTier(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestTierLabelCN(t *testing.T) {
	if TierLabelCN("light") != "浅睡" {
		t.Fatal(TierLabelCN("light"))
	}
	if TierLabelCN("deep") != "深睡" {
		t.Fatal(TierLabelCN("deep"))
	}
	if TierLabelCN("normal") != "正常" {
		t.Fatal(TierLabelCN("normal"))
	}
}

func TestTierInSleep(t *testing.T) {
	if TierInSleep("normal") {
		t.Fatal("normal should not sleep")
	}
	if !TierInSleep("light") || !TierInSleep("deep") {
		t.Fatal("light/deep should sleep")
	}
}
