package redisx

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	keyAudioCyclePrefix = "pet:audio:cycle:"  // + sn:device_ts:kind
	keyAudioCycleZ      = "pet:audio:cycle:z" // score=ended_at, member=id
	keyAudioDiagStats   = "pet:audio:diag:stats"
	audioCycleTTL       = 7 * 24 * time.Hour
	audioCycleZMax      = 500
)

// AudioCycleEvent 上传处理周期时间线条目。
type AudioCycleEvent struct {
	At      int64  `json:"at"` // unix 秒
	Phase   string `json:"phase"`
	Detail  string `json:"detail,omitempty"`
	Seq     int    `json:"seq,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// AudioSessionCycle 一段音频（尤其 kind=0）从开段到拼装的诊断快照。
// Fingerprint 用于快速对比「同一段」多次处理是否一致。
type AudioSessionCycle struct {
	ID           string `json:"id"` // sn:device_ts:kind
	SN           string `json:"sn"`
	DeviceTS     int64  `json:"device_ts"`
	AudioKind    int    `json:"audio_kind"`
	KindLabel    string `json:"kind_label"`
	Fingerprint  string `json:"fingerprint"`
	StartedAt    int64  `json:"started_at"`
	EndedAt      int64  `json:"ended_at"`
	UpdatedAt    int64  `json:"updated_at"`

	DeviceTotal    int   `json:"device_total"`
	ConnUniqueOK   int   `json:"conn_unique_ok"`
	ConnRecvHits   int   `json:"conn_recv_ok_hits"`
	StoreChunks    int   `json:"store_chunks"`
	MissingCount   int   `json:"missing_count"`
	MissingSeqs    []int `json:"missing_seqs,omitempty"`

	DropCRC    int `json:"drop_crc"`
	DropDedup  int `json:"drop_dedup"`
	DropAccept int `json:"drop_accept"`
	DropOther  int `json:"drop_other"`
	GapWarn    int `json:"gap_warn"`

	StoreReady      bool `json:"store_ready"`
	StoreIncomplete bool `json:"store_incomplete"`
	HasProblem      bool `json:"has_problem"`
	// PendingRetransmit：end 瞬间缺帧、补发尚未完成的中间快照（勿当最终缺失）。
	PendingRetransmit bool `json:"pending_retransmit"`
	// MissingFinal：拼装/重拼后仍缺（真缺失）。
	MissingFinal bool `json:"missing_final"`

	Verdict  string   `json:"verdict"`
	Problems []string `json:"problems,omitempty"` // 人话问题列表

	PipelineTriggered bool   `json:"pipeline_triggered"`
	FullTranslate     bool   `json:"full_translate"`
	EstDurationMs     int    `json:"est_duration_ms,omitempty"`
	FrameMs           int    `json:"frame_ms,omitempty"`      // Opus TOC 单帧时长（事件流约定 60）
	MedianGapMs       int    `json:"median_gap_ms,omitempty"` // 相邻 chunk_ts 中位间隙
	TocMs             int    `json:"toc_ms,omitempty"`        // TOC 累加内容时长（≈播放时长）
	SpanMs            int    `json:"span_ms,omitempty"`       // 设备 chunk_ts 首尾跨度
	WallMs            int    `json:"wall_ms,omitempty"`       // 网关开段→end 墙钟（≠音频时长）
	AssembleChunks    int    `json:"assemble_chunks,omitempty"`
	OriginURL         string `json:"origin_url,omitempty"`
	Rebuilt           bool   `json:"rebuilt,omitempty"`
	// SkipReason 拼装后未写入 a_emb_translate 的原因（如 drop_short / encode_fail / oss_fail）。
	SkipReason string `json:"skip_reason,omitempty"`
	SkipDetail string `json:"skip_detail,omitempty"`

	Events []AudioCycleEvent `json:"events,omitempty"`
}

// AudioDiagStats 问题统计。
type AudioDiagStats struct {
	TotalSessions   int64 `json:"total_sessions"`
	ProblemSessions int64 `json:"problem_sessions"`
	OKSessions      int64 `json:"ok_sessions"`
	Incomplete      int64 `json:"incomplete"`      // total 未对齐
	HighCRCDrop     int64 `json:"high_crc_drop"`   // CRC 丢弃偏多
	SeqGap          int64 `json:"seq_gap"`         // 首传跳号
	HighDedup       int64 `json:"high_dedup"`
	EventKind       int64 `json:"event_kind"`
	FileKind        int64 `json:"file_kind"`
}

func cycleID(sn string, deviceTS int64, kind int) string {
	return fmt.Sprintf("%s:%d:%d", sn, deviceTS, kind)
}

// BuildAudioFingerprint 生成周期特征值（便于对比丢帧前后是否同一段）。
func BuildAudioFingerprint(sn string, deviceTS int64, kind, total, chunks, dropCRC, gapWarn int) string {
	raw := fmt.Sprintf("v1|%s|%d|%d|t%d|c%d|crc%d|gap%d", sn, deviceTS, kind, total, chunks, dropCRC, gapWarn)
	sum := sha1.Sum([]byte(raw))
	return "fp_" + hex.EncodeToString(sum[:8])
}

// SaveAudioCycle 写入/覆盖一段音频诊断；维护近期索引与统计。
func (c *Client) SaveAudioCycle(ctx context.Context, cycle *AudioSessionCycle) error {
	if c == nil || c.rdb == nil || cycle == nil || cycle.SN == "" {
		return nil
	}
	now := time.Now().Unix()
	if cycle.ID == "" {
		cycle.ID = cycleID(cycle.SN, cycle.DeviceTS, cycle.AudioKind)
	}
	if cycle.KindLabel == "" {
		if cycle.AudioKind == 1 {
			cycle.KindLabel = "file"
		} else {
			cycle.KindLabel = "event"
		}
	}
	if cycle.EndedAt == 0 {
		cycle.EndedAt = now
	}
	cycle.UpdatedAt = now
	if cycle.Fingerprint == "" {
		cycle.Fingerprint = BuildAudioFingerprint(cycle.SN, cycle.DeviceTS, cycle.AudioKind,
			cycle.DeviceTotal, cycle.StoreChunks, cycle.DropCRC, cycle.GapWarn)
	}
	cycle.HasProblem = cycleComputeHasProblem(cycle)

	b, err := json.Marshal(cycle)
	if err != nil {
		return err
	}
	key := c.k(keyAudioCyclePrefix + cycle.ID)
	pipe := c.rdb.Pipeline()
	pipe.Set(ctx, key, b, audioCycleTTL)
	pipe.ZAdd(ctx, c.k(keyAudioCycleZ), redis.Z{Score: float64(cycle.EndedAt), Member: cycle.ID})
	pipe.ZRemRangeByRank(ctx, c.k(keyAudioCycleZ), 0, int64(-audioCycleZMax-1))
	// stats
	statsKey := c.k(keyAudioDiagStats)
	pipe.HIncrBy(ctx, statsKey, "total_sessions", 1)
	if cycle.AudioKind == 1 {
		pipe.HIncrBy(ctx, statsKey, "file_kind", 1)
	} else {
		pipe.HIncrBy(ctx, statsKey, "event_kind", 1)
	}
	if cycle.HasProblem {
		pipe.HIncrBy(ctx, statsKey, "problem_sessions", 1)
	} else {
		pipe.HIncrBy(ctx, statsKey, "ok_sessions", 1)
	}
	// 待补发中间快照不计入 incomplete，避免诊断页假缺失膨胀
	if !cycle.PendingRetransmit && (cycle.MissingFinal || cycle.StoreIncomplete || cycle.MissingCount > 0) {
		pipe.HIncrBy(ctx, statsKey, "incomplete", 1)
	}
	if cycle.DropCRC >= 3 {
		pipe.HIncrBy(ctx, statsKey, "high_crc_drop", 1)
	}
	if cycle.GapWarn > 0 {
		pipe.HIncrBy(ctx, statsKey, "seq_gap", 1)
	}
	if cycle.DropDedup >= 5 {
		pipe.HIncrBy(ctx, statsKey, "high_dedup", 1)
	}
	_, err = pipe.Exec(ctx)
	return err
}

// PatchAudioCycleAssemble 拼装/重拼阶段回写（不重复累加 stats）。
func (c *Client) PatchAudioCycleAssemble(ctx context.Context, sn string, deviceTS int64, kind, chunks, total, estMs int, incomplete bool, originURL string, rebuilt bool) error {
	if c == nil || c.rdb == nil || sn == "" {
		return nil
	}
	id := cycleID(sn, deviceTS, kind)
	key := c.k(keyAudioCyclePrefix + id)
	raw, err := c.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		// 尚无 end 诊断（极少）：建一条最小记录
		cycle := &AudioSessionCycle{
			ID: id, SN: sn, DeviceTS: deviceTS, AudioKind: kind,
			StoreChunks: chunks, DeviceTotal: total, AssembleChunks: chunks,
			EstDurationMs: estMs, StoreIncomplete: incomplete, OriginURL: originURL, Rebuilt: rebuilt,
			EndedAt: time.Now().Unix(),
			Problems: assembleProblems(total, chunks, incomplete),
			Events: []AudioCycleEvent{{
				At: time.Now().Unix(), Phase: "assemble",
				Detail: fmt.Sprintf("chunks=%d total=%d incomplete=%v rebuilt=%v", chunks, total, incomplete, rebuilt),
			}},
		}
		cycle.Fingerprint = BuildAudioFingerprint(sn, deviceTS, kind, total, chunks, 0, 0)
		return c.SaveAudioCycle(ctx, cycle)
	}
	if err != nil {
		return err
	}
	var cycle AudioSessionCycle
	if err := json.Unmarshal(raw, &cycle); err != nil {
		return err
	}
	cycle.AssembleChunks = chunks
	cycle.StoreChunks = chunks
	if total > 0 {
		cycle.DeviceTotal = total
	}
	cycle.EstDurationMs = estMs
	cycle.StoreIncomplete = incomplete
	// 拼装/重拼后：结束「待补发」中间态；仍缺则记最终缺失
	cycle.PendingRetransmit = false
	missN := 0
	if cycle.DeviceTotal > chunks {
		missN = cycle.DeviceTotal - chunks
	}
	cycle.MissingCount = missN
	cycle.MissingFinal = incomplete || missN > 0
	if !cycle.MissingFinal {
		cycle.MissingSeqs = nil
		cycle.Problems = stripPendingProblems(cycle.Problems)
		if cycle.Verdict == "待补发(中间快照)" || strings.HasPrefix(cycle.Verdict, "待补发") {
			cycle.Verdict = "完整"
		}
	} else {
		cycle.Verdict = "不完整(拼装后仍缺，最终缺失)"
	}
	if originURL != "" {
		cycle.OriginURL = originURL
	}
	if rebuilt {
		cycle.Rebuilt = true
	}
	cycle.UpdatedAt = time.Now().Unix()
	detail := fmt.Sprintf("chunks=%d total=%d incomplete=%v missing_final=%v", chunks, cycle.DeviceTotal, incomplete, cycle.MissingFinal)
	if cycle.TocMs > 0 || cycle.SpanMs > 0 || cycle.FrameMs > 0 {
		detail += fmt.Sprintf(" · frame=%dms toc=%dms span=%dms median_gap=%dms wall=%dms",
			cycle.FrameMs, cycle.TocMs, cycle.SpanMs, cycle.MedianGapMs, cycle.WallMs)
	}
	cycle.Events = append(cycle.Events, AudioCycleEvent{
		At: cycle.UpdatedAt, Phase: map[bool]string{true: "rebuild", false: "assemble"}[rebuilt],
		Detail: detail,
	})
	for _, p := range assembleProblems(cycle.DeviceTotal, chunks, incomplete) {
		if !containsStr(cycle.Problems, p) {
			cycle.Problems = append(cycle.Problems, p)
		}
	}
	cycle.HasProblem = cycleComputeHasProblem(&cycle)
	cycle.Fingerprint = BuildAudioFingerprint(cycle.SN, cycle.DeviceTS, cycle.AudioKind,
		cycle.DeviceTotal, cycle.StoreChunks, cycle.DropCRC, cycle.GapWarn)
	b, err := json.Marshal(&cycle)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, key, b, audioCycleTTL).Err()
}

// PatchAudioCycleTiming 回写 Opus 帧长与时间轴对照（TOC vs 设备 ts），并写入时间线。
func (c *Client) PatchAudioCycleTiming(ctx context.Context, sn string, deviceTS int64, kind, frameMs, medianGapMs, tocMs, spanMs int) error {
	if c == nil || c.rdb == nil || sn == "" {
		return nil
	}
	id := cycleID(sn, deviceTS, kind)
	key := c.k(keyAudioCyclePrefix + id)
	raw, err := c.rdb.Get(ctx, key).Bytes()
	var cycle AudioSessionCycle
	wasMissing := err == redis.Nil
	if wasMissing {
		cycle = AudioSessionCycle{
			ID: id, SN: sn, DeviceTS: deviceTS, AudioKind: kind,
			EndedAt: time.Now().Unix(),
		}
		if kind == 1 {
			cycle.KindLabel = "file"
		} else {
			cycle.KindLabel = "event"
		}
	} else if err != nil {
		return err
	} else if err := json.Unmarshal(raw, &cycle); err != nil {
		return err
	}
	if frameMs > 0 {
		cycle.FrameMs = frameMs
	}
	// median_gap 可为 0（单包）；始终回写便于前端展示
	cycle.MedianGapMs = medianGapMs
	if tocMs > 0 {
		cycle.TocMs = tocMs
		cycle.EstDurationMs = tocMs
	}
	if spanMs > 0 {
		cycle.SpanMs = spanMs
	}
	cycle.UpdatedAt = time.Now().Unix()

	hint := "toc≈span（按 TOC 连续拼装正常）"
	if tocMs > 0 && spanMs >= tocMs*5/4 {
		hint = "toc≪span：设备 ts 跨度长于内容，包间空隙未上传→拼起来偏密/偏快"
		prob := fmt.Sprintf("帧长：toc=%dms ≪ span=%dms（median_gap=%dms frame=%dms）", tocMs, spanMs, medianGapMs, frameMs)
		if !containsStr(cycle.Problems, prob) {
			cycle.Problems = append(cycle.Problems, prob)
		}
	} else if tocMs > 0 && cycle.WallMs >= tocMs*5/4 && (spanMs == 0 || spanMs <= tocMs*5/4) {
		hint = "toc≈span 但 wall 更长：开段到 end 含非音频等待，文件时长按 TOC 正常"
	}
	cycle.Events = append(cycle.Events, AudioCycleEvent{
		At: cycle.UpdatedAt, Phase: "timing",
		Detail: fmt.Sprintf("frame=%dms toc=%dms span=%dms median_gap=%dms wall=%dms · %s",
			cycle.FrameMs, cycle.TocMs, cycle.SpanMs, cycle.MedianGapMs, cycle.WallMs, hint),
	})
	cycle.HasProblem = cycleComputeHasProblem(&cycle)
	if cycle.Fingerprint == "" {
		cycle.Fingerprint = BuildAudioFingerprint(sn, deviceTS, kind, cycle.DeviceTotal, cycle.StoreChunks, cycle.DropCRC, cycle.GapWarn)
	}
	b, err := json.Marshal(&cycle)
	if err != nil {
		return err
	}
	if err := c.rdb.Set(ctx, key, b, audioCycleTTL).Err(); err != nil {
		return err
	}
	if wasMissing {
		_ = c.rdb.ZAdd(ctx, c.k(keyAudioCycleZ), redis.Z{Score: float64(time.Now().Unix()), Member: id}).Err()
	}
	return nil
}

// PatchAudioCycleSkip 记录拼装后未入库原因（时长过短 / 转码失败 / OSS 失败等）。
func (c *Client) PatchAudioCycleSkip(ctx context.Context, sn string, deviceTS int64, kind int, reason, detail string) error {
	if c == nil || c.rdb == nil || sn == "" || reason == "" {
		return nil
	}
	id := cycleID(sn, deviceTS, kind)
	key := c.k(keyAudioCyclePrefix + id)
	raw, err := c.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil
	}
	if err != nil {
		return err
	}
	var cycle AudioSessionCycle
	if err := json.Unmarshal(raw, &cycle); err != nil {
		return err
	}
	cycle.SkipReason = reason
	cycle.SkipDetail = detail
	cycle.UpdatedAt = time.Now().Unix()
	prob := fmt.Sprintf("未入库: %s", detail)
	if detail == "" {
		prob = fmt.Sprintf("未入库: %s", reason)
	}
	if !containsStr(cycle.Problems, prob) {
		cycle.Problems = append(cycle.Problems, prob)
	}
	cycle.HasProblem = true
	cycle.Events = append(cycle.Events, AudioCycleEvent{
		At: cycle.UpdatedAt, Phase: "skip_persist", Reason: reason, Detail: detail,
	})
	b, err := json.Marshal(&cycle)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, key, b, audioCycleTTL).Err()
}

// PatchAudioCycleAnomaly 记录异常但仍继续入库（如事件流 <800ms 与设备约定不符）。
func (c *Client) PatchAudioCycleAnomaly(ctx context.Context, sn string, deviceTS int64, kind int, detail string) error {
	if c == nil || c.rdb == nil || sn == "" || detail == "" {
		return nil
	}
	id := cycleID(sn, deviceTS, kind)
	key := c.k(keyAudioCyclePrefix + id)
	raw, err := c.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil
	}
	if err != nil {
		return err
	}
	var cycle AudioSessionCycle
	if err := json.Unmarshal(raw, &cycle); err != nil {
		return err
	}
	cycle.UpdatedAt = time.Now().Unix()
	prob := "异常: " + detail
	if !containsStr(cycle.Problems, prob) {
		cycle.Problems = append(cycle.Problems, prob)
	}
	cycle.HasProblem = true
	cycle.Events = append(cycle.Events, AudioCycleEvent{
		At: cycle.UpdatedAt, Phase: "anomaly", Reason: "device_contract", Detail: detail,
	})
	b, err := json.Marshal(&cycle)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, key, b, audioCycleTTL).Err()
}

func assembleProblems(total, chunks int, incomplete bool) []string {
	var out []string
	if incomplete || (total > 0 && chunks < total) {
		miss := total - chunks
		if miss < 0 {
			miss = 0
		}
		out = append(out, fmt.Sprintf("最终缺失: device_total=%d chunks=%d missing=%d（拼装后仍缺）", total, chunks, miss))
	}
	return out
}

// cycleComputeHasProblem 待补发中间快照不因缺帧记「有问题」；拼装后 MissingFinal / CRC / 跳号等才算。
func cycleComputeHasProblem(c *AudioSessionCycle) bool {
	if c == nil {
		return false
	}
	if c.DropCRC > 0 || c.GapWarn > 0 {
		return true
	}
	if c.PendingRetransmit {
		for _, p := range c.Problems {
			if strings.HasPrefix(p, "待补发") {
				continue
			}
			if p != "" {
				return true
			}
		}
		return false
	}
	if c.MissingFinal || c.StoreIncomplete || c.MissingCount > 0 {
		return true
	}
	return len(c.Problems) > 0
}

func stripPendingProblems(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}
	out := make([]string, 0, len(ss))
	for _, p := range ss {
		if strings.HasPrefix(p, "待补发") {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// PatchAudioCycleRetransmitSettled 补发窗口结束后回写：已齐则清待补发；仍缺则记最终缺失/跳号。
func (c *Client) PatchAudioCycleRetransmitSettled(ctx context.Context, sn string, deviceTS int64, kind, chunks, finalTotal, gapWarn int, missingFinal bool) error {
	if c == nil || c.rdb == nil || sn == "" {
		return nil
	}
	id := cycleID(sn, deviceTS, kind)
	key := c.k(keyAudioCyclePrefix + id)
	raw, err := c.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil
	}
	if err != nil {
		return err
	}
	var cycle AudioSessionCycle
	if err := json.Unmarshal(raw, &cycle); err != nil {
		return err
	}
	cycle.PendingRetransmit = false
	cycle.StoreChunks = chunks
	if finalTotal > 0 {
		cycle.DeviceTotal = finalTotal
	}
	missN := 0
	if cycle.DeviceTotal > chunks {
		missN = cycle.DeviceTotal - chunks
	}
	cycle.MissingCount = missN
	cycle.MissingFinal = missingFinal || missN > 0
	cycle.StoreIncomplete = cycle.MissingFinal
	if gapWarn > 0 {
		cycle.GapWarn = gapWarn
	}
	cycle.UpdatedAt = time.Now().Unix()
	if !cycle.MissingFinal {
		cycle.MissingSeqs = nil
		cycle.Problems = stripPendingProblems(cycle.Problems)
		if strings.HasPrefix(cycle.Verdict, "待补发") {
			cycle.Verdict = "完整"
		}
		cycle.Events = append(cycle.Events, AudioCycleEvent{
			At: cycle.UpdatedAt, Phase: "retransmit_settled",
			Detail: fmt.Sprintf("补发窗口后已齐 chunks=%d final_total=%d", chunks, cycle.DeviceTotal),
		})
	} else {
		cycle.Verdict = "不完整(补发窗口后仍缺)"
		prob := fmt.Sprintf("最终缺失: device_total=%d chunks=%d missing=%d（补发窗口后）", cycle.DeviceTotal, chunks, missN)
		if !containsStr(cycle.Problems, prob) {
			cycle.Problems = append(stripPendingProblems(cycle.Problems), prob)
		}
		if gapWarn > 0 {
			gprob := fmt.Sprintf("补发后仍跳号/缺失: gap_warn=%d", gapWarn)
			if !containsStr(cycle.Problems, gprob) {
				cycle.Problems = append(cycle.Problems, gprob)
			}
		}
		cycle.Events = append(cycle.Events, AudioCycleEvent{
			At: cycle.UpdatedAt, Phase: "retransmit_settled", Reason: "still_missing",
			Detail: fmt.Sprintf("chunks=%d final_total=%d remain=%d gap_warn=%d", chunks, cycle.DeviceTotal, missN, gapWarn),
		})
	}
	cycle.HasProblem = cycleComputeHasProblem(&cycle)
	cycle.Fingerprint = BuildAudioFingerprint(cycle.SN, cycle.DeviceTS, cycle.AudioKind,
		cycle.DeviceTotal, cycle.StoreChunks, cycle.DropCRC, cycle.GapWarn)
	b, err := json.Marshal(&cycle)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, key, b, audioCycleTTL).Err()
}

// PatchAudioCycleFlags 回写是否触发流水线 / 是否完整翻译。
func (c *Client) PatchAudioCycleFlags(ctx context.Context, sn string, deviceTS int64, kind int, pipeline, fullTranslate bool) error {
	if c == nil || c.rdb == nil || sn == "" {
		return nil
	}
	id := cycleID(sn, deviceTS, kind)
	key := c.k(keyAudioCyclePrefix + id)
	raw, err := c.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil
	}
	if err != nil {
		return err
	}
	var cycle AudioSessionCycle
	if err := json.Unmarshal(raw, &cycle); err != nil {
		return err
	}
	cycle.PipelineTriggered = pipeline
	cycle.FullTranslate = fullTranslate
	cycle.UpdatedAt = time.Now().Unix()
	b, err := json.Marshal(&cycle)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, key, b, audioCycleTTL).Err()
}

// GetAudioCycle 按 sn + device_ts + kind 读取一段诊断。
func (c *Client) GetAudioCycle(ctx context.Context, sn string, deviceTS int64, kind int) (*AudioSessionCycle, error) {
	if c == nil || c.rdb == nil {
		return nil, nil
	}
	raw, err := c.rdb.Get(ctx, c.k(keyAudioCyclePrefix+cycleID(sn, deviceTS, kind))).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cycle AudioSessionCycle
	if err := json.Unmarshal(raw, &cycle); err != nil {
		return nil, err
	}
	return &cycle, nil
}

// ListAudioCycles 近期诊断列表（新→旧）；problemsOnly 只返回有问题的。
// sn 非空时按设备过滤，并扩大扫描范围以免被其它设备挤掉。
func (c *Client) ListAudioCycles(ctx context.Context, sn string, limit int, problemsOnly bool) ([]AudioSessionCycle, error) {
	if c == nil || c.rdb == nil {
		return nil, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	scan := int64(limit*3 - 1)
	if sn != "" {
		scan = int64(audioCycleZMax - 1) // 按 SN 查时扫满索引
	}
	ids, err := c.rdb.ZRevRange(ctx, c.k(keyAudioCycleZ), 0, scan).Result()
	if err != nil {
		return nil, err
	}
	out := make([]AudioSessionCycle, 0, limit)
	for _, id := range ids {
		if sn != "" && !strings.HasPrefix(id, sn+":") {
			continue
		}
		raw, err := c.rdb.Get(ctx, c.k(keyAudioCyclePrefix+id)).Bytes()
		if err == redis.Nil {
			continue
		}
		if err != nil {
			return out, err
		}
		var cycle AudioSessionCycle
		if err := json.Unmarshal(raw, &cycle); err != nil {
			continue
		}
		if problemsOnly && !cycle.HasProblem {
			continue
		}
		out = append(out, cycle)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// SummarizeAudioCycles 对列表现场汇总（用于按设备号查询页）。
func SummarizeAudioCycles(list []AudioSessionCycle) AudioDiagStats {
	var st AudioDiagStats
	for _, c := range list {
		st.TotalSessions++
		if c.AudioKind == 1 {
			st.FileKind++
		} else {
			st.EventKind++
		}
		if c.HasProblem {
			st.ProblemSessions++
		} else {
			st.OKSessions++
		}
		if c.StoreIncomplete || c.MissingCount > 0 || (c.DeviceTotal > 0 && c.StoreChunks < c.DeviceTotal) {
			st.Incomplete++
		}
		if c.DropCRC >= 3 {
			st.HighCRCDrop++
		}
		if c.GapWarn > 0 {
			st.SeqGap++
		}
		if c.DropDedup >= 5 {
			st.HighDedup++
		}
	}
	return st
}

// GetAudioDiagStats 读取累计统计。
func (c *Client) GetAudioDiagStats(ctx context.Context) (*AudioDiagStats, error) {
	if c == nil || c.rdb == nil {
		return &AudioDiagStats{}, nil
	}
	m, err := c.rdb.HGetAll(ctx, c.k(keyAudioDiagStats)).Result()
	if err != nil {
		return nil, err
	}
	h := func(k string) int64 {
		v, _ := strconv.ParseInt(m[k], 10, 64)
		return v
	}
	return &AudioDiagStats{
		TotalSessions:   h("total_sessions"),
		ProblemSessions: h("problem_sessions"),
		OKSessions:      h("ok_sessions"),
		Incomplete:      h("incomplete"),
		HighCRCDrop:     h("high_crc_drop"),
		SeqGap:          h("seq_gap"),
		HighDedup:       h("high_dedup"),
		EventKind:       h("event_kind"),
		FileKind:        h("file_kind"),
	}, nil
}

// PurgeAudioCycles 清除 ended_at 早于 beforeUnix（秒）的诊断；days>0 时 before=now-days*86400。
// resetStats=true 时同时清空累计 stats Hash（不影响仍保留的 cycle）。
func (c *Client) PurgeAudioCycles(ctx context.Context, olderThanDays int, resetStats bool) (removed int, err error) {
	if c == nil || c.rdb == nil {
		return 0, nil
	}
	if olderThanDays < 0 {
		olderThanDays = 0
	}
	before := time.Now().Unix()
	if olderThanDays > 0 {
		before = time.Now().Add(-time.Duration(olderThanDays) * 24 * time.Hour).Unix()
	}
	zkey := c.k(keyAudioCycleZ)
	// score 为 ended_at；取出早于 before 的 member
	ids, err := c.rdb.ZRangeByScore(ctx, zkey, &redis.ZRangeBy{
		Min: "-inf",
		Max: strconv.FormatInt(before, 10),
	}).Result()
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 && !resetStats {
		return 0, nil
	}
	pipe := c.rdb.Pipeline()
	for _, id := range ids {
		pipe.Del(ctx, c.k(keyAudioCyclePrefix+id))
		pipe.ZRem(ctx, zkey, id)
		removed++
	}
	if resetStats {
		pipe.Del(ctx, c.k(keyAudioDiagStats))
	}
	_, err = pipe.Exec(ctx)
	return removed, err
}
