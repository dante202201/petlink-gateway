package config

import "testing"

func TestAudioConfigAcceptsKind(t *testing.T) {
	cases := []struct {
		accept string
		k0, k1 bool
	}{
		{"", true, true},
		{"both", true, true},
		{"event", true, false},
		{"file", false, true},
	}
	for _, c := range cases {
		cfg := AudioConfig{Accept: c.accept}
		if got := cfg.AcceptsKind(0); got != c.k0 {
			t.Fatalf("accept=%q kind0: got %v want %v", c.accept, got, c.k0)
		}
		if got := cfg.AcceptsKind(1); got != c.k1 {
			t.Fatalf("accept=%q kind1: got %v want %v", c.accept, got, c.k1)
		}
	}
}

func TestAudioConfigTranslatesKind(t *testing.T) {
	cases := []struct {
		translate string
		k0, k1    bool
	}{
		{"", true, false},
		{"event", true, false},
		{"file", false, true},
		{"both", true, true},
		{"none", false, false},
	}
	for _, c := range cases {
		cfg := AudioConfig{Translate: c.translate}
		if got := cfg.TranslatesKind(0); got != c.k0 {
			t.Fatalf("translate=%q kind0: got %v want %v", c.translate, got, c.k0)
		}
		if got := cfg.TranslatesKind(1); got != c.k1 {
			t.Fatalf("translate=%q kind1: got %v want %v", c.translate, got, c.k1)
		}
	}
}

func TestAudioConfigArchivesKind(t *testing.T) {
	t.Run("default archive other", func(t *testing.T) {
		cfg := AudioConfig{Accept: "both", Translate: "event"}
		if !cfg.ArchivesKind(0) || !cfg.ArchivesKind(1) {
			t.Fatal("both should archive by default")
		}
	})
	t.Run("archive_other false", func(t *testing.T) {
		f := false
		cfg := AudioConfig{Accept: "both", Translate: "event", ArchiveOther: &f}
		if !cfg.ArchivesKind(0) {
			t.Fatal("translate path still archives")
		}
		if cfg.ArchivesKind(1) {
			t.Fatal("file path should not archive when archive_other=false")
		}
	})
	t.Run("accept event only", func(t *testing.T) {
		cfg := AudioConfig{Accept: "event", Translate: "event"}
		if cfg.ArchivesKind(1) {
			t.Fatal("rejected kind must not archive")
		}
	})
}
