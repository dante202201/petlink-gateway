package gateway

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// bindCheckTTL 绑定结果缓存时长；过期后下次音频帧再查 a_emb_sn。
const bindCheckTTL = 30 * time.Second

// allowAudioByBinding 设备需在 a_emb_sn 有有效 bind_user_id 与 bind_pet_id 才接收录音。
// store 未启用时放行（无法查绑定则不拦截）。查库失败时：有旧缓存沿用，否则放行以免整网拒收。
func (m *ConnManager) allowAudioByBinding(dc *DeviceConn) bool {
	if m == nil || m.store == nil || dc == nil {
		return true
	}

	dc.mu.Lock()
	if !dc.bindCheckedAt.IsZero() && time.Since(dc.bindCheckedAt) < bindCheckTTL {
		ok := dc.bindOK
		dc.mu.Unlock()
		return ok
	}
	hadCache := !dc.bindCheckedAt.IsZero()
	prevOK := dc.bindOK
	dc.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	emb, err := m.store.LookupEmbBySN(ctx, dc.SN)
	cancel()
	if err != nil {
		m.log.Warn("查 a_emb_sn 绑定失败，沿用缓存或临时放行",
			zap.String("sn", dc.SN),
			zap.Bool("had_cache", hadCache),
			zap.Bool("prev_ok", prevOK),
			zap.Error(err),
		)
		if hadCache {
			return prevOK
		}
		return true
	}

	ok := emb != nil && emb.HasActiveBinding()
	dc.mu.Lock()
	dc.bindOK = ok
	dc.bindCheckedAt = time.Now()
	dc.mu.Unlock()
	return ok
}
