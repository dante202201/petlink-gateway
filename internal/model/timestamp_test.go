package model

import "testing"

func TestFrameDurationUS(t *testing.T) {
	if FrameDurationUS(AudioKindEvent) != 60_000 {
		t.Fatalf("event want 60ms, got %d", FrameDurationUS(AudioKindEvent))
	}
	if FrameDurationUS(AudioKindFile) != 20_000 {
		t.Fatalf("file want 20ms, got %d", FrameDurationUS(AudioKindFile))
	}
	if FrameDurationUS(99) != EventFrameDurationUS {
		t.Fatal("unknown kind should use event")
	}
	if FrameDurationSecForKind(AudioKindFile) != 0.02 {
		t.Fatalf("file sec=%v", FrameDurationSecForKind(AudioKindFile))
	}
	if DefaultFrameDurationUS != EventFrameDurationUS {
		t.Fatal("DefaultFrameDurationUS alias broken")
	}
}
