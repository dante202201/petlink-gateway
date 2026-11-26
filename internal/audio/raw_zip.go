package audio

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
)

// RawOpusZipMeta 事件流原帧 zip 内 meta.json。
type RawOpusZipMeta struct {
	SN       string  `json:"sn"`
	DeviceTS int64   `json:"device_ts"`
	Kind     int     `json:"kind"`
	Codec    string  `json:"codec"`
	Total    int     `json:"total"`  // 设备上报 total（取首帧行上的值）
	Chunks   int     `json:"chunks"` // zip 内真实帧数
	Seqs     []int   `json:"seqs"`
	ChunkTS  []int64 `json:"chunk_ts"` // 与 seqs 对齐；无则为 0
}

// PackRawOpusZip 将真实 Opus 帧打成 zip：meta.json + 0000.opus…
// 不做 PLC、不插空帧；files[i] 对应 seqs[i]。
func PackRawOpusZip(meta RawOpusZipMeta, frames [][]byte) ([]byte, error) {
	if len(frames) == 0 {
		return nil, fmt.Errorf("无原帧可打包")
	}
	if len(meta.Seqs) != len(frames) {
		return nil, fmt.Errorf("seqs 与 frames 长度不一致: %d vs %d", len(meta.Seqs), len(frames))
	}
	if len(meta.ChunkTS) != 0 && len(meta.ChunkTS) != len(frames) {
		return nil, fmt.Errorf("chunk_ts 与 frames 长度不一致: %d vs %d", len(meta.ChunkTS), len(frames))
	}
	if meta.Chunks == 0 {
		meta.Chunks = len(frames)
	}
	if len(meta.ChunkTS) == 0 {
		meta.ChunkTS = make([]int64, len(frames))
	}

	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	mw, err := zw.Create("meta.json")
	if err != nil {
		_ = zw.Close()
		return nil, err
	}
	if _, err := mw.Write(metaBytes); err != nil {
		_ = zw.Close()
		return nil, err
	}
	for i, f := range frames {
		if len(f) == 0 {
			_ = zw.Close()
			return nil, fmt.Errorf("第 %d 帧（seq=%d）为空", i, meta.Seqs[i])
		}
		name := fmt.Sprintf("%04d.opus", i)
		fw, err := zw.Create(name)
		if err != nil {
			_ = zw.Close()
			return nil, err
		}
		if _, err := fw.Write(f); err != nil {
			_ = zw.Close()
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
