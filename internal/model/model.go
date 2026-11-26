// Package model 定义与固件对齐的上报、下行指令及内部转发结构体。
package model

import (
	"encoding/json"
	"strings"

	"pet-translate-gateway/internal/util"
)

// IMUSample 动作传感器单点采样；ts 与音频同一时间基（Unix 微秒），便于波形对齐。
// 单位定稿：Acc = g；Gyro = dps（°/s）。
type IMUSample struct {
	TS   int64     `json:"ts"`   // 绝对 Unix 微秒
	Acc  []float64 `json:"acc"`  // g
	Gyro []float64 `json:"gyro"` // dps
}

// DeviceUplink 固件 WebSocket 上行 JSON（snake_case）。
// type: audio | batch | retransmit | locate | geolocate | notify | ack | ping | status | tts_flow | ota_progress
type DeviceUplink struct {
	Type     string `json:"type,omitempty"`
	SN       string `json:"sn,omitempty"`
	Token    string `json:"token,omitempty"`
	ProtoVer int    `json:"proto_ver,omitempty"`

	// 音频 / 补发
	Codec      AudioCodec  `json:"codec,omitempty"` // 0/opus；1/mp3
	Seq        int         `json:"seq"`
	Total      int         `json:"total"`
	End        util.Bool01 `json:"end"`
	Retransmit util.Bool01 `json:"retransmit"` // 也可 type=retransmit；精简补发常只带此字段
	// Kind 音频通路：0=事件流（边采边传）1=录音文件（本地录完再传）；缺省按 0
	Kind   *int        `json:"kind,omitempty"`
	PID    PetID       `json:"pid"`           // 字符串档案 ID；未绑定 ""
	Pet    string      `json:"pet,omitempty"` // 新协议：dog/cat/other 语义品种字符串，设备端优先带
	DB     float64     `json:"db"`
	Bat    int         `json:"bat"`
	Charge util.BoolTF `json:"charge"` // true=充电 / false=未充电
	RSSI   int         `json:"rssi"`
	Ch     int         `json:"ch,omitempty"`
	FW     string      `json:"fw,omitempty"`
	// Boot OTA 重启后第一条 ping 为 1；常规心跳不带或为 0。未知字段可忽略。
	Boot      util.Bool01 `json:"boot,omitempty"`
	Bound     util.Bool01 `json:"bound"`
	TS        int64       `json:"ts"` // 方案A：绝对 Unix 微秒；seq>0 可为相对偏移微秒
	CRC       uint32      `json:"crc,omitempty"`
	Count     int         `json:"count,omitempty"` // batch
	BreedCode string      `json:"breed,omitempty"` // 旧协议占位数字串 0/1（未升级固件会带）；新固件用 Pet 字段
	IMU       []IMUSample `json:"imu,omitempty"`
	// Motion 运动姿态：walk/run/stand/prone/lie（走/跑/站/趴/躺）；可挂在 ping/status/audio/notify
	Motion string `json:"motion,omitempty"`
	// Tier 功耗档位：normal/light/deep（正常/浅睡/深睡）；建议挂在 ping/status，档位变化立刻报
	Tier string `json:"tier,omitempty"`

	// 定位（type=locate|geolocate|loc，或音频按需附带）
	LocType          LocTypeCode  `json:"loc_type,omitempty"` // 定稿：1=wifi；兼容旧 "wifi"
	LocLat           float64      `json:"loc_lat,omitempty"`
	LocLng           float64      `json:"loc_lng,omitempty"`
	LocAcc           float64      `json:"loc_acc,omitempty"`
	LocSpeed         float64      `json:"loc_speed,omitempty"`
	LocMacs          LocMacList   `json:"loc_macs,omitempty"`
	WifiScan         WifiScanList `json:"wifi_scan,omitempty"`
	WifiAccessPoints WifiScanList `json:"wifiAccessPoints,omitempty"`
	LocTS            int64        `json:"loc_ts,omitempty"`

	// 设备配置（type=status/ping 建议周期或变更时上报；真值优先于网关影子）
	Volume           *int   `json:"volume,omitempty"`            // 0~100
	Sensitivity      *int   `json:"sensitivity,omitempty"`       // 0~100
	TranslateEnabled *int   `json:"translate_enabled,omitempty"` // 0/1；可与 auto_record 独立（关文件录音仍可开翻译）
	AutoRecord       *int   `json:"auto_record,omitempty"`       // 0/1 自动录音文件开关
	SleepEnabled     *int   `json:"sleep_enabled,omitempty"`     // 0/1
	SleepWindow      string `json:"sleep_window,omitempty"`      // 如 "0:6"
	LocIntervalMs    *int   `json:"loc_interval_ms,omitempty"`
	// RecMode 录音模式：continuous/1=连续（不走短段过滤）；event/0=事件关段
	RecMode FlexScalar `json:"rec_mode,omitempty"`

	// tts_flow：pause / resume / level（level 带 ms=缓冲剩余毫秒）
	Action   string `json:"action,omitempty"`
	MS       int    `json:"ms,omitempty"`
	TTSBufMs *int   `json:"tts_buf_ms,omitempty"`

	// 关段口径（status.audio_end 或音频尾帧可选）：设备实测沉默 / 段间隙，毫秒
	Silence int `json:"silence,omitempty"`
	SegGap  int `json:"seg_gap,omitempty"`

	// notify / ack
	Event  string `json:"event,omitempty"`
	State  string `json:"state,omitempty"`
	Result int    `json:"result,omitempty"`
	Cmd    string `json:"cmd,omitempty"`

	// ota_progress：stage=download|verify|flash|result；note 可选 interrupted|retry|verify_failed|flash_failed|failed
	Done  int64  `json:"done,omitempty"`
	Stage string `json:"stage,omitempty"`
	Note  string `json:"note,omitempty"`
}

// FlexScalar 兼容字符串或数字（rec_mode 等）。
type FlexScalar string

func (f *FlexScalar) UnmarshalJSON(b []byte) error {
	if f == nil {
		return nil
	}
	if len(b) == 0 || string(b) == "null" {
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = FlexScalar(s)
		return nil
	}
	*f = FlexScalar(strings.TrimSpace(string(b)))
	return nil
}

func (f FlexScalar) String() string { return string(f) }

// RecModeIsContinuous 连续录音模式不走设备短段过滤。
func RecModeIsContinuous(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "continuous", "cont", "continue", "1", "true", "on":
		return true
	default:
		return false
	}
}

// NormalizeLocation 把 wifi_scan / wifiAccessPoints 合并进 loc_macs，统一 type。
func (u *DeviceUplink) NormalizeLocation() {
	if u == nil {
		return
	}
	switch u.Type {
	case "geolocate", "loc":
		u.Type = "locate"
	}
	if len(u.LocMacs) == 0 {
		switch {
		case len(u.WifiScan) > 0:
			u.LocMacs = LocMacList(u.WifiScan)
		case len(u.WifiAccessPoints) > 0:
			u.LocMacs = LocMacList(u.WifiAccessPoints)
		}
	}
	if u.LocType == "" && len(u.LocMacs) > 0 {
		u.LocType = LocTypeWiFi
	} else if u.LocType != "" {
		u.LocType = LocTypeCode(NormalizeLocType(string(u.LocType)))
	}
}

// ExpectsBinary 该 JSON 后是否应紧跟音频 Binary（Opus 或 MP3 裸帧）。
func (u *DeviceUplink) ExpectsBinary() bool {
	if u.IsRetransmit() {
		return true
	}
	switch u.Type {
	case "batch", "locate", "loc", "geolocate", "notify", "ack", "ping", "status", "tts_flow", "ota_progress":
		return false
	case "retransmit", "audio":
		return true
	}
	// 兼容旧固件：无 type 但有 codec/total（勿把 ota_progress.total 误判为音频）
	return u.Codec != "" || u.Total > 0
}

// IsRetransmit 补发帧：type=retransmit 或 retransmit=1。
func (u *DeviceUplink) IsRetransmit() bool {
	return u.Type == "retransmit" || u.Retransmit.Bool()
}

// IsUnboundPID 未绑定占位。
func (u *DeviceUplink) IsUnboundPID() bool {
	return u.PID.IsUnbound()
}

// HasLocation 是否携带定位。
func (u *DeviceUplink) HasLocation() bool {
	switch u.Type {
	case "locate", "loc", "geolocate":
		return true
	}
	return u.LocType != "" || u.LocLat != 0 || u.LocLng != 0 ||
		len(u.LocMacs) > 0 || len(u.WifiScan) > 0 || len(u.WifiAccessPoints) > 0
}

// AudioMessage 投递 Kafka / 入库的音频分片。
type AudioMessage struct {
	SN         string      `json:"sn"`
	Token      string      `json:"token,omitempty"`
	Type       string      `json:"type,omitempty"`
	ProtoVer   int         `json:"proto_ver,omitempty"`
	Codec      string      `json:"codec"`
	Seq        int         `json:"seq"`
	Total      int         `json:"total"`
	End        bool        `json:"end"`
	Retransmit bool        `json:"retransmit"`
	Kind       int         `json:"kind"` // 0=event 1=file
	PID        string      `json:"pid,omitempty"`
	Pet        string      `json:"pet,omitempty"` // 兼容旧下游；新协议用 breed
	Breed      string      `json:"breed,omitempty"`
	DB         float64     `json:"db"`
	Bat        int         `json:"bat"`
	Charge     bool        `json:"charge"` // true=充电
	RSSI       int         `json:"rssi"`
	Ch         int         `json:"ch,omitempty"`
	FW         string      `json:"fw,omitempty"`
	Bound      bool        `json:"bound"`
	TS         int64       `json:"ts"`                  // 绝对 Unix 微秒（方案 A）
	TSOffset   int64       `json:"ts_offset,omitempty"` // 相对段首的微秒；段首为 0
	CRC        uint32      `json:"crc,omitempty"`
	OpusData   []byte      `json:"opus_data"`
	ReceivedAt int64       `json:"received_at"`
	IMU        []IMUSample `json:"imu,omitempty"`
	Motion     string      `json:"motion,omitempty"` // walk|run|stand|prone|lie

	LocType  string     `json:"loc_type,omitempty"`
	LocLat   float64    `json:"loc_lat,omitempty"`
	LocLng   float64    `json:"loc_lng,omitempty"`
	LocAcc   float64    `json:"loc_acc,omitempty"`
	LocSpeed float64    `json:"loc_speed,omitempty"`
	LocMacs  LocMacList `json:"loc_macs,omitempty"`
	LocTS    int64      `json:"loc_ts,omitempty"`
}

// LocationMessage 定位透传。
type LocationMessage struct {
	SN         string     `json:"sn"`
	Type       string     `json:"type,omitempty"`
	LocType    string     `json:"loc_type,omitempty"`
	LocLat     float64    `json:"loc_lat"`
	LocLng     float64    `json:"loc_lng"`
	LocAcc     float64    `json:"loc_acc,omitempty"`
	LocSpeed   float64    `json:"loc_speed,omitempty"`
	LocMacs    LocMacList `json:"loc_macs,omitempty"`
	LocTS      int64      `json:"loc_ts,omitempty"`
	Bat        int        `json:"bat"`
	Charge     bool       `json:"charge"`
	RSSI       int        `json:"rssi"`
	TS         int64      `json:"ts"`
	ReceivedAt int64      `json:"received_at"`
}

// a_emb_translate.status：App 列表默认只出终态（ok/fail），过程态先不出。
const (
	TranslateStatusPending     = "pending"     // 刚建档 / 原音处理中
	TranslateStatusTranslating = "translating" // 原音已就绪，AI/TTS 进行中
	TranslateStatusOK          = "ok"          // 完成（可展示）
	TranslateStatusFail        = "fail"        // 失败（可展示，带 error_msg）
)

// TranslateStatusAppReady 是否可对 App 列表展示。
func TranslateStatusAppReady(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case TranslateStatusOK, TranslateStatusFail:
		return true
	default:
		return false
	}
}

// CmdType 下行命令名（放在 cmd 字段，外层 type 固定为 cmd）。
type CmdType string

const (
	CmdSetSensitivity   CmdType = "set_sensitivity"
	CmdSetVolume        CmdType = "set_volume"
	CmdEnableTranslate  CmdType = "enable_translate"
	CmdEnableAutoRecord CmdType = "enable_auto_record" // 开启/关闭自动录音；data "true"/"false"（亦兼容 0/1）
	CmdEnableSleep      CmdType = "enable_sleep"
	CmdSetSleepWindow   CmdType = "set_sleep_window"
	CmdSetTimezone      CmdType = "set_timezone"
	CmdSetPerfMode      CmdType = "set_perf_mode"
	CmdSetPetType       CmdType = "set_pet_type"
	CmdSetBitrate       CmdType = "set_bitrate"
	CmdEnableLocation   CmdType = "enable_location"
	CmdSetLocInterval   CmdType = "set_loc_interval"
	CmdSetFence         CmdType = "set_fence"
	CmdClearFence       CmdType = "clear_fence"
	CmdSetHomeWifi      CmdType = "set_home_wifi"
	CmdReboot           CmdType = "reboot"
	CmdFactoryReset     CmdType = "factory_reset"
	CmdOTAUpdate        CmdType = "ota_update"
	CmdBindDevice       CmdType = "bind_device"
	CmdUnbindDevice     CmdType = "unbind_device"
	CmdSetRecordMode    CmdType = "set_record_mode"
	CmdSetUploadMode    CmdType = "set_upload_mode"
	CmdTokenRefresh     CmdType = "token_refresh"
	CmdPlayCry          CmdType = "play_cry" // data 如 "0x65"
	CmdRecordNow        CmdType = "record_now"
	// CmdEnterCalibBLE 进入 BLE 标定模式；设备 ack result=0 后离线，App 走 BLE（先订 FFE5，再写 FFE4=0x0D）。
	CmdEnterCalibBLE CmdType = "enter_calib_ble"
)

// OTAUpdateData ota_update.data
type OTAUpdateData struct {
	URL     string `json:"url"`
	Version string `json:"version"`
	Force   bool   `json:"force"`
}

// BindDeviceData bind_device.data
type BindDeviceData struct {
	PID   string `json:"pid"`
	Breed string `json:"breed"`
}

// SetFenceData set_fence.data（第一版单圆，非多边形）
type SetFenceData struct {
	Lat    float64 `json:"lat"`
	Lng    float64 `json:"lng"`
	Radius float64 `json:"radius"` // 建议米；与固件确认
}

// DeviceCommand 下行控制：{"type":"cmd","cmd":"...","data":...}
type DeviceCommand struct {
	Type string      `json:"type"` // 固定 "cmd"
	Cmd  string      `json:"cmd"`
	Data interface{} `json:"data"`
}

// DeviceNotify 下行通知：{"type":"notify","event":"token_expired"}
type DeviceNotify struct {
	Type  string `json:"type"` // 固定 "notify"
	Event string `json:"event"`
}

// TTSMeta 下行 TTS 文字帧（后跟 Binary 一帧 60ms OPUS）。
type TTSMeta struct {
	Type  string `json:"type"` // 固定 "tts"
	Seq   int    `json:"seq"`
	Total int    `json:"total"`
	End   int    `json:"end"` // 0/1
	Codec string `json:"codec"`
	PID   string `json:"pid,omitempty"`
	CRC   uint32 `json:"crc"`
}

// AdminCmdRequest 管理接口下发。
type AdminCmdRequest struct {
	SN   string      `json:"sn"`
	Type string      `json:"type"` // 命令名（兼容旧字段）；优先用 Cmd
	Cmd  string      `json:"cmd"`
	Data interface{} `json:"data"`
}

// AdminTTSRequest TTS 下发。
type AdminTTSRequest struct {
	SN       string `json:"sn"`
	PID      string `json:"pid,omitempty"`
	OpusData []byte `json:"opus_data"`
}
