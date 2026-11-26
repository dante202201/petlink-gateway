package gateway

import (
	"fmt"
	"time"

	"pet-translate-gateway/internal/model"
)

// audioFragmentDedupKey 音频分片去重指纹（须在绝对时间折算之后调用）。
//
// 口径（固件）：
//   - total 是累计值（每发一包 +1），绝不能当分桶/去重键，否则一段会被拆成 total=1,2,3…
//   - 键 = sn + kind + seq + absTS；有 crc 再附上，实时与补发同一键只留一份
//   - kind 保留：事件流 / 录音文件 seq 独立
func audioFragmentDedupKey(sn string, kind, seq int, absTS int64, crc uint32) string {
	if sn == "" {
		sn = "?"
	}
	if crc != 0 {
		return fmt.Sprintf("%s:k%d:%d:%d:%d", sn, kind, seq, absTS, crc)
	}
	return fmt.Sprintf("%s:k%d:%d:%d", sn, kind, seq, absTS)
}

// canonicalDedupAbsTS 去重用绝对微秒：同段统一到「段首 + seq×帧长」。
// 实时包 seq>0 是相对偏移，补发是绝对 Unix；对齐到设备时间轴后才能撞上同一键。
// 旧段补发不落在 live 窗内时，直接用本帧绝对 ts。
func canonicalDedupAbsTS(meta *model.DeviceUplink, liveBase, absTS int64, kind int) int64 {
	if meta == nil {
		return absTS
	}
	frameUS := model.FrameDurationUS(kind)
	if meta.IsRetransmit() {
		if absInLiveSession(liveBase, absTS) && liveBase > 0 {
			return liveBase + int64(meta.Seq)*frameUS
		}
		return absTS
	}
	if liveBase > 0 {
		return liveBase + int64(meta.Seq)*frameUS
	}
	if absTS > 0 {
		return absTS
	}
	return 0
}

// sessionBaseForDedup 入库并段 / 补发并回原段用的段锚（不是去重键）。
// 补发不得用当前 live 基点覆盖旧段。
func sessionBaseForDedup(baseTS int64, meta *model.DeviceUplink, absTS int64) int64 {
	if meta != nil && meta.IsRetransmit() {
		if absInLiveSession(baseTS, absTS) {
			return baseTS
		}
		if meta.Seq == 0 && absTS > 0 {
			return absTS
		}
		return 0
	}
	if baseTS > 0 {
		return baseTS
	}
	if meta != nil && meta.Seq == 0 && absTS > 0 {
		return absTS
	}
	return 0
}

// absInLiveSession 本帧绝对 ts 是否落在当前 live 段内（2 分钟窗，与入库并段一致）。
func absInLiveSession(liveBase, absTS int64) bool {
	if liveBase <= 0 || absTS <= 0 {
		return false
	}
	return absTS >= liveBase && absTS-liveBase < liveSessionSpanUS
}

const liveSessionSpanUS = 120_000_000

// retransmitGapWait 首传 seq 跳号后等待补发的窗口；未补齐再记 gap_warn。
const retransmitGapWait = 4 * time.Second
