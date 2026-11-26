package model

// DeviceCmdDataOption play_cry 等命令的可选 data 取值。
type DeviceCmdDataOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Pet   string `json:"pet,omitempty"`
	File  string `json:"file,omitempty"`
}

// DeviceCmdSpec 单条下行 cmd 说明，供 App 动态构建下发 UI。
type DeviceCmdSpec struct {
	Cmd         string                `json:"cmd"`
	DataType    string                `json:"data_type"` // string | number | object
	Category    string                `json:"category"`  // config | location | device | audio
	Description string                `json:"description"`
	Example     interface{}           `json:"example"`
	ExampleReq  map[string]interface{} `json:"example_request"`
	BLECode     string                `json:"ble_code,omitempty"`
	ConfigKey   string                `json:"config_key,omitempty"`
	DataOptions []DeviceCmdDataOption `json:"data_options,omitempty"`
}

// DeviceCommandsCatalog 云端支持的全部设备下行 cmd。
type DeviceCommandsCatalog struct {
	Total       int               `json:"total"`
	SendMethod  string            `json:"send_method"`
	SendPath    string            `json:"send_path"`
	RequestBody map[string]string `json:"request_body"`
	List        []DeviceCmdSpec   `json:"list"`
}

func exampleReq(cmd string, data interface{}) map[string]interface{} {
	return map[string]interface{}{
		"sn":   "PT-xxx",
		"cmd":  cmd,
		"data": data,
	}
}

var playCryOptions = []DeviceCmdDataOption{
	{Value: "0x65", Label: "饥饿", Pet: "cat", File: "cat_hungry.opus"},
	{Value: "0x66", Label: "求玩耍", Pet: "cat", File: "cat_play.opus"},
	{Value: "0x67", Label: "不满抗议", Pet: "cat", File: "cat_angry.opus"},
	{Value: "0x68", Label: "撒娇求摸", Pet: "cat", File: "cat_purr.opus"},
	{Value: "0x69", Label: "警惕戒备", Pet: "cat", File: "cat_vigilant.opus"},
	{Value: "0x6A", Label: "兴奋欢迎", Pet: "cat", File: "cat_greet.opus"},
	{Value: "0x6B", Label: "孤独寻找", Pet: "cat", File: "cat_howl.opus"},
	{Value: "0x6C", Label: "紧急求助", Pet: "cat", File: "cat_distress.opus"},
	{Value: "0x6D", Label: "害怕不安", Pet: "cat", File: "cat_fear.opus"},
	{Value: "0xC9", Label: "兴奋欢迎", Pet: "dog", File: "dog_greet.opus"},
	{Value: "0xCA", Label: "求玩耍", Pet: "dog", File: "dog_play.opus"},
	{Value: "0xCB", Label: "饥饿", Pet: "dog", File: "dog_hungry.opus"},
	{Value: "0xCC", Label: "警惕保护", Pet: "dog", File: "dog_alert.opus"},
	{Value: "0xCD", Label: "撒娇求摸", Pet: "dog", File: "dog_purr.opus"},
	{Value: "0xCE", Label: "警惕戒备", Pet: "dog", File: "dog_vigilant.opus"},
	{Value: "0xCF", Label: "焦虑不安", Pet: "dog", File: "dog_fear.opus"},
	{Value: "0x00", Label: "未匹配兜底", Pet: "any"},
}

// ListDeviceCommands 返回网关支持的全部 WSS 下行 cmd 及 data 说明。
func ListDeviceCommands() DeviceCommandsCatalog {
	list := []DeviceCmdSpec{
		{
			Cmd: string(CmdSetSensitivity), DataType: "string", Category: "config",
			Description: "设置麦克风灵敏度，0–100",
			Example:     "80", ExampleReq: exampleReq("set_sensitivity", "80"),
			BLECode: "0x06", ConfigKey: "sensitivity",
		},
		{
			Cmd: string(CmdSetVolume), DataType: "string", Category: "config",
			Description: "设置播放音量，0–100",
			Example:     "50", ExampleReq: exampleReq("set_volume", "50"),
			ConfigKey: "volume",
		},
		{
			Cmd: string(CmdEnableTranslate), DataType: "string", Category: "config",
			Description: "开关自动录音，0=关 1=开（有声/事件触发是否自动开录）；设备执行后应回 type=ack。新固件优先用 enable_auto_record",
			Example:     "1", ExampleReq: exampleReq("enable_translate", "1"),
			ConfigKey: "translate_enabled",
		},
		{
			Cmd: string(CmdEnableAutoRecord), DataType: "string", Category: "config",
			Description: "开关自动录音，true=开 false=关（亦兼容 1/0）；与 enable_translate 语义相同，写同一影子 translate_enabled",
			Example:     "true", ExampleReq: exampleReq("enable_auto_record", "true"),
			ConfigKey: "translate_enabled",
			DataOptions: []DeviceCmdDataOption{
				{Value: "true", Label: "开启自动录音"},
				{Value: "false", Label: "关闭自动录音"},
			},
		},
		{
			Cmd: string(CmdEnableSleep), DataType: "string", Category: "config",
			Description: "开关睡眠模式，0=关 1=开",
			Example:     "1", ExampleReq: exampleReq("enable_sleep", "1"),
			ConfigKey: "sleep_enabled",
		},
		{
			Cmd: string(CmdSetSleepWindow), DataType: "string", Category: "config",
			Description: "睡眠时段，格式 开始小时:结束小时",
			Example:     "0:6", ExampleReq: exampleReq("set_sleep_window", "0:6"),
			ConfigKey: "sleep_window",
		},
		{
			Cmd: string(CmdSetTimezone), DataType: "string", Category: "config",
			Description: "相对 UTC 的分钟偏移，北京为 480",
			Example:     "480", ExampleReq: exampleReq("set_timezone", "480"),
		},
		{
			Cmd: string(CmdSetPerfMode), DataType: "number", Category: "config",
			Description: "性能模式，0=low 1=high",
			Example:     1, ExampleReq: exampleReq("set_perf_mode", 1),
		},
		{
			Cmd: string(CmdSetPetType), DataType: "string", Category: "config",
			Description: "宠物类型，如 dog / cat",
			Example:     "dog", ExampleReq: exampleReq("set_pet_type", "dog"),
		},
		{
			Cmd: string(CmdSetBitrate), DataType: "string", Category: "config",
			Description: "设置实时翻译音频上行（type=audio 的 OPUS Binary）码率：0=16kbps、1=24kbps（默认）、2=32kbps；data 为整型字符串。只影响该链路；record_now 约 3s 手动录音用固定 32kbps 独立编码器，不受本档位影响",
			Example:     "1", ExampleReq: exampleReq("set_bitrate", "1"),
			DataOptions: []DeviceCmdDataOption{
				{Value: "0", Label: "16kbps"},
				{Value: "1", Label: "24kbps（默认）"},
				{Value: "2", Label: "32kbps"},
			},
		},
		{
			Cmd: string(CmdSetRecordMode), DataType: "string", Category: "config",
			Description: "录音模式：0=连续录制（每帧都传），1=事件触发（默认，有声音才传）；不是简单开关。设备执行后回 type=ack；非法值 ack.result=1",
			Example:     "1", ExampleReq: exampleReq("set_record_mode", "1"),
		},
		{
			Cmd: string(CmdSetUploadMode), DataType: "string", Category: "config",
			Description: "上传模式，0 / 1（与固件对齐）",
			Example:     "1", ExampleReq: exampleReq("set_upload_mode", "1"),
		},
		{
			Cmd: string(CmdEnableLocation), DataType: "string", Category: "location",
			Description: "开关定位上报，0=关 1=开",
			Example:     "1", ExampleReq: exampleReq("enable_location", "1"),
		},
		{
			Cmd: string(CmdSetLocInterval), DataType: "number", Category: "location",
			Description: "定位上报间隔，单位毫秒",
			Example:     30000, ExampleReq: exampleReq("set_loc_interval", 30000),
			ConfigKey: "loc_interval_ms",
		},
		{
			Cmd: string(CmdSetFence), DataType: "object", Category: "location",
			Description: "设置单圆电子围栏",
			Example: map[string]interface{}{"lat": 39.9, "lng": 116.4, "radius": 200},
			ExampleReq: exampleReq("set_fence", map[string]interface{}{"lat": 39.9, "lng": 116.4, "radius": 200}),
		},
		{
			Cmd: string(CmdClearFence), DataType: "object", Category: "location",
			Description: "清除电子围栏",
			Example: map[string]interface{}{}, ExampleReq: exampleReq("clear_fence", map[string]interface{}{}),
		},
		{
			Cmd: string(CmdSetHomeWifi), DataType: "string", Category: "location",
			Description: "设置家庭 WiFi SSID",
			Example: "MyWiFi", ExampleReq: exampleReq("set_home_wifi", "MyWiFi"),
		},
		{
			Cmd: string(CmdOTAUpdate), DataType: "object", Category: "device",
			Description: "OTA 固件升级",
			Example: map[string]interface{}{"url": "https://example.com/fw.bin", "version": "1.2.0", "force": false, "size": 1048576, "md5": "...", "accept_ranges": true},
			ExampleReq: exampleReq("ota_update", map[string]interface{}{"url": "https://example.com/fw.bin", "version": "1.2.0", "force": true, "size": 1048576, "md5": "...", "accept_ranges": true}),
		},
		{
			Cmd: string(CmdBindDevice), DataType: "object", Category: "device",
			Description: "绑定设备到宠物",
			Example: map[string]interface{}{"pid": "pet-001", "breed": "golden"},
			ExampleReq: exampleReq("bind_device", map[string]interface{}{"pid": "pet-001", "breed": "golden"}),
			BLECode: "0x07",
		},
		{
			Cmd: string(CmdUnbindDevice), DataType: "object", Category: "device",
			Description: "解绑设备",
			Example: map[string]interface{}{}, ExampleReq: exampleReq("unbind_device", map[string]interface{}{}),
			BLECode: "0x08",
		},
		{
			Cmd: string(CmdReboot), DataType: "object", Category: "device",
			Description: "重启设备",
			Example: map[string]interface{}{}, ExampleReq: exampleReq("reboot", map[string]interface{}{}),
		},
		{
			Cmd: string(CmdFactoryReset), DataType: "object", Category: "device",
			Description: "恢复出厂设置",
			Example: map[string]interface{}{}, ExampleReq: exampleReq("factory_reset", map[string]interface{}{}),
			BLECode: "0x02",
		},
		{
			Cmd: string(CmdTokenRefresh), DataType: "object", Category: "device",
			Description: "提示设备走 HTTP refresh 刷新令牌",
			Example: map[string]interface{}{}, ExampleReq: exampleReq("token_refresh", map[string]interface{}{}),
		},
		{
			Cmd: string(CmdPlayCry), DataType: "string", Category: "audio",
			Description: "播放设备本地预置固定叫声，data 为 Hex 字符串",
			Example: "0xCB", ExampleReq: exampleReq("play_cry", "0xCB"),
			DataOptions: playCryOptions,
		},
		{
			Cmd: string(CmdRecordNow), DataType: "object", Category: "audio",
			Description: "触发约 3s 手动录音一次；设备用固定 32kbps 独立编码器（不受 set_bitrate 影响，回放音质优先），按音频协议上报完整 seq=1..total + end=1",
			Example: map[string]interface{}{}, ExampleReq: exampleReq("record_now", map[string]interface{}{}),
		},
		{
			Cmd: string(CmdEnterCalibBLE), DataType: "object", Category: "device",
			Description: "进入 BLE 标定模式；设备回 type=ack result=0 后会离线，App 改走 BLE：必须先订阅 FFE5，再往 FFE4 写单字节 0x0D；FFE5 先回 CALIB:START，约 8s 后回 CALIB:OK 或 CALIB:FAIL+原因。详见 App-BLE 通道协议",
			Example: map[string]interface{}{}, ExampleReq: exampleReq("enter_calib_ble", map[string]interface{}{}),
			BLECode: "0x0D",
		},
	}
	return DeviceCommandsCatalog{
		Total:      len(list),
		SendMethod: "POST",
		SendPath:   "/api/v1/device/cmd",
		RequestBody: map[string]string{
			"sn":   "设备序列号",
			"cmd":  "命令名，见 list[].cmd",
			"data": "命令参数，类型见 list[].data_type 与 example",
		},
		List: list,
	}
}
