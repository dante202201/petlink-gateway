// Package pipeline 设备音频业务：查 a_emb_sn → 写 a_emb_translate →（可选）OSS/AI/TTS。
package pipeline

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"pet-translate-gateway/config"
	aiclient "pet-translate-gateway/internal/ai"
	"pet-translate-gateway/internal/audio"
	"pet-translate-gateway/internal/baidu"
	"pet-translate-gateway/internal/model"
	ossx "pet-translate-gateway/internal/oss"
	redisx "pet-translate-gateway/internal/redis"
	"pet-translate-gateway/internal/store"
)

// ossSuggestedEndpointRe 从阿里云 OSS 403 提示里提取「应该使用的 endpoint」：
// Example: "Endpoint=oss-cn-beijing.aliyuncs.com"
var ossSuggestedEndpointRe = regexp.MustCompile(`Endpoint=([a-z0-9-]+\.aliyuncs\.com)`)

// extractOSSErrorHints 从阿里云 OSS 返回的 ErrorCode=AccessDenied 等错误消息里，把
// 可用 endpoint / requestId 等常见排错字段单独拆出来，避免人工一行行扫。
func extractOSSErrorHints(err error) (suggestedEndpoint, requestID, errorCode string) {
	if err == nil {
		return "", "", ""
	}
	s := err.Error()
	if m := ossSuggestedEndpointRe.FindStringSubmatch(s); len(m) >= 2 {
		suggestedEndpoint = m[1]
	}
	for _, pair := range []struct {
		key   string
		dst   *string
		short bool
	}{
		{"ErrorCode=", &errorCode, true},
		{"RequestId=", &requestID, false},
	} {
		if i := strings.Index(s, pair.key); i >= 0 {
			tail := strings.TrimSpace(s[i+len(pair.key):])
			if pair.short {
				tail = strings.TrimSpace(tail)
				if j := strings.IndexAny(tail, ", \n\t\""); j >= 0 {
					tail = tail[:j]
				}
			} else {
				tail = strings.TrimSpace(tail)
				if j := strings.IndexAny(tail, ", \n\t"); j >= 0 {
					tail = tail[:j]
				}
			}
			*pair.dst = tail
		}
	}
	return suggestedEndpoint, requestID, errorCode
}

// DeviceTTSPusher 将 OPUS 帧推给在线设备（由 ConnManager 实现）。
type DeviceTTSPusher interface {
	SendTTSFrames(sn string, frames [][]byte) error
}

// Service 音频会话业务处理。
type Service struct {
	// persist：database 可用即可写 a_emb_translate
	persist bool
	// uploadOrigin：OSS 可用时上传原音频并回写 origin_audio_url
	uploadOrigin bool
	// fullTranslate：translate + OSS + AI + 百度齐全时跑完整链路
	fullTranslate       bool
	pushDevice          bool
	ffmpegPath          string
	opusBitrate         int
	appEncode           audio.AppEncodeOpts
	fileSampleRate      int // kind=1 可选覆盖；0=与 appEncode.SampleRate 相同（16k）
	appLoudnorm         bool
	appDenoise          bool
	appDenoiseNR        int
	appHighpass         bool
	appLimiter          bool
	appFixRate          bool
	appFixPitch         bool // true=asetrate 降调；false=atempo 只调速
	appPitchRatio       float64
	dropShort           bool
	minDurationSec      float64 // kind=1 文件
	minDurationEventSec float64 // kind=0 事件流
	paceAlignEvent      bool    // 事件流按设备 ts 补静音（默认关）
	store               *store.Store
	oss                 *ossx.Client
	ai                  *aiclient.Client
	baidu               *baidu.Client
	redis               *redisx.Client
	devicePusher        DeviceTTSPusher
	log                 *zap.Logger

	sessionMu   sync.Map // sn:deviceTS:kind → *sync.Mutex，防并发首跑/重拼打架
	rebuildMu   sync.Mutex
	rebuildWait map[string]*time.Timer // 补发迟到重拼防抖

	stitchOn             bool
	stitchWindowMs       int
	deviceSegSilenceMs   int
	stitchMu             sync.Mutex
	stitchWait           map[string]*stitchGroup

	// 事件流原帧 zip：end 时可能早于 a_emb_translate 建档，先缓存 URL 待补写
	rawPackPending  sync.Map // sn:deviceTS → pendingRawPack
	rawPackInflight sync.Map // sn:deviceTS → struct{}，防并发重复打 zip
}

type pendingRawPack struct {
	URL    string
	Frames int
}

// New 创建服务。有数据库即可在 end=true 时写 a_emb_translate；OSS/翻译依赖另判。
func New(tc config.TranslateConfig, st *store.Store, oss *ossx.Client, ai *aiclient.Client, bd *baidu.Client, log *zap.Logger) *Service {
	persist := st != nil
	uploadOrigin := persist && oss != nil
	full := tc.Enabled && uploadOrigin && ai != nil && bd != nil
	if tc.Enabled && !full {
		log.Warn("translate.enabled=true 但 OSS/AI/百度未齐，仅按已启用能力落库/上传，不跑完整 AI/TTS")
	}
	if persist && !tc.Enabled {
		if uploadOrigin {
			log.Info("音频会话将写 a_emb_translate 并上传 App 可播音频到 OSS（translate.enabled=false，跳过 AI/TTS）",
				zap.String("app_format", tc.AppAudioFormat),
			)
		} else {
			log.Info("oss 未启用，end=true 时不写 a_emb_translate（需 origin_audio_url）")
		}
	}
	ffmpeg := tc.FFmpegPath
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	br := tc.OpusBitrate
	if br <= 0 {
		br = 24000
	}
	appFmt := strings.ToLower(strings.TrimSpace(tc.AppAudioFormat))
	if appFmt == "" {
		appFmt = audio.AppFormatMP3
	}
	loudnorm := false // 默认关：短音频 loudnorm 易炸麦抬底噪
	if tc.AppLoudnorm != nil {
		loudnorm = *tc.AppLoudnorm
	}
	denoise := false // 默认关：afftdn 对动物声易出音乐噪声
	if tc.AppDenoise != nil {
		denoise = *tc.AppDenoise
	}
	denoiseNR := tc.AppDenoiseNR
	if denoiseNR <= 0 {
		denoiseNR = 12
	}
	highpass := false // 默认关：先保证拼装原音，听感优化另行打开
	if tc.AppHighpass != nil {
		highpass = *tc.AppHighpass
	}
	highpassHz := tc.AppHighpassHz
	if highpassHz <= 0 {
		highpassHz = 120
	}
	presence := tc.AppPresenceDb
	if presence < 0 {
		presence = 0
	}
	makeup := tc.AppMakeupDb
	if makeup < 0 {
		makeup = 0
	}
	limiter := false
	if tc.AppLimiter != nil {
		limiter = *tc.AppLimiter
	}
	fixRate := false
	if tc.AppFixRate != nil {
		fixRate = *tc.AppFixRate
	}
	fixPitch := false // 默认只调速：事件流常见「只快不尖」
	if tc.AppFixPitch != nil {
		fixPitch = *tc.AppFixPitch
	}
	minDur := tc.MinDurationSec
	if tc.DropShortEnabled && minDur <= 0 {
		minDur = 0.6
	}
	minDurEvent := tc.MinDurationEventSec // 0 = 事件流不过滤
	paceAlign := false                    // 默认关：补 PLC 会「突突」跳音，听不清内容
	if tc.PaceAlignEvent != nil {
		paceAlign = *tc.PaceAlignEvent
	}
	fileSR := tc.FileSampleRate // 0 = 与 AppSampleRate 相同（两路均为 16k）
	svc := &Service{
		persist:       persist,
		uploadOrigin:  uploadOrigin,
		fullTranslate: full,
		pushDevice:    tc.PushToDevice,
		ffmpegPath:    ffmpeg,
		opusBitrate:   br,
		appEncode: audio.AppEncodeOpts{
			Format:     appFmt,
			SampleRate: tc.AppSampleRate,
			MP3Bitrate: tc.AppMP3Bitrate,
			Highpass:   highpass,
			HighpassHz: highpassHz,
			PresenceDb: presence,
			MakeupDb:   makeup,
			Limiter:    limiter,
			Loudnorm:   loudnorm,
			Denoise:    denoise,
			DenoiseNR:  denoiseNR,
			OpusDecode: tc.OpusDecode,
		},
		fileSampleRate:      fileSR,
		appLoudnorm:         loudnorm,
		appDenoise:          denoise,
		appDenoiseNR:        denoiseNR,
		appHighpass:         highpass,
		appLimiter:          limiter,
		appFixRate:          fixRate,
		appFixPitch:         fixPitch,
		appPitchRatio:       tc.AppPitchRatio,
		dropShort:           tc.DropShortEnabled,
		minDurationSec:      minDur,
		minDurationEventSec: minDurEvent,
		paceAlignEvent:     paceAlign,
		store:              st,
		oss:                oss,
		ai:                 ai,
		baidu:              bd,
		log:                log,
		stitchOn:           tc.StitchOn(),
		stitchWindowMs:     tc.StitchWindowMs,
		deviceSegSilenceMs: tc.DeviceSegSilenceMs,
	}
	if tc.DropShortEnabled {
		log.Info("过短录音过滤已开启",
			zap.Float64("min_duration_sec_file", minDur),
			zap.Float64("min_duration_event_sec", minDurEvent),
			zap.Bool("event_drop_short", minDurEvent > 0),
			zap.Bool("pace_align_event", paceAlign),
		)
	}
	if paceAlign {
		log.Warn("事件流 PLC 节拍对齐已开启（易导致突突跳音，仅排障用）", zap.Bool("pace_align_event", true))
	}
	if svc.stitchOn {
		log.Info("事件流拼接窗口已开启（800ms 设备静音 + 实测间隙）",
			zap.Int("stitch_window_ms", svc.stitchWindowMs),
			zap.Int("device_seg_silence_ms", svc.deviceSegSilenceMs),
			zap.String("hint", "真实沉默≈实测间隙+800ms；5s窗口≈5.9s真实沉默；固件关段改2500时窗口同步改2500"),
		)
	}
	log.Info("App 原音后处理（仅事件流 kind=0；文件 kind=1 不处理）",
		zap.Bool("denoise", denoise),
		zap.Int("denoise_nr", denoiseNR),
		zap.Bool("loudnorm", loudnorm),
		zap.Bool("fix_rate", fixRate),
		zap.Float64("pitch_ratio", tc.AppPitchRatio),
		zap.String("opus_decode", audio.OpusDecodeMode(svc.appEncode)),
	)
	if audio.OpusDecodeMode(svc.appEncode) == "ffmpeg" {
		log.Warn("Opus→PCM 强制走 Ogg+ffmpeg（听感 A/B）；确认后请改回 opus_decode: pion",
			zap.String("opus_decode", "ffmpeg"),
		)
	}
	if uploadOrigin {
		if err := audio.CheckFFmpeg(ffmpeg); err != nil {
			log.Warn("oss 已启用但 ffmpeg 不可用，end=true 时原音转 MP3 将失败且不会写 a_emb_translate",
				zap.String("ffmpeg_path", ffmpeg),
				zap.Error(err),
			)
		} else {
			log.Info("ffmpeg 可用，原音将转 App 格式后上传 OSS",
				zap.String("ffmpeg_path", ffmpeg),
				zap.String("app_format", appFmt),
			)
		}
	}
	return svc
}

// SetRedis 注入 Redis（音频会话诊断回写）。
func (s *Service) SetRedis(rdb *redisx.Client) {
	if s != nil {
		s.redis = rdb
	}
}

// SetDevicePusher 注入设备 TTS 推送（避免与 gateway 循环依赖，启动后调用）。
func (s *Service) SetDevicePusher(p DeviceTTSPusher) {
	if s != nil {
		s.devicePusher = p
	}
}

// Enabled 是否处理会话（写 a_emb_translate 和/或完整翻译）。
func (s *Service) Enabled() bool {
	return s != nil && s.persist
}

// FullTranslateEnabled 是否跑 OSS→AI→TTS。
func (s *Service) FullTranslateEnabled() bool {
	return s != nil && s.fullTranslate
}

// UploadOriginEnabled 是否上传原音频到 OSS 并回写 URL。
func (s *Service) UploadOriginEnabled() bool {
	return s != nil && s.uploadOrigin
}

func (s *Service) noteSkip(ctx context.Context, sn string, deviceTS int64, audioKind int, reason, detail string) {
	if s == nil || s.redis == nil || audioKind != model.AudioKindEvent {
		return
	}
	_ = s.redis.PatchAudioCycleSkip(ctx, sn, deviceTS, audioKind, reason, detail)
}

func (s *Service) noteAnomaly(ctx context.Context, sn string, deviceTS int64, audioKind int, detail string) {
	if s == nil || s.redis == nil || audioKind != model.AudioKindEvent || detail == "" {
		return
	}
	_ = s.redis.PatchAudioCycleAnomaly(ctx, sn, deviceTS, audioKind, detail)
}

// applyEventPaceAlign 事件流：默认不改波形；对照「60ms 连续」验收并写诊断（用 chunk_ts，不用墙钟）。
// pace_align_event=true 时才补 PLC（排障用，听感易突突跳，勿用于生产）。
func (s *Service) applyEventPaceAlign(ctx context.Context, sn string, deviceTS int64, audioKind int, frames [][]byte, absTSUs []int64) ([][]byte, int) {
	if s == nil || audioKind != model.AudioKindEvent || len(frames) < 2 {
		return frames, 0
	}
	contentMs := audio.SumOpusPacketsDurationMs(frames)
	spanMs := audio.SessionTSSpanMs(frames, absTSUs)
	medianGap := audio.MedianPacketGapMs(absTSUs)
	frameMs := 20
	pkt0 := audio.OpusTOCInfo{}
	if len(frames) > 0 {
		for _, f := range frames {
			if len(f) > 0 {
				pkt0 = audio.DescribeOpusTOC(f)
				if pkt0.FrameMs > 0 {
					frameMs = pkt0.FrameMs
				}
				if pkt0.PacketMs > 0 {
					frameMs = pkt0.PacketMs // 验收用「包时长」更贴 60ms 多帧包
				}
				break
			}
		}
	}
	// 验收：TOC 时长 ≈ 设备 ts 跨度，且中位间隙接近包时长（连续上传）
	continuousOK := contentMs > 0 && spanMs > 0 &&
		spanMs <= contentMs*5/4 && contentMs <= spanMs*5/4 &&
		(medianGap == 0 || (medianGap >= frameMs*3/4 && medianGap <= frameMs*5/4))
	if s.redis != nil {
		_ = s.redis.PatchAudioCycleTiming(ctx, sn, deviceTS, audioKind, frameMs, medianGap, contentMs, spanMs)
	}
	if continuousOK {
		s.log.Info("事件流帧长验收：接近连续边采边传",
			zap.String("sn", sn),
			zap.Int64("device_ts", deviceTS),
			zap.Int("frame_ms", frameMs),
			zap.Int("pkt0_config", pkt0.Config),
			zap.Int("pkt0_code", pkt0.Code),
			zap.Int("pkt0_frames", pkt0.Frames),
			zap.Int("pkt0_ms", pkt0.PacketMs),
			zap.Int("packets", len(frames)),
			zap.Int("toc_ms", contentMs),
			zap.Int("span_ms", spanMs),
			zap.Int("median_gap_ms", medianGap),
		)
		if pkt0.PacketMs > 0 && pkt0.PacketMs != 60 && pkt0.FrameMs == 20 && pkt0.Frames == 1 {
			s.noteAnomaly(ctx, sn, deviceTS, audioKind,
				fmt.Sprintf("event_toc=20ms×1/包（联调约定多为 60ms/包或 20ms×3）；packets=%d toc=%dms span=%dms",
					len(frames), contentMs, spanMs))
		}
	} else if contentMs > 0 && spanMs >= contentMs*5/4 {
		s.log.Info("事件流帧长验收：TOC 短于时间跨度（将按 app_fix_rate 尝试拉长）",
			zap.String("sn", sn),
			zap.Int64("device_ts", deviceTS),
			zap.Int("frame_ms", frameMs),
			zap.Int("pkt0_ms", pkt0.PacketMs),
			zap.Int("pkt0_frames", pkt0.Frames),
			zap.Int("toc_ms", contentMs),
			zap.Int("span_ms", spanMs),
			zap.Int("median_gap_ms", medianGap),
			zap.Bool("pace_align_event", s.paceAlignEvent),
			zap.Bool("app_fix_rate", s.appFixRate),
		)
		s.noteAnomaly(ctx, sn, deviceTS, audioKind,
			fmt.Sprintf("event_not_continuous: toc_ms=%d span_ms=%d median_gap_ms=%d pkt0=%dms×%d (体感偏快时常因 toc≪span)",
				contentMs, spanMs, medianGap, pkt0.FrameMs, pkt0.Frames))
	}
	if !s.paceAlignEvent {
		return frames, 0
	}
	out, pad, _ := audio.PaceAlignOpusPackets(frames, absTSUs)
	if pad <= 0 {
		return frames, 0
	}
	afterMs := audio.SumOpusPacketsDurationMs(out)
	s.log.Warn("事件流已按节拍补 PLC（排障开关，听感可能突突跳）",
		zap.String("sn", sn),
		zap.Int64("device_ts", deviceTS),
		zap.Int("pad_frames", pad),
		zap.Int("dur_before_ms", contentMs),
		zap.Int("dur_after_ms", afterMs),
	)
	s.noteAnomaly(ctx, sn, deviceTS, audioKind,
		fmt.Sprintf("pace_align: pad_frames=%d median_gap_ms=%d dur %dms→%dms",
			pad, medianGap, contentMs, afterMs))
	return out, pad
}

// appEncodeForSession 设备原音转 App。
// kind=0/1 采集均为 16kHz；Ogg/MP3 统一 16k（aresample）。
// 事件流默认：高通+限幅；不做 loudnorm/afftdn（易炸麦/音乐噪声）。录音文件仅 aresample（可覆盖采样率）。
func (s *Service) appEncodeForSession(audioKind int, frames [][]byte, absTSUs []int64) audio.AppEncodeOpts {
	opts := s.appEncode
	opts.Loudnorm = false
	opts.Denoise = false
	opts.DenoiseNR = s.appDenoiseNR
	opts.Highpass = false
	opts.Limiter = false
	opts.RateRatio = 1
	opts.RatePitch = false
	if audioKind == model.AudioKindFile && s.fileSampleRate > 0 {
		opts.SampleRate = s.fileSampleRate
	}
	if opts.SampleRate <= 0 {
		opts.SampleRate = 16000
	}
	if audioKind != model.AudioKindEvent {
		// 文件路：轻量兜底，避免归档被染色；仍限幅防偶发削波
		opts.Highpass = s.appHighpass
		opts.Limiter = s.appLimiter
		return opts
	}
	opts.Highpass = s.appHighpass
	opts.Limiter = s.appLimiter
	opts.Loudnorm = s.appLoudnorm
	opts.Denoise = s.appDenoise
	opts.RatePitch = s.appFixPitch
	if s.appPitchRatio > 0 && s.appPitchRatio < 1 {
		opts.RateRatio = s.appPitchRatio
	} else if s.appFixRate && len(frames) >= 2 {
		tocMs := audio.SumOpusPacketsDurationMs(frames)
		spanMs := audio.SessionTSSpanMs(frames, absTSUs) // 仅 chunk_ts；无 ts 则 0，不触发
		if tocMs > 0 && spanMs >= tocMs*5/4 && spanMs <= tocMs*3 {
			opts.RateRatio = float64(tocMs) / float64(spanMs)
		}
	}
	return opts
}

// ProcessSessionAsync 异步处理一次音频段（end=true 触发）。
// audioKind：0=事件流 1=录音文件；fullTranslate=true 时跑 OSS→AI→TTS；false 时仅转码上传原音归档。
func (s *Service) ProcessSessionAsync(sn string, deviceTS int64, petHint, pid string, audioKind int, fullTranslate bool) {
	s.processSessionsAsync(sn, deviceTS, nil, petHint, pid, audioKind, fullTranslate)
}

func rawPackPendingKey(sn string, deviceTS int64) string {
	return fmt.Sprintf("%s:%d", sn, deviceTS)
}

// UploadEventRawPackAsync 事件流 end 后：把本段 audio_chunk 真实 Opus 帧打 zip 上传 OSS（无 PLC、不 stitch）。
// 与可播转码 / 多段 stitch 并行独立。同 sn+device_ts 并发只跑一次。
func (s *Service) UploadEventRawPackAsync(sn string, deviceTS int64) {
	if s == nil || s.store == nil || s.oss == nil || sn == "" || deviceTS <= 0 {
		return
	}
	key := rawPackPendingKey(sn, deviceTS)
	if _, loaded := s.rawPackInflight.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	go func() {
		defer s.rawPackInflight.Delete(key)
		// 打 zip + 等 translate 建档；OSS 成功后即使超时也保留 pending 供 flush
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		if err := s.uploadEventRawPack(ctx, sn, deviceTS); err != nil {
			s.log.Warn("事件流原帧 zip 上传失败",
				zap.String("sn", sn),
				zap.Int64("device_ts", deviceTS),
				zap.Error(err),
			)
		}
	}()
}

func (s *Service) uploadEventRawPack(ctx context.Context, sn string, deviceTS int64) error {
	// 已有 URL 则跳过（避免重复打 zip）
	if existing, err := s.store.GetTranslateBySNDeviceTS(ctx, sn, deviceTS); err == nil && existing != nil {
		if strings.TrimSpace(existing.OriginRawURL) != "" {
			return nil
		}
	}
	raw, codec, err := s.store.LoadSessionRawFrames(ctx, sn, deviceTS, model.AudioKindEvent)
	if err != nil {
		return err
	}
	frames := make([][]byte, 0, len(raw))
	seqs := make([]int, 0, len(raw))
	tss := make([]int64, 0, len(raw))
	deviceTotal := 0
	for _, r := range raw {
		frames = append(frames, r.Opus)
		seqs = append(seqs, r.Seq)
		tss = append(tss, r.ChunkTS)
		if r.Total > deviceTotal {
			deviceTotal = r.Total
		}
	}
	meta := audio.RawOpusZipMeta{
		SN: sn, DeviceTS: deviceTS, Kind: model.AudioKindEvent, Codec: codec,
		Total: deviceTotal, Chunks: len(frames), Seqs: seqs, ChunkTS: tss,
	}
	zipBytes, err := audio.PackRawOpusZip(meta, frames)
	if err != nil {
		return err
	}
	filename := fmt.Sprintf("%d.raw.zip", deviceTS)
	object := s.oss.BuildObjectKey("origin_raw", sn, filename)
	url, err := s.oss.UploadBytes(object, zipBytes, "application/zip")
	if err != nil {
		return err
	}
	if url == "" {
		return fmt.Errorf("origin_raw UploadBytes 返回空 URL")
	}
	s.log.Info("✅ [原帧zip] OSS 上传成功",
		zap.String("sn", sn),
		zap.Int64("device_ts", deviceTS),
		zap.Int("frames", len(frames)),
		zap.Int("device_total", deviceTotal),
		zap.String("url", url),
		zap.String("object_key", object),
		zap.Int("zip_bytes", len(zipBytes)),
	)
	// 先落 pending：即使等待建档超时，process 里 flushPendingRawPack 仍能补写
	s.rawPackPending.Store(rawPackPendingKey(sn, deviceTS), pendingRawPack{URL: url, Frames: len(frames)})
	deadline := time.Now().Add(90 * time.Second)
	for {
		ok, err := s.store.SetTranslateOriginRawBySNDeviceTS(ctx, sn, deviceTS, url, len(frames))
		if err != nil {
			return fmt.Errorf("回写 origin_raw_url: %w", err)
		}
		if ok {
			s.rawPackPending.Delete(rawPackPendingKey(sn, deviceTS))
			s.log.Info("已写入 origin_raw_url",
				zap.String("sn", sn),
				zap.Int64("device_ts", deviceTS),
				zap.String("url", url),
				zap.Int("frames", len(frames)),
			)
			return nil
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			// OSS 已成功、pending 已存，交由 flushPendingRawPack / 下次 Ensure
			s.log.Info("原帧 zip 已上传，等待 translate 建档被取消，pending 保留",
				zap.String("sn", sn),
				zap.Int64("device_ts", deviceTS),
				zap.String("url", url),
			)
			return nil
		case <-time.After(500 * time.Millisecond):
		}
	}
	s.log.Info("原帧 zip 已上传，translate 尚未建档，待 flushPendingRawPack 补写",
		zap.String("sn", sn),
		zap.Int64("device_ts", deviceTS),
		zap.String("url", url),
	)
	return nil
}

func (s *Service) flushPendingRawPack(ctx context.Context, sn string, deviceTS int64, translateID int64) {
	if s == nil || translateID <= 0 {
		return
	}
	key := rawPackPendingKey(sn, deviceTS)
	v, ok := s.rawPackPending.Load(key)
	if !ok {
		return
	}
	p, ok := v.(pendingRawPack)
	if !ok || p.URL == "" {
		return
	}
	if err := s.store.SetTranslateOriginRaw(ctx, translateID, p.URL, p.Frames); err != nil {
		s.log.Warn("补写 origin_raw_url 失败",
			zap.String("sn", sn),
			zap.Int64("device_ts", deviceTS),
			zap.Int64("translate_id", translateID),
			zap.Error(err),
		)
		return
	}
	s.rawPackPending.Delete(key)
	s.log.Info("已补写 origin_raw_url",
		zap.String("sn", sn),
		zap.Int64("device_ts", deviceTS),
		zap.Int64("translate_id", translateID),
		zap.String("url", p.URL),
		zap.Int("frames", p.Frames),
	)
}

// ensureEventRawPackAfterTranslate translate 已建档：等并发 zip 结束，仍无 origin_raw_url 则补打。
func (s *Service) ensureEventRawPackAfterTranslate(sn string, deviceTS, translateID int64) {
	if s == nil || sn == "" || deviceTS <= 0 || translateID <= 0 {
		return
	}
	go func() {
		key := rawPackPendingKey(sn, deviceTS)
		deadline := time.Now().Add(100 * time.Second)
		for time.Now().Before(deadline) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			s.flushPendingRawPack(ctx, sn, deviceTS, translateID)
			tr, err := s.store.GetTranslateBySNDeviceTS(ctx, sn, deviceTS)
			cancel()
			if err == nil && tr != nil && strings.TrimSpace(tr.OriginRawURL) != "" {
				return
			}
			if _, busy := s.rawPackInflight.Load(key); !busy {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		s.flushPendingRawPack(ctx, sn, deviceTS, translateID)
		tr, err := s.store.GetTranslateBySNDeviceTS(ctx, sn, deviceTS)
		cancel()
		if err == nil && tr != nil && strings.TrimSpace(tr.OriginRawURL) != "" {
			return
		}
		s.log.Info("translate 已建档仍无 origin_raw_url，补打原帧 zip",
			zap.String("sn", sn),
			zap.Int64("device_ts", deviceTS),
			zap.Int64("translate_id", translateID),
		)
		s.UploadEventRawPackAsync(sn, deviceTS)
	}()
}

func sessionKey(sn string, deviceTS int64, audioKind int) string {
	return fmt.Sprintf("%s:%d:%d", sn, deviceTS, audioKind)
}

func (s *Service) sessionLock(key string) *sync.Mutex {
	actual, _ := s.sessionMu.LoadOrStore(key, &sync.Mutex{})
	return actual.(*sync.Mutex)
}

// ScheduleOriginRebuild 段已 end 后迟到的补发帧：防抖后重拼并覆盖 OSS 原对象。
// 若翻译记录尚未创建（首跑还在等齐），则不再触发，交给首跑 LoadSessionOpus 吃到新帧。
func (s *Service) ScheduleOriginRebuild(sn string, deviceTS int64, petHint, pid string, audioKind int) {
	if s == nil || !s.Enabled() || !s.uploadOrigin {
		return
	}
	key := sessionKey(sn, deviceTS, audioKind)
	s.rebuildMu.Lock()
	defer s.rebuildMu.Unlock()
	if s.rebuildWait == nil {
		s.rebuildWait = make(map[string]*time.Timer)
	}
	if t, ok := s.rebuildWait[key]; ok {
		t.Stop()
	}
	s.rebuildWait[key] = time.AfterFunc(2*time.Second, func() {
		s.rebuildMu.Lock()
		delete(s.rebuildWait, key)
		s.rebuildMu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		exists, err := s.store.HasTranslateSession(ctx, sn, deviceTS)
		if err != nil {
			s.log.Warn("补发重拼：查翻译记录失败", zap.String("sn", sn), zap.Int64("device_ts", deviceTS), zap.Error(err))
			return
		}
		if !exists {
			// 首跑尚未落库：可能仍在等齐窗口内，新帧会被首跑一并拼上
			s.log.Info("补发重拼：翻译记录尚无，跳过（交给首跑拼装）",
				zap.String("sn", sn), zap.Int64("device_ts", deviceTS), zap.Int("audio_kind", audioKind))
			return
		}
		mu := s.sessionLock(key)
		mu.Lock()
		defer mu.Unlock()
		if err := s.rebuildOriginOnly(ctx, sn, deviceTS, audioKind); err != nil {
			s.log.Error("补发重拼原音失败",
				zap.String("sn", sn),
				zap.Int64("device_ts", deviceTS),
				zap.Int("audio_kind", audioKind),
				zap.Error(err),
			)
		}
	})
}

// rebuildOriginOnly 已有 a_emb_translate 时：按当前分片重拼并 PutObject 覆盖同 key 原音。
func (s *Service) rebuildOriginOnly(ctx context.Context, sn string, deviceTS int64, audioKind int) error {
	st, err := s.store.GetSessionChunkStats(ctx, sn, deviceTS, audioKind)
	if err != nil {
		return err
	}
	s.log.Info("补发重拼：对比 total 与入库帧数",
		zap.String("sn", sn),
		zap.Int64("device_ts", deviceTS),
		zap.Int("audio_kind", audioKind),
		zap.Int("chunks", st.Chunks),
		zap.Int("total", st.Total),
		zap.Bool("ready", st.Ready()),
		zap.Bool("incomplete", st.Incomplete()),
	)

	opus, frames, absTSUs, gapFilled, _, codecName, chunks, err := s.store.LoadSessionOpusDetailed(ctx, sn, deviceTS, audioKind)
	if err != nil {
		return err
	}
	frames, _ = s.applyEventPaceAlign(ctx, sn, deviceTS, audioKind, frames, absTSUs)
	acodec := model.AudioCodec(model.NormalizeAudioCodec(codecName))
	if acodec == "" {
		acodec = model.CodecOpus
	}
	frameSec := model.FrameDurationSecForKind(audioKind)
	estMs := int(float64(chunks) * frameSec * 1000)
	if acodec.IsOpus() && len(frames) > 0 {
		if sumMs := audio.SumOpusPacketsDurationMs(frames); sumMs > 0 {
			estMs = sumMs
			frameSec = float64(sumMs) / 1000.0 / float64(len(frames))
		}
	}
	encOpts := s.appEncodeForSession(audioKind, frames, absTSUs)
	if encOpts.RateRatio > 0 && encOpts.RateRatio < 0.999 && estMs > 0 {
		estMs = int(float64(estMs)/encOpts.RateRatio + 0.5)
	}
	appBytes, appExt, appCT, err := audio.EncodeForAppFrames(ctx, s.ffmpegPath, frames, opus, string(acodec), encOpts)
	if err != nil {
		return fmt.Errorf("重拼转码失败: %w", err)
	}
	filename := fmt.Sprintf("%d.%s.%s", deviceTS, audio.OpusDecodeMode(encOpts), appExt)
	object := s.oss.BuildObjectKey("origin", sn, filename)
	originURL, err := s.oss.UploadBytes(object, appBytes, appCT)
	if err != nil {
		return fmt.Errorf("重拼上传 OSS 失败: %w", err)
	}
	duration := ""
	if estMs > 0 {
		duration = fmt.Sprintf("%.1f", float64(estMs)/1000.0)
	}
	s.log.Info("补发重拼：已加载分片",
		zap.String("sn", sn),
		zap.Int64("device_ts", deviceTS),
		zap.Int("chunks", chunks),
		zap.Int("frames_for_mux", len(frames)),
		zap.Int("gap_filled", gapFilled),
		zap.Int("est_duration_ms", estMs),
	)
	id, err := s.store.GetTranslateIDBySNDeviceTS(ctx, sn, deviceTS)
	if err != nil {
		return err
	}
	if id > 0 {
		if err := s.store.SetTranslateOrigin(ctx, id, originURL, duration); err != nil {
			s.log.Warn("重拼回写 origin_audio_url 失败", zap.Int64("id", id), zap.Error(err))
		}
	}
	s.log.Info("✅ 补发重拼原音已覆盖 OSS",
		zap.String("sn", sn),
		zap.Int64("device_ts", deviceTS),
		zap.Int("audio_kind", audioKind),
		zap.Int64("translate_id", id),
		zap.Int("chunks", chunks),
		zap.Int("total", st.Total),
		zap.String("url", originURL),
		zap.String("object_key", object),
	)
	if s.redis != nil {
		estMs := int(float64(chunks) * frameSec * 1000)
		_ = s.redis.PatchAudioCycleAssemble(ctx, sn, deviceTS, audioKind, chunks, st.Total, estMs, st.Incomplete(), originURL, true)
	}
	return nil
}

func (s *Service) process(ctx context.Context, sn string, deviceTS int64, petHint, pid string, audioKind int, fullTranslate bool, stitchTSs []int64) error {
	// App 侧会话必须能播原音：未开 OSS 时不写 a_emb_translate（避免一堆空 URL 记录）
	if !s.uploadOrigin {
		s.log.Info("oss 未启用，跳过 a_emb_translate 入库",
			zap.String("sn", sn),
			zap.Int64("device_ts", deviceTS),
			zap.Int("audio_kind", audioKind),
		)
		s.noteSkip(ctx, sn, deviceTS, audioKind, "oss_disabled", "uploadOrigin=false，不写空 URL 记录")
		return nil
	}

	s.log.Info("📚 [翻译会话开始] 处理整段录音 end=true 后触发",
		zap.String("sn", sn),
		zap.Int64("device_ts", deviceTS),
		zap.Int("audio_kind", audioKind),
		zap.String("kind_label", model.AudioStreamKindLabel(audioKind)),
		zap.String("pet_hint", petHint),
		zap.String("pid", pid),
		zap.Bool("persist", s.persist),
		zap.Bool("upload_oss", s.uploadOrigin),
		zap.Bool("full_translate", fullTranslate),
		zap.Bool("push_device", s.pushDevice),
	)

	// 未绑定用户/宠物：不接收录音（网关侧已拦截；此处兜底，避免历史分片触发 OSS/翻译）
	if emb, err := s.store.LookupEmbBySN(ctx, sn); err != nil {
		s.log.Warn("翻译前查 a_emb_sn 失败，继续处理", zap.String("sn", sn), zap.Error(err))
	} else if !emb.HasActiveBinding() {
		s.log.Info("设备未绑定用户/宠物，跳过录音会话",
			zap.String("sn", sn),
			zap.Int64("device_ts", deviceTS),
			zap.Int("audio_kind", audioKind),
		)
		s.noteSkip(ctx, sn, deviceTS, audioKind, "unbound", "a_emb_sn.bind_user_id/bind_pet_id 为空或0")
		return nil
	}

	// end 可能略早于同批尾包；先短等。更晚的断线补发靠 ScheduleOriginRebuild 覆盖 OSS。
	const waitAttempts = 15 // 最多约 3s
	const waitStep = 200 * time.Millisecond
	var lastStats store.SessionChunkStats
	for i := 0; i < waitAttempts; i++ {
		time.Sleep(waitStep)
		st, err := s.store.GetSessionChunkStats(ctx, sn, deviceTS, audioKind)
		if err != nil {
			s.log.Warn("查询会话分片统计失败",
				zap.String("sn", sn), zap.Int64("device_ts", deviceTS), zap.Int("audio_kind", audioKind), zap.Error(err))
			break
		}
		lastStats = st
		if st.Ready() {
			if i > 0 {
				s.log.Info("会话分片已齐",
					zap.String("sn", sn),
					zap.Int64("device_ts", deviceTS),
					zap.Int("audio_kind", audioKind),
					zap.Int("chunks", st.Chunks),
					zap.Int("total", st.Total),
					zap.Int("wait_rounds", i+1),
				)
			}
			break
		}
		s.log.Debug("等待会话分片到齐",
			zap.String("sn", sn),
			zap.Int64("device_ts", deviceTS),
			zap.Int("audio_kind", audioKind),
			zap.Int("chunks", st.Chunks),
			zap.Int("total", st.Total),
			zap.Bool("has_end", st.HasEnd),
			zap.Int("round", i+1),
		)
	}
	if lastStats.Incomplete() {
		s.log.Warn("会话分片相对 total 未收齐（传输/拼装缺口；迟到补发会触发 OSS 重拼覆盖）",
			zap.String("sn", sn),
			zap.Int64("device_ts", deviceTS),
			zap.Int("audio_kind", audioKind),
			zap.Int("chunks", lastStats.Chunks),
			zap.Int("total", lastStats.Total),
			zap.Int("missing", lastStats.Total-lastStats.Chunks),
		)
	} else if lastStats.Chunks > 0 && !lastStats.Ready() {
		s.log.Warn("会话分片未完全到齐，仍继续拼装",
			zap.String("sn", sn),
			zap.Int64("device_ts", deviceTS),
			zap.Int("audio_kind", audioKind),
			zap.Int("chunks", lastStats.Chunks),
			zap.Int("total", lastStats.Total),
			zap.Bool("has_end", lastStats.HasEnd),
		)
	}

	opus, frames, absTSUs, gapFilled, petFromDB, codecName, chunks, err := s.store.LoadSessionOpusDetailed(ctx, sn, deviceTS, audioKind)
	if err != nil {
		s.log.Info("会话分片不可用，跳过 a_emb_translate",
			zap.String("sn", sn),
			zap.Int64("device_ts", deviceTS),
			zap.Int("audio_kind", audioKind),
			zap.Error(err),
		)
		s.noteSkip(ctx, sn, deviceTS, audioKind, "load_fail", err.Error())
		return nil
	}
	if len(stitchTSs) > 0 && audioKind == model.AudioKindEvent {
		frames, absTSUs, opus, chunks, gapFilled, petFromDB, codecName = s.appendStitchSegments(
			ctx, sn, audioKind, frames, absTSUs, opus, chunks, gapFilled, petFromDB, codecName, stitchTSs)
		s.log.Info("事件流多段已拼接",
			zap.String("sn", sn),
			zap.Int64("device_ts", deviceTS),
			zap.Int64s("stitch_device_ts", stitchTSs),
			zap.Int("chunks", chunks),
			zap.Int("frames_for_mux", len(frames)),
		)
	}
	frames, pacePad := s.applyEventPaceAlign(ctx, sn, deviceTS, audioKind, frames, absTSUs)
	acodec := model.AudioCodec(model.NormalizeAudioCodec(codecName))
	if acodec == "" {
		acodec = model.CodecOpus
	}
	frameSec := model.FrameDurationSecForKind(audioKind)
	frameMs := int(frameSec * 1000)
	estDurMs := int(float64(chunks) * frameSec * 1000)
	if acodec.IsOpus() && len(frames) > 0 {
		if ms := audio.OpusPacketFrameSizeMs(frames[0]); ms > 0 {
			frameMs = ms
			frameSec = float64(ms) / 1000.0
		}
		// 按包总时长（含一包多帧 + 补洞帧 + 节拍补静音）估算
		if sumMs := audio.SumOpusPacketsDurationMs(frames); sumMs > 0 {
			estDurMs = sumMs
			frameSec = float64(sumMs) / 1000.0 / float64(len(frames))
		}
	}
	missing := 0
	if lastStats.Total > chunks {
		missing = lastStats.Total - chunks
	}
	s.log.Info("完整录音已从 audio_chunk 加载（用于翻译/OSS 上传）",
		zap.String("sn", sn),
		zap.Int64("device_ts", deviceTS),
		zap.Int("audio_kind", audioKind),
		zap.Int("opus_total_bytes", len(opus)),
		zap.Int("chunks", chunks),
		zap.Int("frames_for_mux", len(frames)),
		zap.Int("gap_filled", gapFilled),
		zap.Int("pace_pad", pacePad),
		zap.Int("device_total", lastStats.Total),
		zap.Bool("chunks_match_total", lastStats.Total <= 0 || chunks >= lastStats.Total),
		zap.Int("missing", missing),
		zap.String("codec", string(acodec)),
		zap.String("pet_type", petFromDB),
		zap.Int("frame_ms", frameMs),
		zap.Int("est_duration_ms", estDurMs),
	)
	if audioKind == model.AudioKindEvent {
		s.log.Info("=======事件音频拼装=======",
			zap.String("sn", sn),
			zap.Int64("device_ts", deviceTS),
			zap.Int("chunks", chunks),
			zap.Int("device_total", lastStats.Total),
			zap.Int("missing", missing),
			zap.Bool("incomplete", lastStats.Incomplete()),
			zap.Int("est_duration_ms", estDurMs),
			zap.String("hint", "est_duration_ms 偏短且 incomplete=true → 丢帧导致听感偏快"),
		)
		if s.redis != nil {
			_ = s.redis.PatchAudioCycleAssemble(ctx, sn, deviceTS, audioKind, chunks, lastStats.Total, estDurMs, lastStats.Incomplete(), "", false)
		}
	}
	// 过短/空内容：噪声或半截上报，不入库（MP3 单帧约 144B，Opus 60ms=16kbps~40B；留 10B 冗余到 50）
	const minAudioBytes = 50
	if len(opus) < minAudioBytes {
		s.log.Info("会话音频过短，跳过 a_emb_translate 入库",
			zap.String("sn", sn),
			zap.Int64("device_ts", deviceTS),
			zap.Int("audio_bytes", len(opus)),
			zap.Int("chunks", chunks),
			zap.String("codec", string(acodec)),
			zap.Int("min_bytes", minAudioBytes),
		)
		s.noteSkip(ctx, sn, deviceTS, audioKind, "too_short_bytes",
			fmt.Sprintf("opus_bytes=%d < min=%d", len(opus), minAudioBytes))
		return nil
	}
	// 整段时长过滤：文件路用 min_duration_sec；事件流默认不过滤（设备侧已滤 <800ms，见 upload_audio.txt）
	// 仅当显式配置 min_duration_event_sec>0 时才对事件流过滤。
	if s.dropShort {
		minDur := 0.0
		thresholdKey := ""
		if audioKind == model.AudioKindEvent {
			if s.minDurationEventSec > 0 {
				minDur = s.minDurationEventSec
				thresholdKey = "min_duration_event_sec"
			}
		} else if s.minDurationSec > 0 {
			minDur = s.minDurationSec
			thresholdKey = "min_duration_sec"
		}
		if minDur > 0 {
			durSec := float64(estDurMs) / 1000.0
			if durSec < minDur {
				detail := fmt.Sprintf("%s: duration_sec=%.3f < %.3f (chunks=%d frames=%d gap_filled=%d frame_ms=%d kind=%d est_ms=%d)",
					thresholdKey, durSec, minDur, chunks, len(frames), gapFilled, frameMs, audioKind, estDurMs)
				s.log.Info("会话时长过短，丢弃（不入库）",
					zap.String("sn", sn),
					zap.Int64("device_ts", deviceTS),
					zap.Int("audio_kind", audioKind),
					zap.String("threshold_key", thresholdKey),
					zap.Float64("duration_sec", durSec),
					zap.Float64("min_duration", minDur),
					zap.Int("chunks", chunks),
					zap.Int("frames_for_mux", len(frames)),
					zap.Int("gap_filled", gapFilled),
					zap.Int("frame_ms", frameMs),
					zap.Int("est_duration_ms", estDurMs),
				)
				s.noteSkip(ctx, sn, deviceTS, audioKind, "drop_short", detail)
				return nil
			}
		}
	}
	// 对照 docs/upload_audio.txt：设备侧事件流最短约 800ms；更短仍入库但记 Redis 异常，便于查固件/拼装
	if audioKind == model.AudioKindEvent && estDurMs > 0 && estDurMs < 800 {
		recvSpanMs, _ := s.store.GetSessionRecvSpanMs(ctx, sn, deviceTS, audioKind)
		s.log.Warn("事件流时长低于设备约定最短 800ms（仍继续入库）",
			zap.String("sn", sn),
			zap.Int64("device_ts", deviceTS),
			zap.Int("est_duration_ms", estDurMs),
			zap.Int64("recv_span_ms", recvSpanMs),
			zap.Int("chunks", chunks),
			zap.Int("frame_ms", frameMs),
			zap.Int("gap_filled", gapFilled),
			zap.String("hint", "upload_audio.txt: 不足800ms固件应丢弃不上传；若仍上传需查固件或帧长/丢帧"),
		)
		s.noteAnomaly(ctx, sn, deviceTS, audioKind,
			fmt.Sprintf("below_device_min_800ms: est_ms=%d recv_span_ms=%d chunks=%d frame_ms=%d gap_filled=%d (设备约定事件流≥800ms)",
				estDurMs, recvSpanMs, chunks, frameMs, gapFilled))
	}
	s.log.Info("✅ 完整录音数据读取成功，准备入库+处理",
		zap.String("sn", sn),
		zap.Int64("device_ts", deviceTS),
		zap.Int("opus_total_bytes", len(opus)),
		zap.Int("shards", chunks),
		zap.String("codec", string(acodec)),
		zap.String("pet_from_db", petFromDB),
		zap.String("pet_hint_raw", petHint),
	)

	existing, err := s.store.GetTranslateBySNDeviceTS(ctx, sn, deviceTS)
	if err != nil {
		return err
	}
	if existing != nil {
		st := strings.ToLower(strings.TrimSpace(existing.Status))
		if st == "ok" {
			s.log.Info("同 sn+device_ts 已有翻译记录，跳过重复入库",
				zap.String("sn", sn),
				zap.Int64("device_ts", deviceTS),
				zap.Int64("id", existing.ID),
			)
			if audioKind == model.AudioKindEvent {
				s.flushPendingRawPack(ctx, sn, deviceTS, existing.ID)
				if strings.TrimSpace(existing.OriginRawURL) == "" {
					s.ensureEventRawPackAfterTranslate(sn, deviceTS, existing.ID)
				}
			}
			s.noteSkip(ctx, sn, deviceTS, audioKind, "already_exists",
				"同 sn+device_ts 已有 a_emb_translate，跳过重复入库")
			return nil
		}
		origin := strings.TrimSpace(existing.OriginAudioURL)
		dur := strings.TrimSpace(existing.OriginAudioDuration)
		if origin != "" && (st == model.TranslateStatusPending || st == model.TranslateStatusTranslating || st == model.TranslateStatusFail) {
			petType := strings.TrimSpace(existing.PetType)
			if petType == "" {
				petType = "dog"
			}
			s.log.Info("翻译记录未完成，续跑后续步骤",
				zap.String("sn", sn),
				zap.Int64("device_ts", deviceTS),
				zap.Int64("id", existing.ID),
				zap.String("status", existing.Status),
				zap.Bool("full_translate", fullTranslate),
			)
			if !fullTranslate {
				if err := s.store.FinishTranslateOK(context.Background(), existing.ID, origin, "", "", "", dur); err != nil {
					return err
				}
				return nil
			}
			return s.runTranslateAfterOrigin(ctx, existing.ID, sn, petType, origin, dur)
		}
	}

	s.log.Info("开始转码并上传原音（App 格式）",
		zap.String("sn", sn),
		zap.Int64("device_ts", deviceTS),
		zap.String("src_codec", string(acodec)),
		zap.String("app_format", s.appEncode.Format),
		zap.Int("frames", len(frames)),
		zap.String("ffmpeg", s.ffmpegPath),
	)

	// petType 选择顺序：
	// 1. 丢弃 hint / db 中所有"占位数字串"（0/1/4294967295/FFFFFFFF）
	// 2. petHint（设备 pet→breedCode 解析结果，优先来自 meta.Pet="dog"）
	// 3. petFromDB（audio_chunk.pet 列，排除 breed 占位 0）
	// 4. 最后兜底 "dog"
	isValidPetStr := func(p string) bool {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "", "0", "1", "4294967295", "ffffffff", "-1":
			return false
		}
		return true
	}
	petType := ""
	if isValidPetStr(petHint) {
		petType = strings.TrimSpace(petHint)
	}
	if petType == "" && isValidPetStr(petFromDB) {
		petType = strings.TrimSpace(petFromDB)
	}
	if petType == "" {
		petType = "dog"
	}
	s.log.Debug("翻译会话 petType 决策",
		zap.String("sn", sn),
		zap.Int64("device_ts", deviceTS),
		zap.String("petHint", petHint),
		zap.String("petFromDB", petFromDB),
		zap.String("final_pet_type", petType),
	)

	if model.IsUnboundPetID(pid) {
		pid = ""
	}
	userID, embID := 0, 0
	emb, err := s.store.LookupEmbBySN(ctx, sn)
	if err != nil {
		s.log.Warn("查 a_emb_sn 失败，翻译记录 emb_id/user_id 留空", zap.String("sn", sn), zap.Error(err))
	} else if emb == nil {
		s.log.Warn("a_emb_sn 无此 SN，仍写 a_emb_translate（user_id/emb_id 空）", zap.String("sn", sn))
	} else {
		embID = emb.ID
		// status=1 正常绑定才带出用户/宠物；解绑仅保留 emb_id
		if emb.Status == 1 {
			userID = emb.UserID
			if pid == "" {
				pid = emb.PetID
			}
		} else {
			s.log.Info("a_emb_sn 设备已解绑/无效，仅关联 emb_id",
				zap.String("sn", sn),
				zap.Int("emb_id", embID),
				zap.Int("status", emb.Status),
			)
		}
	}
	if model.IsUnboundPetID(pid) {
		pid = ""
	}

	duration := ""
	if estDurMs > 0 {
		duration = fmt.Sprintf("%.1f", float64(estDurMs)/1000.0)
	} else if chunks > 0 {
		duration = fmt.Sprintf("%.1f", float64(chunks)*frameSec)
	}

	encodeStart := time.Now()
	encOpts := s.appEncodeForSession(audioKind, frames, absTSUs)
	if encOpts.RateRatio > 0 && encOpts.RateRatio < 0.999 && estDurMs > 0 {
		estDurMs = int(float64(estDurMs)/encOpts.RateRatio + 0.5)
		duration = fmt.Sprintf("%.1f", float64(estDurMs)/1000.0)
	}
	s.log.Info("⏳ [原音转码] 开始",
		zap.String("sn", sn),
		zap.Int64("device_ts", deviceTS),
		zap.Int("raw_bytes", len(opus)),
		zap.Int("frames", len(frames)),
		zap.Int("gap_filled", gapFilled),
		zap.Int("est_duration_ms", estDurMs),
		zap.String("opus_decode", audio.OpusDecodeMode(encOpts)),
		zap.Bool("denoise", encOpts.Denoise),
		zap.Int("denoise_nr", encOpts.DenoiseNR),
		zap.Bool("loudnorm", encOpts.Loudnorm),
		zap.Float64("rate_ratio", encOpts.RateRatio),
		zap.Bool("rate_pitch", encOpts.RatePitch),
		zap.String("app_format", encOpts.Format),
	)
	appBytes, appExt, appCT, err := audio.EncodeForAppFrames(ctx, s.ffmpegPath, frames, opus, string(acodec), encOpts)
	encodeDur := time.Since(encodeStart)
	if err != nil {
		s.log.Warn("❌ [原音转码] 失败，跳过 a_emb_translate 入库",
			zap.String("sn", sn),
			zap.Int64("device_ts", deviceTS),
			zap.String("src_codec", string(acodec)),
			zap.String("app_format", s.appEncode.Format),
			zap.Int("raw_bytes", len(opus)),
			zap.Int("frames", len(frames)),
			zap.Duration("dur", encodeDur),
			zap.Error(err),
		)
		s.noteSkip(ctx, sn, deviceTS, audioKind, "encode_fail", err.Error())
		return nil
	}
	s.log.Info("✅ [原音转码] 成功",
		zap.String("sn", sn),
		zap.Int64("device_ts", deviceTS),
		zap.String("opus_decode", audio.OpusDecodeMode(encOpts)),
		zap.String("app_format", appExt),
		zap.Int("raw_bytes", len(opus)),
		zap.Int("app_bytes", len(appBytes)),
		zap.Duration("dur", encodeDur),
	)

	filename := fmt.Sprintf("%d.%s.%s", deviceTS, audio.OpusDecodeMode(encOpts), appExt)
	object := s.oss.BuildObjectKey("origin", sn, filename)
	uploadStart := time.Now()
	s.log.Info("⏳ [原音上传] 开始 OSS",
		zap.String("sn", sn),
		zap.Int64("device_ts", deviceTS),
		zap.String("object_key", object),
		zap.String("content_type", appCT),
		zap.Int("app_bytes", len(appBytes)),
	)
	originURL, err := s.oss.UploadBytes(object, appBytes, appCT)
	uploadDur := time.Since(uploadStart)
	if err != nil {
		suggestedEndpoint, reqID, errCode := extractOSSErrorHints(err)
		fields := []zap.Field{
			zap.String("sn", sn),
			zap.Int64("device_ts", deviceTS),
			zap.String("src_codec", string(acodec)),
			zap.String("app_format", appExt),
			zap.Int("raw_bytes", len(opus)),
			zap.Int("app_bytes", len(appBytes)),
			zap.String("object_key", object),
			zap.Error(err),
		}
		if errCode != "" {
			fields = append(fields, zap.String("oss_error_code", errCode))
		}
		if reqID != "" {
			fields = append(fields, zap.String("oss_request_id", reqID))
		}
		if suggestedEndpoint != "" {
			fields = append(fields,
				zap.String("oss_suggested_endpoint", suggestedEndpoint),
				zap.String("config_tip", "请修改 config.yaml 中 oss.endpoint 为上面的值，并保证 oss.bucket 地域与 endpoint 一致"),
			)
		}
		s.log.Warn("❌ [原音上传] OSS 失败，跳过 a_emb_translate 入库", fields...)
		s.noteSkip(ctx, sn, deviceTS, audioKind, "oss_fail", err.Error())
		return nil
	}
	if originURL == "" {
		s.log.Warn("❌ [原音上传] OSS 无 URL，跳过 a_emb_translate 入库",
			zap.String("sn", sn),
			zap.Int64("device_ts", deviceTS),
			zap.String("src_codec", string(acodec)),
			zap.Int("app_bytes", len(appBytes)),
			zap.Duration("dur", uploadDur),
		)
		s.noteSkip(ctx, sn, deviceTS, audioKind, "oss_empty_url", "UploadBytes 返回空 URL")
		return nil
	}
	s.log.Info("✅ [原音上传] OSS 成功",
		zap.String("sn", sn),
		zap.Int64("device_ts", deviceTS),
		zap.String("url", originURL),
		zap.String("object_key", object),
		zap.Duration("dur", uploadDur),
	)
	if s.redis != nil && audioKind == model.AudioKindEvent {
		_ = s.redis.PatchAudioCycleAssemble(ctx, sn, deviceTS, audioKind, chunks, lastStats.Total, estDurMs, lastStats.Incomplete(), originURL, false)
	}

	dbStart := time.Now()
	s.log.Info("⏳ [原音入库] 写入 a_emb_translate",
		zap.String("sn", sn),
		zap.Int64("device_ts", deviceTS),
		zap.Int("audio_kind", audioKind),
		zap.String("kind_label", model.AudioStreamKindLabel(audioKind)),
		zap.Int("user_id", userID),
		zap.Int("emb_id", embID),
		zap.String("pet_type", petType),
	)
	id, alreadyExists, err := s.store.CreateTranslatePending(ctx, userID, embID, sn, petType, pid, deviceTS, audioKind)
	if err != nil {
		return fmt.Errorf("创建 a_emb_translate 失败: %w", err)
	}
	if alreadyExists {
		s.log.Info("a_emb_translate 已存在，续写 origin 并继续后续步骤",
			zap.String("sn", sn),
			zap.Int64("device_ts", deviceTS),
			zap.Int("audio_kind", audioKind),
			zap.Int64("translate_id", id),
			zap.Duration("dur", time.Since(dbStart)),
		)
	} else {
		s.log.Info("✅ [原音入库] pending 已创建",
			zap.String("sn", sn),
			zap.Int64("id", id),
			zap.Int("audio_kind", audioKind),
			zap.Int("user_id", userID),
			zap.Int("emb_id", embID),
			zap.String("pid", pid),
			zap.String("pet_type", petType),
			zap.Int64("device_ts", deviceTS),
			zap.String("codec", string(acodec)),
			zap.Int("audio_bytes", len(opus)),
			zap.Int("chunks", chunks),
			zap.String("duration", duration),
			zap.Bool("full_translate", fullTranslate),
			zap.Duration("dur", time.Since(dbStart)),
		)
	}

	if err := s.store.SetTranslateOrigin(ctx, id, originURL, duration); err != nil {
		s.log.Warn("回写 origin_audio_url 失败", zap.Int64("id", id), zap.Error(err))
	}
	if audioKind == model.AudioKindEvent {
		s.flushPendingRawPack(ctx, sn, deviceTS, id)
		// 建档后兜底：等并发 zip 结束，仍无 URL 再补打
		s.ensureEventRawPackAfterTranslate(sn, deviceTS, id)
	}

	if !fullTranslate {
		if err := s.store.FinishTranslateOK(context.Background(), id, originURL, "", "", "", duration); err != nil {
			_ = s.store.FinishTranslateFail(context.Background(), id, err.Error())
			return err
		}
		s.log.Info("✅ [原音入库] 完成 status=ok",
			zap.String("sn", sn),
			zap.Int64("id", id),
			zap.String("origin_audio_url", originURL),
			zap.String("duration", duration),
			zap.Duration("total_dur", time.Since(encodeStart)),
		)
		return nil
	}

	return s.runTranslateAfterOrigin(ctx, id, sn, petType, originURL, duration)
}

// runTranslateAfterOrigin 原音已在 OSS 后跑 AI→TTS。pending/translating/fail 可续跑。
func (s *Service) runTranslateAfterOrigin(ctx context.Context, id int64, sn, petType, originURL, duration string) error {
	fail := func(e error) error {
		_ = s.store.FinishTranslateFail(context.Background(), id, e.Error())
		return e
	}
	if err := s.store.MarkTranslateTranslating(ctx, id); err != nil {
		s.log.Warn("标记 translating 失败（继续翻译）", zap.Int64("id", id), zap.Error(err))
	}
	start := time.Now()
	s.log.Info("⏳ [AI翻译] 开始 status=translating",
		zap.String("sn", sn),
		zap.Int64("id", id),
		zap.String("pet_type", petType),
		zap.String("origin_audio_url", originURL),
		zap.String("duration", duration),
		zap.String("ai_endpoint", s.ai.Endpoint()),
	)
	aiStart := time.Now()
	aiRes, err := s.ai.TranslateByURL(ctx, petType, originURL, duration)
	if err != nil {
		s.log.Warn("❌ [AI翻译] 失败",
			zap.String("sn", sn),
			zap.Int64("id", id),
			zap.Duration("dur", time.Since(aiStart)),
			zap.Error(err),
		)
		return fail(fmt.Errorf("AI翻译失败: %w", err))
	}
	text := ""
	aiRaw := ""
	if aiRes != nil {
		text = strings.TrimSpace(aiRes.PetSays)
		aiRaw = string(aiRes.Raw)
	}
	s.log.Info("✅ [AI翻译] 成功",
		zap.String("sn", sn),
		zap.Int64("id", id),
		zap.String("pet_says", text),
		zap.Bool("is_supported", aiRes != nil && aiRes.IsSupported),
		zap.Duration("dur", time.Since(aiStart)),
	)

	var ttsURL string
	var opusFrames [][]byte
	if text != "" {
		s.log.Info("⏳ [百度TTS] 开始",
			zap.String("sn", sn),
			zap.Int64("id", id),
			zap.String("text", text),
		)
		ttsStart := time.Now()
		pcm, err := s.baidu.Text2AudioPCM16k(ctx, text, "sn_"+sn)
		if err != nil {
			s.log.Warn("❌ [百度TTS] 失败",
				zap.String("sn", sn),
				zap.Int64("id", id),
				zap.Duration("dur", time.Since(ttsStart)),
				zap.Error(err),
			)
			return fail(fmt.Errorf("百度TTS失败: %w", err))
		}
		s.log.Info("✅ [百度TTS] PCM 合成成功",
			zap.String("sn", sn),
			zap.Int("pcm_bytes", len(pcm)),
			zap.Duration("dur", time.Since(ttsStart)),
		)

		bundle, err := audio.PCM16kToOpus(ctx, s.ffmpegPath, pcm, s.opusBitrate)
		if err != nil {
			return fail(fmt.Errorf("PCM转OPUS失败: %w", err))
		}
		opusFrames = bundle.Frames

		appTTS, appExt, appCT, err := audio.EncodeForApp(ctx, s.ffmpegPath, pcm, "s16le", s.appEncode)
		if err != nil {
			return fail(fmt.Errorf("TTS转App格式失败: %w", err))
		}
		ttsObject := s.oss.BuildObjectKey("tts", sn, fmt.Sprintf("%d.%s", time.Now().UnixNano(), appExt))
		ttsURL, err = s.oss.UploadBytes(ttsObject, appTTS, appCT)
		if err != nil {
			return fail(fmt.Errorf("TTS App音频上传OSS失败: %w", err))
		}
		s.log.Info("✅ [TTS上传] App 可播音频已上 OSS；设备仍下发裸 Opus 帧",
			zap.String("sn", sn),
			zap.String("url", ttsURL),
			zap.String("app_format", appExt),
			zap.Int("app_bytes", len(appTTS)),
			zap.Int("device_opus_frames", len(opusFrames)),
		)
	} else {
		s.log.Info("AI 译文为空，跳过 TTS（当前音频未捕获有效猫狗声纹）",
			zap.String("sn", sn),
			zap.Int64("id", id),
		)
	}

	if err := s.store.FinishTranslateOK(context.Background(), id, originURL, ttsURL, text, aiRaw, duration); err != nil {
		return fail(err)
	}

	if len(opusFrames) > 0 {
		if err := s.store.SaveTranslateDeviceOpus(context.Background(), id, opusFrames); err != nil {
			s.log.Warn("保存设备 Opus 失败（不影响本次下发）",
				zap.String("sn", sn),
				zap.Int64("id", id),
				zap.Int("frames", len(opusFrames)),
				zap.Error(err),
			)
		} else {
			s.log.Info("✅ [设备Opus入库] 已写入 a_emb_translate.device_opus",
				zap.String("sn", sn),
				zap.Int64("id", id),
				zap.Int("frames", len(opusFrames)),
			)
		}
	}

	if s.pushDevice && len(opusFrames) > 0 && s.devicePusher != nil {
		if err := s.devicePusher.SendTTSFrames(sn, opusFrames); err != nil {
			s.log.Warn("TTS推送设备失败", zap.String("sn", sn), zap.Error(err))
		} else {
			s.log.Info("TTS已推送设备", zap.String("sn", sn), zap.Int("frames", len(opusFrames)))
		}
	}

	s.log.Info("✅ [翻译流水线] 完成",
		zap.String("sn", sn),
		zap.Int64("id", id),
		zap.String("pet_says", text),
		zap.String("origin", originURL),
		zap.String("tts", ttsURL),
		zap.Duration("total_dur", time.Since(start)),
	)
	return nil
}
