package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersistFirmwareSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	base := "server:\n  addr: \":8443\"\n\nfirmware:\n  version: \"1.0.0\"\n  url: \"http://old\"\n  force: false\n  size: 1\n  md5: \"\"\n  changelog: \"\"\n\nlocate:\n  provider: mock\n"
	if err := os.WriteFile(path, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	fw := FirmwareConfig{
		Version: "1.2.3",
		URL:     "https://cdn.example.com/a.bin",
		Force:   true,
		Size:    99,
		MD5:     "abc",
		Changelog: "hi",
	}
	if err := persistFirmwareSection(path, fw); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, want := range []string{
		`version: "1.2.3"`,
		`url: "https://cdn.example.com/a.bin"`,
		`force: true`,
		`size: 99`,
		`md5: "abc"`,
		`server:`,
		`locate:`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
}
