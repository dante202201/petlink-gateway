package audio

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestOpusDecodeMode(t *testing.T) {
	if OpusDecodeMode(AppEncodeOpts{}) != "pion" {
		t.Fatal("default want pion")
	}
	if OpusDecodeMode(AppEncodeOpts{OpusDecode: "ffmpeg"}) != "ffmpeg" {
		t.Fatal("want ffmpeg")
	}
	if OpusDecodeMode(AppEncodeOpts{OpusDecode: "libopus"}) != "ffmpeg" {
		t.Fatal("libopus alias want ffmpeg")
	}
}

func TestEncodeForAppEmpty(t *testing.T) {
	_, _, _, err := EncodeForApp(context.Background(), "ffmpeg", nil, "opus", AppEncodeOpts{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestAppEncodeOptsNormalize(t *testing.T) {
	o := AppEncodeOpts{}.normalize()
	if o.Format != AppFormatMP3 || o.SampleRate != 16000 || o.MP3Bitrate != 96000 || o.RateRatio != 1 {
		// SampleRate 默认仍 16000（normalize）；流水线配置默认 44100
		t.Fatalf("normalize=%+v", o)
	}
}

func TestBuildAppAudioFilter(t *testing.T) {
	passthrough := buildAppAudioFilter(AppEncodeOpts{SampleRate: 44100, RateRatio: 1}.normalize())
	if passthrough != "aresample=44100" {
		t.Fatalf("no-opt chain should be aresample only, af=%q", passthrough)
	}

	af := buildAppAudioFilter(AppEncodeOpts{
		SampleRate: 44100, Highpass: true, HighpassHz: 120, PresenceDb: 3, MakeupDb: 6, Limiter: true, RateRatio: 1,
	}.normalize())
	if !strings.Contains(af, "aresample=44100") || !strings.Contains(af, "highpass=f=120") ||
		!strings.Contains(af, "highshelf=") || !strings.Contains(af, "volume=6.0dB") || !strings.Contains(af, "alimiter=") {
		t.Fatalf("want aresample+hp120+presence+makeup+limiter, af=%q", af)
	}
	if strings.Contains(af, "loudnorm=") || strings.Contains(af, "afftdn=") {
		t.Fatalf("default chain should not loudnorm/afftdn: %q", af)
	}

	afLegacy := buildAppAudioFilter(AppEncodeOpts{
		SampleRate: 16000, Denoise: true, DenoiseNR: 12, Loudnorm: true, RateRatio: 0.67,
	}.normalize())
	if !strings.Contains(afLegacy, "atempo=") || !strings.Contains(afLegacy, "afftdn=") || !strings.Contains(afLegacy, "loudnorm=") {
		t.Fatalf("optional denoise/loudnorm: %q", afLegacy)
	}

	afPitch := buildAppAudioFilter(AppEncodeOpts{SampleRate: 16000, RateRatio: 0.67, RatePitch: true}.normalize())
	if !strings.Contains(afPitch, "asetrate=") || strings.Contains(afPitch, "atempo=") {
		t.Fatalf("RatePitch should use asetrate: %q", afPitch)
	}

	chain := atempoFilters(0.4)
	if len(chain) < 2 || chain[0] != "atempo=0.5" {
		t.Fatalf("atempo chain for 0.4: %v", chain)
	}
}

func TestPCM16kToMP3RequiresFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	pcm := make([]byte, 640)
	out, err := PCM16kToMP3(context.Background(), "ffmpeg", pcm, AppEncodeOpts{})
	if err != nil {
		t.Fatalf("PCM16kToMP3: %v", err)
	}
	if len(out) < 100 {
		t.Fatalf("mp3 too short: %d", len(out))
	}
}
