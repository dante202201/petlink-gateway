package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// FirmwarePackage 固件包记录（历史列表；is_current=1 为设备查询当前生效包）。
type FirmwarePackage struct {
	ID        int64  `json:"id"`
	Version   string `json:"version"`
	URL       string `json:"url"`
	Size      int64  `json:"size"`
	MD5       string `json:"md5"`
	Force     bool   `json:"force"`
	Changelog string `json:"changelog"`
	Filename  string `json:"filename"`
	IsCurrent bool   `json:"is_current"`
	CreatedAt int64  `json:"created_at"` // Unix 秒
}

// CountFirmwarePackages 固件包数量。
func (s *Store) CountFirmwarePackages(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("database disabled")
	}
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM a_emb_firmware`).Scan(&n)
	return n, err
}

// ListFirmwarePackages 按 id 倒序列出固件包。
func (s *Store) ListFirmwarePackages(ctx context.Context, limit int) ([]FirmwarePackage, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("database disabled")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	q := `
SELECT id, IFNULL(version,''), IFNULL(url,''), IFNULL(size,0), IFNULL(md5,''),
       IFNULL(force_flag,0), IFNULL(changelog,''), IFNULL(filename,''),
       IFNULL(is_current,0), IFNULL(created_at,0)
FROM a_emb_firmware
ORDER BY id DESC
LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]FirmwarePackage, 0)
	for rows.Next() {
		var p FirmwarePackage
		var force, cur int
		if err := rows.Scan(
			&p.ID, &p.Version, &p.URL, &p.Size, &p.MD5,
			&force, &p.Changelog, &p.Filename, &cur, &p.CreatedAt,
		); err != nil {
			return nil, err
		}
		p.Force = force != 0
		p.IsCurrent = cur != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetFirmwarePackage 按 id 取一条。
func (s *Store) GetFirmwarePackage(ctx context.Context, id int64) (*FirmwarePackage, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("database disabled")
	}
	q := `
SELECT id, IFNULL(version,''), IFNULL(url,''), IFNULL(size,0), IFNULL(md5,''),
       IFNULL(force_flag,0), IFNULL(changelog,''), IFNULL(filename,''),
       IFNULL(is_current,0), IFNULL(created_at,0)
FROM a_emb_firmware WHERE id = ?`
	var p FirmwarePackage
	var force, cur int
	err := s.db.QueryRowContext(ctx, q, id).Scan(
		&p.ID, &p.Version, &p.URL, &p.Size, &p.MD5,
		&force, &p.Changelog, &p.Filename, &cur, &p.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.Force = force != 0
	p.IsCurrent = cur != 0
	return &p, nil
}

// GetCurrentFirmwarePackage 当前生效包；无则 nil。
func (s *Store) GetCurrentFirmwarePackage(ctx context.Context) (*FirmwarePackage, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("database disabled")
	}
	q := `
SELECT id, IFNULL(version,''), IFNULL(url,''), IFNULL(size,0), IFNULL(md5,''),
       IFNULL(force_flag,0), IFNULL(changelog,''), IFNULL(filename,''),
       IFNULL(is_current,0), IFNULL(created_at,0)
FROM a_emb_firmware WHERE is_current = 1
ORDER BY id DESC LIMIT 1`
	var p FirmwarePackage
	var force, cur int
	err := s.db.QueryRowContext(ctx, q).Scan(
		&p.ID, &p.Version, &p.URL, &p.Size, &p.MD5,
		&force, &p.Changelog, &p.Filename, &cur, &p.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.Force = force != 0
	p.IsCurrent = true
	return &p, nil
}

// InsertFirmwarePackage 插入一条；不自动设为当前（由 SetCurrentFirmwarePackage 处理）。
func (s *Store) InsertFirmwarePackage(ctx context.Context, p *FirmwarePackage) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("database disabled")
	}
	if p == nil {
		return 0, fmt.Errorf("nil package")
	}
	now := p.CreatedAt
	if now <= 0 {
		now = time.Now().Unix()
	}
	force := 0
	if p.Force {
		force = 1
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO a_emb_firmware
  (version, url, size, md5, force_flag, changelog, filename, is_current, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?)`,
		p.Version, p.URL, p.Size, p.MD5, force, p.Changelog, p.Filename, now,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetCurrentFirmwarePackage 将指定 id 标为当前，并清除其它行的 is_current。
func (s *Store) SetCurrentFirmwarePackage(ctx context.Context, id int64) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("database disabled")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM a_emb_firmware WHERE id = ?`, id).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("firmware id=%d not found", id)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE a_emb_firmware SET is_current = 0 WHERE is_current = 1`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE a_emb_firmware SET is_current = 1 WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateFirmwarePackageMeta 更新已有记录的元数据（不改 url/size/md5 时可部分更新）。
func (s *Store) UpdateFirmwarePackageMeta(ctx context.Context, id int64, version, url, md5, changelog string, size int64, force bool) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("database disabled")
	}
	forceN := 0
	if force {
		forceN = 1
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE a_emb_firmware
SET version = ?, url = ?, size = ?, md5 = ?, force_flag = ?, changelog = ?
WHERE id = ?`,
		version, url, size, md5, forceN, changelog, id,
	)
	return err
}

// DeleteFirmwarePackage 删除一条；若删的是当前包，调用方需另设当前或清空 yaml。
func (s *Store) DeleteFirmwarePackage(ctx context.Context, id int64) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("database disabled")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM a_emb_firmware WHERE id = ?`, id)
	return err
}
