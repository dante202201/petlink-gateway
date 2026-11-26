package audio

import "testing"

func TestExtractOpusPacketsEmpty(t *testing.T) {
	_, err := ExtractOpusPackets([]byte("xxxx"))
	if err == nil {
		t.Fatal("expected error")
	}
}
