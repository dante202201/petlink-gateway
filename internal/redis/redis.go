// Package redisx 封装设备鉴权、在线状态、离线指令与定位缓存的 Redis 操作。
package redisx

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"pet-translate-gateway/config"
)

const (
	keyAuthHash         = "pet:device:auth"
	keyTokenPrefix      = "pet:device:token:"
	keyTokenRevPrefix   = "pet:device:token_rev:"
	keyMetaPrefix       = "pet:device:meta:"
	keyOnlinePrefix     = "pet:online:"
	keyConnPrefix       = "pet:conn:"
	keyOfflineCmdPrefix = "pet:offline_cmd:"
	keyWifiFPPrefix     = "pet:wifi_fp:"
	keyLastLocPrefix    = "pet:device:last_loc:"
	keyStatusPrefix     = "pet:device:status:"
	keyPendingAutoRec   = "pet:pending_auto_record:"
)

// Client Redis 工具客户端，集中管理本网关全部 Redis 读写。
type Client struct {
	rdb       *redis.Client
	prefix    string
	onlineTTL time.Duration
	connTTL   time.Duration
}

// DeviceMeta 激活时写入的设备基础信息。
type DeviceMeta struct {
	SN        string `json:"sn"`
	FWVer     string `json:"fw_ver"`
	MAC       string `json:"mac"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// CachedLocation WiFi 指纹或设备最近一次解析坐标。
type CachedLocation struct {
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
	Accuracy float64 `json:"accuracy"`
	Source   string  `json:"source,omitempty"`
	Updated  int64   `json:"updated,omitempty"`
}

// DeviceStatus 设备统一状态快照，聚合 meta+上报+在线+位置信息。
// 注意：数值/布尔字段全部不带 omitempty，避免 0/false 被 JSON 序列化时"吞掉" → App 侧拿不到合法零值。
type DeviceStatus struct {
	SN               string          `json:"sn"`
	FWVer            string          `json:"fw_ver,omitempty"`
	MAC              string          `json:"mac,omitempty"`
	Online           bool            `json:"online"`
	ConnID           string          `json:"conn_id,omitempty"`
	ConnectedAt      int64           `json:"connected_at,omitempty"`
	LastSeenAt       int64           `json:"last_seen_at,omitempty"`
	LastReportAt     int64           `json:"last_report_at,omitempty"`
	Battery          int             `json:"b"`
	Charge           bool            `json:"charge"`
	RSSI             int             `json:"rssi"`
	Bound            int             `json:"bound"`
	PID              string          `json:"pid,omitempty"`
	Breed            string          `json:"breed,omitempty"`
	DB               float64         `json:"db"`
	LastEvent        string          `json:"last_event,omitempty"`
	LastState        string          `json:"last_state,omitempty"`
	Motion           string          `json:"motion,omitempty"` // walk|run|stand|prone|lie
	Tier             string          `json:"tier,omitempty"`   // normal|light|deep
	IMU              json.RawMessage `json:"imu,omitempty"`    // 最近一次上报的 IMU 采样数组
	LastLocLat       float64         `json:"last_loc_lat,omitempty"`
	LastLocLng       float64         `json:"last_loc_lng,omitempty"`
	LastLocAcc       float64         `json:"last_loc_acc,omitempty"`
	LastLocTS        int64           `json:"last_loc_ts,omitempty"`
	LocSpeed         float64         `json:"loc_speed,omitempty"`
	LocType          string          `json:"loc_type,omitempty"`
	Ch               int             `json:"ch,omitempty"`
	ProtoVer         int             `json:"proto_ver,omitempty"`
	Volume           *int            `json:"volume,omitempty"`
	Sensitivity      *int            `json:"sensitivity,omitempty"`
	TranslateEnabled *bool           `json:"translate_enabled,omitempty"`
	AutoRecord       *bool           `json:"auto_record,omitempty"` // 自动录音 0/1；与 translate_enabled 同步
	SleepEnabled     *bool           `json:"sleep_enabled,omitempty"`
	SleepWindow      string          `json:"sleep_window,omitempty"`
	LocIntervalMs    *int            `json:"loc_interval_ms,omitempty"`
	ConfigSource     string          `json:"config_source,omitempty"` // device | shadow
	ConfigUpdatedAt  int64           `json:"config_updated_at,omitempty"`
	ActivatedAt      int64           `json:"activated_at,omitempty"`
	// OfflineReason 离线原因：空=普通掉线/未知；calib_ble=enter_calib_ble 后主动离线（与意外掉线区分）
	OfflineReason string `json:"offline_reason,omitempty"`
}

// 离线原因常量（写入 DeviceStatus.OfflineReason / 状态接口 offline_reason）。
const (
	OfflineReasonCalibBLE = "calib_ble" // 主动进入 BLE 标定离线
)

// DeviceStatusPatch 用于 UpdateDeviceStatus：
// 字段"是否显式 set"和"set 后的值"分离，
// 解决布尔 false / 数值 0 / 空字符串 是"合法值"还是"调用方没传"的二义性。
// 约定：调用 refreshDeviceStatusFromMeta 时，从 meta 解到的字段一律显式 Set=true（0/false 也要覆盖）。
type DeviceStatusPatch struct {
	SetFWVer            bool
	SetMAC              bool
	SetConnID           bool
	SetConnectedAt      bool
	SetLastSeenAt       bool
	SetLastReportAt     bool
	SetBattery          bool
	SetCharge           bool
	SetRSSI             bool
	SetBound            bool
	SetPID              bool
	SetBreed            bool
	SetDB               bool
	SetLastEvent        bool
	SetLastState        bool
	SetMotion           bool
	SetTier             bool
	SetIMU              bool
	SetLastLoc          bool
	SetLocExtra         bool
	SetCh               bool
	SetProtoVer         bool
	SetVolume           bool
	SetSensitivity      bool
	SetTranslateEnabled bool
	SetAutoRecord       bool
	SetSleepEnabled     bool
	SetSleepWindow      bool
	SetLocIntervalMs    bool
	SetConfigSource     bool
	SetConfigUpdatedAt  bool
	SetActivatedAt      bool
	SetOfflineReason    bool

	FWVer            string
	MAC              string
	ConnID           string
	ConnectedAt      int64
	LastSeenAt       int64
	LastReportAt     int64
	Battery          int
	Charge           bool
	RSSI             int
	Bound            int
	PID              string
	Breed            string
	DB               float64
	LastEvent        string
	LastState        string
	Motion           string
	Tier             string
	IMU              json.RawMessage
	LastLocLat       float64
	LastLocLng       float64
	LastLocAcc       float64
	LastLocTS        int64
	LocSpeed         float64
	LocType          string
	Ch               int
	ProtoVer         int
	Volume           int
	Sensitivity      int
	TranslateEnabled bool
	AutoRecord       bool
	SleepEnabled     bool
	SleepWindow      string
	LocIntervalMs    int
	ConfigSource     string
	ConfigUpdatedAt  int64
	ActivatedAt      int64
	OfflineReason    string
}

// New 根据配置创建 Redis 客户端并 Ping 探活。
func New(cfg config.RedisConfig) (*Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
		PoolSize: cfg.PoolSize,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis ping 失败: %w", err)
	}

	prefix := strings.TrimSpace(cfg.KeyPrefix)
	if prefix == "" {
		prefix = "petlink"
	}
	prefix = strings.TrimRight(prefix, ":")

	return &Client{
		rdb:       rdb,
		prefix:    prefix,
		onlineTTL: cfg.OnlineTTL,
		connTTL:   cfg.ConnTTL,
	}, nil
}

// k 给业务 key 加上统一前缀，如 petlink:pet:online:SN。
func (c *Client) k(key string) string {
	if c.prefix == "" {
		return key
	}
	return c.prefix + ":" + key
}

// Close 关闭底层连接池。
func (c *Client) Close() error {
	return c.rdb.Close()
}

// ValidateDeviceToken 校验设备 SN 与 Token。
func (c *Client) ValidateDeviceToken(ctx context.Context, sn, token string) (bool, error) {
	if sn == "" || token == "" {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	val, err := c.rdb.HGet(ctx, c.k(keyAuthHash), sn).Result()
	if err == nil {
		return val == token, nil
	}
	if err != redis.Nil {
		return false, err
	}

	val, err = c.rdb.Get(ctx, c.k(keyTokenPrefix+sn)).Result()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return val == token, nil
}

// SaveDeviceToken 写入/更新设备 Token（Hash + 单 Key 双写），并维护 token→sn 反查。
func (c *Client) SaveDeviceToken(ctx context.Context, sn, token string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	sn = strings.TrimSpace(sn)
	token = strings.TrimSpace(token)
	old, _ := c.rdb.HGet(ctx, c.k(keyAuthHash), sn).Result()
	pipe := c.rdb.TxPipeline()
	if old != "" && old != token {
		pipe.Del(ctx, c.k(keyTokenRevPrefix+old))
	}
	pipe.HSet(ctx, c.k(keyAuthHash), sn, token)
	pipe.Set(ctx, c.k(keyTokenPrefix+sn), token, 0)
	if token != "" {
		pipe.Set(ctx, c.k(keyTokenRevPrefix+token), sn, 0)
	}
	_, err := pipe.Exec(ctx)
	return err
}

// LookupSNByToken 用 token 反查 SN。新写入走反查 key；旧数据回退扫描鉴权 Hash。
func (c *Client) LookupSNByToken(ctx context.Context, token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if sn, err := c.rdb.Get(ctx, c.k(keyTokenRevPrefix+token)).Result(); err == nil && strings.TrimSpace(sn) != "" {
		ok, verr := c.ValidateDeviceToken(ctx, sn, token)
		if verr != nil {
			return "", verr
		}
		if ok {
			return sn, nil
		}
	} else if err != nil && err != redis.Nil {
		return "", err
	}
	m, err := c.rdb.HGetAll(ctx, c.k(keyAuthHash)).Result()
	if err != nil && err != redis.Nil {
		return "", err
	}
	for sn, val := range m {
		if val == token {
			_ = c.rdb.Set(ctx, c.k(keyTokenRevPrefix+token), sn, 0).Err()
			return sn, nil
		}
	}
	return "", nil
}

// GetDeviceToken 读取当前 Token。
func (c *Client) GetDeviceToken(ctx context.Context, sn string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	val, err := c.rdb.HGet(ctx, c.k(keyAuthHash), sn).Result()
	if err == nil {
		return val, nil
	}
	if err != redis.Nil {
		return "", err
	}
	val, err = c.rdb.Get(ctx, c.k(keyTokenPrefix+sn)).Result()
	if err == redis.Nil {
		return "", nil
	}
	return val, err
}

// SaveDeviceMeta 保存设备激活元信息。
func (c *Client) SaveDeviceMeta(ctx context.Context, meta *DeviceMeta) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	b, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, c.k(keyMetaPrefix+meta.SN), b, 0).Err()
}

// SetOnline 标记设备在线。
func (c *Client) SetOnline(ctx context.Context, sn string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return c.rdb.Set(ctx, c.k(keyOnlinePrefix+sn), "1", c.onlineTTL).Err()
}

// RefreshOnline 刷新在线 TTL。
func (c *Client) RefreshOnline(ctx context.Context, sn string) error {
	return c.SetOnline(ctx, sn)
}

// SetOffline 删除在线标记。
func (c *Client) SetOffline(ctx context.Context, sn string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return c.rdb.Del(ctx, c.k(keyOnlinePrefix+sn), c.k(keyConnPrefix+sn)).Err()
}

// SetOfflineIfConn 仅当 conn 映射仍是指定 connID 时下线，避免踢旧连时误清新连接。
func (c *Client) SetOfflineIfConn(ctx context.Context, sn, connID string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	key := c.k(keyConnPrefix + sn)
	cur, err := c.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return c.rdb.Del(ctx, c.k(keyOnlinePrefix+sn)).Err()
	}
	if err != nil {
		return err
	}
	if cur != connID {
		return nil
	}
	return c.rdb.Del(ctx, c.k(keyOnlinePrefix+sn), key).Err()
}

// ListOnlineSNs 扫描当前仍带在线 TTL 的设备序列号。
func (c *Client) ListOnlineSNs(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	pattern := c.k(keyOnlinePrefix + "*")
	prefix := c.k(keyOnlinePrefix)
	var sns []string
	var cursor uint64
	for {
		keys, next, err := c.rdb.Scan(ctx, cursor, pattern, 200).Result()
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			sn := strings.TrimPrefix(k, prefix)
			if sn != "" && sn != k {
				sns = append(sns, sn)
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return sns, nil
}

// SetConnMapping 写入连接映射缓存。
func (c *Client) SetConnMapping(ctx context.Context, sn, connID string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return c.rdb.Set(ctx, c.k(keyConnPrefix+sn), connID, c.connTTL).Err()
}

// RefreshConnMapping 刷新连接映射 TTL（心跳时调用）。
func (c *Client) RefreshConnMapping(ctx context.Context, sn, connID string) error {
	return c.SetConnMapping(ctx, sn, connID)
}

// PushOfflineCmd 离线下行指令入队。
func (c *Client) PushOfflineCmd(ctx context.Context, sn, cmdJSON string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return c.rdb.RPush(ctx, c.k(keyOfflineCmdPrefix+sn), cmdJSON).Err()
}

// GetOfflineCmdLen 返回当前离线指令队列长度（仅用于接口响应，非强一致）。
func (c *Client) GetOfflineCmdLen(ctx context.Context, sn string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	n, err := c.rdb.LLen(ctx, c.k(keyOfflineCmdPrefix+sn)).Result()
	if err == redis.Nil {
		return 0, nil
	}
	return n, err
}

// PopAllOfflineCmds 拉取并清空离线指令。
func (c *Client) PopAllOfflineCmds(ctx context.Context, sn string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	key := c.k(keyOfflineCmdPrefix + sn)
	pipe := c.rdb.TxPipeline()
	lrange := pipe.LRange(ctx, key, 0, -1)
	pipe.Del(ctx, key)
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, err
	}
	return lrange.Val(), nil
}

// LPushOfflineCmds rollback：把一批未下发成功的离线指令放回队列左侧（下次 Pop 时顺序保持不变）。
// args 顺序：剩余 cmds[0] 是最早那条失败的，LPush 到队列最前面；
// 所以要反转之后再 LPush，或者直接用 LPush args...，Redis 会按参数序依次入列左（保证顺序还原）。
func (c *Client) LPushOfflineCmds(ctx context.Context, sn string, cmds ...string) error {
	if len(cmds) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	iface := make([]interface{}, len(cmds))
	for i := range cmds {
		iface[i] = cmds[i]
	}
	return c.rdb.LPush(ctx, c.k(keyOfflineCmdPrefix+sn), iface...).Err()
}

// GetString 读取字符串缓存（自动加前缀）。
func (c *Client) GetString(ctx context.Context, key string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	val, err := c.rdb.Get(ctx, c.k(key)).Result()
	if err == redis.Nil {
		return "", nil
	}
	return val, err
}

// SetString 写入字符串缓存（自动加前缀）。
func (c *Client) SetString(ctx context.Context, key, val string, ttl time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return c.rdb.Set(ctx, c.k(key), val, ttl).Err()
}

// IsOnline 查询是否在线。
func (c *Client) IsOnline(ctx context.Context, sn string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	n, err := c.rdb.Exists(ctx, c.k(keyOnlinePrefix+sn)).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// GetWifiFingerprint 按 WiFi MAC 指纹取缓存坐标。
func (c *Client) GetWifiFingerprint(ctx context.Context, fp string) (*CachedLocation, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	val, err := c.rdb.Get(ctx, c.k(keyWifiFPPrefix+fp)).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var loc CachedLocation
	if err := json.Unmarshal([]byte(val), &loc); err != nil {
		return nil, err
	}
	return &loc, nil
}

// SetWifiFingerprint 写入 WiFi 指纹坐标。
func (c *Client) SetWifiFingerprint(ctx context.Context, fp string, loc *CachedLocation, ttl time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	b, err := json.Marshal(loc)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, c.k(keyWifiFPPrefix+fp), b, ttl).Err()
}

// SaveLastLocation 记录设备最近一次解析坐标，并同步写入状态快照（避免查询时 last_loc 与 status 两套数据打架）。
func (c *Client) SaveLastLocation(ctx context.Context, sn string, loc *CachedLocation) error {
	if sn == "" || loc == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	b, err := json.Marshal(loc)
	if err != nil {
		return err
	}
	if err := c.rdb.Set(ctx, c.k(keyLastLocPrefix+sn), b, 0).Err(); err != nil {
		return err
	}
	return c.UpdateDeviceStatus(ctx, sn, &DeviceStatusPatch{
		SetLastLoc: true,
		LastLocLat: loc.Lat,
		LastLocLng: loc.Lng,
		LastLocAcc: loc.Accuracy,
		LastLocTS:  loc.Updated,
	})
}

// GetDeviceStatus 获取设备统一状态：从 status key 读取并补齐 online/conn、meta、last_loc。
func (c *Client) GetDeviceStatus(ctx context.Context, sn string) (*DeviceStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if sn == "" {
		return nil, fmt.Errorf("sn empty")
	}

	pipe := c.rdb.TxPipeline()
	statusCmd := pipe.Get(ctx, c.k(keyStatusPrefix+sn))
	onlineCmd := pipe.Exists(ctx, c.k(keyOnlinePrefix+sn))
	connCmd := pipe.Get(ctx, c.k(keyConnPrefix+sn))
	metaCmd := pipe.Get(ctx, c.k(keyMetaPrefix+sn))
	lastLocCmd := pipe.Get(ctx, c.k(keyLastLocPrefix+sn))
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, err
	}

	ds := &DeviceStatus{SN: sn}

	if val, err := statusCmd.Result(); err == nil && val != "" {
		_ = json.Unmarshal([]byte(val), ds)
	}
	ds.SN = sn

	if n, _ := onlineCmd.Result(); n > 0 {
		ds.Online = true
	}

	if connID, err := connCmd.Result(); err == nil {
		ds.ConnID = connID
	}

	if val, err := metaCmd.Result(); err == nil && val != "" {
		var meta DeviceMeta
		if json.Unmarshal([]byte(val), &meta) == nil {
			if ds.FWVer == "" {
				ds.FWVer = meta.FWVer
			}
			if ds.MAC == "" {
				ds.MAC = meta.MAC
			}
			if ds.ActivatedAt == 0 {
				ds.ActivatedAt = meta.CreatedAt
			}
		}
	}

	// last_loc 仅在「比 status 快照更新」或「快照尚无坐标」时覆盖，避免旧指纹坐标盖掉设备刚上报的坐标
	if val, err := lastLocCmd.Result(); err == nil && val != "" {
		var loc CachedLocation
		if json.Unmarshal([]byte(val), &loc) == nil && (loc.Lat != 0 || loc.Lng != 0) {
			statusHasLoc := ds.LastLocLat != 0 || ds.LastLocLng != 0
			if !statusHasLoc || loc.Updated >= ds.LastLocTS {
				ds.LastLocLat = loc.Lat
				ds.LastLocLng = loc.Lng
				ds.LastLocAcc = loc.Accuracy
				ds.LastLocTS = loc.Updated
			}
		}
	}

	return ds, nil
}

// GetDeviceStatusView 获取设备状态 API 视图（含离线指令队列长度、bound 由 pid+breed 计算）。
func (c *Client) GetDeviceStatusView(ctx context.Context, sn string) (*DeviceStatusView, error) {
	ds, err := c.GetDeviceStatus(ctx, sn)
	if err != nil {
		return nil, err
	}
	pending, _ := c.GetOfflineCmdLen(ctx, sn)
	return ds.ToView(pending), nil
}

// GetDeviceStatusViewWithToken 管理端状态：在视图上附带当前 Redis 中的 device token。
func (c *Client) GetDeviceStatusViewWithToken(ctx context.Context, sn string) (*DeviceStatusView, error) {
	v, err := c.GetDeviceStatusView(ctx, sn)
	if err != nil {
		return nil, err
	}
	if v == nil {
		v = &DeviceStatusView{SN: sn}
	}
	tok, err := c.GetDeviceToken(ctx, sn)
	if err != nil {
		return v, nil // 状态仍返回；token 空
	}
	v.Token = tok
	return v, nil
}

// UpdateDeviceStatus 增量更新设备状态字段（按 patch.SetXXX 开关决定是否覆盖，不再靠零值判断，0/false/空字符串都能正确显式落库）。
// 使用 WATCH + 重试，避免并发 ping/status 的 GET-修改-SET 互相覆盖（否则 auto_record 等配置会被冲掉）。
func (c *Client) UpdateDeviceStatus(ctx context.Context, sn string, patch *DeviceStatusPatch) error {
	if sn == "" || patch == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	key := c.k(keyStatusPrefix + sn)
	var lastErr error
	for attempt := 0; attempt < 8; attempt++ {
		lastErr = c.rdb.Watch(ctx, func(tx *redis.Tx) error {
			val, err := tx.Get(ctx, key).Result()
			ds := &DeviceStatus{SN: sn}
			if err == nil && val != "" {
				_ = json.Unmarshal([]byte(val), ds)
			} else if err != nil && err != redis.Nil {
				return err
			}
			ds.SN = sn
			applyDeviceStatusPatch(ds, patch)
			b, err := json.Marshal(ds)
			if err != nil {
				return err
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, key, b, 0)
				return nil
			})
			return err
		}, key)
		if lastErr == nil {
			return nil
		}
		if lastErr == redis.TxFailedErr {
			continue
		}
		return lastErr
	}
	return fmt.Errorf("update device status conflict after retries sn=%s: %w", sn, lastErr)
}

func applyDeviceStatusPatch(ds *DeviceStatus, patch *DeviceStatusPatch) {
	if ds == nil || patch == nil {
		return
	}
	if patch.SetFWVer {
		ds.FWVer = patch.FWVer
	}
	if patch.SetMAC {
		ds.MAC = patch.MAC
	}
	if patch.SetConnID {
		ds.ConnID = patch.ConnID
	}
	if patch.SetConnectedAt {
		ds.ConnectedAt = patch.ConnectedAt
	}
	if patch.SetLastSeenAt {
		ds.LastSeenAt = patch.LastSeenAt
	}
	if patch.SetLastReportAt {
		ds.LastReportAt = patch.LastReportAt
	}
	if patch.SetBattery {
		ds.Battery = patch.Battery
	}
	if patch.SetCharge {
		ds.Charge = patch.Charge
	}
	if patch.SetRSSI {
		ds.RSSI = patch.RSSI
	}
	if patch.SetBound {
		ds.Bound = patch.Bound
	}
	if patch.SetPID {
		ds.PID = patch.PID
	}
	if patch.SetBreed {
		ds.Breed = patch.Breed
	}
	if patch.SetDB {
		ds.DB = patch.DB
	}
	if patch.SetLastEvent {
		ds.LastEvent = patch.LastEvent
	}
	if patch.SetLastState {
		ds.LastState = patch.LastState
	}
	if patch.SetMotion {
		ds.Motion = patch.Motion
	}
	if patch.SetTier {
		ds.Tier = patch.Tier
	}
	if patch.SetIMU {
		ds.IMU = patch.IMU
	}
	if patch.SetLastLoc {
		ds.LastLocLat = patch.LastLocLat
		ds.LastLocLng = patch.LastLocLng
		ds.LastLocAcc = patch.LastLocAcc
		ds.LastLocTS = patch.LastLocTS
	}
	if patch.SetLocExtra {
		ds.LocSpeed = patch.LocSpeed
		ds.LocType = patch.LocType
	}
	if patch.SetCh {
		ds.Ch = patch.Ch
	}
	if patch.SetProtoVer {
		ds.ProtoVer = patch.ProtoVer
	}
	if patch.SetVolume {
		v := patch.Volume
		ds.Volume = &v
	}
	if patch.SetSensitivity {
		v := patch.Sensitivity
		ds.Sensitivity = &v
	}
	if patch.SetTranslateEnabled {
		v := patch.TranslateEnabled
		ds.TranslateEnabled = &v
	}
	if patch.SetAutoRecord {
		v := patch.AutoRecord
		ds.AutoRecord = &v
	}
	if patch.SetSleepEnabled {
		v := patch.SleepEnabled
		ds.SleepEnabled = &v
	}
	if patch.SetSleepWindow {
		ds.SleepWindow = patch.SleepWindow
	}
	if patch.SetLocIntervalMs {
		v := patch.LocIntervalMs
		ds.LocIntervalMs = &v
	}
	if patch.SetConfigSource {
		ds.ConfigSource = patch.ConfigSource
	}
	if patch.SetConfigUpdatedAt {
		ds.ConfigUpdatedAt = patch.ConfigUpdatedAt
	}
	if patch.SetActivatedAt && ds.ActivatedAt == 0 {
		ds.ActivatedAt = patch.ActivatedAt
	}
	if patch.SetOfflineReason {
		ds.OfflineReason = patch.OfflineReason
	}
}

// SaveDeviceStatus 全量写入设备状态。
func (c *Client) SaveDeviceStatus(ctx context.Context, ds *DeviceStatus) error {
	if ds == nil || ds.SN == "" {
		return fmt.Errorf("device status sn empty")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	b, err := json.Marshal(ds)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, c.k(keyStatusPrefix+ds.SN), b, 0).Err()
}
