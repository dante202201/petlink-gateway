// Package audio App 播放用转码：设备侧仍为裸 Opus，上传 OSS 前转为 MP3/WAV。
package audio

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

const (
	AppFormatMP3 = "mp3"
	AppFormatWAV = "wav"
)

// AppEncodeOpts App 侧 OSS 音频编码参数。
type AppEncodeOpts struct {
	Format     string // mp3 | wav，默认 mp3
	SampleRate int    // 默认 16000
	MP3Bitrate int    // 默认 96000（16k 单声道过低易沙沙声）
	// Highpass 高通（默认 false）；去掉低频轰鸣。过高会把偏弱叫声削没。
	Highpass bool
	// HighpassHz 截止频率，仅 Highpass=true 时有效
	HighpassHz int
	// PresenceDb 中高频搁架增益（dB），0=不加
	PresenceDb float64
	// MakeupDb 高通后补偿增益（dB），0=不补
	MakeupDb float64
	// Limiter 真峰限幅到约 -1dB（默认 false）
	Limiter bool
	// Denoise 降噪（默认 false；afftdn 对非平稳动物声易出音乐噪声）
	Denoise bool
	// DenoiseNR afftdn 降噪强度 1–30，默认 12
	DenoiseNR int
	// Loudnorm 响度归一（默认 false；短音频单遍易炸麦抬底噪）
	Loudnorm bool
	// RateRatio 播放速率：1=不改；<1 拉慢（对齐 TOC 短于设备 ts 跨度）。
	// 默认用 atempo 只调速不改音调；RatePitch=true 时改用 asetrate（速+调）。
	RateRatio float64
	// RatePitch true：asetrate+aresample（降速并降调）；false：atempo（只调速）。
	RatePitch bool
	// OpusDecode 设备 Opus→PCM：pion（默认，整段连续解码）| ffmpeg（强制 Ogg+libopus，听感 A/B）。
	OpusDecode string
}

func (o AppEncodeOpts) normalize() AppEncodeOpts {
	out := o
	if out.Format == "" {
		out.Format = AppFormatMP3
	}
	if out.SampleRate <= 0 {
		out.SampleRate = 16000
	}
	if out.MP3Bitrate <= 0 {
		out.MP3Bitrate = 96000
	}
	if out.RateRatio <= 0 {
		out.RateRatio = 1
	}
	if out.Denoise && out.DenoiseNR <= 0 {
		out.DenoiseNR = 12
	}
	if out.Highpass && out.HighpassHz <= 0 {
		out.HighpassHz = 120
	}
	if out.HighpassHz > 300 {
		out.HighpassHz = 300
	}
	if out.MakeupDb < 0 {
		out.MakeupDb = 0
	}
	if out.MakeupDb > 18 {
		out.MakeupDb = 18
	}
	return out
}

// OpusDecodeMode 归一化解码路径：pion | ffmpeg。
func OpusDecodeMode(opts AppEncodeOpts) string {
	switch strings.ToLower(strings.TrimSpace(opts.OpusDecode)) {
	case "ffmpeg", "ogg", "libopus":
		return "ffmpeg"
	default:
		return "pion"
	}
}

// CheckFFmpeg 探测 ffmpeg 是否可执行（启动时或排障用）。
func CheckFFmpeg(ffmpegPath string) error {
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	cmd := exec.Command(ffmpegPath, "-version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg 不可用(%q): %w", ffmpegPath, err)
	}
	return nil
}

// EncodeForApp 将设备拼接后的裸音频转为 App 可播格式。
// srcFormat: opus | mp3（与 model.CodecOpus / CodecMP3 一致）。
// 返回：编码后字节、文件后缀、HTTP Content-Type。
func EncodeForApp(ctx context.Context, ffmpegPath string, raw []byte, srcFormat string, opts AppEncodeOpts) ([]byte, string, string, error) {
	return EncodeForAppFrames(ctx, ffmpegPath, nil, raw, srcFormat, opts)
}

// EncodeForAppFrames 优先用分片列表；frames 为空时退化为 raw（Ogg 容器）拼接。
// Opus→App：整段连续解码到 48k PCM（一个解码器按 seq 喂完全部包），再滤镜+封装 MP3/WAV。
// 时长由 PCM 样点数决定，不跟 Ogg granule / 播放器追进度跑偏。
func EncodeForAppFrames(ctx context.Context, ffmpegPath string, frames [][]byte, raw []byte, srcFormat string, opts AppEncodeOpts) ([]byte, string, string, error) {
	opts = opts.normalize()
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	src := strings.ToLower(strings.TrimSpace(srcFormat))
	if src == "" {
		src = "opus"
	}

	// Opus：连续解码或强制 Ogg+ffmpeg → 48k PCM → 目标采样率 MP3/WAV
	if src == "opus" || src == "0" {
		if len(frames) == 0 && len(raw) == 0 {
			return nil, "", "", fmt.Errorf("音频为空")
		}
		var pcm48 []byte
		var err error
		if len(frames) > 0 {
			pcm48, err = decodeOpusFramesToPCM48(ctx, ffmpegPath, frames, opts)
			if err != nil {
				return nil, "", "", err
			}
		} else {
			// 已是 ogg/opus 容器时当 ogg 解
			pcm48, err = decodeOggToPCM48(ctx, ffmpegPath, raw)
			if err != nil {
				return nil, "", "", err
			}
		}
		return encodePCM48ToApp(ctx, ffmpegPath, pcm48, opts)
	}

	if len(raw) == 0 {
		return nil, "", "", fmt.Errorf("音频为空")
	}
	af := buildAppAudioFilter(opts)
	switch opts.Format {
	case AppFormatWAV:
		b, err := transcode(ctx, ffmpegPath, raw, src, wavArgs(opts.SampleRate, af))
		if err != nil {
			return nil, "", "", err
		}
		return b, "wav", "audio/wav", nil
	default:
		b, err := transcode(ctx, ffmpegPath, raw, src, mp3Args(opts.SampleRate, opts.MP3Bitrate, af))
		if err != nil {
			return nil, "", "", err
		}
		return b, "mp3", "audio/mpeg", nil
	}
}

// decodeOpusFramesToPCM48 按 opts.OpusDecode 选择 pion 连续解码或强制 Ogg+ffmpeg。
func decodeOpusFramesToPCM48(ctx context.Context, ffmpegPath string, frames [][]byte, opts AppEncodeOpts) ([]byte, error) {
	mode := OpusDecodeMode(opts)
	if mode == "ffmpeg" {
		ogg, err := MuxOpusPacketsToOgg(frames, 16000)
		if err != nil {
			return nil, fmt.Errorf("opus 强制 ffmpeg：ogg 封装失败: %w", err)
		}
		pcm48, err := decodeOggToPCM48(ctx, ffmpegPath, ogg)
		if err != nil {
			return nil, fmt.Errorf("opus 强制 ffmpeg：解 pcm 失败: %w", err)
		}
		return pcm48, nil
	}
	pcm48, err := DecodeOpusPacketsToPCM48(frames)
	if err != nil {
		// 连续解码失败时回退 Ogg+ffmpeg，避免整段丢入库
		ogg, muxErr := MuxOpusPacketsToOgg(frames, 16000)
		if muxErr != nil {
			return nil, fmt.Errorf("opus 连续解码失败: %w（ogg 回退封装也失败: %v）", err, muxErr)
		}
		pcm48, err = decodeOggToPCM48(ctx, ffmpegPath, ogg)
		if err != nil {
			return nil, fmt.Errorf("opus 连续解码失败且 ogg 回退也失败: %w", err)
		}
	}
	return pcm48, nil
}

// decodeOggToPCM48 将 Ogg Opus 解成 48kHz s16le mono（Opus 内部时钟）。
func decodeOggToPCM48(ctx context.Context, ffmpegPath string, ogg []byte) ([]byte, error) {
	if len(ogg) == 0 {
		return nil, fmt.Errorf("ogg 为空")
	}
	// 不依赖容器声明时长截断：尽量读完所有包
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-fflags", "+genpts+igndts",
		"-f", "ogg", "-i", "pipe:0",
		"-vn", "-map", "0:a:0",
		"-c:a", "pcm_s16le",
		"-ac", "1",
		"-ar", "48000",
		"-f", "s16le", "pipe:1",
	}
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	cmd.Stdin = bytes.NewReader(ogg)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("ffmpeg 解 opus/ogg→pcm 失败: %s", msg)
	}
	if len(stdout.Bytes()) < 2 {
		return nil, fmt.Errorf("ffmpeg 解 pcm 为空")
	}
	return stdout.Bytes(), nil
}

// encodePCM48ToApp 48k PCM → App 采样率 + 滤镜 → mp3/wav。
func encodePCM48ToApp(ctx context.Context, ffmpegPath string, pcm48 []byte, opts AppEncodeOpts) ([]byte, string, string, error) {
	opts = opts.normalize()
	af := buildAppAudioFilter(opts)
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "s16le", "-ar", "48000", "-ac", "1", "-i", "pipe:0",
		"-vn",
	}
	if af != "" {
		args = append(args, "-af", af)
	}
	switch opts.Format {
	case AppFormatWAV:
		args = append(args,
			"-c:a", "pcm_s16le",
			"-ar", fmt.Sprintf("%d", opts.SampleRate),
			"-ac", "1",
			"-f", "wav", "pipe:1",
		)
		b, err := runFFmpeg(ctx, ffmpegPath, pcm48, args)
		if err != nil {
			return nil, "", "", err
		}
		return b, "wav", "audio/wav", nil
	default:
		args = append(args,
			"-c:a", "libmp3lame",
			"-ar", fmt.Sprintf("%d", opts.SampleRate),
			"-ac", "1",
			"-b:a", fmt.Sprintf("%d", opts.MP3Bitrate),
			"-write_xing", "1",
			"-f", "mp3", "pipe:1",
		)
		b, err := runFFmpeg(ctx, ffmpegPath, pcm48, args)
		if err != nil {
			return nil, "", "", err
		}
		return b, "mp3", "audio/mpeg", nil
	}
}

func runFFmpeg(ctx context.Context, ffmpegPath string, stdin []byte, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("ffmpeg 失败: %s", msg)
	}
	if len(stdout.Bytes()) == 0 {
		return nil, fmt.Errorf("ffmpeg 输出为空")
	}
	return stdout.Bytes(), nil
}

// PCM16kToMP3 百度 TTS PCM → MP3（App 播放）；设备仍走 PCM16kToOpus 分帧。
// TTS 不做 rate/loudnorm，避免改变合成人声。
func PCM16kToMP3(ctx context.Context, ffmpegPath string, pcm []byte, opts AppEncodeOpts) ([]byte, error) {
	if len(pcm) == 0 {
		return nil, fmt.Errorf("pcm 为空")
	}
	opts = opts.normalize()
	opts.Loudnorm = false
	opts.Denoise = false
	opts.RateRatio = 1
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	return transcode(ctx, ffmpegPath, pcm, "s16le", mp3Args(opts.SampleRate, opts.MP3Bitrate, ""))
}

// buildAppAudioFilter 设备原音：aresample → 可选速率 → 高通 →（可选 denoise/loudnorm）→ 限幅。
// Opus 解码固定 48kHz；必须显式 aresample 到 16k。
// 默认不做 loudnorm/afftdn（短段易炸麦/音乐噪声）；限幅 -1dB 兜底。
func buildAppAudioFilter(opts AppEncodeOpts) string {
	var parts []string
	sr := opts.SampleRate
	if sr <= 0 {
		sr = 16000
	}
	r := opts.RateRatio
	if r > 0 && r < 0.999 && opts.RatePitch {
		if r < 0.4 {
			r = 0.4
		}
		asr := int(float64(sr)*r + 0.5)
		if asr < 4000 {
			asr = 4000
		}
		parts = append(parts,
			fmt.Sprintf("asetrate=%d", asr),
			fmt.Sprintf("aresample=%d", sr),
		)
	} else {
		parts = append(parts, fmt.Sprintf("aresample=%d", sr))
		if r > 0 && r < 0.999 {
			if r < 0.4 {
				r = 0.4
			}
			parts = append(parts, atempoFilters(r)...)
		}
	}
	if opts.Highpass {
		hz := opts.HighpassHz
		if hz <= 0 {
			hz = 120
		}
		parts = append(parts, fmt.Sprintf("highpass=f=%d", hz))
	}
	if opts.PresenceDb > 0.05 {
		g := opts.PresenceDb
		if g > 8 {
			g = 8
		}
		parts = append(parts, fmt.Sprintf("highshelf=f=2500:t=q:w=0.7:g=%.1f", g))
	}
	if opts.MakeupDb > 0.05 {
		parts = append(parts, fmt.Sprintf("volume=%.1fdB", opts.MakeupDb))
	}
	if opts.Denoise {
		nr := opts.DenoiseNR
		if nr <= 0 {
			nr = 12
		}
		if nr > 30 {
			nr = 30
		}
		parts = append(parts, fmt.Sprintf("afftdn=nr=%d:nf=-25", nr))
	}
	if opts.Loudnorm {
		parts = append(parts, "loudnorm=I=-16:TP=-1.5:LRA=11")
	}
	if opts.Limiter {
		// 真峰约 -1 dBFS；level=disabled 避免限幅器再自动抬增益把底噪拉响
		parts = append(parts, "alimiter=limit=0.8912509381:level=disabled")
	}
	return strings.Join(parts, ",")
}

// atempoFilters 只调速不改音调；单级 atempo 仅支持 0.5–2.0，更慢时串联。
func atempoFilters(speed float64) []string {
	if speed <= 0 || speed >= 0.999 {
		return nil
	}
	var out []string
	for speed < 0.5 {
		out = append(out, "atempo=0.5")
		speed /= 0.5
	}
	if speed < 0.999 {
		out = append(out, fmt.Sprintf("atempo=%.4f", speed))
	}
	return out
}

func mp3Args(sampleRate, bitrate int, af string) []string {
	args := make([]string, 0, 12)
	if af != "" {
		args = append(args, "-af", af)
	}
	return append(args,
		"-c:a", "libmp3lame",
		"-ar", fmt.Sprintf("%d", sampleRate),
		"-ac", "1",
		"-b:a", fmt.Sprintf("%d", bitrate),
		"-write_xing", "1",
		"-f", "mp3", "pipe:1",
	)
}

func wavArgs(sampleRate int, af string) []string {
	args := make([]string, 0, 10)
	if af != "" {
		args = append(args, "-af", af)
	}
	return append(args,
		"-c:a", "pcm_s16le",
		"-ar", fmt.Sprintf("%d", sampleRate),
		"-ac", "1",
		"-f", "wav", "pipe:1",
	)
}

func transcode(ctx context.Context, ffmpegPath string, raw []byte, inputFormat string, outputArgs []string) ([]byte, error) {
	inFmt, inArgs := inputFormatArgs(inputFormat)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-f", inFmt}
	args = append(args, inArgs...)
	args = append(args, "-i", "pipe:0", "-vn", "-map", "0:a:0")
	args = append(args, outputArgs...)

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	cmd.Stdin = bytes.NewReader(raw)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		outFmt := "audio"
		for i, a := range outputArgs {
			if a == "-f" && i+1 < len(outputArgs) {
				outFmt = outputArgs[i+1]
				break
			}
		}
		return nil, fmt.Errorf("ffmpeg 转 %s 失败: %s", outFmt, msg)
	}
	if len(stdout.Bytes()) == 0 {
		return nil, fmt.Errorf("ffmpeg 输出为空")
	}
	return stdout.Bytes(), nil
}

func inputFormatArgs(format string) (string, []string) {
	switch strings.ToLower(format) {
	case "s16le", "pcm":
		return "s16le", []string{"-ar", "16000", "-ac", "1"}
	case "mp3", "mpeg":
		return "mp3", nil
	case "ogg":
		return "ogg", nil
	default:
		return "opus", nil
	}
}
