// Package locate 提供 WiFi 扫点 → 经纬度解析（可插拔）。
// 当前实现：mock 固定坐标 + fingerprint 指纹缓存；后续可接高德/Google。
package locate

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"go.uber.org/zap"

	"pet-translate-gateway/config"
	"pet-translate-gateway/internal/model"
	redisx "pet-translate-gateway/internal/redis"
	"pet-translate-gateway/internal/util"
)

// Result 定位解析结果（对齐固件期望字段）。
type Result struct {
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
	Accuracy float64 `json:"accuracy"`
	Source   string  `json:"source,omitempty"` // mock / fingerprint
}

// Resolver WiFi 定位解析器接口。
type Resolver interface {
	Resolve(ctx context.Context, sn string, scan []model.WifiAP) (*Result, error)
}

// Service 默认解析实现。
type Service struct {
	cfg   config.LocateConfig
	redis *redisx.Client
	log   *zap.Logger
}

// New 创建定位解析服务。
func New(cfg config.LocateConfig, rdb *redisx.Client, log *zap.Logger) *Service {
	return &Service{cfg: cfg, redis: rdb, log: log}
}

// Resolve 根据周边 WiFi 解析坐标。
func (s *Service) Resolve(ctx context.Context, sn string, scan []model.WifiAP) (*Result, error) {
	if len(scan) == 0 {
		return nil, fmt.Errorf("wifi_scan 为空")
	}

	fp := Fingerprint(scan)

	if s.cfg.Provider == "fingerprint" || s.cfg.Provider == "mock" {
		if s.cfg.Provider == "fingerprint" {
			cached, err := s.redis.GetWifiFingerprint(ctx, fp)
			if err != nil {
				return nil, err
			}
			if cached != nil {
				res := &Result{
					Lat:      cached.Lat,
					Lng:      cached.Lng,
					Accuracy: cached.Accuracy,
					Source:   "fingerprint",
				}
				_ = s.redis.SaveLastLocation(ctx, sn, &redisx.CachedLocation{
					Lat: res.Lat, Lng: res.Lng, Accuracy: res.Accuracy,
					Source: res.Source, Updated: util.NowUTCUnix(),
				})
				return res, nil
			}
		}

		// 未命中或纯 mock：使用配置默认坐标，并写入指纹（便于同一环境稳定返回）
		res := &Result{
			Lat:      s.cfg.MockLat,
			Lng:      s.cfg.MockLng,
			Accuracy: s.cfg.MockAcc,
			Source:   "mock",
		}
		loc := &redisx.CachedLocation{
			Lat: res.Lat, Lng: res.Lng, Accuracy: res.Accuracy,
			Source: res.Source, Updated: util.NowUTCUnix(),
		}
		if s.cfg.Provider == "fingerprint" {
			if err := s.redis.SetWifiFingerprint(ctx, fp, loc, s.cfg.FingerprintTTL); err != nil {
				s.log.Warn("写入 WiFi 指纹失败", zap.String("sn", sn), zap.Error(err))
			}
		}
		_ = s.redis.SaveLastLocation(ctx, sn, loc)
		s.log.Info("定位解析完成",
			zap.String("sn", sn),
			zap.String("source", res.Source),
			zap.String("fp", fp[:8]),
			zap.Float64("lat", res.Lat),
			zap.Float64("lng", res.Lng),
			zap.Int("aps", len(scan)),
		)
		return res, nil
	}

	return nil, fmt.Errorf("未知 locate.provider: %s", s.cfg.Provider)
}

// BindFingerprint 管理接口：把某组 WiFi 指纹绑定到真实坐标（校准用）。
func (s *Service) BindFingerprint(ctx context.Context, scan []model.WifiAP, lat, lng, acc float64) (string, error) {
	if len(scan) == 0 {
		return "", fmt.Errorf("wifi_scan 为空")
	}
	if acc <= 0 {
		acc = s.cfg.MockAcc
	}
	fp := Fingerprint(scan)
	loc := &redisx.CachedLocation{
		Lat: lat, Lng: lng, Accuracy: acc,
		Source: "manual", Updated: util.NowUTCUnix(),
	}
	ttl := s.cfg.FingerprintTTL
	if ttl < 0 {
		ttl = 0
	}
	if err := s.redis.SetWifiFingerprint(ctx, fp, loc, ttl); err != nil {
		return "", err
	}
	return fp, nil
}

// Fingerprint 由 MAC 列表生成稳定指纹（忽略 RSSI，按 MAC 排序）。
func Fingerprint(scan []model.WifiAP) string {
	macs := make([]string, 0, len(scan))
	for _, ap := range scan {
		mac := strings.ToLower(strings.TrimSpace(ap.MAC))
		mac = strings.ReplaceAll(mac, "-", ":")
		if mac == "" {
			continue
		}
		macs = append(macs, mac)
	}
	sort.Strings(macs)
	h := sha1.Sum([]byte(strings.Join(macs, "|")))
	return hex.EncodeToString(h[:])
}
