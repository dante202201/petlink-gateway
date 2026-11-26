package audio

import (
	"context"
	"os/exec"
	"testing"
)

func TestDecodeOpusPacketsToPCM48Empty(t *testing.T) {
	if _, err := DecodeOpusPacketsToPCM48(nil); err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeOpusPacketsToPCM48PLCGaps(t *testing.T) {
	// 全空包：走 TOC 空帧 / 静音占位，验证仍能输出连续 PCM
	packets := [][]byte{nil, nil, nil}
	pcm, err := DecodeOpusPacketsToPCM48(packets)
	if err != nil {
		t.Fatal(err)
	}
	samples := len(pcm) / 2
	wantMin := 20 * 48 * len(packets)
	if samples < wantMin {
		t.Fatalf("samples=%d want >= %d", samples, wantMin)
	}
}

func TestDecodeOpusPacketsToPCM48AllBad(t *testing.T) {
	// 非法载荷应整段失败，便于 EncodeForAppFrames 回退 Ogg+ffmpeg
	packets := [][]byte{{0xFF, 0x00}, {0xFE, 0x01}}
	if _, err := DecodeOpusPacketsToPCM48(packets); err == nil {
		t.Fatal("expected error when all packets fail")
	}
}

func TestDecodeOpusPacketsContinuousRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	ctx := context.Background()
	pcm := make([]byte, 16000*2) // 1s 16k mono 静音
	bundle, err := PCM16kToOpus(ctx, "ffmpeg", pcm, 24000)
	if err != nil {
		t.Fatalf("PCM16kToOpus: %v", err)
	}
	if len(bundle.Frames) < 2 {
		t.Fatalf("frames=%d", len(bundle.Frames))
	}

	pcm48, err := DecodeOpusPacketsToPCM48(bundle.Frames)
	if err != nil {
		t.Fatalf("DecodeOpusPacketsToPCM48: %v", err)
	}
	// 约 1s @ 48k mono → 96000 samples；允许编解码边界误差
	samples := len(pcm48) / 2
	if samples < 48000*8/10 || samples > 48000*12/10 {
		t.Fatalf("pcm48 samples=%d want ~48000", samples)
	}

	mp3, ext, ct, err := EncodeForAppFrames(ctx, "ffmpeg", bundle.Frames, nil, "opus", AppEncodeOpts{})
	if err != nil {
		t.Fatalf("EncodeForAppFrames: %v", err)
	}
	if ext != "mp3" || ct != "audio/mpeg" || len(mp3) < 200 {
		t.Fatalf("mp3 ext=%s ct=%s len=%d", ext, ct, len(mp3))
	}
}
