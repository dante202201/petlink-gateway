package redisx

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"

	"pet-translate-gateway/internal/model"
)

// ApplyDeviceConfig 合并设备配置到状态快照。
// fromDevice=true 表示上行真值；false 为下行影子（仅填补尚未有值的字段，或总是覆盖——设备真值优先用 fromDevice 全量覆盖对应字段）。
func (c *Client) ApplyDeviceConfig(ctx context.Context, sn string, upd *model.DeviceConfigUpdate, fromDevice bool) error {
	if c == nil || sn == "" || upd == nil {
		return nil
	}
	patch := &DeviceStatusPatch{}
	if upd.Volume != nil {
		patch.SetVolume = true
		patch.Volume = *upd.Volume
	}
	if upd.Sensitivity != nil {
		patch.SetSensitivity = true
		patch.Sensitivity = *upd.Sensitivity
	}
	if upd.TranslateEnabled != nil {
		patch.SetTranslateEnabled = true
		patch.TranslateEnabled = *upd.TranslateEnabled
		// 与 auto_record 保持一致
		if upd.AutoRecord == nil {
			patch.SetAutoRecord = true
			patch.AutoRecord = *upd.TranslateEnabled
		}
	}
	if upd.AutoRecord != nil {
		patch.SetAutoRecord = true
		patch.AutoRecord = *upd.AutoRecord
		// 与 translate_enabled 保持一致（旧 App 字段）
		if upd.TranslateEnabled == nil {
			patch.SetTranslateEnabled = true
			patch.TranslateEnabled = *upd.AutoRecord
		}
	}
	if upd.SleepEnabled != nil {
		patch.SetSleepEnabled = true
		patch.SleepEnabled = *upd.SleepEnabled
	}
	if upd.SleepWindow != nil {
		patch.SetSleepWindow = true
		patch.SleepWindow = *upd.SleepWindow
	}
	if upd.LocIntervalMs != nil {
		patch.SetLocIntervalMs = true
		patch.LocIntervalMs = *upd.LocIntervalMs
	}
	if fromDevice {
		patch.SetConfigSource = true
		patch.ConfigSource = "device"
	} else {
		patch.SetConfigSource = true
		patch.ConfigSource = "shadow"
	}
	patch.SetConfigUpdatedAt = true
	patch.ConfigUpdatedAt = time.Now().UnixMilli()
	return c.UpdateDeviceStatus(ctx, sn, patch)
}

const pendingAutoRecordTTL = 10 * time.Minute

// SetPendingAutoRecord 记录待 ACK 确认的 auto_record 期望值（下发 enable_auto_record 时调用，成功 ACK 前不写状态）。
func (c *Client) SetPendingAutoRecord(ctx context.Context, sn string, enabled bool) error {
	if c == nil || sn == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	v := "0"
	if enabled {
		v = "1"
	}
	return c.rdb.Set(ctx, c.k(keyPendingAutoRec+sn), v, pendingAutoRecordTTL).Err()
}

// TakePendingAutoRecord 取出并清除待确认的 auto_record；无记录时 ok=false。
func (c *Client) TakePendingAutoRecord(ctx context.Context, sn string) (enabled bool, ok bool, err error) {
	if c == nil || sn == "" {
		return false, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	key := c.k(keyPendingAutoRec + sn)
	val, err := c.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	_ = c.rdb.Del(ctx, key).Err()
	return val == "1" || val == "true", true, nil
}
