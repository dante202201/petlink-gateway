package model

import (
	"encoding/json"
	"testing"
)

func TestAudioCodecUnmarshal(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{`0`, CodecOpus},
		{`"0"`, CodecOpus},
		{`"opus"`, CodecOpus},
		{`1`, CodecMP3},
		{`"1"`, CodecMP3},
		{`"mp3"`, CodecMP3},
		{`"MP3"`, CodecMP3},
		{`null`, ""},
		{`""`, ""},
	}
	for _, tc := range cases {
		var c AudioCodec
		if err := json.Unmarshal([]byte(tc.raw), &c); err != nil {
			t.Fatalf("unmarshal %s: %v", tc.raw, err)
		}
		if string(c) != tc.want {
			t.Fatalf("%s => %q, want %q", tc.raw, c, tc.want)
		}
	}
}

func TestAudioCodecInUplink(t *testing.T) {
	raw := []byte(`{"type":"audio","codec":1,"seq":0,"total":2,"end":1,"ts":1}`)
	var u DeviceUplink
	if err := json.Unmarshal(raw, &u); err != nil {
		t.Fatal(err)
	}
	if !u.Codec.IsMP3() {
		t.Fatalf("codec=%q want mp3", u.Codec)
	}
	out, err := json.Marshal(u.Codec)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "1" {
		t.Fatalf("marshal=%s want 1", out)
	}
}

func TestAudioCodecHelpers(t *testing.T) {
	c := AudioCodec(CodecMP3)
	if c.OSSContentType() != "audio/mpeg" || c.FileExt() != "mp3" || c.FrameDurationSec() != 0.06 {
		t.Fatalf("mp3 helpers: %s %s %v", c.OSSContentType(), c.FileExt(), c.FrameDurationSec())
	}
	c = AudioCodec(CodecOpus)
	if c.OSSContentType() != "audio/opus" || c.FileExt() != "opus" || c.FrameDurationSec() != 0.06 {
		t.Fatalf("opus helpers: %s %s %v", c.OSSContentType(), c.FileExt(), c.FrameDurationSec())
	}
}
