// 后台管理：密码登录、固件元数据读写、固件直传 OSS、固件历史列表。
package api

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"pet-translate-gateway/config"
	ossx "pet-translate-gateway/internal/oss"
	redisx "pet-translate-gateway/internal/redis"
	"pet-translate-gateway/internal/store"
)

const maxFirmwareUpload = 64 << 20 // 64 MiB

// OnlineConn 本网关进程内 WebSocket 在线连接。
type OnlineConn struct {
	SN          string `json:"sn"`
	ConnID      string `json:"conn_id"`
	ConnectedAt int64  `json:"connected_at"`
}

// DeviceHub 管理端下发命令 / 列在线设备（由 ConnManager 实现，避免 api↔gateway 循环依赖）。
type DeviceHub interface {
	SendCmdToDevice(sn string, cmd interface{}) error
	OnlineList() []OnlineConn
}

// AdminHandler 后台管理 API。
type AdminHandler struct {
	cfg   *config.Config
	fw    *config.LiveFirmware
	oss   *ossx.Client
	store *store.Store
	redis *redisx.Client
	hub   DeviceHub
	log   *zap.Logger
	mu    sync.Mutex
	toks  map[string]time.Time
}

// NewAdminHandler 创建；admin.enabled=false 时仍可注册，接口返回 403。
func NewAdminHandler(cfg *config.Config, fw *config.LiveFirmware, oss *ossx.Client, st *store.Store, rdb *redisx.Client, log *zap.Logger) *AdminHandler {
	return &AdminHandler{
		cfg:   cfg,
		fw:    fw,
		oss:   oss,
		store: st,
		redis: rdb,
		log:   log,
		toks:  make(map[string]time.Time),
	}
}

// SetDeviceHub 注入设备连接管理（启动后调用）。
func (h *AdminHandler) SetDeviceHub(hub DeviceHub) {
	if h != nil {
		h.hub = hub
	}
}

// Register 注册管理路由。
func (h *AdminHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/admin/login", h.handleLogin)
	mux.HandleFunc("/api/admin/firmware", h.handleFirmware)
	mux.HandleFunc("/api/admin/firmware/upload", h.handleUpload)
	mux.HandleFunc("/api/admin/firmware/list", h.handleFirmwareList)
	mux.HandleFunc("/api/admin/firmware/activate", h.handleFirmwareActivate)
	mux.HandleFunc("/api/admin/firmware/delete", h.handleFirmwareDelete)
	mux.HandleFunc("/api/admin/ota/logs", h.handleOTALogs)
	mux.HandleFunc("/api/admin/ota/push", h.handleOTAPush)
	mux.HandleFunc("/api/admin/devices/online", h.handleOnlineDevices)
	h.registerStatic(mux)
}

func (h *AdminHandler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.cfg.Admin.Enabled {
		writeErr(w, http.StatusForbidden, "admin disabled")
		return
	}
	if h.cfg.Admin.Password == "" {
		writeErr(w, http.StatusForbidden, "admin password not configured")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Password), []byte(h.cfg.Admin.Password)) != 1 {
		writeErr(w, http.StatusUnauthorized, "invalid password")
		return
	}
	tok, err := genAdminToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "token generate failed")
		return
	}
	ttl := h.cfg.Admin.TokenTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	h.mu.Lock()
	h.toks[tok] = time.Now().Add(ttl)
	h.purgeLocked()
	h.mu.Unlock()
	writeOK(w, map[string]interface{}{
		"token":      tok,
		"expires_in": int(ttl.Seconds()),
	})
}

func (h *AdminHandler) handleFirmware(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.ensureFirmwareSeeded(r.Context())
		if h.store != nil {
			if pkg, err := h.store.GetCurrentFirmwarePackage(r.Context()); err == nil && pkg != nil {
				writeOK(w, fwPkgView(*pkg, true))
				return
			}
		}
		fw := h.currentFW()
		writeOK(w, fwView(fw))
	case http.MethodPut, http.MethodPost:
		var req struct {
			Version   string `json:"version"`
			URL       string `json:"url"`
			Force     int    `json:"force"`
			Size      int64  `json:"size"`
			MD5       string `json:"md5"`
			Changelog string `json:"changelog"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		cur := h.currentFW()
		next := cur
		if req.Version != "" {
			next.Version = req.Version
		}
		if req.URL != "" {
			next.URL = req.URL
		}
		if req.Size > 0 {
			next.Size = req.Size
		}
		if req.MD5 != "" {
			next.MD5 = req.MD5
		}
		next.Force = req.Force != 0
		next.Changelog = req.Changelog
		if next.Version == "" || next.URL == "" {
			writeErr(w, http.StatusBadRequest, "version and url required")
			return
		}
		if h.fw == nil {
			writeErr(w, http.StatusServiceUnavailable, "firmware live config unavailable")
			return
		}
		if err := h.fw.Update(next); err != nil {
			h.log.Error("写回固件配置失败", zap.Error(err))
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// 元数据保存：更新当前行，或插入新历史记录并标为当前
		if h.store != nil {
			ctx := r.Context()
			if curPkg, err := h.store.GetCurrentFirmwarePackage(ctx); err == nil && curPkg != nil {
				_ = h.store.UpdateFirmwarePackageMeta(ctx, curPkg.ID, next.Version, next.URL, next.MD5, next.Changelog, next.Size, next.Force)
			} else {
				id, ierr := h.store.InsertFirmwarePackage(ctx, &store.FirmwarePackage{
					Version: next.Version, URL: next.URL, Size: next.Size, MD5: next.MD5,
					Force: next.Force, Changelog: next.Changelog,
				})
				if ierr == nil && id > 0 {
					_ = h.store.SetCurrentFirmwarePackage(ctx, id)
				}
			}
		}
		writeOK(w, fwView(next))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *AdminHandler) handleFirmwareList(w http.ResponseWriter, r *http.Request) {
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
	h.ensureFirmwareSeeded(r.Context())
	limit := 100
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	list, err := h.store.ListFirmwarePackages(r.Context(), limit)
	if err != nil {
		h.log.Error("列出固件失败", zap.Error(err))
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var currentID int64
	for _, p := range list {
		if p.IsCurrent {
			currentID = p.ID
			break
		}
	}
	writeOK(w, map[string]interface{}{
		"list":       listView(list),
		"current_id": currentID,
		"total":      len(list),
	})
}

func (h *AdminHandler) handleFirmwareActivate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
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
	var req struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID <= 0 {
		writeErr(w, http.StatusBadRequest, "id required")
		return
	}
	pkg, err := h.store.GetFirmwarePackage(r.Context(), req.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if pkg == nil {
		writeErr(w, http.StatusNotFound, "firmware not found")
		return
	}
	if err := h.store.SetCurrentFirmwarePackage(r.Context(), req.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if h.fw != nil {
		next := config.FirmwareConfig{
			Version: pkg.Version, URL: pkg.URL, Force: pkg.Force,
			Size: pkg.Size, MD5: pkg.MD5, Changelog: pkg.Changelog,
		}
		if err := h.fw.Update(next); err != nil {
			h.log.Error("激活后写回 yaml 失败", zap.Error(err), zap.Int64("id", req.ID))
			writeErr(w, http.StatusInternalServerError, "activated in db but save config failed: "+err.Error())
			return
		}
	}
	h.log.Info("固件已设为当前", zap.Int64("id", req.ID), zap.String("version", pkg.Version))
	writeOK(w, fwPkgView(*pkg, true))
}

func (h *AdminHandler) handleFirmwareDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
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
	var id int64
	if r.Method == http.MethodDelete {
		id, _ = strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
	} else {
		var req struct {
			ID int64 `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		id = req.ID
	}
	if id <= 0 {
		writeErr(w, http.StatusBadRequest, "id required")
		return
	}
	pkg, err := h.store.GetFirmwarePackage(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if pkg == nil {
		writeErr(w, http.StatusNotFound, "firmware not found")
		return
	}
	if pkg.IsCurrent {
		writeErr(w, http.StatusBadRequest, "cannot delete current firmware; activate another first")
		return
	}
	if err := h.store.DeleteFirmwarePackage(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, map[string]interface{}{"deleted_id": id})
}

func (h *AdminHandler) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.auth(w, r) {
		return
	}
	if h.oss == nil {
		writeErr(w, http.StatusServiceUnavailable, "oss not enabled")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFirmwareUpload+1<<20)
	if err := r.ParseMultipartForm(maxFirmwareUpload); err != nil {
		writeErr(w, http.StatusBadRequest, "multipart too large or invalid")
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "file required")
		return
	}
	defer file.Close()

	version := strings.TrimSpace(r.FormValue("version"))
	if version == "" {
		writeErr(w, http.StatusBadRequest, "version required")
		return
	}
	changelog := r.FormValue("changelog")
	force := r.FormValue("force") == "1" || strings.EqualFold(r.FormValue("force"), "true")

	data, err := io.ReadAll(io.LimitReader(file, maxFirmwareUpload+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read file failed")
		return
	}
	if len(data) == 0 {
		writeErr(w, http.StatusBadRequest, "empty file")
		return
	}
	if len(data) > maxFirmwareUpload {
		writeErr(w, http.StatusRequestEntityTooLarge, "file exceeds 64MB")
		return
	}

	sum := md5.Sum(data)
	md5hex := hex.EncodeToString(sum[:])
	filename := path.Base(hdr.Filename)
	if filename == "" || filename == "." {
		filename = "firmware.bin"
	}
	object := h.oss.BuildFirmwareKey(version, filename)
	ct := hdr.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	url, err := h.oss.UploadBytes(object, data, ct)
	if err != nil {
		h.log.Error("固件上传 OSS 失败", zap.Error(err))
		writeErr(w, http.StatusBadGateway, "oss upload failed: "+err.Error())
		return
	}

	next := config.FirmwareConfig{
		Version:   version,
		URL:       url,
		Force:     force,
		Size:      int64(len(data)),
		MD5:       md5hex,
		Changelog: changelog,
	}
	if h.fw == nil {
		writeErr(w, http.StatusServiceUnavailable, "firmware live config unavailable")
		return
	}
	if err := h.fw.Update(next); err != nil {
		h.log.Error("上传后写回配置失败", zap.Error(err), zap.String("url", url))
		writeErr(w, http.StatusInternalServerError, "uploaded but save config failed: "+err.Error())
		return
	}

	var pkgID int64
	if h.store != nil {
		id, ierr := h.store.InsertFirmwarePackage(r.Context(), &store.FirmwarePackage{
			Version: version, URL: url, Size: int64(len(data)), MD5: md5hex,
			Force: force, Changelog: changelog, Filename: filename,
		})
		if ierr != nil {
			h.log.Warn("固件已上传但写入历史表失败", zap.Error(ierr), zap.String("version", version))
		} else {
			pkgID = id
			if err := h.store.SetCurrentFirmwarePackage(r.Context(), id); err != nil {
				h.log.Warn("固件历史写入后设当前失败", zap.Error(err), zap.Int64("id", id))
			}
		}
	}

	h.log.Info("固件已上传并生效",
		zap.String("version", version),
		zap.String("url", url),
		zap.Int("bytes", len(data)),
		zap.String("md5", md5hex),
		zap.Int64("package_id", pkgID),
	)
	view := fwView(next)
	view["id"] = pkgID
	view["filename"] = filename
	view["is_current"] = true
	writeOK(w, view)
}

func (h *AdminHandler) currentFW() config.FirmwareConfig {
	if h.fw != nil {
		return h.fw.Get()
	}
	return h.cfg.Firmware
}

// ensureFirmwareSeeded 库空时把 yaml 当前固件灌进历史表，便于列表看到「之前的数据」。
func (h *AdminHandler) ensureFirmwareSeeded(ctx context.Context) {
	if h.store == nil {
		return
	}
	n, err := h.store.CountFirmwarePackages(ctx)
	if err != nil || n > 0 {
		return
	}
	fw := h.currentFW()
	if strings.TrimSpace(fw.Version) == "" || strings.TrimSpace(fw.URL) == "" {
		return
	}
	id, err := h.store.InsertFirmwarePackage(ctx, &store.FirmwarePackage{
		Version: fw.Version, URL: fw.URL, Size: fw.Size, MD5: fw.MD5,
		Force: fw.Force, Changelog: fw.Changelog,
	})
	if err != nil || id <= 0 {
		if err != nil {
			h.log.Warn("灌入历史固件失败", zap.Error(err))
		}
		return
	}
	_ = h.store.SetCurrentFirmwarePackage(ctx, id)
	h.log.Info("已将 config.yaml 固件灌入历史表", zap.Int64("id", id), zap.String("version", fw.Version))
}

func fwView(fw config.FirmwareConfig) map[string]interface{} {
	force := 0
	if fw.Force {
		force = 1
	}
	return map[string]interface{}{
		"version":   fw.Version,
		"url":       fw.URL,
		"force":     force,
		"size":      fw.Size,
		"md5":       fw.MD5,
		"changelog": fw.Changelog,
	}
}

func fwPkgView(p store.FirmwarePackage, asCurrent bool) map[string]interface{} {
	force := 0
	if p.Force {
		force = 1
	}
	cur := p.IsCurrent || asCurrent
	return map[string]interface{}{
		"id":         p.ID,
		"version":    p.Version,
		"url":        p.URL,
		"force":      force,
		"size":       p.Size,
		"md5":        p.MD5,
		"changelog":  p.Changelog,
		"filename":   p.Filename,
		"is_current": cur,
		"created_at": p.CreatedAt,
	}
}

func listView(list []store.FirmwarePackage) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(list))
	for _, p := range list {
		out = append(out, fwPkgView(p, false))
	}
	return out
}

func (h *AdminHandler) auth(w http.ResponseWriter, r *http.Request) bool {
	if !h.cfg.Admin.Enabled {
		writeErr(w, http.StatusForbidden, "admin disabled")
		return false
	}
	tok := r.Header.Get("X-Admin-Token")
	if tok == "" {
		auth := r.Header.Get("Authorization")
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			tok = strings.TrimSpace(auth[7:])
		}
	}
	if tok == "" {
		writeErr(w, http.StatusUnauthorized, "admin token required")
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.purgeLocked()
	exp, ok := h.toks[tok]
	if !ok || time.Now().After(exp) {
		delete(h.toks, tok)
		writeErr(w, http.StatusUnauthorized, "admin token invalid")
		return false
	}
	return true
}

func (h *AdminHandler) purgeLocked() {
	now := time.Now()
	for k, exp := range h.toks {
		if now.After(exp) {
			delete(h.toks, k)
		}
	}
}

func genAdminToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
