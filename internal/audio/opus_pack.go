package audio

import (
	"encoding/binary"
	"fmt"
)

// PackOpusFrames 将设备下行 Opus 帧打包为长度前缀二进制（每帧 4 字节小端长度 + payload）。
// 用于 a_emb_translate.device_opus 落库，便于事后再次 SendTTSFrames。
func PackOpusFrames(frames [][]byte) ([]byte, error) {
	if len(frames) == 0 {
		return nil, nil
	}
	n := 0
	for i, f := range frames {
		if len(f) == 0 {
			return nil, fmt.Errorf("第 %d 帧为空", i)
		}
		if len(f) > 0x7fffffff {
			return nil, fmt.Errorf("第 %d 帧过大: %d", i, len(f))
		}
		n += 4 + len(f)
	}
	out := make([]byte, 0, n)
	for _, f := range frames {
		var hdr [4]byte
		binary.LittleEndian.PutUint32(hdr[:], uint32(len(f)))
		out = append(out, hdr[:]...)
		out = append(out, f...)
	}
	return out, nil
}

// UnpackOpusFrames 从 PackOpusFrames 格式还原帧列表。
func UnpackOpusFrames(blob []byte) ([][]byte, error) {
	if len(blob) == 0 {
		return nil, nil
	}
	var frames [][]byte
	off := 0
	for off < len(blob) {
		if off+4 > len(blob) {
			return nil, fmt.Errorf("device_opus 截断：缺少长度头 @%d", off)
		}
		n := int(binary.LittleEndian.Uint32(blob[off : off+4]))
		off += 4
		if n <= 0 || off+n > len(blob) {
			return nil, fmt.Errorf("device_opus 截断：帧长=%d @%d total=%d", n, off, len(blob))
		}
		f := make([]byte, n)
		copy(f, blob[off:off+n])
		frames = append(frames, f)
		off += n
	}
	return frames, nil
}
