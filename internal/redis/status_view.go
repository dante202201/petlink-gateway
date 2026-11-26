package redisx

import (
	"encoding/json"
	"time"

	"pet-translate-gateway/internal/model"
)

// DeviceStatusView 设备状态 API 响应（/api/v1/device/status、/api/app/v1/device/status 同源）。
// 字段不重复：电量用 b、固件用 fw_ver、品种用 breed；配置项始终输出（未上报为 null）。
type DeviceStatusView struct {
	SN               string  `json:"sn"`
	Token            string  `json:"token,omitempty"` // 管理端与 App 状态接口填充当前设备 token
	Online           bool    `json:"online"`
	ConnID           string  `json:"conn_id"`
	ConnectedAt      int64   `json:"connected_at"`
	LastSeenAt       int64   `json:"last_seen_at"`
	LastReportAt     int64   `json:"last_report_at"`
	SecondsSinceSeen int64   `json:"seconds_since_seen"`
	ActivatedAt      int64   `json:"activated_at"`
	MAC              string  `json:"mac"`
	FWVer            string  `json:"fw_ver"`
	ProtoVer         int     `json:"proto_ver"`
	Battery          int     `json:"b"`
	Charge           bool    `json:"charge"`
	RSSI             int     `json:"rssi"`
	Ch               int     `json:"ch"`
	DB               float64 `json:"db"`
	Bound            int     `json:"bound"`
	PID              string  `json:"pid"`
	Breed            string  `json:"breed"`
	LastEvent        string  `json:"last_event"`
	LastState        string  `json:"last_state"`
	StateLabel       string  `json:"state_label"` // 空闲|录音中|睡眠…；由 last_state 推导
	Motion           string          `json:"motion"`       // walk|run|stand|prone|lie；空=未上报
	MotionLabel      string          `json:"motion_label"` // 走|跑|站|趴|躺；与 motion 同步
	Tier             string          `json:"tier"`         // normal|light|deep；空=未上报
	TierLabel        string          `json:"tier_label"`   // 正常|浅睡|深睡
	IMU              json.RawMessage `json:"imu"`          // 最近上报的 IMU 数组；未上报为 null
	InSleep          bool            `json:"in_sleep"` // last_state/event 为 sleep 类，或 tier=light/deep
	LastLocLat       float64 `json:"last_loc_lat"`
	LastLocLng       float64 `json:"last_loc_lng"`
	LastLocAcc       float64 `json:"last_loc_acc"`
	LastLocTS        int64   `json:"last_loc_ts"`
	LocSpeed         float64 `json:"loc_speed"`
	LocType          string  `json:"loc_type"`

	// 配置（设备上报真值或 App 下行影子；未设置时为 null，键始终存在）
	Volume           *int   `json:"volume"`
	Sensitivity      *int   `json:"sensitivity"`
	TranslateEnabled *bool  `json:"translate_enabled"`
	AutoRecord       *bool  `json:"auto_record"` // 自动录音；默认 false（设备默认 0）；ACK/上报后更新
	SleepEnabled     *bool  `json:"sleep_enabled"`
	SleepWindow      string `json:"sleep_window"`
	LocIntervalMs    *int   `json:"loc_interval_ms"`
	ConfigSource     string `json:"config_source"`
	ConfigUpdatedAt  int64  `json:"config_updated_at"`

	// OfflineReason 离线原因：空=普通掉线/未知；calib_ble=enter_calib_ble 后主动离线
	OfflineReason string `json:"offline_reason,omitempty"`
	// ActiveOffline 是否为主动离线（当前仅 calib_ble）；App 可展示「标定中」而非「掉线」
	ActiveOffline bool `json:"active_offline"`

	OfflineCmdPending int64 `json:"offline_cmd_pending"`
}

// ToView 转为接口响应；offlinePending 来自 Redis 离线指令队列长度。
func (ds *DeviceStatus) ToView(offlinePending int64) *DeviceStatusView {
	if ds == nil {
		return &DeviceStatusView{}
	}
	breed := ds.Breed
	nowMs := time.Now().UnixMilli()
	var since int64
	if ds.LastSeenAt > 0 && nowMs > ds.LastSeenAt {
		since = (nowMs - ds.LastSeenAt) / 1000
	}
	inSleep := model.StateInSleep(ds.LastState) || model.StateInSleep(ds.LastEvent) || model.TierInSleep(ds.Tier)
	return &DeviceStatusView{
		SN:                ds.SN,
		Online:            ds.Online,
		ConnID:            ds.ConnID,
		ConnectedAt:       ds.ConnectedAt,
		LastSeenAt:        ds.LastSeenAt,
		LastReportAt:      ds.LastReportAt,
		SecondsSinceSeen:  since,
		ActivatedAt:       ds.ActivatedAt,
		MAC:               ds.MAC,
		FWVer:             ds.FWVer,
		ProtoVer:          ds.ProtoVer,
		Battery:           ds.Battery,
		Charge:            ds.Charge,
		RSSI:              ds.RSSI,
		Ch:                ds.Ch,
		DB:                ds.DB,
		Bound:             model.BoundFromProfile(ds.PID, breed),
		PID:               ds.PID,
		Breed:             breed,
		LastEvent:         ds.LastEvent,
		LastState:         ds.LastState,
		StateLabel:        model.StateLabelCN(ds.LastState),
		Motion:            ds.Motion,
		MotionLabel:       model.MotionLabelCN(ds.Motion),
		Tier:              ds.Tier,
		TierLabel:         model.TierLabelCN(ds.Tier),
		IMU:               ds.IMU,
		InSleep:           inSleep,
		LastLocLat:        ds.LastLocLat,
		LastLocLng:        ds.LastLocLng,
		LastLocAcc:        ds.LastLocAcc,
		LastLocTS:         ds.LastLocTS,
		LocSpeed:          ds.LocSpeed,
		LocType:           ds.LocType,
		Volume:            copyIntPtr(ds.Volume),
		Sensitivity:       copyIntPtr(ds.Sensitivity),
		TranslateEnabled:  boolPtr(effectiveTranslateEnabled(ds)),
		AutoRecord:        boolPtr(effectiveAutoRecord(ds)),
		SleepEnabled:      copyBoolPtr(ds.SleepEnabled),
		SleepWindow:       ds.SleepWindow,
		LocIntervalMs:     copyIntPtr(ds.LocIntervalMs),
		ConfigSource:      ds.ConfigSource,
		ConfigUpdatedAt:   ds.ConfigUpdatedAt,
		OfflineReason:     ds.OfflineReason,
		ActiveOffline:     ds.OfflineReason == OfflineReasonCalibBLE,
		OfflineCmdPending: offlinePending,
	}
}

// preferAutoRecord 自动录音：优先 auto_record；未设时回落 translate_enabled（仅报旧字段的固件）。
func preferAutoRecord(ds *DeviceStatus) *bool {
	if ds == nil {
		return nil
	}
	if ds.AutoRecord != nil {
		return ds.AutoRecord
	}
	return ds.TranslateEnabled
}

// preferTranslateEnabled 翻译/事件开关：优先 translate_enabled；未设时回落 auto_record。
func preferTranslateEnabled(ds *DeviceStatus) *bool {
	if ds == nil {
		return nil
	}
	if ds.TranslateEnabled != nil {
		return ds.TranslateEnabled
	}
	return ds.AutoRecord
}

// effectiveAutoRecord 网关侧自动录音生效值：未设置时默认 false。
func effectiveAutoRecord(ds *DeviceStatus) bool {
	v := preferAutoRecord(ds)
	return v != nil && *v
}

// effectiveTranslateEnabled 接口 translate_enabled：未设置时默认 false；与 auto_record 可不一致。
func effectiveTranslateEnabled(ds *DeviceStatus) bool {
	v := preferTranslateEnabled(ds)
	return v != nil && *v
}

// AutoRecordOff 自动录音关闭（含默认未设置）时返回 true。
func (ds *DeviceStatus) AutoRecordOff() bool {
	return !effectiveAutoRecord(ds)
}

// isSleepState 兼容旧调用；实现已迁到 model.StateInSleep。
func isSleepState(s string) bool {
	return model.StateInSleep(s)
}

func copyIntPtr(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func copyBoolPtr(p *bool) *bool {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func boolPtr(v bool) *bool {
	return &v
}
