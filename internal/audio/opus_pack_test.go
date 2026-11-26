package audio

import "testing"

func TestPackUnpackOpusFrames(t *testing.T) {
	in := [][]byte{
		{0x01, 0x02, 0x03},
		{0xAA, 0xBB},
		{0x10},
	}
	blob, err := PackOpusFrames(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) != 4+3+4+2+4+1 {
		t.Fatalf("blob len=%d", len(blob))
	}
	out, err := UnpackOpusFrames(blob)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("frames=%d", len(out))
	}
	for i := range in {
		if string(out[i]) != string(in[i]) {
			t.Fatalf("frame %d mismatch", i)
		}
	}
}

func TestUnpackOpusFramesEmpty(t *testing.T) {
	frames, err := UnpackOpusFrames(nil)
	if err != nil || frames != nil {
		t.Fatalf("got %v %v", frames, err)
	}
}

func TestUnpackOpusFramesTruncated(t *testing.T) {
	if _, err := UnpackOpusFrames([]byte{0x01, 0x00}); err == nil {
		t.Fatal("want error")
	}
}
