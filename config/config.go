package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config 网关全局配置根结构。
type Config struct {
	Server    ServerConfig    `mapstructure:"server"`
	WebSocket WebSocketConfig `mapstructure:"websocket"`
	Redis     RedisConfig     `mapstructure:"redis"`
	Kafka     KafkaConfig     `mapstructure:"kafka"`
	Database  DatabaseConfig  `mapstructure:"database"`
	OSS       OSSConfig       `mapstructure:"oss"`
	Baidu     BaiduConfig     `mapstructure:"baidu"`
	AI        AIConfig        `mapstructure:"ai"`
	Translate TranslateConfig `mapstructure:"translate"`
	Audio     AudioConfig     `mapstructure:"audio"`
	Device    DeviceConfig    `mapstructure:"device"`
	Firmware  FirmwareConfig  `mapstructure:"firmware"`
	Locate    LocateConfig    `mapstructure:"locate"`
	Admin     AdminConfig     `mapstructure:"admin"`
	Log       LogConfig       `mapstructure:"log"`
}

// ServerConfig HTTP/WSS 服务配置。
type ServerConfig struct {
	Addr               string        `mapstructure:"addr"`
	CertFile           string        `mapstructure:"cert_file"`
	KeyFile            string        `mapstructure:"key_file"`
	CertReloadInterval time.Duration `mapstructure:"cert_reload_interval"`
	WSPath             string        `mapstructure:"ws_path"`
	// PublicBaseURL 下发给设备的对外可达地址，如 https://gw.example.com:443
	// activate 响应里的 server_url = PublicBaseURL + WSPath
	PublicBaseURL string `mapstructure:"public_base_url"`
	// BehindProxy=true：本进程只听明文 HTTP，TLS 由前面 Nginx/宝塔终结（推荐反代部署）。
	// false（默认）：本进程 ListenAndServeTLS，自行提供 HTTPS/WSS。
	BehindProxy bool `mapstructure:"behind_proxy"`
}

// WebSocketConfig WebSocket 连接与心跳、限流配置。
type WebSocketConfig struct {
	DevicePingInterval time.Duration `mapstructure:"device_ping_interval"`
	PongTimeout        time.Duration `mapstructure:"pong_timeout"`
	WriteTimeout       time.Duration `mapstructure:"write_timeout"`
	ReadTimeout        time.Duration `mapstructure:"read_timeout"`
	ReadBufferSize     int           `mapstructure:"read_buffer_size"`
	WriteBufferSize    int           `mapstructure:"write_buffer_size"`
	MaxQPSPerDevice    int           `mapstructure:"max_qps_per_device"` // 逻辑消息/秒；音频 Text+Binary 算 1；超限告警放行不踢
	DedupWindowSize    int           `mapstructure:"dedup_window_size"`
	// EnableServerPing 是否主动向设备发 WebSocket Ping。部分固件栈收到服务端 Ping 会直接断连，可关。
	EnableServerPing bool `mapstructure:"enable_server_ping"`
}

// RedisConfig Redis 连接与 TTL 配置。
type RedisConfig struct {
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
	// KeyPrefix 所有业务 Key 统一前缀，如 petlink
	KeyPrefix string        `mapstructure:"key_prefix"`
	OnlineTTL time.Duration `mapstructure:"online_ttl"`
	ConnTTL   time.Duration `mapstructure:"conn_ttl"`
	PoolSize  int           `mapstructure:"pool_size"`
}

// KafkaConfig Kafka 生产者配置。
type KafkaConfig struct {
	// Enabled=false 时不连接 Kafka，使用 Noop 仅打日志（本地通路联调）
	Enabled       bool          `mapstructure:"enabled"`
	Brokers       []string      `mapstructure:"brokers"`
	TopicAudio    string        `mapstructure:"topic_audio"`
	TopicLocation string        `mapstructure:"topic_location"`
	Timeout       time.Duration `mapstructure:"timeout"`
}

// DatabaseConfig 音频等业务数据持久化（供 App 查询）。
type DatabaseConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Driver  string `mapstructure:"driver"`
	DSN     string `mapstructure:"dsn"`
}

// OSSConfig 阿里云 OSS（原音频与 TTS 结果上传）。
type OSSConfig struct {
	Enabled         bool   `mapstructure:"enabled"`
	Endpoint        string `mapstructure:"endpoint"`
	AccessKeyID     string `mapstructure:"access_key_id"`
	AccessKeySecret string `mapstructure:"access_key_secret"`
	Bucket          string `mapstructure:"bucket"`
	// PublicBase 对外访问域名，如 https://xxx.oss-cn-hangzhou.aliyuncs.com 或 CDN
	PublicBase string `mapstructure:"public_base"`
	// ObjectPrefix 对象前缀，如 device/audio
	ObjectPrefix string `mapstructure:"object_prefix"`
}

// BaiduConfig 百度短文本语音合成（对齐 Translate.php）。
type BaiduConfig struct {
	Enabled      bool   `mapstructure:"enabled"`
	ClientID     string `mapstructure:"client_id"`
	ClientSecret string `mapstructure:"client_secret"`
	// TokenRedisKey Redis 缓存 access_token 的 key
	TokenRedisKey string `mapstructure:"token_redis_key"`
	// 默认合成参数
	Per int `mapstructure:"per"`
	Spd int `mapstructure:"spd"`
	Pit int `mapstructure:"pit"`
	Vol int `mapstructure:"vol"`
	Aue int `mapstructure:"aue"` // 3=mp3
}

// AIConfig 宠物语音翻译 AI 服务（对齐 Translate.php translateAll）。
type AIConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// BaseURL AI 主机，如 http://10.0.0.10（可不带端口）
	BaseURL string `mapstructure:"base_url"`
	// TranslatePort 翻译服务端口；与 base_url 拼成 base_url:port/translate（PHP: ai_translate_port）
	// 若 base_url 已含端口（如 :6001），本项可留 0 不覆盖。
	TranslatePort int           `mapstructure:"translate_port"`
	Timeout       time.Duration `mapstructure:"timeout"`
}

// TranslateEndpoint 拼出完整翻译地址，如 http://10.0.0.10:6001/translate。
func (c AIConfig) TranslateEndpoint() string {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		return ""
	}
	if c.TranslatePort > 0 {
		u, err := url.Parse(base)
		if err == nil && u.Host != "" && u.Port() == "" {
			u.Host = net.JoinHostPort(u.Hostname(), strconv.Itoa(c.TranslatePort))
			base = strings.TrimRight(u.String(), "/")
		} else if err != nil {
			// 解析失败时兜底拼接
			base = fmt.Sprintf("%s:%d", base, c.TranslatePort)
		}
	}
	return base + "/translate"
}

// TranslateConfig 设备音频翻译流水线开关。
type TranslateConfig struct {
	// Enabled 收到 end=true 后是否自动跑 OSS→AI→百度TTS→转OPUS
	Enabled bool `mapstructure:"enabled"`
	// PushToDevice 合成 OPUS 后是否按 60ms 帧推给在线设备喇叭
	PushToDevice bool `mapstructure:"push_to_device"`
	// FFmpegPath ffmpeg 可执行文件；设备 Opus→App MP3、TTS 分帧等；默认 "ffmpeg"
	FFmpegPath string `mapstructure:"ffmpeg_path"`
	// OpusBitrate 设备 TTS 分帧码率，默认 24000（对齐设备端）
	OpusBitrate int `mapstructure:"opus_bitrate"`
	// AppAudioFormat 上传 OSS 给 App 播放的格式：mp3 | wav；设备链路仍用 Opus
	AppAudioFormat string `mapstructure:"app_audio_format"`
	// AppMP3Bitrate App MP3 码率，默认 96000（16k mono；过低易沙沙声）
	AppMP3Bitrate int `mapstructure:"app_mp3_bitrate"`
	// AppSampleRate App 播放采样率，默认 44100（16k MP3 在部分浏览器/播放器会越播越快；采集仍 16k，仅播出重采样）
	AppSampleRate int `mapstructure:"app_sample_rate"`
	// FileSampleRate 已废弃别名：若配置则覆盖 AppSampleRate 仅用于 kind=1；默认 0=与 App 相同（须为 16k）
	FileSampleRate int `mapstructure:"file_sample_rate"`
	// AppLoudnorm 响度归一（默认 false；短音频单遍易炸麦抬底噪）
	AppLoudnorm *bool `mapstructure:"app_loudnorm"`
	// AppDenoise afftdn 降噪（默认 false；对动物声易出音乐噪声）
	AppDenoise *bool `mapstructure:"app_denoise"`
	// AppDenoiseNR afftdn 强度 1–30，默认 12
	AppDenoiseNR int `mapstructure:"app_denoise_nr"`
	// AppHighpass 高通（默认 false）
	AppHighpass *bool `mapstructure:"app_highpass"`
	// AppHighpassHz 高通截止；仅 app_highpass=true 时有效
	AppHighpassHz int `mapstructure:"app_highpass_hz"`
	// AppPresenceDb 中高频搁架增益 dB，0=关闭
	AppPresenceDb float64 `mapstructure:"app_presence_db"`
	// AppMakeupDb 高通后补偿增益 dB，0=不补
	AppMakeupDb float64 `mapstructure:"app_makeup_db"`
	// AppLimiter 真峰限幅约 -1dB（默认 false）
	AppLimiter *bool `mapstructure:"app_limiter"`
	// AppFixRate 事件流按 toc/span 用 atempo 拉慢（默认 false）。
	AppFixRate *bool `mapstructure:"app_fix_rate"`
	// AppFixPitch 与 app_fix_rate 联用：true 时 asetrate 降调（默认 false）
	AppFixPitch *bool `mapstructure:"app_fix_pitch"`
	// AppPitchRatio 强制播放速率（0=不用强制；如 0.67）。优先于自动 toc/span
	AppPitchRatio float64 `mapstructure:"app_pitch_ratio"`
	// DropShortEnabled 是否丢弃过短整段录音（不入库/不翻译）
	DropShortEnabled bool `mapstructure:"drop_short_enabled"`
	// MinDurationSec 录音文件（kind=1）最短有效时长（秒）；仅 drop_short_enabled=true 时生效，默认 0.6
	MinDurationSec float64 `mapstructure:"min_duration_sec"`
	// MinDurationEventSec 事件流（kind=0）最短时长；0=不过滤（默认）；>0 才启用
	MinDurationEventSec float64 `mapstructure:"min_duration_event_sec"`
	// PaceAlignEvent 事件流按设备时间戳在包间补 PLC 静音（默认 false）。
	// 实测会把连续叫声切成「突突」碎片，听感更差；偏短/节拍问题应固件统一 20ms 连续上传或 TOC=真实帧长。
	PaceAlignEvent *bool `mapstructure:"pace_align_event"`
	// OpusDecode 设备 Opus→PCM：pion（默认，整段连续解码）| ffmpeg（强制 Ogg+libopus）。
	// 听感 A/B：32kbps 仍沙时改 ffmpeg 重启对比，确认是否解码器差异。
	OpusDecode string `mapstructure:"opus_decode"`
	// StitchEnabled 事件流相邻段是否按实测 ts 间隙拼接后再翻译。nil/true=开
	StitchEnabled *bool `mapstructure:"stitch_enabled"`
	// StitchWindowMs 相邻两段实测 ts 间隙窗口（毫秒）。默认 5000。
	// 设备关段仍是 800ms 数字静音；真实沉默 ≈ 实测间隙 + DeviceSegSilenceMs。
	// 固件若把关段阈值抬到 2500，本窗口必须同步改成 2500。
	StitchWindowMs int `mapstructure:"stitch_window_ms"`
	// DeviceSegSilenceMs 每段尾巴数字静音（毫秒），仅用于对数/估算真实沉默。默认 800
	DeviceSegSilenceMs int `mapstructure:"device_seg_silence_ms"`
	// TTSInterGapMs 上一路最后一帧到下一路首帧的间隔（毫秒）。默认 500
	TTSInterGapMs int `mapstructure:"tts_inter_gap_ms"`
	// TTSQueueCap 每设备 TTS 队列封顶（含在途）。默认 2，多则丢老保新
	TTSQueueCap int `mapstructure:"tts_queue_cap"`
}

// AudioConfig 设备上行音频双通路（kind=0 事件流 / kind=1 录音文件）。
type AudioConfig struct {
	// Accept 接收哪些通路：event | file | both（默认 both）
	Accept string `mapstructure:"accept"`
	// Translate 哪一路跑识别翻译：event | file | both | none（默认 event，避免同一声翻两次）
	Translate string `mapstructure:"translate"`
	// ArchiveOther accept 了但未进 translate 的通路，end 时是否仍 OSS 归档原音；nil/缺省=true
	ArchiveOther *bool `mapstructure:"archive_other"`
}

// AcceptsKind 是否接收该 kind（0/1）。
func (c AudioConfig) AcceptsKind(kind int) bool {
	switch strings.ToLower(strings.TrimSpace(c.Accept)) {
	case "event":
		return kind == 0
	case "file":
		return kind == 1
	case "both", "":
		return true
	default:
		return true
	}
}

// TranslatesKind 该 kind 是否跑完整识别翻译。
func (c AudioConfig) TranslatesKind(kind int) bool {
	switch strings.ToLower(strings.TrimSpace(c.Translate)) {
	case "none", "off", "false":
		return false
	case "file":
		return kind == 1
	case "both":
		return true
	case "event", "":
		return kind == 0
	default:
		return kind == 0
	}
}

// StitchOn 事件流相邻段是否拼接。默认开。
func (c TranslateConfig) StitchOn() bool {
	if c.StitchEnabled == nil {
		return true
	}
	return *c.StitchEnabled
}

// ArchivesKind end 时是否走 OSS 归档（含仅原音、不翻译）。
func (c AudioConfig) ArchivesKind(kind int) bool {
	if !c.AcceptsKind(kind) {
		return false
	}
	if c.TranslatesKind(kind) {
		return true
	}
	if c.ArchiveOther == nil {
		return true
	}
	return *c.ArchiveOther
}

// DeviceConfig 设备激活 / Token 相关配置。
type DeviceConfig struct {
	TokenBytes int `mapstructure:"token_bytes"`
}

// FirmwareConfig 固件 OTA 版本信息（固件包上传到 OSS/CDN，此处下发元数据）。
type FirmwareConfig struct {
	// Version 最新版本号，如 1.2.0；空表示未配置
	Version string `mapstructure:"version" json:"version"`
	// URL 固件包下载地址（OSS/CDN 公网 URL）
	URL string `mapstructure:"url" json:"url"`
	// Force 是否强制升级
	Force bool `mapstructure:"force" json:"force"`
	// Size 文件字节数，0 表示未填
	Size int64 `mapstructure:"size" json:"size"`
	// MD5 可选校验
	MD5 string `mapstructure:"md5" json:"md5"`
	// Changelog 可选更新说明
	Changelog string `mapstructure:"changelog" json:"changelog"`
	// DownloadMode 已忽略。下载接口一律代理成 200/206，禁止 302（设备 HTTP 客户端不跟随重定向）。
	DownloadMode string `mapstructure:"download_mode" json:"download_mode,omitempty"`
}

// AdminConfig 后台管理（固件上传等），需密码登录。
type AdminConfig struct {
	Enabled  bool          `mapstructure:"enabled"`
	Password string        `mapstructure:"password"`
	TokenTTL time.Duration `mapstructure:"token_ttl"`
	// StaticDir 后台 SPA 静态目录（vite build 产出 dist）。空则尝试 ./web/test-console/dist。
	StaticDir string `mapstructure:"static_dir"`
}

// LocateConfig WiFi 定位解析配置。
type LocateConfig struct {
	// Provider: mock（固定坐标）| fingerprint（按 MAC 指纹缓存，未命中回退 mock）
	Provider string  `mapstructure:"provider"`
	MockLat  float64 `mapstructure:"mock_lat"`
	MockLng  float64 `mapstructure:"mock_lng"`
	MockAcc  float64 `mapstructure:"mock_acc"`
	// FingerprintTTL WiFi 指纹坐标缓存 TTL，0 表示不过期
	FingerprintTTL time.Duration `mapstructure:"fingerprint_ttl"`
}

// LogConfig zap 日志配置。
type LogConfig struct {
	Level      string `mapstructure:"level"`
	Filename   string `mapstructure:"filename"`
	MaxSize    int    `mapstructure:"max_size"`
	MaxBackups int    `mapstructure:"max_backups"`
	MaxAge     int    `mapstructure:"max_age"`
	Compress   bool   `mapstructure:"compress"`
}

// Load 从指定路径加载 YAML 配置。
func Load(path string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}

	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// applyDefaults 填充可缺省项。
func (c *Config) applyDefaults() {
	if c.Device.TokenBytes <= 0 {
		c.Device.TokenBytes = 16
	}
	if c.Redis.KeyPrefix == "" {
		c.Redis.KeyPrefix = "petlink"
	}
	if c.Locate.Provider == "" {
		c.Locate.Provider = "fingerprint"
	}
	if c.Locate.MockAcc <= 0 {
		c.Locate.MockAcc = 50
	}
	// 默认给一个上海附近坐标，方便联调
	if c.Locate.MockLat == 0 && c.Locate.MockLng == 0 {
		c.Locate.MockLat = 31.2304
		c.Locate.MockLng = 121.4737
	}
	if c.Database.Driver == "" {
		c.Database.Driver = "sqlite"
	}
	if c.Database.DSN == "" {
		c.Database.DSN = "./data/petlink.db"
	}
	if c.Baidu.TokenRedisKey == "" {
		c.Baidu.TokenRedisKey = "baidu_translate_access_token"
	}
	if c.Baidu.Aue == 0 {
		c.Baidu.Aue = 3
	}
	if c.Baidu.Spd == 0 {
		c.Baidu.Spd = 5
	}
	if c.Baidu.Pit == 0 {
		c.Baidu.Pit = 5
	}
	if c.Baidu.Vol == 0 {
		c.Baidu.Vol = 5
	}
	if c.AI.Timeout <= 0 {
		c.AI.Timeout = 60 * time.Second
	}
	if c.OSS.ObjectPrefix == "" {
		c.OSS.ObjectPrefix = "device/audio"
	}
	if c.Translate.FFmpegPath == "" {
		c.Translate.FFmpegPath = "ffmpeg"
	}
	if c.Translate.OpusBitrate <= 0 {
		c.Translate.OpusBitrate = 24000
	}
	if c.Translate.AppAudioFormat == "" {
		c.Translate.AppAudioFormat = "mp3"
	}
	if c.Translate.AppMP3Bitrate <= 0 {
		c.Translate.AppMP3Bitrate = 96000
	}
	if c.Translate.AppSampleRate <= 0 {
		c.Translate.AppSampleRate = 44100
	}
	if c.Translate.AppHighpassHz <= 0 {
		c.Translate.AppHighpassHz = 120
	}
	if c.Translate.AppPresenceDb < 0 {
		c.Translate.AppPresenceDb = 0
	}
	if c.Translate.AppMakeupDb < 0 {
		c.Translate.AppMakeupDb = 0
	}
	// file_sample_rate：仅显式配置且 >0 时用于 kind=1；默认与 app 同为 16k（两路同源 PCM）
	if c.Translate.DropShortEnabled && c.Translate.MinDurationSec <= 0 {
		c.Translate.MinDurationSec = 0.6
	}
	// min_duration_event_sec：>0 才对事件流做时长过滤（推荐 1s）；0/不配 = 事件流不过滤
	if c.Translate.StitchWindowMs <= 0 {
		c.Translate.StitchWindowMs = 5000
	}
	if c.Translate.DeviceSegSilenceMs <= 0 {
		c.Translate.DeviceSegSilenceMs = 800
	}
	if c.Translate.TTSInterGapMs <= 0 {
		c.Translate.TTSInterGapMs = 500
	}
	if c.Translate.TTSQueueCap <= 0 {
		c.Translate.TTSQueueCap = 2
	}

	if c.Audio.Accept == "" {
		c.Audio.Accept = "both"
	}
	if c.Audio.Translate == "" {
		c.Audio.Translate = "event"
	}
	if c.Admin.TokenTTL <= 0 {
		c.Admin.TokenTTL = 24 * time.Hour
	}
	if strings.TrimSpace(c.Firmware.DownloadMode) == "" {
		c.Firmware.DownloadMode = "direct"
	}
}

func (c *Config) validate() error {
	if c.Server.Addr == "" {
		return fmt.Errorf("server.addr 不能为空")
	}
	if !c.Server.BehindProxy {
		if c.Server.CertFile == "" || c.Server.KeyFile == "" {
			return fmt.Errorf("server.cert_file/key_file 不能为空（或设 behind_proxy: true 由反代终结 TLS）")
		}
	}
	if c.Server.WSPath == "" {
		return fmt.Errorf("server.ws_path 不能为空")
	}
	if c.Server.PublicBaseURL == "" {
		return fmt.Errorf("server.public_base_url 不能为空（activate 需下发 server_url）")
	}
	if c.WebSocket.MaxQPSPerDevice <= 0 {
		return fmt.Errorf("websocket.max_qps_per_device 必须大于 0")
	}
	if c.WebSocket.PongTimeout <= 0 {
		return fmt.Errorf("websocket.pong_timeout 必须大于 0")
	}
	if c.Redis.Addr == "" {
		return fmt.Errorf("redis.addr 不能为空")
	}
	if c.Kafka.Enabled {
		if len(c.Kafka.Brokers) == 0 {
			return fmt.Errorf("kafka.enabled=true 时 brokers 不能为空")
		}
		if c.Kafka.TopicAudio == "" || c.Kafka.TopicLocation == "" {
			return fmt.Errorf("kafka.enabled=true 时 topic 不能为空")
		}
	}
	switch c.Locate.Provider {
	case "mock", "fingerprint":
	default:
		return fmt.Errorf("locate.provider 仅支持 mock/fingerprint")
	}
	switch strings.ToLower(strings.TrimSpace(c.Firmware.DownloadMode)) {
	case "", "direct", "redirect", "proxy":
	default:
		return fmt.Errorf("firmware.download_mode 仅支持 direct / redirect / proxy")
	}
	if c.Database.Enabled {
		d := strings.ToLower(strings.TrimSpace(c.Database.Driver))
		if d == "" {
			d = "sqlite"
			c.Database.Driver = d
		}
		if d != "sqlite" && d != "mysql" {
			return fmt.Errorf("database.driver 仅支持 sqlite / mysql")
		}
		if c.Database.DSN == "" {
			return fmt.Errorf("database.enabled=true 时 dsn 不能为空")
		}
	}
	if c.Translate.Enabled {
		if !c.Database.Enabled {
			return fmt.Errorf("translate.enabled=true 时需要 database.enabled=true")
		}
		if !c.OSS.Enabled {
			return fmt.Errorf("translate.enabled=true 时需要 oss.enabled=true")
		}
		if !c.AI.Enabled || c.AI.TranslateEndpoint() == "" {
			return fmt.Errorf("translate.enabled=true 时需要配置 ai.base_url（及 translate_port）")
		}
		if !c.Baidu.Enabled || c.Baidu.ClientID == "" || c.Baidu.ClientSecret == "" {
			return fmt.Errorf("translate.enabled=true 时需要配置 baidu client_id/secret")
		}
		if c.OSS.Endpoint == "" || c.OSS.AccessKeyID == "" || c.OSS.Bucket == "" || c.OSS.PublicBase == "" {
			return fmt.Errorf("translate.enabled=true 时 oss 配置不完整")
		}
	}
	return nil
}

// WSServerURL 拼装下发给设备的 WebSocket 入口（不含 query）。
func (c *Config) WSServerURL() string {
	base := c.Server.PublicBaseURL
	// 去掉末尾斜杠
	for len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	path := c.Server.WSPath
	if path == "" {
		path = "/ws"
	}
	if path[0] != '/' {
		path = "/" + path
	}
	return base + path
}
