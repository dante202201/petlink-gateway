package audio

import (
	"encoding/binary"
	"fmt"
)

// OpusPacketFrameSizeMs 从裸 Opus 包 TOC 解析「单帧」时长（毫秒）。RFC 6716 §3.1 Table 2。
// 不含一包多帧；包总时长请用 OpusPacketDurationMs。
func OpusPacketFrameSizeMs(pkt []byte) int {
	if len(pkt) == 0 {
		return 20
	}
	config := int(pkt[0] >> 3)
	switch {
	case config <= 11: // SILK-only：10/20/40/60
		return []int{10, 20, 40, 60}[config&3]
	case config <= 15: // Hybrid：10/20
		return []int{10, 20}[config&1]
	default: // CELT-only：2.5/5/10/20 → 用整毫秒（2.5 按 3）
		return []int{3, 5, 10, 20}[config&3]
	}
}

// OpusPacketFrameDurationMs 兼容旧名：返回单帧时长（毫秒）。
func OpusPacketFrameDurationMs(pkt []byte) int {
	return OpusPacketFrameSizeMs(pkt)
}

// OpusPacketFrameCount 解析 TOC 中一包含多少帧（code 0/1/2/3）。
func OpusPacketFrameCount(pkt []byte) int {
	if len(pkt) == 0 {
		return 1
	}
	switch pkt[0] & 3 {
	case 0:
		return 1
	case 1, 2:
		return 2
	case 3:
		if len(pkt) < 2 {
			return 1
		}
		m := int(pkt[1] & 0x3f)
		if m < 1 {
			return 1
		}
		if m > 48 {
			return 48
		}
		return m
	}
	return 1
}

// OpusPacketDurationMs 一包音频总时长（毫秒）= 单帧时长 × 帧数。
// Ogg granule 必须按此累加；若只按单帧累加而 TOC 为多帧包，时长标短 → 听感语速变快。
func OpusPacketDurationMs(pkt []byte) int {
	return OpusPacketFrameSizeMs(pkt) * OpusPacketFrameCount(pkt)
}

// OpusTOCInfo 首包 TOC 诊断，便于对照「文件 1.6s / 体感 2s」。
type OpusTOCInfo struct {
	Config   int
	Code     int
	FrameMs  int
	Frames   int
	PacketMs int
}

// DescribeOpusTOC 解析首个非空包的 TOC。
func DescribeOpusTOC(pkt []byte) OpusTOCInfo {
	if len(pkt) == 0 {
		return OpusTOCInfo{}
	}
	return OpusTOCInfo{
		Config:   int(pkt[0] >> 3),
		Code:     int(pkt[0] & 3),
		FrameMs:  OpusPacketFrameSizeMs(pkt),
		Frames:   OpusPacketFrameCount(pkt),
		PacketMs: OpusPacketDurationMs(pkt),
	}
}

// SumOpusPacketsDurationMs 累加包列表总时长；零长度补洞按邻包/默认 60ms 计。
func SumOpusPacketsDurationMs(packets [][]byte) int {
	gapMs := 60
	for _, p := range packets {
		if len(p) > 0 {
			if d := OpusPacketDurationMs(p); d > 0 {
				gapMs = d
				break
			}
		}
	}
	total := 0
	for _, p := range packets {
		if len(p) == 0 {
			total += gapMs
			continue
		}
		total += OpusPacketDurationMs(p)
	}
	return total
}

// MakeOpusPLCFrame 生成与 template 同配置的空 Opus 帧（仅 TOC），供包间 PLC 对齐。
func MakeOpusPLCFrame(template []byte) []byte {
	if len(template) == 0 {
		// 无模板时默认事件流 60ms SILK WB code0：config=3 → (3<<3)=0x18
		return []byte{0x18}
	}
	return []byte{template[0] &^ 0x03}
}

// MakeOpusPLCFrameForKind 按通路缺省帧长生成空包 TOC（无邻包模板时用）。
// kind=1 → 20ms (0x48)；其余 → 60ms (0x18)。
func MakeOpusPLCFrameForKind(kind int, template []byte) []byte {
	if len(template) > 0 {
		return MakeOpusPLCFrame(template)
	}
	if kind == 1 {
		return []byte{0x48} // SILK WB 20ms mono code0
	}
	return []byte{0x18} // SILK WB 60ms mono code0
}

// FillOpusSeqGaps 按 seq 在 [min,max] 内补洞；seqs 与 frames 等长。
// 无 ts 时缺省按事件流 60ms 插值（兼容旧调用）。
func FillOpusSeqGaps(seqs []int, frames [][]byte) (out [][]byte, filled int) {
	out, _, filled = FillOpusSeqGapsWithTSPace(seqs, frames, nil, EventPaceFrameUs)
	return out, filled
}

// FillOpusSeqGapsWithTS 同 FillOpusSeqGaps；pace 从邻包 TOC / 中位间隙推断，否则 60ms。
func FillOpusSeqGapsWithTS(seqs []int, frames [][]byte, absTSUs []int64) (out [][]byte, outTS []int64, filled int) {
	return FillOpusSeqGapsWithTSPace(seqs, frames, absTSUs, InferPaceFrameUs(frames, absTSUs))
}

// FillOpusSeqGapsWithTSKind 按 audio_kind 选缺省包间隔：0→60ms，1→20ms。
func FillOpusSeqGapsWithTSKind(seqs []int, frames [][]byte, absTSUs []int64, audioKind int) (out [][]byte, outTS []int64, filled int) {
	return FillOpusSeqGapsWithTSPace(seqs, frames, absTSUs, PaceFrameUsForKind(audioKind))
}

// FillOpusSeqGapsWithTSPace 同 FillOpusSeqGaps，并按邻帧绝对时间戳线性插值补洞点的 ts（微秒）。
// paceUs：无双侧邻 ts 可线性插值时，用此前进步长；<=0 时按 InferPaceFrameUs。
func FillOpusSeqGapsWithTSPace(seqs []int, frames [][]byte, absTSUs []int64, paceUs int64) (out [][]byte, outTS []int64, filled int) {
	if len(seqs) == 0 || len(seqs) != len(frames) {
		return frames, absTSUs, 0
	}
	if paceUs <= 0 {
		paceUs = InferPaceFrameUs(frames, absTSUs)
	}
	hasTS := len(absTSUs) == len(frames)
	minSeq, maxSeq := seqs[0], seqs[0]
	bySeq := make(map[int][]byte, len(seqs))
	byTS := make(map[int]int64, len(seqs))
	for i, seq := range seqs {
		bySeq[seq] = frames[i]
		if hasTS {
			byTS[seq] = absTSUs[i]
		}
		if seq < minSeq {
			minSeq = seq
		}
		if seq > maxSeq {
			maxSeq = seq
		}
	}
	out = make([][]byte, 0, maxSeq-minSeq+1)
	outTS = make([]int64, 0, maxSeq-minSeq+1)
	for seq := minSeq; seq <= maxSeq; seq++ {
		if f, ok := bySeq[seq]; ok && len(f) > 0 {
			out = append(out, f)
			outTS = append(outTS, byTS[seq])
			continue
		}
		// 只补序号洞：零长度包（无 TOC）；Ogg 侧按邻包/默认帧长累加 granule，不按收包间隙插静音
		out = append(out, nil)
		outTS = append(outTS, interpolateSeqTS(byTS, seq, minSeq, maxSeq, paceUs))
		filled++
	}
	return out, outTS, filled
}

func interpolateSeqTS(byTS map[int]int64, seq, minSeq, maxSeq int, paceUs int64) int64 {
	if paceUs <= 0 {
		paceUs = EventPaceFrameUs
	}
	prevSeq, prevTS := -1, int64(0)
	for s := seq - 1; s >= minSeq; s-- {
		if ts, ok := byTS[s]; ok && ts > 0 {
			prevSeq, prevTS = s, ts
			break
		}
	}
	nextSeq, nextTS := -1, int64(0)
	for s := seq + 1; s <= maxSeq; s++ {
		if ts, ok := byTS[s]; ok && ts > 0 {
			nextSeq, nextTS = s, ts
			break
		}
	}
	if prevSeq >= 0 && nextSeq > prevSeq && nextTS > prevTS {
		span := nextSeq - prevSeq
		return prevTS + (nextTS-prevTS)*int64(seq-prevSeq)/int64(span)
	}
	if prevSeq >= 0 {
		return prevTS + int64(seq-prevSeq)*paceUs
	}
	if nextSeq >= 0 {
		return nextTS - int64(nextSeq-seq)*paceUs
	}
	return 0
}

// EventPaceFrameUs / FilePaceFrameUs 缺 seq 插值用的包间隔（微秒）。
const (
	EventPaceFrameUs int64 = 60_000 // kind=0
	FilePaceFrameUs  int64 = 20_000 // kind=1
)

// DefaultPaceFrameUs 兼容旧名：等同事件流 60ms。
const DefaultPaceFrameUs = EventPaceFrameUs

// PaceFrameUsForKind 按通路返回缺省包间隔。kind=1→20ms，其余→60ms。
func PaceFrameUsForKind(kind int) int64 {
	if kind == 1 {
		return FilePaceFrameUs
	}
	return EventPaceFrameUs
}

// InferPaceFrameUs 从邻包 TOC 或中位间隙推断包间隔；都没有则 60ms。
func InferPaceFrameUs(frames [][]byte, absTSUs []int64) int64 {
	if med := MedianPacketGapMs(absTSUs); med >= 45 {
		return EventPaceFrameUs
	} else if med > 0 && med <= 30 {
		return FilePaceFrameUs
	}
	for _, f := range frames {
		if len(f) == 0 {
			continue
		}
		if d := OpusPacketDurationMs(f); d > 0 {
			return int64(d) * 1000
		}
	}
	return EventPaceFrameUs
}

// MedianPacketGapMs 相邻绝对时间戳中位间隙（毫秒）。
func MedianPacketGapMs(absTSUs []int64) int {
	var gaps []int
	for i := 0; i+1 < len(absTSUs); i++ {
		if absTSUs[i] <= 0 || absTSUs[i+1] <= absTSUs[i] {
			continue
		}
		gaps = append(gaps, int((absTSUs[i+1]-absTSUs[i])/1000))
	}
	return medianInt(gaps)
}

// SessionTSSpanMs 首尾时间戳跨度 + 末包内容时长（毫秒）；用于对照 TOC 总时长。
func SessionTSSpanMs(packets [][]byte, absTSUs []int64) int {
	if len(packets) == 0 || len(absTSUs) != len(packets) {
		return 0
	}
	firstTS, lastTS := int64(0), int64(0)
	lastIdx := -1
	for i, ts := range absTSUs {
		if ts <= 0 {
			continue
		}
		if firstTS == 0 || ts < firstTS {
			firstTS = ts
		}
		if ts >= lastTS {
			lastTS = ts
			lastIdx = i
		}
	}
	if firstTS <= 0 || lastTS < firstTS || lastIdx < 0 {
		return 0
	}
	lastContent := OpusPacketDurationMs(packets[lastIdx])
	if lastContent <= 0 {
		lastContent = 20
	}
	return int((lastTS-firstTS)/1000) + lastContent
}

// PaceAlignOpusPackets 按设备绝对时间戳（微秒）在包间插入 PLC 静音，使播放时长贴近采集时间轴。
// 警告：会把连续音频切成碎片，听感「突突」跳；默认不要对事件流开启，仅排障实验用。
//
// 策略：
//  1. 用首尾 ts 跨度（+末包内容）作为目标时长上限，避免 received_at 抖动导致逐间隙过补；
//  2. 仅当目标明显长于 TOC 内容（≥1.25×）才补；
//  3. 中位间隙 ≥2×帧长时按间隙补；否则把差额均匀摊到各间隙（抗收包抖动）。
func PaceAlignOpusPackets(packets [][]byte, absTSUs []int64) (out [][]byte, padCount int, medianGapMs int) {
	if len(packets) == 0 {
		return packets, 0, 0
	}
	if len(absTSUs) != len(packets) {
		return packets, 0, 0
	}

	contentMs := SumOpusPacketsDurationMs(packets)
	if contentMs <= 0 {
		return packets, 0, 0
	}
	medianGapMs = MedianPacketGapMs(absTSUs)
	spanMs := SessionTSSpanMs(packets, absTSUs)
	if spanMs <= 0 {
		return packets, 0, medianGapMs
	}

	frameMs := OpusPacketFrameSizeMs(packets[0])
	if frameMs <= 0 {
		frameMs = 20
	}

	targetMs := spanMs
	if targetMs < contentMs*5/4 {
		return packets, 0, medianGapMs
	}
	needPadMs := targetMs - contentMs
	if needPadMs < frameMs {
		return packets, 0, medianGapMs
	}

	template := packets[0]
	if medianGapMs >= frameMs*2 {
		out, padCount = paceAlignByGaps(packets, absTSUs, template, frameMs, needPadMs)
		return out, padCount, medianGapMs
	}
	out, padCount = paceAlignUniform(packets, template, frameMs, needPadMs)
	return out, padCount, medianGapMs
}

func paceAlignByGaps(packets [][]byte, absTSUs []int64, template []byte, frameMs, maxPadMs int) ([][]byte, int) {
	out := make([][]byte, 0, len(packets)*2)
	padCount := 0
	remainBudget := maxPadMs
	for i, pkt := range packets {
		if len(pkt) == 0 {
			continue
		}
		out = append(out, pkt)
		if i+1 >= len(packets) || remainBudget < frameMs {
			continue
		}
		contentMs := OpusPacketDurationMs(pkt)
		if contentMs <= 0 {
			contentMs = frameMs
		}
		if absTSUs[i] <= 0 || absTSUs[i+1] <= absTSUs[i] {
			continue
		}
		gapMs := int((absTSUs[i+1] - absTSUs[i]) / 1000)
		if gapMs < contentMs*2 {
			continue
		}
		padMs := gapMs - contentMs
		if padMs > remainBudget {
			padMs = remainBudget
		}
		if padMs > 3000 {
			padMs = 3000
		}
		for padMs >= frameMs {
			out = append(out, MakeOpusPLCFrame(template))
			padCount++
			padMs -= frameMs
			remainBudget -= frameMs
		}
	}
	return out, padCount
}

func paceAlignUniform(packets [][]byte, template []byte, frameMs, needPadMs int) ([][]byte, int) {
	nGap := 0
	for _, pkt := range packets {
		if len(pkt) > 0 {
			nGap++
		}
	}
	if nGap >= 2 {
		nGap-- // 间隙数 = 有效包数 - 1
	}
	if nGap <= 0 || needPadMs < frameMs {
		return packets, 0
	}
	totalPadFrames := needPadMs / frameMs
	out := make([][]byte, 0, len(packets)+totalPadFrames)
	padCount := 0
	placed := 0
	gapIdx := 0
	for i, pkt := range packets {
		if len(pkt) == 0 {
			continue
		}
		out = append(out, pkt)
		if i+1 >= len(packets) || placed >= totalPadFrames {
			continue
		}
		// 把剩余 pad 帧尽量均匀分到剩余间隙
		remainGaps := nGap - gapIdx
		if remainGaps <= 0 {
			continue
		}
		remainPads := totalPadFrames - placed
		n := remainPads / remainGaps
		if remainPads%remainGaps > 0 {
			n++
		}
		for j := 0; j < n && placed < totalPadFrames; j++ {
			out = append(out, MakeOpusPLCFrame(template))
			padCount++
			placed++
		}
		gapIdx++
	}
	return out, padCount
}

func medianInt(v []int) int {
	if len(v) == 0 {
		return 0
	}
	c := append([]int(nil), v...)
	for i := 1; i < len(c); i++ {
		for j := i; j > 0 && c[j] < c[j-1]; j-- {
			c[j], c[j-1] = c[j-1], c[j]
		}
	}
	return c[len(c)/2]
}

// MuxOpusPacketsToOgg 将设备 WS 裸 Opus 包列表封装为 Ogg Opus（RFC 7845），供 ffmpeg / App 解码。
// granule 从 OpusHead pre-skip(312) 起算；每包按自身 TOC 累加；零长度补洞包按邻包时长（缺省 60ms）累加。
func MuxOpusPacketsToOgg(packets [][]byte, sampleRate int) ([]byte, error) {
	if len(packets) == 0 {
		return nil, fmt.Errorf("opus 包列表为空")
	}
	if sampleRate <= 0 {
		sampleRate = 16000
	}

	const (
		serial  uint32 = 0x4C4B4757 // "PLGW"
		preskip        = 312
	)

	gapMs := 60 // 零长度补洞默认按事件流 60ms；若有邻包 TOC 则跟邻包
	for _, p := range packets {
		if len(p) > 0 {
			if d := OpusPacketDurationMs(p); d > 0 {
				gapMs = d
				break
			}
		}
	}

	w := &oggWriter{serial: serial}
	head := buildOpusHead(sampleRate)
	tags := buildOpusTags("PetLinkGateway")
	if err := w.writePageWithType(0x02, 0, [][]byte{head}); err != nil {
		return nil, err
	}
	if err := w.writePageWithType(0x00, 0, [][]byte{tags}); err != nil {
		return nil, err
	}

	granule := uint64(preskip)
	lastIdx := len(packets) - 1
	for i, pkt := range packets {
		durMs := gapMs
		if len(pkt) > 0 {
			if d := OpusPacketDurationMs(pkt); d > 0 {
				durMs = d
			}
		}
		granule += uint64(durMs) * 48 // 48kHz 时间基
		headerType := byte(0)
		if i == lastIdx {
			headerType = 0x04 // EOS；末页 granule = 总时长（含 preskip 起点上的累加）
		}
		pagePkts := [][]byte{pkt}
		if pkt == nil {
			pagePkts = [][]byte{[]byte{}} // 零长度 Opus 包
		}
		if err := w.writePageWithType(headerType, granule, pagePkts); err != nil {
			return nil, err
		}
	}
	if len(w.buf) == 0 {
		return nil, fmt.Errorf("ogg 封装结果为空")
	}
	return w.buf, nil
}

func buildOpusHead(sampleRate int) []byte {
	b := make([]byte, 19)
	copy(b, "OpusHead")
	b[8] = 1  // version
	b[9] = 1  // channels
	binary.LittleEndian.PutUint16(b[10:12], 312) // pre-skip
	binary.LittleEndian.PutUint32(b[12:16], uint32(sampleRate))
	// output gain 0, mapping family 0
	return b
}

func buildOpusTags(vendor string) []byte {
	vb := []byte(vendor)
	b := make([]byte, 8+4+len(vb)+4)
	copy(b, "OpusTags")
	binary.LittleEndian.PutUint32(b[8:12], uint32(len(vb)))
	copy(b[12:], vb)
	// user comment list length 0 at end
	return b
}

type oggWriter struct {
	serial   uint32
	seq      uint32
	buf      []byte
}

func (w *oggWriter) writePage(headerType byte, granule uint64, packets [][]byte) error {
	return w.writePageWithType(headerType, granule, packets)
}

func (w *oggWriter) writePageWithType(headerType byte, granule uint64, packets [][]byte) error {
	segments, body := segmentTable(packets)
	hdr := make([]byte, 27+len(segments))
	copy(hdr, "OggS")
	hdr[4] = 0
	hdr[5] = headerType
	binary.LittleEndian.PutUint64(hdr[6:14], granule)
	binary.LittleEndian.PutUint32(hdr[14:18], w.serial)
	binary.LittleEndian.PutUint32(hdr[18:22], w.seq)
	hdr[26] = byte(len(segments))
	copy(hdr[27:], segments)
	page := append(hdr, body...)
	crc := oggCRC(page)
	binary.LittleEndian.PutUint32(page[22:26], crc)
	w.buf = append(w.buf, page...)
	w.seq++
	return nil
}

func segmentTable(packets [][]byte) ([]byte, []byte) {
	var segs []byte
	var body []byte
	for _, p := range packets {
		if len(p) == 0 {
			// Ogg 允许 0 长度 segment 表示空包；否则 page 无 segment，demux 易截断/错乱
			segs = append(segs, 0)
			continue
		}
		remain := len(p)
		off := 0
		for remain > 0 {
			n := remain
			if n > 255 {
				n = 255
			}
			segs = append(segs, byte(n))
			body = append(body, p[off:off+n]...)
			off += n
			remain -= n
			if n < 255 {
				break
			}
			// 包长恰为 255 的倍数时，必须再跟一个 0 段结束该 packet，否则会被当成跨 page 延续
			if remain == 0 {
				segs = append(segs, 0)
				break
			}
		}
	}
	return segs, body
}

func oggCRC(data []byte) uint32 {
	var crc uint32
	for i, b := range data {
		if i >= 22 && i < 26 {
			b = 0
		}
		crc = (crc << 8) ^ crcTable[byte(crc>>24)^b]
	}
	return crc
}

var crcTable [256]uint32

func init() {
	const poly uint32 = 0x04c11db7
	for i := range crcTable {
		r := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if r&0x80000000 != 0 {
				r = (r << 1) ^ poly
			} else {
				r <<= 1
			}
		}
		crcTable[i] = r
	}
}
