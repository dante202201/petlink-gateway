package audio

import (
	"bytes"
	"context"
	"os/exec"
	"testing"
)

func TestMuxOpusPacketsToOgg(t *testing.T) {
	// TOC config=1 → SILK 20ms（设备上行典型）
	packets := [][]byte{
		{0x08, 0xFF, 0xFE},
		{0x08, 0xFF, 0xFD},
	}
	if OpusPacketFrameDurationMs(packets[0]) != 20 {
		t.Fatalf("frame ms=%d want 20", OpusPacketFrameDurationMs(packets[0]))
	}
	ogg, err := MuxOpusPacketsToOgg(packets, 16000)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(ogg, []byte("OggS")) {
		t.Fatalf("missing ogg magic: %x", ogg[:4])
	}
	if ogg[5]&0x02 == 0 {
		t.Fatal("first page missing BOS flag")
	}
	if len(ogg) < 50 {
		t.Fatalf("ogg too short: %d", len(ogg))
	}
	round, err := ExtractOpusPackets(ogg)
	if err != nil {
		t.Fatalf("ExtractOpusPackets: %v", err)
	}
	if len(round) != len(packets) {
		t.Fatalf("packet count: got %d want %d", len(round), len(packets))
	}
}

func TestOpusPacketFrameDurationMs60(t *testing.T) {
	// SILK config=3 → 60ms（TTS 下行常见）
	pkt := []byte{0x18} // config=3
	if OpusPacketFrameDurationMs(pkt) != 60 {
		t.Fatalf("got %d", OpusPacketFrameDurationMs(pkt))
	}
}

func TestOpusPacketDurationMsTwoFrames(t *testing.T) {
	// config=1 (20ms) + code=1 (2 frames) → 包总时长 40ms
	pkt := []byte{0x09, 0x00, 0x00} // 0x08|1
	if OpusPacketFrameSizeMs(pkt) != 20 {
		t.Fatalf("frame=%d", OpusPacketFrameSizeMs(pkt))
	}
	if OpusPacketFrameCount(pkt) != 2 {
		t.Fatalf("count=%d", OpusPacketFrameCount(pkt))
	}
	if OpusPacketDurationMs(pkt) != 40 {
		t.Fatalf("dur=%d want 40", OpusPacketDurationMs(pkt))
	}
}

func TestSegmentTableEmptyAnd255(t *testing.T) {
	segs, body := segmentTable([][]byte{nil, {}})
	if len(segs) != 2 || segs[0] != 0 || segs[1] != 0 || len(body) != 0 {
		t.Fatalf("empty packets: segs=%v body=%d", segs, len(body))
	}
	p := make([]byte, 255)
	segs, body = segmentTable([][]byte{p})
	if len(segs) != 2 || segs[0] != 255 || segs[1] != 0 || len(body) != 255 {
		t.Fatalf("255-aligned: segs=%v body=%d", segs, len(body))
	}
	p2 := make([]byte, 256)
	segs, body = segmentTable([][]byte{p2})
	if len(segs) != 2 || segs[0] != 255 || segs[1] != 1 || len(body) != 256 {
		t.Fatalf("256: segs=%v body=%d", segs, len(body))
	}
}

func TestDescribeOpusTOC60(t *testing.T) {
	// SILK WB 60ms code0：config=11 → (11<<3)=0x58
	pkt := []byte{0x58, 0x00}
	info := DescribeOpusTOC(pkt)
	if info.PacketMs != 60 || info.Frames != 1 {
		t.Fatalf("%+v", info)
	}
	// 20ms×3：config=9 (20ms WB), code=3, M=3
	pkt3 := []byte{(9 << 3) | 3, 3}
	info3 := DescribeOpusTOC(pkt3)
	if info3.FrameMs != 20 || info3.Frames != 3 || info3.PacketMs != 60 {
		t.Fatalf("%+v", info3)
	}
}

func TestFillOpusSeqGaps(t *testing.T) {
	seqs := []int{0, 2}
	frames := [][]byte{{0x48, 1}, {0x48, 2}}
	out, filled := FillOpusSeqGaps(seqs, frames)
	if filled != 1 || len(out) != 3 {
		t.Fatalf("filled=%d len=%d", filled, len(out))
	}
	if out[1] != nil && len(out[1]) != 0 {
		t.Fatalf("gap want zero-length, got %x", out[1])
	}
	if SumOpusPacketsDurationMs(out) != 60 { // 3×20ms (TOC 0x48 = 20ms SILK)
		t.Fatalf("dur=%d", SumOpusPacketsDurationMs(out))
	}
}

func TestFillOpusSeqGapsWithTSKindPace(t *testing.T) {
	base := int64(1_700_000_000_000_000)
	seqs := []int{0, 2}
	frames := [][]byte{{0x18, 1}, {0x18, 2}} // 60ms TOC
	// 仅一侧有 ts 时靠 pace 步进；事件流 60ms
	ts := []int64{base, 0}
	_, outTS, filled := FillOpusSeqGapsWithTSKind(seqs, frames, ts, 0)
	if filled != 1 {
		t.Fatalf("filled=%d", filled)
	}
	if outTS[1] != base+60_000 {
		t.Fatalf("event gap ts=%d want %d", outTS[1], base+60_000)
	}
	_, outTSFile, _ := FillOpusSeqGapsWithTSKind(seqs, frames, ts, 1)
	if outTSFile[1] != base+20_000 {
		t.Fatalf("file gap ts=%d want %d", outTSFile[1], base+20_000)
	}
}

func TestMakeOpusPLCFrameDefault60(t *testing.T) {
	if got := MakeOpusPLCFrame(nil); len(got) != 1 || got[0] != 0x18 {
		t.Fatalf("default PLC=%x want 0x18", got)
	}
	if got := MakeOpusPLCFrameForKind(1, nil); got[0] != 0x48 {
		t.Fatalf("file PLC=%x", got)
	}
}

func TestPaceAlignOpusPackets(t *testing.T) {
	// 4 个 20ms 包，设备 ts 间隔 60ms → 跨度 180+20=200ms，按间隙补
	pkts := [][]byte{{0x48, 1}, {0x48, 2}, {0x48, 3}, {0x48, 4}}
	base := int64(1_000_000_000_000_000)
	ts := []int64{base, base + 60_000, base + 120_000, base + 180_000}
	out, pad, med := PaceAlignOpusPackets(pkts, ts)
	if med != 60 {
		t.Fatalf("median=%d", med)
	}
	if pad != 6 { // 目标 200ms，内容 80ms，需补 120ms=6 帧
		t.Fatalf("pad=%d want 6", pad)
	}
	if got := SumOpusPacketsDurationMs(out); got != 200 {
		t.Fatalf("dur=%d want 200", got)
	}
}

func TestPaceAlignOpusPacketsUniformSpan(t *testing.T) {
	// 中位间隙 30ms（<2×20）：不按尖峰过补，对齐到首尾跨度
	pkts := make([][]byte, 5)
	for i := range pkts {
		pkts[i] = []byte{0x48, byte(i + 1)}
	}
	base := int64(1_000_000_000_000_000)
	// 间隙 30,30,30,90 → 中位 30；跨度 180+20=200；内容 100 → 补约 100ms=5 帧
	ts := []int64{base, base + 30_000, base + 60_000, base + 90_000, base + 180_000}
	out, pad, med := PaceAlignOpusPackets(pkts, ts)
	if med != 30 {
		t.Fatalf("median=%d", med)
	}
	got := SumOpusPacketsDurationMs(out)
	if got < 180 || got > 220 {
		t.Fatalf("dur=%d pad=%d want ~200", got, pad)
	}
	if pad < 4 || pad > 6 {
		t.Fatalf("pad=%d want ~5", pad)
	}
}

func TestMuxOpusPacketsEmpty(t *testing.T) {
	_, err := MuxOpusPacketsToOgg(nil, 16000)
	if err == nil {
		t.Fatal("expected error")
	}
}

// TestOpusRoundTripToMP3：PCM→ffmpeg Opus 分帧→Ogg 重封装→MP3，验证设备侧裸 Opus 转 App MP3 链路。
func TestOpusRoundTripToMP3(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	ctx := context.Background()
	pcm := make([]byte, 16000*2) // 1s 静音
	bundle, err := PCM16kToOpus(ctx, "ffmpeg", pcm, 24000)
	if err != nil {
		t.Fatalf("PCM16kToOpus: %v", err)
	}
	mp3, ext, ct, err := EncodeForAppFrames(ctx, "ffmpeg", bundle.Frames, nil, "opus", AppEncodeOpts{})
	if err != nil {
		t.Fatalf("EncodeForAppFrames: %v", err)
	}
	if ext != "mp3" || ct != "audio/mpeg" {
		t.Fatalf("format: ext=%s ct=%s", ext, ct)
	}
	if len(mp3) < 200 {
		t.Fatalf("mp3 too short: %d", len(mp3))
	}
}
