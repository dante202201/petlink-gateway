package model

import "testing"

func TestTranslateStatusAppReady(t *testing.T) {
	if TranslateStatusAppReady("pending") || TranslateStatusAppReady("translating") {
		t.Fatal("processing must not be app-ready")
	}
	if !TranslateStatusAppReady("ok") || !TranslateStatusAppReady("fail") {
		t.Fatal("terminal must be app-ready")
	}
	if !TranslateStatusAppReady("OK") {
		t.Fatal("case insensitive")
	}
}
