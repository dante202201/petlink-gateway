package store

import "testing"

func TestEmbDeviceHasActiveBinding(t *testing.T) {
	if (&EmbDevice{}).HasActiveBinding() {
		t.Fatal("empty should be unbound")
	}
	if (&EmbDevice{UserID: 1}).HasActiveBinding() {
		t.Fatal("pet empty should be unbound")
	}
	if (&EmbDevice{UserID: 1, PetID: "0"}).HasActiveBinding() {
		t.Fatal("pet 0 should be unbound")
	}
	if (&EmbDevice{UserID: 0, PetID: "123"}).HasActiveBinding() {
		t.Fatal("user 0 should be unbound")
	}
	var nilDev *EmbDevice
	if nilDev.HasActiveBinding() {
		t.Fatal("nil should be unbound")
	}
	if !(&EmbDevice{UserID: 42, PetID: "123"}).HasActiveBinding() {
		t.Fatal("valid bind should be active")
	}
}
