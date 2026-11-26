// Package audio 音频转码：百度 PCM → Ogg Opus（App）+ 分帧（设备）。
package audio

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// OpusBundle PCM 转 OPUS 后的结果。
type OpusBundle struct {
	// OggOpus 完整 Ogg Opus 文件，上传 OSS 给 App 播放
	OggOpus []byte
	// Frames 每帧一个 OPUS 包（已去掉 Ogg 头），按 60ms 推给设备
	Frames [][]byte
}

// PCM16kToOpus 将 s16le / 16kHz / mono PCM 转为 Ogg Opus，并拆出音频帧。
// 依赖本机 ffmpeg（需带 libopus）。
func PCM16kToOpus(ctx context.Context, ffmpegPath string, pcm []byte, bitrate int) (*OpusBundle, error) {
	if len(pcm) == 0 {
		return nil, fmt.Errorf("pcm 为空")
	}
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	if bitrate <= 0 {
		bitrate = 24000
	}

	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-f", "s16le", "-ar", "16000", "-ac", "1", "-i", "pipe:0",
		"-c:a", "libopus",
		"-ar", "16000", "-ac", "1",
		"-b:a", fmt.Sprintf("%d", bitrate),
		"-frame_duration", "60",
		"-application", "voip",
		"-f", "ogg", "pipe:1",
	}
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	cmd.Stdin = bytes.NewReader(pcm)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("ffmpeg 转 opus 失败（请确认已安装 ffmpeg 且支持 libopus）: %s", msg)
	}
	ogg := stdout.Bytes()
	if len(ogg) == 0 {
		return nil, fmt.Errorf("ffmpeg 输出为空")
	}
	frames, err := ExtractOpusPackets(ogg)
	if err != nil {
		return nil, err
	}
	return &OpusBundle{OggOpus: ogg, Frames: frames}, nil
}

// ExtractOpusPackets 从 Ogg Opus 中提取音频包（跳过 OpusHead / OpusTags）。
func ExtractOpusPackets(ogg []byte) ([][]byte, error) {
	packets, err := readOggPackets(ogg)
	if err != nil {
		return nil, err
	}
	out := make([][]byte, 0, len(packets))
	for _, p := range packets {
		if len(p) >= 8 && string(p[:8]) == "OpusHead" {
			continue
		}
		if len(p) >= 8 && string(p[:8]) == "OpusTags" {
			continue
		}
		if len(p) == 0 {
			continue
		}
		cp := make([]byte, len(p))
		copy(cp, p)
		out = append(out, cp)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ogg 中无 opus 音频帧")
	}
	return out, nil
}

func readOggPackets(data []byte) ([][]byte, error) {
	var packets [][]byte
	var cont []byte
	i := 0
	for i+27 <= len(data) {
		if string(data[i:i+4]) != "OggS" {
			return nil, fmt.Errorf("无效 ogg：偏移 %d 不是 OggS", i)
		}
		nsegs := int(data[i+26])
		hdr := 27 + nsegs
		if i+hdr > len(data) {
			return nil, fmt.Errorf("ogg 页头截断")
		}
		segTable := data[i+27 : i+hdr]
		bodyLen := 0
		for _, s := range segTable {
			bodyLen += int(s)
		}
		if i+hdr+bodyLen > len(data) {
			return nil, fmt.Errorf("ogg 页体截断")
		}
		body := data[i+hdr : i+hdr+bodyLen]
		off := 0
		for _, seg := range segTable {
			l := int(seg)
			chunk := body[off : off+l]
			off += l
			cont = append(cont, chunk...)
			if l < 255 {
				packets = append(packets, cont)
				cont = nil
			}
		}
		i += hdr + bodyLen
	}
	if len(cont) > 0 {
		packets = append(packets, cont)
	}
	return packets, nil
}
