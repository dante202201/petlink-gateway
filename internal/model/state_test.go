package model

import "testing"

func TestStateInSleep(t *testing.T) {
	for _, s := range []string{"sleep", "SLEEP", "sleeping", "night_sleep", "deep_sleep"} {
		if !StateInSleep(s) {
			t.Fatalf("%q should be sleep", s)
		}
	}
	if StateInSleep("idle") || StateInSleep("") {
		t.Fatal("idle/empty should not be sleep")
	}
}

func TestStateLabelCN(t *testing.T) {
	if StateLabelCN("sleep") != "睡眠" {
		t.Fatal(StateLabelCN("sleep"))
	}
	if StateLabelCN("idle") != "空闲" {
		t.Fatal(StateLabelCN("idle"))
	}
	if StateLabelCN("recording") != "录音中" {
		t.Fatal(StateLabelCN("recording"))
	}
}
