package audio

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"testing"
)

func TestPackRawOpusZip(t *testing.T) {
	frames := [][]byte{{0x01, 0x02}, {0x03}}
	meta := RawOpusZipMeta{
		SN: "S1", DeviceTS: 100, Kind: 0, Codec: "opus",
		Total: 2, Seqs: []int{0, 1}, ChunkTS: []int64{10, 20},
	}
	z, err := PackRawOpusZip(meta, frames)
	if err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string][]byte{}
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		names[f.Name] = b
	}
	if _, ok := names["meta.json"]; !ok {
		t.Fatal("missing meta.json")
	}
	if !bytes.Equal(names["0000.opus"], frames[0]) || !bytes.Equal(names["0001.opus"], frames[1]) {
		t.Fatalf("frame bytes mismatch")
	}
	var got RawOpusZipMeta
	if err := json.Unmarshal(names["meta.json"], &got); err != nil {
		t.Fatal(err)
	}
	if got.Chunks != 2 || got.SN != "S1" || got.Seqs[1] != 1 {
		t.Fatalf("meta=%+v", got)
	}
}
