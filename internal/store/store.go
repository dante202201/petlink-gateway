// Package store 持久化设备上行音频，供 App 等业务端查询。
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"go.uber.org/zap"
	_ "modernc.org/sqlite"

	"pet-translate-gateway/config"
	"pet-translate-gateway/internal/audio"
	"pet-translate-gateway/internal/model"
)

var storeLog, _ = zap.NewProduction()

// SetLogger 覆盖包级 logger（主进程初始化后注入），避免 nil panic。
func SetLogger(log *zap.Logger) {
	if log != nil {
		storeLog = log
	}
}

// AudioRecord 入库后的音频分片（含主键，供 App 列表/详情）。
type AudioRecord struct {
	ID         int64           `json:"id"`
	SN         string          `json:"sn"`
	Codec      string          `json:"codec"`
	Seq        int             `json:"seq"`
	Total      int             `json:"total"`
	End        bool            `json:"end"`
	Retransmit bool            `json:"retransmit"`
	Pet        string          `json:"pet,omitempty"`
	DB         float64         `json:"db"`
	Bat        int             `json:"bat"`
	RSSI       int             `json:"rssi"`
	DeviceTS   int64           `json:"device_ts"`
	ReceivedAt int64           `json:"received_at"`
	CRC        uint32          `json:"crc,omitempty"`
	PID        string          `json:"pid,omitempty"`
	OpusLen    int             `json:"opus_len"`
	OpusData   []byte          `json:"-"`
	OpusBase64 string          `json:"opus_base64,omitempty"`
	LocType    string          `json:"loc_type,omitempty"`
	LocLat     float64         `json:"loc_lat,omitempty"`
	LocLng     float64         `json:"loc_lng,omitempty"`
	LocAcc     float64         `json:"loc_acc,omitempty"`
	LocSpeed   float64         `json:"loc_speed,omitempty"`
	LocMacs    json.RawMessage `json:"loc_macs,omitempty"`
	LocTS      int64           `json:"loc_ts,omitempty"`
	IMU        json.RawMessage `json:"imu,omitempty"`    // [{ts,acc,gyro}]，已 decode；无数据则省略
	Motion     string          `json:"motion,omitempty"` // walk|run|stand|prone|lie
}

// Store 数据库访问封装。
type Store struct {
	db     *sql.DB
	driver string
}

// New 打开数据库并自动建表。enabled=false 时返回 nil,nil。
func New(cfg config.DatabaseConfig) (*Store, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	driver := strings.ToLower(strings.TrimSpace(cfg.Driver))
	if driver == "" {
		driver = "sqlite"
	}

	var db *sql.DB
	var err error
	switch driver {
	case "sqlite":
		if err := os.MkdirAll(filepath.Dir(cfg.DSN), 0o755); err != nil {
			return nil, fmt.Errorf("创建数据目录失败: %w", err)
		}
		db, err = sql.Open("sqlite", cfg.DSN)
		if err != nil {
			return nil, fmt.Errorf("打开 sqlite 失败: %w", err)
		}
		db.SetMaxOpenConns(1)
		db.SetConnMaxLifetime(time.Hour)
	case "mysql":
		db, err = sql.Open("mysql", cfg.DSN)
		if err != nil {
			return nil, fmt.Errorf("打开 mysql 失败: %w", err)
		}
		db.SetMaxOpenConns(25)
		db.SetMaxIdleConns(5)
		db.SetConnMaxLifetime(time.Hour)
	default:
		return nil, fmt.Errorf("unsupported driver: %s（支持 sqlite / mysql）", driver)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%s ping 失败: %w", driver, err)
	}

	s := &Store{db: db, driver: driver}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭连接。
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	stmts := sqliteMigrateStmts
	if s.driver == "mysql" {
		stmts = mysqlMigrateStmts
	}
	for _, ddl := range stmts {
		if _, err := s.db.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("建表失败: %w", err)
		}
	}
	alters := []string{
		`ALTER TABLE audio_chunk ADD COLUMN pet_profile_id TEXT`,
		`ALTER TABLE audio_chunk ADD COLUMN breed_code TEXT`,
		`ALTER TABLE audio_chunk ADD COLUMN imu_json TEXT`,
		`ALTER TABLE audio_chunk ADD COLUMN motion TEXT`,
		`ALTER TABLE audio_chunk ADD COLUMN audio_kind INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE audio_chunk ADD COLUMN chunk_ts INTEGER NOT NULL DEFAULT 0`,
	}
	if s.driver == "mysql" {
		alters = []string{
			`ALTER TABLE audio_chunk ADD COLUMN pet_profile_id VARCHAR(128)`,
			`ALTER TABLE audio_chunk ADD COLUMN breed_code VARCHAR(64)`,
			`ALTER TABLE audio_chunk ADD COLUMN imu_json TEXT`,
			`ALTER TABLE audio_chunk ADD COLUMN motion VARCHAR(16)`,
			`ALTER TABLE audio_chunk ADD COLUMN audio_kind TINYINT NOT NULL DEFAULT 0`,
			`ALTER TABLE audio_chunk ADD COLUMN chunk_ts BIGINT NOT NULL DEFAULT 0`,
			`ALTER TABLE a_emb_translate ADD COLUMN emb_id INT NULL`,
			`ALTER TABLE a_emb_translate ADD COLUMN emb_sn VARCHAR(100) NULL`,
			`ALTER TABLE a_emb_translate ADD COLUMN pid VARCHAR(128) NULL`,
			`ALTER TABLE a_emb_translate ADD COLUMN status VARCHAR(16) NULL`,
			`ALTER TABLE a_emb_translate ADD COLUMN error_msg VARCHAR(512) NULL`,
			`ALTER TABLE a_emb_translate ADD COLUMN device_ts BIGINT NULL`,
			`ALTER TABLE a_emb_translate ADD COLUMN result_says VARCHAR(512) NULL`,
			`ALTER TABLE a_emb_translate ADD COLUMN createtime INT NULL`,
			`ALTER TABLE a_emb_translate ADD COLUMN resulttime INT NULL`,
			`ALTER TABLE a_emb_translate ADD COLUMN device_opus MEDIUMBLOB NULL`,
			`ALTER TABLE a_emb_translate ADD COLUMN device_opus_frames INT NULL`,
			`ALTER TABLE a_emb_translate ADD COLUMN audio_kind TINYINT NOT NULL DEFAULT 0`,
			`ALTER TABLE a_emb_translate ADD COLUMN origin_raw_url VARCHAR(512) NULL`,
			`ALTER TABLE a_emb_translate ADD COLUMN origin_raw_frames INT NULL`,
			`ALTER TABLE a_emb_translate ADD KEY emb_sn_id (emb_sn, id)`,
			`ALTER TABLE a_emb_translate ADD UNIQUE KEY uk_emb_sn_device_ts (emb_sn, device_ts)`,
		}
	} else {
		alters = append(alters,
			`ALTER TABLE a_emb_translate ADD COLUMN user_id INTEGER`,
			`ALTER TABLE a_emb_translate ADD COLUMN emb_id INTEGER`,
			`ALTER TABLE a_emb_translate ADD COLUMN emb_sn TEXT`,
			`ALTER TABLE a_emb_translate ADD COLUMN pid TEXT`,
			`ALTER TABLE a_emb_translate ADD COLUMN status TEXT`,
			`ALTER TABLE a_emb_translate ADD COLUMN error_msg TEXT`,
			`ALTER TABLE a_emb_translate ADD COLUMN device_ts INTEGER`,
			`ALTER TABLE a_emb_translate ADD COLUMN result_says TEXT`,
			`ALTER TABLE a_emb_translate ADD COLUMN createtime INTEGER`,
			`ALTER TABLE a_emb_translate ADD COLUMN resulttime INTEGER`,
			`ALTER TABLE a_emb_translate ADD COLUMN device_opus BLOB`,
			`ALTER TABLE a_emb_translate ADD COLUMN device_opus_frames INTEGER`,
			`ALTER TABLE a_emb_translate ADD COLUMN audio_kind INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE a_emb_translate ADD COLUMN origin_raw_url TEXT`,
			`ALTER TABLE a_emb_translate ADD COLUMN origin_raw_frames INTEGER`,
			`CREATE UNIQUE INDEX IF NOT EXISTS uk_emb_sn_device_ts ON a_emb_translate(emb_sn, device_ts)`,
		)
	}
	for _, alter := range alters {
		if _, err := s.db.ExecContext(ctx, alter); err != nil {
			msg := strings.ToLower(err.Error())
			if strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists") || strings.Contains(msg, "duplicate key") {
				continue
			}
		}
	}
	return nil
}

var sqliteMigrateStmts = []string{
	`CREATE TABLE IF NOT EXISTS audio_chunk (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  sn TEXT NOT NULL,
  codec TEXT NOT NULL DEFAULT 'opus',
  seq INTEGER NOT NULL DEFAULT 0,
  total INTEGER NOT NULL DEFAULT 0,
  is_end INTEGER NOT NULL DEFAULT 0,
  retransmit INTEGER NOT NULL DEFAULT 0,
  pet TEXT,
  db_level REAL NOT NULL DEFAULT 0,
  bat INTEGER NOT NULL DEFAULT 0,
  rssi INTEGER NOT NULL DEFAULT 0,
  device_ts INTEGER NOT NULL DEFAULT 0,
  received_at INTEGER NOT NULL DEFAULT 0,
  crc INTEGER NOT NULL DEFAULT 0,
  opus BLOB NOT NULL,
  loc_type TEXT,
  loc_lat REAL,
  loc_lng REAL,
  loc_acc REAL,
  loc_speed REAL,
  loc_macs TEXT,
  loc_ts INTEGER,
  pet_profile_id TEXT,
  breed_code TEXT,
  imu_json TEXT,
  motion TEXT,
  audio_kind INTEGER NOT NULL DEFAULT 0,
  chunk_ts INTEGER NOT NULL DEFAULT 0
)`,
	`CREATE INDEX IF NOT EXISTS idx_audio_sn_recv ON audio_chunk(sn, received_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_audio_sn_devts ON audio_chunk(sn, device_ts DESC)`,
	`CREATE TABLE IF NOT EXISTS a_emb_sn (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  emb_sn TEXT NOT NULL UNIQUE,
  keysecret TEXT,
  bind_user_id INTEGER,
  bind_pet_id TEXT,
  status INTEGER DEFAULT 1,
  bind_at INTEGER,
  created_at INTEGER
)`,
	`CREATE TABLE IF NOT EXISTS a_emb_translate (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER,
  emb_id INTEGER,
  emb_sn TEXT,
  origin_audio_url TEXT,
  audio_url TEXT,
  translate_text TEXT,
  origin_audio_duration TEXT,
  createtime INTEGER,
  resulttime INTEGER,
  pet_type TEXT,
  ai_result TEXT,
  feedback_type INTEGER,
  feedback_emotion_type INTEGER,
  pid TEXT,
  status TEXT NOT NULL DEFAULT 'pending',
  error_msg TEXT,
  device_ts INTEGER NOT NULL DEFAULT 0,
  result_says TEXT,
  device_opus BLOB,
  device_opus_frames INTEGER,
  audio_kind INTEGER NOT NULL DEFAULT 0,
  origin_raw_url TEXT,
  origin_raw_frames INTEGER,
  UNIQUE(emb_sn, device_ts)
)`,
	`CREATE INDEX IF NOT EXISTS idx_emb_translate_sn ON a_emb_translate(emb_sn, id DESC)`,
	`CREATE TABLE IF NOT EXISTS a_emb_firmware (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  version TEXT NOT NULL DEFAULT '',
  url TEXT NOT NULL DEFAULT '',
  size INTEGER NOT NULL DEFAULT 0,
  md5 TEXT NOT NULL DEFAULT '',
  force_flag INTEGER NOT NULL DEFAULT 0,
  changelog TEXT NOT NULL DEFAULT '',
  filename TEXT NOT NULL DEFAULT '',
  is_current INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL DEFAULT 0
)`,
	`CREATE INDEX IF NOT EXISTS idx_firmware_current ON a_emb_firmware(is_current, id DESC)`,
	`CREATE TABLE IF NOT EXISTS a_emb_ota_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  sn TEXT NOT NULL,
  from_fw TEXT NOT NULL DEFAULT '',
  target_fw TEXT NOT NULL DEFAULT '',
  url TEXT NOT NULL DEFAULT '',
  force_flag INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'pushed',
  fail_reason TEXT NOT NULL DEFAULT '',
  disconnected INTEGER NOT NULL DEFAULT 0,
  reconnected INTEGER NOT NULL DEFAULT 0,
  result_fw TEXT NOT NULL DEFAULT '',
  pushed_at INTEGER NOT NULL DEFAULT 0,
  ack_at INTEGER NOT NULL DEFAULT 0,
  disconnect_at INTEGER NOT NULL DEFAULT 0,
  reconnect_at INTEGER NOT NULL DEFAULT 0,
  boot_at INTEGER NOT NULL DEFAULT 0,
  result_at INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL DEFAULT 0
)`,
	`CREATE INDEX IF NOT EXISTS idx_ota_log_sn ON a_emb_ota_log(sn, id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_ota_log_status ON a_emb_ota_log(status, id DESC)`,
}

var mysqlMigrateStmts = []string{
	`CREATE TABLE IF NOT EXISTS audio_chunk (
  id BIGINT NOT NULL AUTO_INCREMENT,
  sn VARCHAR(64) NOT NULL,
  codec VARCHAR(32) NOT NULL DEFAULT 'opus',
  seq INT NOT NULL DEFAULT 0,
  total INT NOT NULL DEFAULT 0,
  is_end TINYINT NOT NULL DEFAULT 0,
  retransmit TINYINT NOT NULL DEFAULT 0,
  pet VARCHAR(32),
  db_level DOUBLE NOT NULL DEFAULT 0,
  bat INT NOT NULL DEFAULT 0,
  rssi INT NOT NULL DEFAULT 0,
  device_ts BIGINT NOT NULL DEFAULT 0,
  received_at BIGINT NOT NULL DEFAULT 0,
  crc BIGINT NOT NULL DEFAULT 0,
  opus MEDIUMBLOB NOT NULL,
  loc_type VARCHAR(16),
  loc_lat DOUBLE,
  loc_lng DOUBLE,
  loc_acc DOUBLE,
  loc_speed DOUBLE,
  loc_macs TEXT,
  loc_ts BIGINT,
  pet_profile_id VARCHAR(128),
  breed_code VARCHAR(64),
  imu_json TEXT,
  motion VARCHAR(16),
  audio_kind TINYINT NOT NULL DEFAULT 0,
  chunk_ts BIGINT NOT NULL DEFAULT 0 COMMENT '本帧绝对 Unix 微秒（归一后）',
  PRIMARY KEY (id),
  KEY idx_audio_sn_recv (sn, received_at),
  KEY idx_audio_sn_devts (sn, device_ts)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS a_emb_sn (
  id INT NOT NULL AUTO_INCREMENT,
  emb_sn VARCHAR(100) NOT NULL COMMENT '设备序列号',
  keysecret VARCHAR(200) DEFAULT NULL,
  bind_user_id INT DEFAULT NULL,
  bind_pet_id VARCHAR(128) DEFAULT NULL,
  status TINYINT DEFAULT 1,
  bind_at INT DEFAULT NULL,
  created_at INT DEFAULT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_emb_sn (emb_sn),
  KEY idx_bind_user (bind_user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS a_emb_translate (
  id INT NOT NULL AUTO_INCREMENT,
  user_id INT DEFAULT NULL,
  emb_id INT DEFAULT NULL,
  emb_sn VARCHAR(100) DEFAULT NULL,
  origin_audio_url VARCHAR(512) DEFAULT NULL,
  audio_url VARCHAR(512) DEFAULT NULL,
  translate_text TEXT,
  origin_audio_duration VARCHAR(10) DEFAULT NULL,
  createtime INT DEFAULT NULL,
  resulttime INT DEFAULT NULL,
  pet_type VARCHAR(50) DEFAULT NULL,
  ai_result TEXT,
  feedback_type TINYINT DEFAULT NULL,
  feedback_emotion_type SMALLINT DEFAULT NULL,
  pid VARCHAR(128) DEFAULT NULL,
  status VARCHAR(16) NOT NULL DEFAULT 'pending',
  error_msg VARCHAR(512) DEFAULT NULL,
  device_ts BIGINT NOT NULL DEFAULT 0,
  result_says VARCHAR(512) DEFAULT NULL,
  device_opus MEDIUMBLOB NULL,
  device_opus_frames INT DEFAULT NULL,
  audio_kind TINYINT NOT NULL DEFAULT 0 COMMENT '0=事件流 1=录音文件',
  origin_raw_url VARCHAR(512) DEFAULT NULL COMMENT '事件流原帧zip OSS URL',
  origin_raw_frames INT DEFAULT NULL COMMENT '原帧zip内真实帧数',
  PRIMARY KEY (id),
  KEY user_id (user_id),
  KEY emb_id (emb_id),
  KEY emb_sn_id (emb_sn, id),
  UNIQUE KEY uk_emb_sn_device_ts (emb_sn, device_ts)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS a_emb_firmware (
  id INT NOT NULL AUTO_INCREMENT,
  version VARCHAR(64) NOT NULL DEFAULT '' COMMENT '固件版本号',
  url VARCHAR(512) NOT NULL DEFAULT '' COMMENT 'OSS/CDN 直链',
  size BIGINT NOT NULL DEFAULT 0 COMMENT '字节数',
  md5 VARCHAR(32) NOT NULL DEFAULT '' COMMENT '整包 MD5',
  force_flag TINYINT NOT NULL DEFAULT 0 COMMENT '1=强制升级',
  changelog VARCHAR(1024) NOT NULL DEFAULT '' COMMENT '更新说明',
  filename VARCHAR(255) NOT NULL DEFAULT '' COMMENT '原始文件名',
  is_current TINYINT NOT NULL DEFAULT 0 COMMENT '1=当前对设备生效',
  created_at INT NOT NULL DEFAULT 0 COMMENT 'Unix 秒',
  PRIMARY KEY (id),
  KEY idx_firmware_current (is_current, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='固件 OTA 包历史'`,
	`CREATE TABLE IF NOT EXISTS a_emb_ota_log (
  id BIGINT NOT NULL AUTO_INCREMENT,
  sn VARCHAR(64) NOT NULL COMMENT '设备序列号',
  from_fw VARCHAR(64) NOT NULL DEFAULT '' COMMENT '下发前设备当前版本',
  target_fw VARCHAR(64) NOT NULL DEFAULT '' COMMENT '目标版本',
  url VARCHAR(512) NOT NULL DEFAULT '' COMMENT '固件 URL',
  force_flag TINYINT NOT NULL DEFAULT 0,
  status VARCHAR(16) NOT NULL DEFAULT 'pushed' COMMENT 'pushed/confirming/success/fail',
  fail_reason VARCHAR(64) NOT NULL DEFAULT '',
  disconnected TINYINT NOT NULL DEFAULT 0 COMMENT 'ack 后是否断过线',
  reconnected TINYINT NOT NULL DEFAULT 0 COMMENT '是否已重连',
  result_fw VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'boot ping 上报的 fw',
  pushed_at BIGINT NOT NULL DEFAULT 0 COMMENT 'Unix 毫秒',
  ack_at BIGINT NOT NULL DEFAULT 0,
  disconnect_at BIGINT NOT NULL DEFAULT 0,
  reconnect_at BIGINT NOT NULL DEFAULT 0,
  boot_at BIGINT NOT NULL DEFAULT 0,
  result_at BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL DEFAULT 0,
  updated_at BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (id),
  KEY idx_ota_log_sn (sn, id),
  KEY idx_ota_log_status (status, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='设备 OTA 升级判定日志'`,
}

// SaveAudio 写入一条音频分片，返回主键 id。
func (s *Store) SaveAudio(ctx context.Context, msg *model.AudioMessage, sessionDeviceTS int64) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	// 段级 sessionDeviceTS：必须全段每块 chunk 都写同一个值（取 dc.audioBaseTS 段首绝对微秒），
	// 否则 LoadSessionOpus WHERE sn+device_ts 只能取到最后一块。
	// 兜底：极端情况下 audioBaseTS==0 降级退 msg.TS（块自己的绝对微秒），避免全段都写 0。
	if sessionDeviceTS <= 0 {
		sessionDeviceTS = msg.TS
	}

	var macs []byte
	if len(msg.LocMacs) > 0 {
		b, err := json.Marshal(msg.LocMacs)
		if err != nil {
			return 0, err
		}
		macs = b
	}
	var imuJSON []byte
	if len(msg.IMU) > 0 {
		b, err := json.Marshal(msg.IMU)
		if err != nil {
			return 0, err
		}
		imuJSON = b
	}

	end := 0
	if msg.End {
		end = 1
	}
	retrans := 0
	if msg.Retransmit {
		retrans = 1
	}

	storeLog.Debug("SaveAudio chunk",
		zap.String("sn", msg.SN),
		zap.Int("seq", msg.Seq),
		zap.Int("total", msg.Total),
		zap.Bool("end", msg.End),
		zap.Int("kind", msg.Kind),
		zap.Int64("session_device_ts", sessionDeviceTS),
		zap.Int64("chunk_abs_ts", msg.TS),
		zap.Int("opus_len", len(msg.OpusData)),
		zap.Int("bat", msg.Bat),
		zap.Bool("charge", msg.Charge),
		zap.String("pet", msg.Pet),
		zap.String("breed_code", msg.Breed),
	)

	res, err := s.db.ExecContext(ctx, `
INSERT INTO audio_chunk (
  sn, codec, seq, total, is_end, retransmit, pet, db_level, bat, rssi,
  device_ts, received_at, crc, opus, loc_type, loc_lat, loc_lng, loc_acc, loc_speed, loc_macs, loc_ts,
  pet_profile_id, breed_code, imu_json, motion, audio_kind, chunk_ts
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.SN, msg.Codec, msg.Seq, msg.Total, end, retrans, msg.Pet, msg.DB, msg.Bat, msg.RSSI,
		sessionDeviceTS, msg.ReceivedAt, msg.CRC, msg.OpusData,
		msg.LocType, msg.LocLat, msg.LocLng, msg.LocAcc, msg.LocSpeed, stringOrNull(macs), msg.LocTS,
		pidOrNull(msg.PID), nullIfEmpty(msg.Breed), stringOrNull(imuJSON), nullIfEmpty(msg.Motion), msg.Kind, msg.TS,
	)
	if err != nil {
		storeLog.Error("保存音频分片失败",
			zap.String("sn", msg.SN),
			zap.Int("seq", msg.Seq),
			zap.Int("total", msg.Total),
			zap.Int64("session_device_ts", sessionDeviceTS),
			zap.Int64("chunk_abs_ts", msg.TS),
			zap.Error(err),
		)
		return 0, err
	}
	return res.LastInsertId()
}

func stringOrNull(b []byte) interface{} {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func pidOrNull(pid string) interface{} {
	if model.IsUnboundPetID(pid) {
		return nil
	}
	return pid
}

// ListAudio 按设备列表（不含 opus 二进制，仅长度）。
// sinceID>0 时按 id 升序取新数据（App 轮询）；否则按 id 倒序 + offset 翻页。
func (s *Store) ListAudio(ctx context.Context, sn string, limit, offset int, sinceID int64) ([]AudioRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("database disabled")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	q := `
SELECT id, sn, codec, seq, total, is_end, retransmit, IFNULL(pet,''), db_level, bat, rssi,
       device_ts, received_at, crc, LENGTH(opus),
       IFNULL(loc_type,''), IFNULL(loc_lat,0), IFNULL(loc_lng,0), IFNULL(loc_acc,0), IFNULL(loc_speed,0),
       loc_macs, IFNULL(loc_ts,0), IFNULL(pet_profile_id,''), IFNULL(imu_json,''), IFNULL(motion,'')
FROM audio_chunk
WHERE (? = '' OR sn = ?)`
	args := []interface{}{sn, sn}
	if sinceID > 0 {
		q += ` AND id > ? ORDER BY id ASC LIMIT ?`
		args = append(args, sinceID, limit)
	} else {
		q += ` ORDER BY id DESC LIMIT ? OFFSET ?`
		args = append(args, limit, offset)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]AudioRecord, 0, limit)
	for rows.Next() {
		var r AudioRecord
		var end, retrans int
		var macs sql.NullString
		var imuStr string
		if err := rows.Scan(
			&r.ID, &r.SN, &r.Codec, &r.Seq, &r.Total, &end, &retrans, &r.Pet, &r.DB, &r.Bat, &r.RSSI,
			&r.DeviceTS, &r.ReceivedAt, &r.CRC, &r.OpusLen,
			&r.LocType, &r.LocLat, &r.LocLng, &r.LocAcc, &r.LocSpeed, &macs, &r.LocTS, &r.PID, &imuStr, &r.Motion,
		); err != nil {
			return nil, err
		}
		r.End = end == 1
		r.Retransmit = retrans == 1
		if macs.Valid {
			r.LocMacs = json.RawMessage(macs.String)
		}
		r.IMU = decodeAIResultJSON(imuStr)
		list = append(list, r)
	}
	return list, rows.Err()
}

// GetAudio 按 id 取详情（含 opus 原始字节）。
func (s *Store) GetAudio(ctx context.Context, id int64) (*AudioRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("database disabled")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	var r AudioRecord
	var end, retrans int
	var macs sql.NullString
	var imuStr string
	err := s.db.QueryRowContext(ctx, `
SELECT id, sn, codec, seq, total, is_end, retransmit, IFNULL(pet,''), db_level, bat, rssi,
       device_ts, received_at, crc, opus, LENGTH(opus),
       IFNULL(loc_type,''), IFNULL(loc_lat,0), IFNULL(loc_lng,0), IFNULL(loc_acc,0), IFNULL(loc_speed,0),
       loc_macs, IFNULL(loc_ts,0), IFNULL(pet_profile_id,''), IFNULL(imu_json,''), IFNULL(motion,'')
FROM audio_chunk WHERE id = ?`, id).Scan(
		&r.ID, &r.SN, &r.Codec, &r.Seq, &r.Total, &end, &retrans, &r.Pet, &r.DB, &r.Bat, &r.RSSI,
		&r.DeviceTS, &r.ReceivedAt, &r.CRC, &r.OpusData, &r.OpusLen,
		&r.LocType, &r.LocLat, &r.LocLng, &r.LocAcc, &r.LocSpeed, &macs, &r.LocTS, &r.PID, &imuStr, &r.Motion,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.End = end == 1
	r.Retransmit = retrans == 1
	if macs.Valid {
		r.LocMacs = json.RawMessage(macs.String)
	}
	r.IMU = decodeAIResultJSON(imuStr)
	return &r, nil
}

// LoadSessionOpus 按 sn+device_ts（可选 audio_kind）按 seq 升序取出分片并拼接。
// 同一 seq 多行（补发）只保留 id 最大的一条，避免拼进重复包。
// 缺 seq 会插入空 Opus PLC 帧，保证时间轴连续（避免跳帧导致听感变快）。
// audioKind：0/1 过滤通路；其它值（如 -1）不按 kind 过滤（兼容旧调用）。
// frames 为各分片裸包（转 App MP3 须按包封装 Ogg）；codec 取首个非空分片的编码（opus/mp3）。
func (s *Store) LoadSessionOpus(ctx context.Context, sn string, deviceTS int64, audioKind int) (opus []byte, frames [][]byte, petType, codec string, chunks int, err error) {
	opus, frames, _, _, petType, codec, chunks, err = s.LoadSessionOpusDetailed(ctx, sn, deviceTS, audioKind)
	return opus, frames, petType, codec, chunks, err
}

// LoadSessionOpusDetailed 同 LoadSessionOpus，额外返回补洞数与每帧绝对时间戳（微秒，与 frames 对齐）。
// absTSUs：仅设备 chunk_ts（绝对 Unix 微秒）；无则 0。不用 received_at 冒充音频时间轴。
func (s *Store) LoadSessionOpusDetailed(ctx context.Context, sn string, deviceTS int64, audioKind int) (opus []byte, frames [][]byte, absTSUs []int64, gapFilled int, petType, codec string, chunks int, err error) {
	if s == nil || s.db == nil {
		return nil, nil, nil, 0, "", "", 0, fmt.Errorf("database disabled")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	q := `
SELECT seq, IFNULL(pet,''), IFNULL(codec,''), opus,
       IFNULL(chunk_ts,0), IFNULL(received_at,0)
FROM audio_chunk
WHERE sn = ? AND device_ts = ?`
	args := []interface{}{sn, deviceTS}
	if audioKind == model.AudioKindEvent || audioKind == model.AudioKindFile {
		q += ` AND audio_kind = ?`
		args = append(args, audioKind)
	}
	q += ` ORDER BY seq ASC, id ASC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "chunk_ts") {
			return s.loadSessionOpusDetailedLegacy(ctx, sn, deviceTS, audioKind)
		}
		return nil, nil, nil, 0, "", "", 0, err
	}
	defer rows.Close()

	type chunkRow struct {
		pet, codec string
		opus       []byte
		tsUs       int64
	}
	bySeq := make(map[int]chunkRow)
	var seqOrder []int
	for rows.Next() {
		var seq int
		var pet, c string
		var chunk []byte
		var chunkTS, recvAt int64
		if err := rows.Scan(&seq, &pet, &c, &chunk, &chunkTS, &recvAt); err != nil {
			return nil, nil, nil, 0, "", "", 0, err
		}
		_ = recvAt // 墙钟收包时刻，不参与音频时间轴
		tsUs := chunkTS
		if tsUs < 0 {
			tsUs = 0
		}
		if _, seen := bySeq[seq]; !seen {
			seqOrder = append(seqOrder, seq)
			cp := make([]byte, len(chunk))
			copy(cp, chunk)
			bySeq[seq] = chunkRow{pet: pet, codec: c, opus: cp, tsUs: tsUs}
		}
		// 同 seq 已见：保留首传，忽略后到的补发重复行
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, 0, "", "", 0, err
	}

	isPlaceholderPet := func(p string) bool {
		switch p {
		case "", "0", "1", "4294967295", "FFFFFFFF", "ffffffff", "-1":
			return true
		}
		return false
	}

	rawFrames := make([][]byte, 0, len(seqOrder))
	rawTS := make([]int64, 0, len(seqOrder))
	for _, seq := range seqOrder {
		row := bySeq[seq]
		if !isPlaceholderPet(row.pet) && petType == "" {
			petType = row.pet
		}
		if codec == "" && row.codec != "" {
			codec = model.NormalizeAudioCodec(row.codec)
			if codec == "" {
				codec = row.codec
			}
		}
		rawFrames = append(rawFrames, row.opus)
		rawTS = append(rawTS, row.tsUs)
	}
	if len(rawFrames) == 0 {
		return nil, nil, nil, 0, "", "", 0, fmt.Errorf("会话无音频分片 sn=%s ts=%d kind=%d", sn, deviceTS, audioKind)
	}

	chunks = len(seqOrder)
	frames, absTSUs, gapFilled = audio.FillOpusSeqGapsWithTSKind(seqOrder, rawFrames, rawTS, audioKind)

	var buf []byte
	for _, f := range frames {
		buf = append(buf, f...)
	}
	if codec == "" {
		codec = model.CodecOpus
	}
	return buf, frames, absTSUs, gapFilled, petType, codec, chunks, nil
}

// SessionRawFrame 会话内一条真实入库原帧（无 PLC）。
type SessionRawFrame struct {
	Seq     int
	Opus    []byte
	ChunkTS int64 // 绝对 Unix 微秒；无则 0
	Total   int   // 设备上报 total（取该 seq 行上的值）
}

// LoadSessionRawFrames 按 sn+device_ts+kind 取真实 Opus 帧（按 seq 升序，同 seq 保留首传），不做 PLC 补洞。
func (s *Store) LoadSessionRawFrames(ctx context.Context, sn string, deviceTS int64, audioKind int) ([]SessionRawFrame, string, error) {
	if s == nil || s.db == nil {
		return nil, "", fmt.Errorf("database disabled")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	q := `
SELECT seq, IFNULL(codec,''), opus, IFNULL(chunk_ts,0), IFNULL(total,0)
FROM audio_chunk
WHERE sn = ? AND device_ts = ?`
	args := []interface{}{sn, deviceTS}
	if audioKind == model.AudioKindEvent || audioKind == model.AudioKindFile {
		q += ` AND audio_kind = ?`
		args = append(args, audioKind)
	}
	q += ` ORDER BY seq ASC, id ASC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "chunk_ts") {
			return s.loadSessionRawFramesLegacy(ctx, sn, deviceTS, audioKind)
		}
		return nil, "", err
	}
	defer rows.Close()

	type row struct {
		seq, total int
		codec      string
		opus       []byte
		ts         int64
	}
	bySeq := make(map[int]row)
	var order []int
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.seq, &r.codec, &r.opus, &r.ts, &r.total); err != nil {
			return nil, "", err
		}
		if _, seen := bySeq[r.seq]; seen {
			continue
		}
		cp := make([]byte, len(r.opus))
		copy(cp, r.opus)
		r.opus = cp
		bySeq[r.seq] = r
		order = append(order, r.seq)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(order) == 0 {
		return nil, "", fmt.Errorf("会话无音频分片 sn=%s ts=%d kind=%d", sn, deviceTS, audioKind)
	}
	out := make([]SessionRawFrame, 0, len(order))
	codec := ""
	for _, seq := range order {
		r := bySeq[seq]
		if codec == "" && r.codec != "" {
			codec = model.NormalizeAudioCodec(r.codec)
			if codec == "" {
				codec = r.codec
			}
		}
		ts := r.ts
		if ts < 0 {
			ts = 0
		}
		out = append(out, SessionRawFrame{Seq: r.seq, Opus: r.opus, ChunkTS: ts, Total: r.total})
	}
	if codec == "" {
		codec = model.CodecOpus
	}
	return out, codec, nil
}

func (s *Store) loadSessionRawFramesLegacy(ctx context.Context, sn string, deviceTS int64, audioKind int) ([]SessionRawFrame, string, error) {
	q := `
SELECT seq, IFNULL(codec,''), opus, IFNULL(total,0)
FROM audio_chunk WHERE sn = ? AND device_ts = ?`
	args := []interface{}{sn, deviceTS}
	if audioKind == model.AudioKindEvent || audioKind == model.AudioKindFile {
		q += ` AND audio_kind = ?`
		args = append(args, audioKind)
	}
	q += ` ORDER BY seq ASC, id ASC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	bySeq := make(map[int]SessionRawFrame)
	var order []int
	codec := ""
	for rows.Next() {
		var seq, total int
		var c string
		var opus []byte
		if err := rows.Scan(&seq, &c, &opus, &total); err != nil {
			return nil, "", err
		}
		if _, seen := bySeq[seq]; seen {
			continue
		}
		cp := make([]byte, len(opus))
		copy(cp, opus)
		bySeq[seq] = SessionRawFrame{Seq: seq, Opus: cp, Total: total}
		order = append(order, seq)
		if codec == "" && c != "" {
			codec = model.NormalizeAudioCodec(c)
			if codec == "" {
				codec = c
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(order) == 0 {
		return nil, "", fmt.Errorf("会话无音频分片 sn=%s ts=%d kind=%d", sn, deviceTS, audioKind)
	}
	out := make([]SessionRawFrame, 0, len(order))
	for _, seq := range order {
		out = append(out, bySeq[seq])
	}
	if codec == "" {
		codec = model.CodecOpus
	}
	return out, codec, nil
}

func (s *Store) loadSessionOpusDetailedLegacy(ctx context.Context, sn string, deviceTS int64, audioKind int) (opus []byte, frames [][]byte, absTSUs []int64, gapFilled int, petType, codec string, chunks int, err error) {
	q := `
SELECT seq, IFNULL(pet,''), IFNULL(codec,''), opus, IFNULL(received_at,0)
FROM audio_chunk WHERE sn = ? AND device_ts = ?`
	args := []interface{}{sn, deviceTS}
	if audioKind == model.AudioKindEvent || audioKind == model.AudioKindFile {
		q += ` AND audio_kind = ?`
		args = append(args, audioKind)
	}
	q += ` ORDER BY seq ASC, id ASC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, nil, 0, "", "", 0, err
	}
	defer rows.Close()

	type chunkRow struct {
		pet, codec string
		opus       []byte
		tsUs       int64
	}
	bySeq := make(map[int]chunkRow)
	var seqOrder []int
	for rows.Next() {
		var seq int
		var pet, c string
		var chunk []byte
		var recvAt int64
		if err := rows.Scan(&seq, &pet, &c, &chunk, &recvAt); err != nil {
			return nil, nil, nil, 0, "", "", 0, err
		}
		_ = recvAt
		if _, seen := bySeq[seq]; !seen {
			seqOrder = append(seqOrder, seq)
			cp := make([]byte, len(chunk))
			copy(cp, chunk)
			bySeq[seq] = chunkRow{pet: pet, codec: c, opus: cp, tsUs: 0}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, 0, "", "", 0, err
	}
	rawFrames := make([][]byte, 0, len(seqOrder))
	rawTS := make([]int64, 0, len(seqOrder))
	for _, seq := range seqOrder {
		row := bySeq[seq]
		if petType == "" && row.pet != "" && row.pet != "0" && row.pet != "1" {
			petType = row.pet
		}
		if codec == "" && row.codec != "" {
			codec = model.NormalizeAudioCodec(row.codec)
			if codec == "" {
				codec = row.codec
			}
		}
		rawFrames = append(rawFrames, row.opus)
		rawTS = append(rawTS, row.tsUs)
	}
	if len(rawFrames) == 0 {
		return nil, nil, nil, 0, "", "", 0, fmt.Errorf("会话无音频分片 sn=%s ts=%d kind=%d", sn, deviceTS, audioKind)
	}
	chunks = len(seqOrder)
	frames, absTSUs, gapFilled = audio.FillOpusSeqGapsWithTSKind(seqOrder, rawFrames, rawTS, audioKind)
	var buf []byte
	for _, f := range frames {
		buf = append(buf, f...)
	}
	if codec == "" {
		codec = model.CodecOpus
	}
	return buf, frames, absTSUs, gapFilled, petType, codec, chunks, nil
}

// SessionChunkStats 某段音频分片完整度统计（用于 end 后等齐 / total 对比）。
type SessionChunkStats struct {
	Chunks int // 去重后的 seq 数
	MaxSeq int
	Total  int // 仅取自 end=1 帧的 total（滚动中间值忽略）；0 表示 end 未带 total
	HasEnd bool
}

// ExpectedFrames 收段应有帧数：优先 end 帧 total，并与 MaxSeq+1 取较大值。
// 设备 total 为滚动值（≈seq+1），勿用全段 MAX(total) 的中间态；以 end + 实际帧数为准。
func (st SessionChunkStats) ExpectedFrames() int {
	exp := st.Total
	if st.HasEnd {
		if n := st.MaxSeq + 1; n > exp {
			exp = n
		}
	}
	return exp
}

// Ready 是否已齐：有 end，且去重帧数 >= ExpectedFrames（无声明时 Expected=MaxSeq+1）。
func (st SessionChunkStats) Ready() bool {
	if st.Chunks == 0 || !st.HasEnd {
		return false
	}
	if exp := st.ExpectedFrames(); exp > 0 && st.Chunks < exp {
		return false
	}
	return true
}

// Incomplete 有 end 但入库去重帧数仍不足 ExpectedFrames。
func (st SessionChunkStats) Incomplete() bool {
	if !st.HasEnd {
		return false
	}
	exp := st.ExpectedFrames()
	return exp > 0 && st.Chunks < exp
}

// GetSessionRecvSpanMs 段内首尾 received_at 跨度（毫秒），用于对照 Opus 估算时长。
func (s *Store) GetSessionRecvSpanMs(ctx context.Context, sn string, deviceTS int64, audioKind int) (int64, error) {
	if s == nil || s.db == nil || sn == "" || deviceTS == 0 {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	q := `SELECT MIN(received_at), MAX(received_at) FROM audio_chunk WHERE sn=? AND device_ts=?`
	args := []interface{}{sn, deviceTS}
	if audioKind == model.AudioKindEvent || audioKind == model.AudioKindFile {
		q += ` AND audio_kind=?`
		args = append(args, audioKind)
	}
	var minR, maxR sql.NullInt64
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&minR, &maxR); err != nil {
		return 0, err
	}
	if !minR.Valid || !maxR.Valid || maxR.Int64 < minR.Int64 {
		return 0, nil
	}
	return maxR.Int64 - minR.Int64, nil
}

// SessionChunkKey sn+device_ts+kind 会话键。
type SessionChunkKey struct {
	DeviceTS  int64
	AudioKind int
}

// MapSessionChunkStatsBySN 按 sn 汇总各段 audio_chunk 去重帧数 / total / end。
func (s *Store) MapSessionChunkStatsBySN(ctx context.Context, sn string, limit int) (map[SessionChunkKey]SessionChunkStats, error) {
	out := make(map[SessionChunkKey]SessionChunkStats)
	if s == nil || s.db == nil || sn == "" {
		return out, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
SELECT device_ts, IFNULL(audio_kind,0),
       COUNT(DISTINCT seq), IFNULL(MAX(seq),0),
       IFNULL(MAX(CASE WHEN is_end = 1 THEN total ELSE NULL END), 0),
       IFNULL(MAX(is_end),0)
FROM audio_chunk
WHERE sn = ?
GROUP BY device_ts, IFNULL(audio_kind,0)
ORDER BY MAX(id) DESC
LIMIT ?`, sn, limit)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var k SessionChunkKey
		var st SessionChunkStats
		var hasEnd int
		if err := rows.Scan(&k.DeviceTS, &k.AudioKind, &st.Chunks, &st.MaxSeq, &st.Total, &hasEnd); err != nil {
			return out, err
		}
		st.HasEnd = hasEnd != 0
		out[k] = st
	}
	return out, rows.Err()
}

// GetSessionChunkStats 统计 sn+device_ts(+kind) 去重帧数 / total / 是否已 end。
func (s *Store) GetSessionChunkStats(ctx context.Context, sn string, deviceTS int64, audioKind int) (SessionChunkStats, error) {
	var st SessionChunkStats
	if s == nil || s.db == nil {
		return st, fmt.Errorf("database disabled")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	q := `
SELECT COUNT(DISTINCT seq), IFNULL(MAX(seq),0),
       IFNULL(MAX(CASE WHEN is_end = 1 THEN total ELSE NULL END), 0),
       IFNULL(MAX(is_end),0)
FROM audio_chunk WHERE sn = ? AND device_ts = ?`
	args := []interface{}{sn, deviceTS}
	if audioKind == model.AudioKindEvent || audioKind == model.AudioKindFile {
		q += ` AND audio_kind = ?`
		args = append(args, audioKind)
	}
	var hasEnd int
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&st.Chunks, &st.MaxSeq, &st.Total, &hasEnd)
	if err != nil {
		return st, err
	}
	st.HasEnd = hasEnd != 0
	return st, nil
}

// MaxAudioSessionSpanUS 补发并回原段时，用绝对 ts 反查 session 的最大跨度（2 分钟）。
const MaxAudioSessionSpanUS = 120_000_000

// ResolveSessionDeviceTS 重连/补发时找回原段 device_ts（段首绝对微秒）。
// 顺序：绝对 ts 落入的近期同 kind 段 → 未收尾同 kind 段（仅 absTS 未给出或也落在该段内）→ 同 seq 且时间相容。
// 禁止「只按最新同 seq」：live seq=5 与旧段补发 seq=5 会误并进当前段。
func (s *Store) ResolveSessionDeviceTS(ctx context.Context, sn string, kind, seq int, absTS int64) (int64, error) {
	if s == nil || s.db == nil || sn == "" {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if kind != model.AudioKindFile {
		kind = model.AudioKindEvent
	}

	var ts int64
	absOK := model.IsAbsoluteUnixMicros(absTS)

	// 1) 绝对 ts 落入 [device_ts, device_ts+span)
	if absOK {
		err := s.db.QueryRowContext(ctx, `
SELECT device_ts FROM audio_chunk
WHERE sn = ? AND audio_kind = ?
  AND device_ts > 0 AND device_ts <= ? AND (? - device_ts) < ?
ORDER BY device_ts DESC LIMIT 1`, sn, kind, absTS, absTS, MaxAudioSessionSpanUS).Scan(&ts)
		if err == nil && ts > 0 {
			return ts, nil
		}
		if err != nil && err != sql.ErrNoRows {
			return 0, err
		}
	}

	// 2) 同 kind 尚未 end 的最近一段（断线中途；相对 ts 重连也走这里）
	err := s.db.QueryRowContext(ctx, `
SELECT c.device_ts FROM audio_chunk c
WHERE c.sn = ? AND c.audio_kind = ?
  AND NOT EXISTS (
    SELECT 1 FROM audio_chunk e
    WHERE e.sn = c.sn AND e.device_ts = c.device_ts AND e.audio_kind = c.audio_kind AND e.is_end = 1
  )
ORDER BY c.id DESC LIMIT 1`, sn, kind).Scan(&ts)
	if err == nil && ts > 0 {
		if !absOK || (absTS >= ts && absTS-ts < MaxAudioSessionSpanUS) {
			return ts, nil
		}
	} else if err != nil && err != sql.ErrNoRows {
		return 0, err
	}

	// 3) 同 seq 最近一行，且与 absTS 相容（无绝对 ts 时不采用，避免跨段误并）
	if absOK {
		err = s.db.QueryRowContext(ctx, `
SELECT device_ts FROM audio_chunk
WHERE sn = ? AND audio_kind = ? AND seq = ?
ORDER BY id DESC LIMIT 1`, sn, kind, seq).Scan(&ts)
		if err == nil && ts > 0 && absTS >= ts && absTS-ts < MaxAudioSessionSpanUS {
			return ts, nil
		}
		if err != nil && err != sql.ErrNoRows {
			return 0, err
		}
	}
	return 0, nil
}

// AudioFragMeta 补发回填用的历史分片元数据。
type AudioFragMeta struct {
	Codec string
	Total int
	TS    int64
	PID   string
}

// LookupAudioMeta 按 sn+seq（可选 crc、audio_kind）取最近一条记录，供断线重连精简补发回填。
func (s *Store) LookupAudioMeta(ctx context.Context, sn string, seq int, crc uint32) (*AudioFragMeta, error) {
	return s.LookupAudioMetaByKind(ctx, sn, seq, crc, -1)
}

// LookupAudioMetaByKind 同 LookupAudioMeta；kind>=0 时优先同通路分片。
func (s *Store) LookupAudioMetaByKind(ctx context.Context, sn string, seq int, crc uint32, kind int) (*AudioFragMeta, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	q := `
SELECT IFNULL(codec,''), total, device_ts, IFNULL(pet_profile_id,'')
FROM audio_chunk
WHERE sn = ? AND seq = ?`
	args := []interface{}{sn, seq}
	if kind == 0 || kind == 1 {
		q += ` AND audio_kind = ?`
		args = append(args, kind)
	}
	if crc != 0 {
		q += ` AND crc = ?`
		args = append(args, crc)
	}
	q += ` ORDER BY id DESC LIMIT 1`

	var meta AudioFragMeta
	var pid sql.NullString
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&meta.Codec, &meta.Total, &meta.TS, &pid)
	if err == sql.ErrNoRows {
		// 旧库无 audio_kind 列或尚无同 kind 行时，降级不按 kind 查
		if kind == 0 || kind == 1 {
			return s.LookupAudioMetaByKind(ctx, sn, seq, crc, -1)
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if pid.Valid && pid.String != "" {
		meta.PID = pid.String
	}
	return &meta, nil
}

// EmbDevice a_emb_sn 绑定信息。
type EmbDevice struct {
	ID     int
	UserID int
	PetID  string
	Status int
}

// HasActiveBinding 是否具备可接收录音的绑定：bind_user_id>0 且 bind_pet_id 非空/非0。
func (d *EmbDevice) HasActiveBinding() bool {
	if d == nil {
		return false
	}
	if d.UserID <= 0 {
		return false
	}
	return !model.IsUnboundPetID(d.PetID)
}

// LookupEmbBySN 按序列号查设备绑定（App 服务写入 a_emb_sn）。
func (s *Store) LookupEmbBySN(ctx context.Context, sn string) (*EmbDevice, error) {
	if s == nil || s.db == nil || sn == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var d EmbDevice
	var uid sql.NullInt64
	var pet sql.NullString
	var st sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
SELECT id, bind_user_id, bind_pet_id, status FROM a_emb_sn WHERE emb_sn = ? LIMIT 1`, sn).Scan(&d.ID, &uid, &pet, &st)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if uid.Valid {
		d.UserID = int(uid.Int64)
	}
	if pet.Valid {
		d.PetID = pet.String
	}
	d.Status = 1
	if st.Valid {
		d.Status = int(st.Int64)
	}
	return &d, nil
}

// TranslateRecord 对齐 a_emb_translate。
type TranslateRecord struct {
	ID                  int64           `json:"id"`
	UserID              int             `json:"user_id,omitempty"`
	EmbID               int             `json:"emb_id,omitempty"`
	EmbSN               string          `json:"emb_sn"`
	SN                  string          `json:"sn"` // 同 emb_sn，联调用
	PID                 string          `json:"pid,omitempty"`
	PetType             string          `json:"pet_type"`
	DeviceTS            int64           `json:"device_ts"`
	AudioKind           int             `json:"audio_kind"`       // 0=事件流 1=录音文件
	AudioKindLabel      string          `json:"audio_kind_label"` // event | file
	Status              string          `json:"status"`
	OriginAudioURL      string          `json:"origin_audio_url"`
	OriginRawURL        string          `json:"origin_raw_url,omitempty"`    // 事件流原帧 zip（无 PLC/不拼接）
	OriginRawFrames     int             `json:"origin_raw_frames,omitempty"` // zip 内真实帧数
	AudioURL            string          `json:"audio_url"`
	TranslateText       string          `json:"translate_text"`
	ResultSays          string          `json:"result_says"`
	OriginAudioDuration string          `json:"origin_audio_duration,omitempty"`
	AIResult            json.RawMessage `json:"ai_result,omitempty"` // AI 原始 JSON，接口中已 decode 为对象
	ErrorMsg            string          `json:"error_msg,omitempty"`
	HasDeviceOpus       bool            `json:"has_device_opus"`              // 是否已存设备下行 Opus（可 replay）
	DeviceOpusFrames    int             `json:"device_opus_frames,omitempty"` // Opus 帧数
	FeedbackType        *int            `json:"feedback_type,omitempty"`
	FeedbackEmotionType *int            `json:"feedback_emotion_type,omitempty"`
	CreateTime          int64           `json:"createtime"`
	ResultTime          int64           `json:"resulttime"`
}

const translateSelectCols = `id, user_id, emb_id, IFNULL(emb_sn,''), IFNULL(pid,''), IFNULL(pet_type,''), device_ts,
       IFNULL(audio_kind,0), status,
       IFNULL(origin_audio_url,''), IFNULL(origin_raw_url,''), IFNULL(origin_raw_frames,0),
       IFNULL(audio_url,''), IFNULL(translate_text,''), IFNULL(result_says,''),
       IFNULL(origin_audio_duration,''), IFNULL(ai_result,''), IFNULL(error_msg,''),
       feedback_type, feedback_emotion_type,
       IFNULL(createtime,0), IFNULL(resulttime,0),
       IFNULL(device_opus_frames,0), CASE WHEN device_opus IS NOT NULL AND LENGTH(device_opus)>0 THEN 1 ELSE 0 END`

func scanTranslate(scan func(...interface{}) error) (*TranslateRecord, error) {
	var r TranslateRecord
	var uid, eid, fb, emo sql.NullInt64
	var aiResultStr string
	var opusFrames, hasOpus, rawFrames int
	if err := scan(
		&r.ID, &uid, &eid, &r.EmbSN, &r.PID, &r.PetType, &r.DeviceTS, &r.AudioKind, &r.Status,
		&r.OriginAudioURL, &r.OriginRawURL, &rawFrames,
		&r.AudioURL, &r.TranslateText, &r.ResultSays,
		&r.OriginAudioDuration, &aiResultStr, &r.ErrorMsg,
		&fb, &emo, &r.CreateTime, &r.ResultTime,
		&opusFrames, &hasOpus,
	); err != nil {
		return nil, err
	}
	r.AIResult = decodeAIResultJSON(aiResultStr)
	r.DeviceOpusFrames = opusFrames
	r.HasDeviceOpus = hasOpus != 0
	r.OriginRawFrames = rawFrames
	r.SN = r.EmbSN
	if r.AudioKind != model.AudioKindFile {
		r.AudioKind = model.AudioKindEvent
	}
	r.AudioKindLabel = model.AudioStreamKindLabel(r.AudioKind)
	if uid.Valid {
		r.UserID = int(uid.Int64)
	}
	if eid.Valid {
		r.EmbID = int(eid.Int64)
	}
	if fb.Valid {
		v := int(fb.Int64)
		r.FeedbackType = &v
	}
	if emo.Valid {
		v := int(emo.Int64)
		r.FeedbackEmotionType = &v
	}
	return &r, nil
}

// decodeAIResultJSON 将库内 TEXT 的 JSON 解码为接口可嵌套的 RawMessage（ai_result / imu_json 共用）。
// 合法 JSON 原样嵌入；非法则包一层字符串，保证响应始终是合法 JSON。
func decodeAIResultJSON(s string) json.RawMessage {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	b, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	return json.RawMessage(b)
}

func intOrNull(n int) interface{} {
	if n == 0 {
		return nil
	}
	return n
}

// CreateTranslatePending 写入 a_emb_translate（pending），幂等：唯一键 uk_emb_sn_device_ts(emb_sn,device_ts)。
// audioKind：0=事件流 1=录音文件。
// 返回：(translateID, alreadyExists, error)；若 alreadyExists=true 说明该 (sn,device_ts) 段已写入过，无需再跑 OSS/AI。
func (s *Store) CreateTranslatePending(ctx context.Context, userID, embID int, sn, petType, pid string, deviceTS int64, audioKind int) (int64, bool, error) {
	if s == nil || s.db == nil {
		return 0, false, fmt.Errorf("database disabled")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	now := time.Now().Unix()
	if audioKind != model.AudioKindFile {
		audioKind = model.AudioKindEvent
	}

	var insertSQL string
	if s.driver == "mysql" {
		insertSQL = `INSERT IGNORE INTO a_emb_translate (
  user_id, emb_id, emb_sn, pid, pet_type, device_ts, audio_kind, status, createtime
) VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?)`
	} else {
		insertSQL = `INSERT OR IGNORE INTO a_emb_translate (
  user_id, emb_id, emb_sn, pid, pet_type, device_ts, audio_kind, status, createtime
) VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?)`
	}
	args := []interface{}{
		intOrNull(userID), intOrNull(embID), sn, nullIfEmpty(pid), petType, deviceTS, audioKind, now,
	}
	res, err := s.db.ExecContext(ctx, insertSQL, args...)
	if err != nil {
		storeLog.Error("写入 a_emb_translate pending 失败",
			zap.String("sn", sn),
			zap.Int64("device_ts", deviceTS),
			zap.Int("audio_kind", audioKind),
			zap.String("pid", pid),
			zap.Error(err),
		)
		return 0, false, err
	}
	id, errID := res.LastInsertId()
	rows, _ := res.RowsAffected()
	if rows > 0 {
		return id, false, errID
	}
	// RowsAffected==0：唯一键命中 → 查 existing id 返回 alreadyExists=true
	var existingID int64
	qErr := s.db.QueryRowContext(ctx,
		`SELECT id FROM a_emb_translate WHERE emb_sn=? AND device_ts=?`, sn, deviceTS).Scan(&existingID)
	if qErr != nil {
		storeLog.Error("a_emb_translate 唯一键命中后回查 existing id 失败",
			zap.String("sn", sn), zap.Int64("device_ts", deviceTS), zap.Error(qErr))
		return 0, false, qErr
	}
	return existingID, true, nil
}

// HasTranslateSession 同 sn+device_ts 是否已有翻译会话（防重复灌库）。
func (s *Store) HasTranslateSession(ctx context.Context, sn string, deviceTS int64) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("database disabled")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var n int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM a_emb_translate WHERE emb_sn=? AND device_ts=?`, sn, deviceTS).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// GetTranslateIDBySNDeviceTS 按 sn+device_ts 取翻译记录 id；无则 0。
func (s *Store) GetTranslateIDBySNDeviceTS(ctx context.Context, sn string, deviceTS int64) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("database disabled")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var id int64
	err := s.db.QueryRowContext(ctx, `
SELECT id FROM a_emb_translate WHERE emb_sn=? AND device_ts=? ORDER BY id DESC LIMIT 1`, sn, deviceTS).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

// SetTranslateOrigin 原音频已上 OSS 后先回写 URL（后续 AI/TTS 失败时 App 仍能取原音）。
func (s *Store) SetTranslateOrigin(ctx context.Context, id int64, originURL, duration string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("database disabled")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `
UPDATE a_emb_translate SET origin_audio_url=?, origin_audio_duration=? WHERE id=?`,
		originURL, nullIfEmpty(duration), id)
	return err
}

// SetTranslateOriginRaw 事件流原帧 zip URL（无 PLC / 不拼接）。
func (s *Store) SetTranslateOriginRaw(ctx context.Context, id int64, rawURL string, frames int) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("database disabled")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `
UPDATE a_emb_translate SET origin_raw_url=?, origin_raw_frames=? WHERE id=?`,
		rawURL, frames, id)
	return err
}

// SetTranslateOriginRawBySNDeviceTS 按 sn+device_ts 回写原帧 zip；无记录返回 false。
func (s *Store) SetTranslateOriginRawBySNDeviceTS(ctx context.Context, sn string, deviceTS int64, rawURL string, frames int) (bool, error) {
	id, err := s.GetTranslateIDBySNDeviceTS(ctx, sn, deviceTS)
	if err != nil {
		return false, err
	}
	if id <= 0 {
		return false, nil
	}
	if err := s.SetTranslateOriginRaw(ctx, id, rawURL, frames); err != nil {
		return false, err
	}
	return true, nil
}

// MarkTranslateTranslating 原音已就绪、进入 AI/TTS：status=translating（App 列表仍不出）。
func (s *Store) MarkTranslateTranslating(ctx context.Context, id int64) error {
	return s.SetTranslateStatus(ctx, id, model.TranslateStatusTranslating)
}

// SetTranslateStatus 更新翻译状态（pending / translating / ok / fail）。
func (s *Store) SetTranslateStatus(ctx context.Context, id int64, status string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("database disabled")
	}
	status = strings.ToLower(strings.TrimSpace(status))
	switch status {
	case model.TranslateStatusPending, model.TranslateStatusTranslating,
		model.TranslateStatusOK, model.TranslateStatusFail:
	default:
		return fmt.Errorf("invalid translate status %q", status)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `UPDATE a_emb_translate SET status=? WHERE id=?`, status, id)
	return err
}

// FinishTranslateOK 回填 OSS / 译文。
func (s *Store) FinishTranslateOK(ctx context.Context, id int64, originURL, ttsURL, text, aiRaw, duration string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("database disabled")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	resultSays := text
	if resultSays == "" {
		resultSays = "当前音频未捕获有效猫狗声纹信号"
	}
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
UPDATE a_emb_translate SET
  status='ok', origin_audio_url=?, audio_url=?, translate_text=?, result_says=?,
  origin_audio_duration=?, ai_result=?, error_msg='', resulttime=?
WHERE id=?`, originURL, ttsURL, text, resultSays, nullIfEmpty(duration), aiRaw, now, id)
	return err
}

// SaveTranslateDeviceOpus 保存设备下行裸 Opus 帧（PackOpusFrames 格式），供事后 replay。
func (s *Store) SaveTranslateDeviceOpus(ctx context.Context, id int64, frames [][]byte) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("database disabled")
	}
	if id <= 0 {
		return fmt.Errorf("invalid id")
	}
	blob, err := audio.PackOpusFrames(frames)
	if err != nil {
		return err
	}
	if len(blob) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = s.db.ExecContext(ctx, `
UPDATE a_emb_translate SET device_opus=?, device_opus_frames=? WHERE id=?`,
		blob, len(frames), id)
	return err
}

// LoadTranslateDeviceOpus 读取并解包设备下行 Opus 帧。
func (s *Store) LoadTranslateDeviceOpus(ctx context.Context, id int64) ([][]byte, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("database disabled")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var blob []byte
	err := s.db.QueryRowContext(ctx, `SELECT device_opus FROM a_emb_translate WHERE id=?`, id).Scan(&blob)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return audio.UnpackOpusFrames(blob)
}

// FinishTranslateFail 标记失败。
func (s *Store) FinishTranslateFail(ctx context.Context, id int64, errMsg string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("database disabled")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
UPDATE a_emb_translate SET status='fail', error_msg=?, resulttime=? WHERE id=?`,
		errMsg, now, id)
	return err
}

// ListTranslate 翻译结果列表。
// readyOnly=true（App 默认）：只返回终态 ok/fail，翻译中的 pending/translating 先不出。
func (s *Store) ListTranslate(ctx context.Context, sn string, limit, offset int, sinceID int64, readyOnly bool) ([]TranslateRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("database disabled")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	q := `
SELECT ` + translateSelectCols + `
FROM a_emb_translate
WHERE (? = '' OR emb_sn = ?)`
	args := []interface{}{sn, sn}
	if readyOnly {
		q += ` AND status IN ('ok','fail')`
	}
	if sinceID > 0 {
		q += ` AND id > ? ORDER BY id ASC LIMIT ?`
		args = append(args, sinceID, limit)
	} else {
		q += ` ORDER BY id DESC LIMIT ? OFFSET ?`
		args = append(args, limit, offset)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]TranslateRecord, 0, limit)
	for rows.Next() {
		r, err := scanTranslate(rows.Scan)
		if err != nil {
			return nil, err
		}
		list = append(list, *r)
	}
	return list, rows.Err()
}

// GetTranslate 详情。
func (s *Store) GetTranslate(ctx context.Context, id int64) (*TranslateRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("database disabled")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	r, err := scanTranslate(s.db.QueryRowContext(ctx, `
SELECT `+translateSelectCols+`
FROM a_emb_translate WHERE id=?`, id).Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// GetTranslateBySNDeviceTS 按 sn+device_ts 取最新一条翻译/原音记录。
func (s *Store) GetTranslateBySNDeviceTS(ctx context.Context, sn string, deviceTS int64) (*TranslateRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("database disabled")
	}
	if sn == "" || deviceTS == 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	r, err := scanTranslate(s.db.QueryRowContext(ctx, `
SELECT `+translateSelectCols+`
FROM a_emb_translate WHERE emb_sn=? AND device_ts=? ORDER BY id DESC LIMIT 1`, sn, deviceTS).Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// MapTranslateByDeviceTS 按 sn 拉近期翻译记录，以 device_ts 为键（同 ts 保留 id 更大的）。
// 含 pending/translating，供诊断页对照，不按 App 终态过滤。
func (s *Store) MapTranslateByDeviceTS(ctx context.Context, sn string, limit int) (map[int64]*TranslateRecord, error) {
	list, err := s.ListTranslate(ctx, sn, limit, 0, 0, false)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*TranslateRecord, len(list))
	for i := range list {
		r := list[i]
		if r.DeviceTS == 0 {
			continue
		}
		if prev, ok := out[r.DeviceTS]; ok && prev.ID >= r.ID {
			continue
		}
		cp := r
		out[r.DeviceTS] = &cp
	}
	return out, nil
}
