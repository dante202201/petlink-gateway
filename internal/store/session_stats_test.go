package store

import "testing"

func TestSessionChunkStatsReady(t *testing.T) {
	if (SessionChunkStats{}).Ready() {
		t.Fatal("empty not ready")
	}
	if (SessionChunkStats{Chunks: 3, HasEnd: false}).Ready() {
		t.Fatal("no end not ready")
	}
	if !(SessionChunkStats{Chunks: 10, Total: 10, MaxSeq: 9, HasEnd: true}).Ready() {
		t.Fatal("full should be ready")
	}
	if (SessionChunkStats{Chunks: 8, Total: 10, MaxSeq: 9, HasEnd: true}).Ready() {
		t.Fatal("short of total not ready")
	}
	// end 未带 total：用 MaxSeq+1
	if !(SessionChunkStats{Chunks: 5, Total: 0, MaxSeq: 4, HasEnd: true}).Ready() {
		t.Fatal("unknown total with end and full seq should be ready")
	}
	if (SessionChunkStats{Chunks: 4, Total: 0, MaxSeq: 4, HasEnd: true}).Ready() {
		t.Fatal("holes with end should not be ready")
	}
	// 滚动中间 total 不应单独决定：缺洞时 MaxSeq+1 更大则等齐
	if (SessionChunkStats{Chunks: 8, Total: 5, MaxSeq: 9, HasEnd: true}).Ready() {
		t.Fatal("MaxSeq+1 > Total with holes should wait")
	}
	if !(SessionChunkStats{Chunks: 8, Total: 10, MaxSeq: 9, HasEnd: true}).Incomplete() {
		t.Fatal("want incomplete")
	}
	if (SessionChunkStats{Chunks: 10, Total: 10, MaxSeq: 9, HasEnd: true}).Incomplete() {
		t.Fatal("full should not be incomplete")
	}
	if (SessionChunkStats{Chunks: 10, Total: 10, MaxSeq: 9, HasEnd: true}).ExpectedFrames() != 10 {
		t.Fatal("expected 10")
	}
	if (SessionChunkStats{Chunks: 5, Total: 0, MaxSeq: 4, HasEnd: true}).ExpectedFrames() != 5 {
		t.Fatal("expected MaxSeq+1")
	}
}
