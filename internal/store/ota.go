package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// OTA 日志状态。
const (
	OTAStatusPushed     = "pushed"     // 已下发，等待 ack
	OTAStatusConfirming = "confirming" // ack result=0，等待 boot=1 或超时
	OTAStatusSuccess    = "success"
	OTAStatusFail       = "fail"
)

// OTA 失败原因。
const (
	OTAReasonAckRejected     = "ack_rejected"      // ack result≠0
	OTAReasonAckTimeout      = "ack_timeout"       // 未收到 ack
	OTAReasonRollback        = "rollback"          // boot=1 但 fw 仍是旧版本
	OTAReasonTimeoutNoReboot = "timeout_no_reboot" // 断过线但十分钟内无 boot=1
	OTAReasonSkipped         = "skipped"           // 连接从未断开（本地版本已不低于目标）
	OTAReasonSuperseded      = "superseded"        // 被新的升级任务覆盖
	OTAReasonRetried         = "retried"           // 已按进度/ack 规则自动重推，仍等待落定
)

// OTALog 设备 OTA 升级判定记录。时间戳均为 Unix 毫秒。
type OTALog struct {
	ID           int64  `json:"id"`
	SN           string `json:"sn"`
	FromFW       string `json:"from_fw"`
	TargetFW     string `json:"target_fw"`
	URL          string `json:"url"`
	Force        bool   `json:"force"`
	Status       string `json:"status"`
	FailReason   string `json:"fail_reason"`
	Disconnected bool   `json:"disconnected"`
	Reconnected  bool   `json:"reconnected"`
	ResultFW     string `json:"result_fw"`
	PushedAt     int64  `json:"pushed_at"`
	AckAt        int64  `json:"ack_at"`
	DisconnectAt int64  `json:"disconnect_at"`
	ReconnectAt  int64  `json:"reconnect_at"`
	BootAt       int64  `json:"boot_at"`
	ResultAt     int64  `json:"result_at"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

const otaLogSelect = `
SELECT id, IFNULL(sn,''), IFNULL(from_fw,''), IFNULL(target_fw,''), IFNULL(url,''),
       IFNULL(force_flag,0), IFNULL(status,''), IFNULL(fail_reason,''),
       IFNULL(disconnected,0), IFNULL(reconnected,0), IFNULL(result_fw,''),
       IFNULL(pushed_at,0), IFNULL(ack_at,0), IFNULL(disconnect_at,0),
       IFNULL(reconnect_at,0), IFNULL(boot_at,0), IFNULL(result_at,0),
       IFNULL(created_at,0), IFNULL(updated_at,0)
FROM a_emb_ota_log`

func scanOTALog(s interface {
	Scan(dest ...interface{}) error
}) (*OTALog, error) {
	var r OTALog
	var force, disc, reconn int
	if err := s.Scan(
		&r.ID, &r.SN, &r.FromFW, &r.TargetFW, &r.URL,
		&force, &r.Status, &r.FailReason, &disc, &reconn, &r.ResultFW,
		&r.PushedAt, &r.AckAt, &r.DisconnectAt, &r.ReconnectAt, &r.BootAt, &r.ResultAt,
		&r.CreatedAt, &r.UpdatedAt,
	); err != nil {
		return nil, err
	}
	r.Force = force != 0
	r.Disconnected = disc != 0
	r.Reconnected = reconn != 0
	return &r, nil
}

func bool01(v bool) int {
	if v {
		return 1
	}
	return 0
}

// InsertOTALog 写入一条升级日志。
func (s *Store) InsertOTALog(ctx context.Context, r *OTALog) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("database disabled")
	}
	if r == nil || strings.TrimSpace(r.SN) == "" {
		return 0, fmt.Errorf("sn required")
	}
	now := time.Now().UnixMilli()
	if r.CreatedAt <= 0 {
		r.CreatedAt = now
	}
	r.UpdatedAt = now
	if r.PushedAt <= 0 {
		r.PushedAt = now
	}
	if r.Status == "" {
		r.Status = OTAStatusPushed
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO a_emb_ota_log
  (sn, from_fw, target_fw, url, force_flag, status, fail_reason,
   disconnected, reconnected, result_fw, pushed_at, ack_at, disconnect_at,
   reconnect_at, boot_at, result_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.SN, r.FromFW, r.TargetFW, r.URL, bool01(r.Force), r.Status, r.FailReason,
		bool01(r.Disconnected), bool01(r.Reconnected), r.ResultFW,
		r.PushedAt, r.AckAt, r.DisconnectAt, r.ReconnectAt, r.BootAt, r.ResultAt,
		r.CreatedAt, r.UpdatedAt,
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	r.ID = id
	return id, nil
}

// GetOTALog 按 id 取一条。
func (s *Store) GetOTALog(ctx context.Context, id int64) (*OTALog, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("database disabled")
	}
	row := s.db.QueryRowContext(ctx, otaLogSelect+` WHERE id = ?`, id)
	r, err := scanOTALog(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

// GetPendingOTABySN 该设备最新一条未落定记录。
func (s *Store) GetPendingOTABySN(ctx context.Context, sn string) (*OTALog, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("database disabled")
	}
	sn = strings.TrimSpace(sn)
	if sn == "" {
		return nil, nil
	}
	row := s.db.QueryRowContext(ctx, otaLogSelect+`
WHERE sn = ? AND status IN ('`+OTAStatusPushed+`','`+OTAStatusConfirming+`')
ORDER BY id DESC LIMIT 1`, sn)
	r, err := scanOTALog(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

// ListPendingOTALogs 所有未落定记录（超时扫描用）。
func (s *Store) ListPendingOTALogs(ctx context.Context) ([]OTALog, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("database disabled")
	}
	rows, err := s.db.QueryContext(ctx, otaLogSelect+`
WHERE status IN ('`+OTAStatusPushed+`','`+OTAStatusConfirming+`')
ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]OTALog, 0)
	for rows.Next() {
		r, err := scanOTALog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// ListOTALogs 管理端列表。sn / status 可选过滤。
func (s *Store) ListOTALogs(ctx context.Context, sn, status string, limit int) ([]OTALog, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("database disabled")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	q := otaLogSelect + ` WHERE 1=1`
	args := make([]interface{}, 0, 3)
	if sn = strings.TrimSpace(sn); sn != "" {
		q += ` AND sn = ?`
		args = append(args, sn)
	}
	if status = strings.TrimSpace(status); status != "" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]OTALog, 0)
	for rows.Next() {
		r, err := scanOTALog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// OTALogPatch 增量更新未落定记录。
type OTALogPatch struct {
	Status       *string
	FailReason   *string
	Disconnected *bool
	Reconnected  *bool
	ResultFW     *string
	PushedAt     *int64
	AckAt        *int64
	DisconnectAt *int64
	ReconnectAt  *int64
	BootAt       *int64
	ResultAt     *int64
}

// UpdateOTALog 按 id 增量更新。
func (s *Store) UpdateOTALog(ctx context.Context, id int64, p OTALogPatch) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("database disabled")
	}
	if id <= 0 {
		return fmt.Errorf("id required")
	}
	sets := []string{"updated_at = ?"}
	args := []interface{}{time.Now().UnixMilli()}
	if p.Status != nil {
		sets = append(sets, "status = ?")
		args = append(args, *p.Status)
	}
	if p.FailReason != nil {
		sets = append(sets, "fail_reason = ?")
		args = append(args, *p.FailReason)
	}
	if p.Disconnected != nil {
		sets = append(sets, "disconnected = ?")
		args = append(args, bool01(*p.Disconnected))
	}
	if p.Reconnected != nil {
		sets = append(sets, "reconnected = ?")
		args = append(args, bool01(*p.Reconnected))
	}
	if p.ResultFW != nil {
		sets = append(sets, "result_fw = ?")
		args = append(args, *p.ResultFW)
	}
	if p.PushedAt != nil {
		sets = append(sets, "pushed_at = ?")
		args = append(args, *p.PushedAt)
	}
	if p.AckAt != nil {
		sets = append(sets, "ack_at = ?")
		args = append(args, *p.AckAt)
	}
	if p.DisconnectAt != nil {
		sets = append(sets, "disconnect_at = ?")
		args = append(args, *p.DisconnectAt)
	}
	if p.ReconnectAt != nil {
		sets = append(sets, "reconnect_at = ?")
		args = append(args, *p.ReconnectAt)
	}
	if p.BootAt != nil {
		sets = append(sets, "boot_at = ?")
		args = append(args, *p.BootAt)
	}
	if p.ResultAt != nil {
		sets = append(sets, "result_at = ?")
		args = append(args, *p.ResultAt)
	}
	args = append(args, id)
	_, err := s.db.ExecContext(ctx, `UPDATE a_emb_ota_log SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	return err
}
