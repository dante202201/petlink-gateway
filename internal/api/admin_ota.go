package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"pet-translate-gateway/internal/model"
	"pet-translate-gateway/internal/store"
)

func (h *AdminHandler) handleOTALogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.auth(w, r) {
		return
	}
	if h.store == nil {
		writeErr(w, http.StatusServiceUnavailable, "database not enabled")
		return
	}
	limit := 100
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	sn := strings.TrimSpace(r.URL.Query().Get("sn"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	list, err := h.store.ListOTALogs(r.Context(), sn, status, limit)
	if err != nil {
		h.log.Error("列出 OTA 日志失败", zap.Error(err))
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, map[string]interface{}{
		"list":  otaLogViewList(list),
		"total": len(list),
	})
}

func (h *AdminHandler) handleOTAPush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.auth(w, r) {
		return
	}
	if h.hub == nil {
		writeErr(w, http.StatusServiceUnavailable, "device hub unavailable")
		return
	}
	var req struct {
		SN      string      `json:"sn"`
		URL     string      `json:"url"`
		Version string      `json:"version"`
		Force   interface{} `json:"force"` // true/false，兼容 0/1
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	req.SN = strings.TrimSpace(req.SN)
	if req.SN == "" {
		writeErr(w, http.StatusBadRequest, "sn required")
		return
	}
	force := parseAdminForce(req.Force)
	url := strings.TrimSpace(req.URL)
	version := strings.TrimSpace(req.Version)
	if url == "" || version == "" {
		fw := h.currentFW()
		if url == "" {
			url = strings.TrimSpace(fw.URL)
		}
		if version == "" {
			version = strings.TrimSpace(fw.Version)
		}
	}
	if url == "" || version == "" {
		writeErr(w, http.StatusBadRequest, "version and url required (or publish a current firmware first)")
		return
	}
	fw := h.currentFW()
	if h.store != nil {
		if pkg, err := h.store.GetCurrentFirmwarePackage(r.Context()); err == nil && pkg != nil {
			if strings.TrimSpace(pkg.URL) != "" {
				fw.URL = pkg.URL
			}
			if strings.TrimSpace(pkg.Version) != "" {
				fw.Version = pkg.Version
			}
			if pkg.Size > 0 {
				fw.Size = pkg.Size
			}
			if strings.TrimSpace(pkg.MD5) != "" {
				fw.MD5 = pkg.MD5
			}
		}
	}
	data := map[string]interface{}{
		"url":           url,
		"version":       version,
		"force":         force,
		"accept_ranges": true,
	}
	if fw.Size > 0 {
		data["size"] = fw.Size
	}
	if md5 := strings.TrimSpace(fw.MD5); md5 != "" {
		data["md5"] = md5
	}
	// 标准模式带 download_url。下载接口代理成 200/206，设备不跟随 302。
	base := strings.TrimRight(strings.TrimSpace(h.cfg.Server.PublicBaseURL), "/")
	base = strings.Replace(base, "wss://", "https://", 1)
	base = strings.Replace(base, "ws://", "http://", 1)
	if base != "" {
		data["download_url"] = base + "/api/device/firmware/download"
	}
	cmd := &model.DeviceCommand{Type: "cmd", Cmd: string(model.CmdOTAUpdate), Data: data}
	if err := h.hub.SendCmdToDevice(req.SN, cmd); err != nil {
		h.log.Error("管理端下发 OTA 失败", zap.String("sn", req.SN), zap.Error(err))
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.log.Info("管理端已下发 ota_update",
		zap.String("sn", req.SN),
		zap.String("version", version),
		zap.String("url", url),
		zap.Bool("force", force),
		zap.Any("size", data["size"]),
		zap.Any("md5", data["md5"]),
	)
	out := map[string]interface{}{
		"sn":      req.SN,
		"cmd":     "ota_update",
		"url":     url,
		"version": version,
		"force":   force,
		"data":    data,
	}
	if h.store != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		if rec, err := h.store.GetPendingOTABySN(ctx, req.SN); err == nil && rec != nil {
			out["ota_log_id"] = rec.ID
			out["ota_status"] = rec.Status
		}
		cancel()
	}
	writeOK(w, out)
}

func (h *AdminHandler) handleOnlineDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.auth(w, r) {
		return
	}
	seen := map[string]struct{}{}
	items := make([]map[string]interface{}, 0)

	addSN := func(sn, connID string, connectedAt int64) {
		sn = strings.TrimSpace(sn)
		if sn == "" {
			return
		}
		if _, ok := seen[sn]; ok {
			return
		}
		seen[sn] = struct{}{}
		row := map[string]interface{}{
			"sn":           sn,
			"online":       true,
			"conn_id":      connID,
			"connected_at": connectedAt,
			"token":        "",
			"fw_ver":       "",
			"b":            0,
			"rssi":         0,
			"last_seen_at": int64(0),
		}
		if h.redis != nil {
			if v, err := h.redis.GetDeviceStatusViewWithToken(r.Context(), sn); err == nil && v != nil {
				row["token"] = v.Token
				row["fw_ver"] = v.FWVer
				row["b"] = v.Battery
				row["charge"] = v.Charge
				row["rssi"] = v.RSSI
				row["last_seen_at"] = v.LastSeenAt
				row["connected_at"] = v.ConnectedAt
				row["conn_id"] = v.ConnID
				row["online"] = v.Online
				row["mac"] = v.MAC
				row["pid"] = v.PID
			} else if tok, err := h.redis.GetDeviceToken(r.Context(), sn); err == nil {
				row["token"] = tok
			}
			if connID != "" {
				row["conn_id"] = connID
			}
			if connectedAt > 0 {
				row["connected_at"] = connectedAt
			}
			row["online"] = true
		}
		items = append(items, row)
	}

	if h.hub != nil {
		for _, c := range h.hub.OnlineList() {
			addSN(c.SN, c.ConnID, c.ConnectedAt)
		}
	}
	if h.redis != nil {
		if sns, err := h.redis.ListOnlineSNs(r.Context()); err != nil {
			h.log.Warn("扫描 Redis 在线设备失败", zap.Error(err))
		} else {
			for _, sn := range sns {
				addSN(sn, "", 0)
			}
		}
	}
	sort.Slice(items, func(i, j int) bool {
		si, _ := items[i]["sn"].(string)
		sj, _ := items[j]["sn"].(string)
		return si < sj
	})
	writeOK(w, map[string]interface{}{
		"list":  items,
		"count": len(items),
	})
}

func otaLogViewList(list []store.OTALog) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(list))
	for _, r := range list {
		out = append(out, otaLogView(r))
	}
	return out
}

func otaLogView(r store.OTALog) map[string]interface{} {
	return map[string]interface{}{
		"id":            r.ID,
		"sn":            r.SN,
		"from_fw":       r.FromFW,
		"target_fw":     r.TargetFW,
		"url":           r.URL,
		"force":         r.Force,
		"status":        r.Status,
		"fail_reason":   r.FailReason,
		"disconnected":  r.Disconnected,
		"reconnected":   r.Reconnected,
		"result_fw":     r.ResultFW,
		"pushed_at":     r.PushedAt,
		"ack_at":        r.AckAt,
		"disconnect_at": r.DisconnectAt,
		"reconnect_at":  r.ReconnectAt,
		"boot_at":       r.BootAt,
		"result_at":     r.ResultAt,
		"created_at":    r.CreatedAt,
		"updated_at":    r.UpdatedAt,
	}
}

// parseAdminForce 管理端 force 字段：true/false，兼容 0/1。
func parseAdminForce(v interface{}) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case int:
		return t != 0
	case int64:
		return t != 0
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i != 0
		}
	case string:
		s := strings.TrimSpace(strings.ToLower(t))
		return s == "1" || s == "true"
	}
	return false
}
