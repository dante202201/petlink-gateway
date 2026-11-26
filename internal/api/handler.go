// Package api 实现固件 HTTP 接口：激活、Token 刷新、WiFi 定位、固件版本查询。
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"pet-translate-gateway/config"
	"pet-translate-gateway/internal/locate"
	"pet-translate-gateway/internal/model"
	redisx "pet-translate-gateway/internal/redis"
	"pet-translate-gateway/internal/store"
	"pet-translate-gateway/internal/util"
)

// Handler 设备 HTTP API。
type Handler struct {
	cfg      *config.Config
	firmware *config.LiveFirmware
	store    *store.Store
	redis    *redisx.Client
	locate   *locate.Service
	log      *zap.Logger
}

// New 创建 API Handler。fw 可为 nil（此时读 cfg.Firmware 静态值）；st 可为 nil（无库时退回 yaml）。
func New(cfg *config.Config, rdb *redisx.Client, loc *locate.Service, log *zap.Logger, fw *config.LiveFirmware, st *store.Store) *Handler {
	return &Handler{cfg: cfg, redis: rdb, locate: loc, log: log, firmware: fw, store: st}
}

// Register 注册固件约定路径（同时挂 v1 别名便于管理）。
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/device/activate", h.handleActivate)
	mux.HandleFunc("/api/device/refresh", h.handleRefresh)
	mux.HandleFunc("/api/device/locate", h.handleLocate)
	mux.HandleFunc("/api/device/geolocate", h.handleLocate) // 固件别名
	mux.HandleFunc("/api/device/firmware", h.handleFirmware)
	mux.HandleFunc("/api/device/firmware/download", h.handleFirmwareDownload)

	// 别名
	mux.HandleFunc("/api/v1/device/activate", h.handleActivate)
	mux.HandleFunc("/api/v1/device/refresh", h.handleRefresh)
	mux.HandleFunc("/api/v1/device/locate", h.handleLocate)
	mux.HandleFunc("/api/v1/device/geolocate", h.handleLocate)
	mux.HandleFunc("/api/v1/device/firmware", h.handleFirmware)
	mux.HandleFunc("/api/v1/device/firmware/download", h.handleFirmwareDownload)

	// 联调：手动绑定 WiFi 指纹到真实坐标
	mux.HandleFunc("/api/v1/locate/bind", h.handleBindFingerprint)
}

// ensureFormAPI 同 admin.go ensureForm：确保 POST/PUT 非 JSON 请求，r.FormValue 能读到 form-data / urlencoded
func ensureFormAPI(r *http.Request) {
	if r.Method == http.MethodGet {
		return
	}
	ct := r.Header.Get("Content-Type")
	if strings.Contains(ct, "application/x-www-form-urlencoded") {
		_ = r.ParseForm()
		return
	}
	if strings.Contains(ct, "multipart/form-data") {
		_ = r.ParseMultipartForm(32 << 20)
		return
	}
}

// --- activate ---

type activateReq struct {
	SN    string `json:"sn"`
	FWVer string `json:"fw_ver"`
	MAC   string `json:"mac"`
}

type activateResp struct {
	Token     string `json:"token"`
	ServerURL string `json:"server_url"`
}

func (h *Handler) handleActivate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req activateReq
	ct := r.Header.Get("Content-Type")
	if strings.Contains(ct, "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid json")
			return
		}
	} else {
		ensureFormAPI(r)
		req.SN = strings.TrimSpace(r.FormValue("sn"))
		req.FWVer = strings.TrimSpace(r.FormValue("fw_ver"))
		if req.FWVer == "" {
			req.FWVer = strings.TrimSpace(r.FormValue("fw"))
		}
		req.MAC = strings.TrimSpace(r.FormValue("mac"))
	}
	if req.SN == "" {
		writeErr(w, http.StatusBadRequest, "sn required")
		return
	}

	token, err := genToken(h.cfg.Device.TokenBytes)
	if err != nil {
		h.log.Error("生成 token 失败", zap.Error(err))
		writeErr(w, http.StatusInternalServerError, "token generate failed")
		return
	}

	ctx := r.Context()
	if err := h.redis.SaveDeviceToken(ctx, req.SN, token); err != nil {
		h.log.Error("保存 token 失败", zap.String("sn", req.SN), zap.Error(err))
		writeErr(w, http.StatusInternalServerError, "save token failed")
		return
	}

	now := util.NowUTCUnix()
	meta := &redisx.DeviceMeta{
		SN: req.SN, FWVer: req.FWVer, MAC: req.MAC,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := h.redis.SaveDeviceMeta(ctx, meta); err != nil {
		h.log.Warn("保存设备 meta 失败", zap.String("sn", req.SN), zap.Error(err))
	}

	resp := activateResp{
		Token:     token,
		ServerURL: h.cfg.WSServerURL(),
	}
	h.log.Info("设备激活成功",
		zap.String("sn", req.SN),
		zap.String("fw", req.FWVer),
		zap.String("mac", req.MAC),
		zap.String("server_url", resp.ServerURL),
	)
	writeOK(w, resp)
}

// --- refresh ---

type refreshReq struct {
	SN    string `json:"sn"`
	Token string `json:"token"` // 旧 token
}

type refreshResp struct {
	Token string `json:"token"`
}

func (h *Handler) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req refreshReq
	ct := r.Header.Get("Content-Type")
	if strings.Contains(ct, "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid json")
			return
		}
	} else {
		ensureFormAPI(r)
		req.SN = strings.TrimSpace(r.FormValue("sn"))
		req.Token = strings.TrimSpace(r.FormValue("token"))
	}
	if req.SN == "" || req.Token == "" {
		writeErr(w, http.StatusBadRequest, "sn and token required")
		return
	}

	ctx := r.Context()
	ok, err := h.redis.ValidateDeviceToken(ctx, req.SN, req.Token)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "auth service unavailable")
		return
	}
	if !ok {
		h.log.Warn("token 刷新鉴权失败", zap.String("sn", req.SN))
		writeErr(w, http.StatusUnauthorized, "token invalid")
		return
	}

	newToken, err := genToken(h.cfg.Device.TokenBytes)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "token generate failed")
		return
	}
	if err := h.redis.SaveDeviceToken(ctx, req.SN, newToken); err != nil {
		writeErr(w, http.StatusInternalServerError, "save token failed")
		return
	}

	h.log.Info("token 刷新成功", zap.String("sn", req.SN))
	writeOK(w, refreshResp{Token: newToken})
}

// --- locate / geolocate ---

type locateResp struct {
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
	Accuracy float64 `json:"accuracy"`
}

func (h *Handler) handleLocate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var sn string
	var scan []model.WifiAP
	ct := r.Header.Get("Content-Type")
	if strings.Contains(ct, "application/json") {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeErr(w, http.StatusBadRequest, "read body failed")
			return
		}
		parsedSN, parsedScan, err := parseLocateBody(body)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		sn = parsedSN
		scan = parsedScan
	} else {
		ensureFormAPI(r)
		sn = strings.TrimSpace(r.FormValue("sn"))
		// form-data：把 wifi_scan 当 JSON 字符串读
		wifiScan := strings.TrimSpace(r.FormValue("wifi_scan"))
		if wifiScan == "" {
			wifiScan = strings.TrimSpace(r.FormValue("wifiAccessPoints"))
		}
		if wifiScan == "" {
			wifiScan = strings.TrimSpace(r.FormValue("wifi_list"))
		}
		if wifiScan != "" {
			bodyMap := map[string]json.RawMessage{
				"wifi_scan": json.RawMessage(wifiScan),
			}
			if s, err := model.ExtractWifiScan(bodyMap); err == nil {
				scan = []model.WifiAP(s)
			}
		}
	}

	if sn == "" {
		writeErr(w, http.StatusBadRequest, "sn required")
		return
	}
	if len(scan) == 0 {
		writeErr(w, http.StatusBadRequest, "wifi_scan required")
		return
	}

	res, err := h.locate.Resolve(r.Context(), sn, scan)
	if err != nil {
		h.log.Warn("定位解析失败", zap.String("sn", sn), zap.Error(err))
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w, locateResp{
		Lat: res.Lat, Lng: res.Lng, Accuracy: res.Accuracy,
	})
}

// parseLocateBody 兼容：
// {"sn","wifi_scan":[{mac,rssi}]}
// {"sn","wifiAccessPoints":[{macAddress,signalStrength}]}
// {"sn","wifi_list":[{bssid,rssi}]}
func parseLocateBody(body []byte) (string, []model.WifiAP, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", nil, fmt.Errorf("invalid json")
	}
	sn := ""
	for _, k := range []string{"sn", "device_sn", "deviceSn", "device_id"} {
		if v, ok := raw[k]; ok {
			_ = json.Unmarshal(v, &sn)
			if sn != "" {
				break
			}
		}
	}
	scan, err := model.ExtractWifiScan(raw)
	if err != nil {
		return sn, nil, err
	}
	return sn, []model.WifiAP(scan), nil
}

// --- firmware latest ---

type firmwareReq struct {
	SN    string `json:"sn"`
	Token string `json:"token"`
	FWVer string `json:"fw_ver"`
}

type firmwareResp struct {
	Version      string `json:"version"`
	URL          string `json:"url"`                    // OSS/CDN 直链
	Force        bool   `json:"force"`
	Size         int64  `json:"size,omitempty"`
	MD5          string `json:"md5,omitempty"`
	AcceptRanges bool   `json:"accept_ranges"`
	DownloadURL  string `json:"download_url,omitempty"` // 可选；网关鉴权下载，无则不返回
	Changelog    string `json:"changelog,omitempty"`
}

// handleFirmware 返回当前最新可用固件，无需鉴权。
// 优先 a_emb_firmware.is_current，无库时退回 yaml。不做版本比对。
func (h *Handler) handleFirmware(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	_, _, cur := parseDeviceAuth(r, true)

	fw, src, err := h.resolveCurrentFirmware(r.Context())
	if err != nil || fw.Version == "" || fw.URL == "" {
		writeErr(w, http.StatusNotFound, "firmware not configured")
		return
	}

	// 标准模式始终带网关下载地址。该接口只回 200/206 文件流，不回 302。
	dl := publicHTTPBase(r) + "/api/device/firmware/download"
	mode := "proxy"
	h.log.Info("固件版本查询",
		zap.String("device_fw_ver", cur),
		zap.String("latest", fw.Version),
		zap.String("source", src),
		zap.String("download_mode", mode),
		zap.String("download_url", dl),
	)
	writeOK(w, firmwareResp{
		Version:      fw.Version,
		URL:          fw.URL,
		Force:        fw.Force,
		Size:         fw.Size,
		MD5:          fw.MD5,
		AcceptRanges: true,
		DownloadURL:  dl,
		Changelog:    fw.Changelog,
	})
}

// resolveCurrentFirmware 当前对设备生效的固件：DB 当前包优先，否则 yaml。
// 库空且 yaml 有配置时自动灌一条并标为当前，与后台管理打通。
func (h *Handler) resolveCurrentFirmware(ctx context.Context) (config.FirmwareConfig, string, error) {
	if h.store != nil {
		pkg, err := h.store.GetCurrentFirmwarePackage(ctx)
		if err != nil {
			h.log.Warn("读固件当前版本失败，回退 yaml", zap.Error(err))
		} else if pkg != nil && strings.TrimSpace(pkg.Version) != "" && strings.TrimSpace(pkg.URL) != "" {
			return config.FirmwareConfig{
				Version:   pkg.Version,
				URL:       pkg.URL,
				Force:     pkg.Force,
				Size:      pkg.Size,
				MD5:       pkg.MD5,
				Changelog: pkg.Changelog,
			}, "db", nil
		}
		// 库无当前包：尝试用 yaml 灌库一次
		yamlFW := h.cfg.Firmware
		if h.firmware != nil {
			yamlFW = h.firmware.Get()
		}
		if n, cerr := h.store.CountFirmwarePackages(ctx); cerr == nil && n == 0 &&
			strings.TrimSpace(yamlFW.Version) != "" && strings.TrimSpace(yamlFW.URL) != "" {
			id, ierr := h.store.InsertFirmwarePackage(ctx, &store.FirmwarePackage{
				Version: yamlFW.Version, URL: yamlFW.URL, Size: yamlFW.Size, MD5: yamlFW.MD5,
				Force: yamlFW.Force, Changelog: yamlFW.Changelog,
			})
			if ierr == nil && id > 0 {
				_ = h.store.SetCurrentFirmwarePackage(ctx, id)
				h.log.Info("设备查询时已将 yaml 固件灌入历史表", zap.Int64("id", id), zap.String("version", yamlFW.Version))
				return yamlFW, "db", nil
			}
		}
	}
	fw := h.cfg.Firmware
	if h.firmware != nil {
		fw = h.firmware.Get()
	}
	if fw.Version == "" || fw.URL == "" {
		return config.FirmwareConfig{}, "none", fmt.Errorf("firmware not configured")
	}
	return fw, "yaml", nil
}

// --- bind fingerprint (admin) ---

func (h *Handler) handleBindFingerprint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var scan []model.WifiAP
	var lat, lng, acc float64
	ct := r.Header.Get("Content-Type")
	if strings.Contains(ct, "application/json") {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeErr(w, http.StatusBadRequest, "read body failed")
			return
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(body, &raw); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		s, err := model.ExtractWifiScan(raw)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		scan = []model.WifiAP(s)
		_ = json.Unmarshal(raw["lat"], &lat)
		_ = json.Unmarshal(raw["lng"], &lng)
		_ = json.Unmarshal(raw["accuracy"], &acc)
	} else {
		ensureFormAPI(r)
		// form-data：lat / lng / accuracy 直接读；wifi_scan / wifiAccessPoints / wifi_list 当 JSON 字符串
		latStr := strings.TrimSpace(r.FormValue("lat"))
		lngStr := strings.TrimSpace(r.FormValue("lng"))
		accStr := strings.TrimSpace(r.FormValue("accuracy"))
		if accStr == "" {
			accStr = strings.TrimSpace(r.FormValue("acc"))
		}
		fmt.Sscanf(latStr, "%f", &lat)
		fmt.Sscanf(lngStr, "%f", &lng)
		fmt.Sscanf(accStr, "%f", &acc)
		wifiScan := strings.TrimSpace(r.FormValue("wifi_scan"))
		if wifiScan == "" {
			wifiScan = strings.TrimSpace(r.FormValue("wifiAccessPoints"))
		}
		if wifiScan == "" {
			wifiScan = strings.TrimSpace(r.FormValue("wifi_list"))
		}
		if wifiScan != "" {
			bodyMap := map[string]json.RawMessage{
				"wifi_scan": json.RawMessage(wifiScan),
			}
			if s, err := model.ExtractWifiScan(bodyMap); err == nil {
				scan = []model.WifiAP(s)
			}
		}
	}

	if len(scan) == 0 {
		writeErr(w, http.StatusBadRequest, "wifi_scan required")
		return
	}
	fp, err := h.locate.BindFingerprint(r.Context(), scan, lat, lng, acc)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w, map[string]interface{}{
		"ok":          true,
		"fingerprint": fp,
		"lat":         lat,
		"lng":         lng,
	})
}

func genToken(n int) (string, error) {
	if n <= 0 {
		n = 16
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeOK 成功：{"code":1,"data":...}（业务成功=1；任何失败=0，HTTP 头保留 4xx/5xx）
func writeOK(w http.ResponseWriter, data interface{}) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"code": 1, "data": data})
}

// writeErr 失败：{"code":0,"msg":"..."}；401=令牌无效，429=过频，5xx=服务端错误。
func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]interface{}{"code": 0, "msg": msg})
}
