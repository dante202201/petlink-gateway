package audio

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/opus"
)

const (
	opusDecodeSampleRate = 48000
	opusDecodeChannels   = 1
	// 单包上限 120ms @ 48k mono
	opusDecodeMaxFrameSamples = opusDecodeSampleRate * 120 / 1000
)

// DecodeOpusPacketsToPCM48 整段连续解码：一个 Opus 解码器按 seq 顺序喂完全部包，输出 48kHz s16le mono。
// 空包（seq 洞）喂与邻包同 TOC 的空帧以保持解码器状态；仍失败则填等长静音。
func DecodeOpusPacketsToPCM48(packets [][]byte) ([]byte, error) {
	if len(packets) == 0 {
		return nil, fmt.Errorf("opus 包列表为空")
	}

	dec, err := opus.NewDecoderWithOutput(opusDecodeSampleRate, opusDecodeChannels)
	if err != nil {
		return nil, fmt.Errorf("创建 opus 解码器失败: %w", err)
	}

	gapMs := inferGapMs(packets)
	estSamples := 0
	for _, p := range packets {
		if len(p) == 0 {
			estSamples += gapMs * opusDecodeSampleRate / 1000
			continue
		}
		if d := OpusPacketDurationMs(p); d > 0 {
			estSamples += d * opusDecodeSampleRate / 1000
			gapMs = d
		} else {
			estSamples += gapMs * opusDecodeSampleRate / 1000
		}
	}
	if estSamples <= 0 {
		estSamples = opusDecodeSampleRate / 10
	}

	out := make([]byte, 0, estSamples*2)
	pcm := make([]int16, opusDecodeMaxFrameSamples)
	gapMs = inferGapMs(packets)
	var template []byte
	nonEmpty, decodedOK := 0, 0

	for _, pkt := range packets {
		if len(pkt) == 0 {
			if _, ok := decodeOne(&dec, MakeOpusPLCFrame(template), pcm, &out); !ok {
				appendSilence(gapSamples(gapMs), &out)
			}
			continue
		}
		nonEmpty++
		if _, ok := decodeOne(&dec, pkt, pcm, &out); !ok {
			dur := OpusPacketDurationMs(pkt)
			if dur <= 0 {
				dur = gapMs
			}
			if _, ok2 := decodeOne(&dec, MakeOpusPLCFrame(pkt), pcm, &out); !ok2 {
				appendSilence(gapSamples(dur), &out)
			}
			continue
		}
		decodedOK++
		template = pkt
		if d := OpusPacketDurationMs(pkt); d > 0 {
			gapMs = d
		}
	}

	if nonEmpty > 0 && decodedOK == 0 {
		return nil, fmt.Errorf("opus 连续解码：%d 个非空包全部失败", nonEmpty)
	}
	if len(out) < 2 {
		return nil, fmt.Errorf("opus 连续解码 pcm 为空")
	}
	return out, nil
}

func inferGapMs(packets [][]byte) int {
	for _, p := range packets {
		if len(p) > 0 {
			if d := OpusPacketDurationMs(p); d > 0 {
				return d
			}
		}
	}
	return 60 // 事件流缺省包长
}

func gapSamples(gapMs int) int {
	if gapMs <= 0 {
		gapMs = 60
	}
	if gapMs > 120 {
		gapMs = 120
	}
	return gapMs * opusDecodeSampleRate / 1000
}

// decodeOne 向同一解码器喂一包；成功则追加 PCM 并返回样点数。
func decodeOne(dec *opus.Decoder, pkt []byte, pcm []int16, out *[]byte) (int, bool) {
	if len(pkt) == 0 {
		return 0, false
	}
	n, err := dec.DecodeToInt16(pkt, pcm)
	if err != nil || n <= 0 {
		return 0, false
	}
	if n > len(pcm) {
		n = len(pcm)
	}
	*out = appendInt16LE(*out, pcm[:n])
	return n, true
}

func appendSilence(n int, out *[]byte) {
	if n <= 0 {
		return
	}
	*out = append(*out, make([]byte, n*2)...)
}

func appendInt16LE(dst []byte, samples []int16) []byte {
	off := len(dst)
	dst = append(dst, make([]byte, len(samples)*2)...)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(dst[off+i*2:], uint16(s))
	}
	return dst
}
