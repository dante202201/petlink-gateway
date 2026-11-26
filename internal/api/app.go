package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"go.uber.org/zap"

	redisx "pet-translate-gateway/internal/redis"
	"pet-translate-gateway/internal/store"
	"pet-translate-gateway/internal/util"
)

// AppHandler App 业务读取接口（从数据库拉设备上行音频）。
type AppHandler struct {
	store *store.Store
	redis *redisx.Client
	tts   DeviceTTSPusher
	log   *zap.Logger
}

// DeviceTTSPusher 再次下发设备 Opus（由 ConnManager 实现）。
type DeviceTTSPusher interface {
	SendTTSFrames(sn string, frames [][]byte) error
}

// NewAppHandler 创建 App API；store 可为 nil（库未启用时接口返回 503）。
func NewAppHandler(st *store.Store, rdb *redisx.Client, log *zap.Logger) *AppHandler {
	return &AppHandler{store: st, redis: rdb, log: log}
}

// SetDevicePusher 注入 TTS 下发能力（用于 translate Opus replay）。
func (h *AppHandler) SetDevicePusher(p DeviceTTSPusher) {
	if h != nil {
		h.tts = p
	}
}

// Register 注册 App 查询路由。
func (h *AppHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/app/v1/audio", h.handleListAudio)
	mux.HandleFunc("/api/app/v1/audio/", h.handleGetAudio)
	mux.HandleFunc("/api/app/v1/translates", h.handleListTranslate)
	mux.HandleFunc("/api/app/v1/translates/replay", h.handleReplayTranslateTTS)
	mux.HandleFunc("/api/app/v1/translates/", h.handleGetTranslate)
	mux.HandleFunc("/api/app/v1/device/status", h.handleDeviceStatus)
	mux.HandleFunc("/api/app/v1/device/status/list", h.handleDeviceStatusList)
	mux.HandleFunc("/api/app/v1/device/commands", HandleDeviceCommands)
}

// GET /api/app/v1/audio?sn=&limit=&offset=&since_id=
// 列表不含 opus 内容，只有 opus_len，便于 App 列表页。
// since_id>0：取 id 更大的新分片（升序），供轮询；否则倒序翻页。
func (h *AppHandler) handleListAudio(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "database disabled"})
		return
	}

	q := r.URL.Query()
	sn := q.Get("sn")
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	sinceID, _ := strconv.ParseInt(q.Get("since_id"), 10, 64)

	list, err := h.store.ListAudio(r.Context(), sn, limit, offset, sinceID)
	if err != nil {
		h.log.Error("查询音频列表失败", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items":         list,
		"count":         len(list),
		"limit":         limitOrDefault(limit),
		"offset":        offset,
		"since_id":      sinceID,
		"next_since_id": nextSinceID(list, sinceID),
	})
}

// GET /api/app/v1/audio/{id}
// 详情含 opus_base64，App 可自行解码展示/播放链路。
func (h *AppHandler) handleGetAudio(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "database disabled"})
		return
	}

	idStr := r.URL.Path[len("/api/app/v1/audio/"):]
	if idStr == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	rec, err := h.store.GetAudio(r.Context(), id)
	if err != nil {
		h.log.Error("查询音频详情失败", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if rec == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	rec.OpusBase64 = base64.StdEncoding.EncodeToString(rec.OpusData)
	rec.OpusData = nil
	writeJSON(w, http.StatusOK, rec)
}

// GET /api/app/v1/translates?sn=&limit=&offset=
func (h *AppHandler) handleListTranslate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "database disabled"})
		return
	}
	q := r.URL.Query()
	sn := q.Get("sn")
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	sinceID, _ := strconv.ParseInt(q.Get("since_id"), 10, 64)
	// 默认只出终态 ok/fail；include_processing=1 才带 pending/translating（联调）
	readyOnly := true
	switch strings.ToLower(strings.TrimSpace(q.Get("include_processing"))) {
	case "1", "true", "yes", "all":
		readyOnly = false
	}
	list, err := h.store.ListTranslate(r.Context(), sn, limit, offset, sinceID, readyOnly)
	if err != nil {
		h.log.Error("查询翻译列表失败", zap.Error(err))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": list, "count": len(list), "limit": limitOrDefault(limit), "offset": offset,
		"since_id": sinceID, "next_since_id": nextSinceIDTranslate(list, sinceID),
		"ready_only": readyOnly,
	})
}

// GET /api/app/v1/translates/{id}
func (h *AppHandler) handleGetTranslate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "database disabled"})
		return
	}
	idStr := r.URL.Path[len("/api/app/v1/translates/"):]
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	rec, err := h.store.GetTranslate(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if rec == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// handleReplayTranslateTTS POST /api/app/v1/translates/replay
// 支持 JSON / form-data / x-www-form-urlencoded：
//   {"id":676} 或 {"id":"676","sn":"PT-xxx"} 或 id=676&sn=...
func (h *AppHandler) handleReplayTranslateTTS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.store == nil {
		writeErr(w, http.StatusServiceUnavailable, "database disabled")
		return
	}
	if h.tts == nil {
		writeErr(w, http.StatusServiceUnavailable, "tts pusher not ready")
		return
	}

	var req struct {
		ID          util.Int64Flex `json:"id" form:"id"`
		TranslateID util.Int64Flex `json:"translate_id" form:"translate_id"`
		SN          string         `json:"sn" form:"sn"`
	}
	if err := util.BindRequest(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	id := req.ID.Int64()
	if id <= 0 {
		id = req.TranslateID.Int64()
	}
	snOverride := strings.TrimSpace(req.SN)
	if id <= 0 {
		writeErr(w, http.StatusBadRequest, "id required")
		return
	}

	rec, err := h.store.GetTranslate(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rec == nil {
		writeErr(w, http.StatusNotFound, "translate not found")
		return
	}
	frames, err := h.store.LoadTranslateDeviceOpus(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(frames) == 0 {
		writeErr(w, http.StatusBadRequest, "no device_opus for this translate id")
		return
	}
	sn := snOverride
	if sn == "" {
		sn = rec.EmbSN
	}
	if sn == "" {
		writeErr(w, http.StatusBadRequest, "sn required")
		return
	}
	if err := h.tts.SendTTSFrames(sn, frames); err != nil {
		h.log.Error("App TTS replay 失败", zap.Int64("id", id), zap.String("sn", sn), zap.Error(err))
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.log.Info("App TTS replay 已下发", zap.Int64("id", id), zap.String("sn", sn), zap.Int("frames", len(frames)))
	writeOK(w, map[string]interface{}{
		"id":                 id,
		"sn":                 sn,
		"frames":             len(frames),
		"translate_text":     rec.TranslateText,
		"has_device_opus":    true,
		"device_opus_frames": rec.DeviceOpusFrames,
	})
}

// handleDeviceStatus 单台设备状态查询：GET ?sn=xxx 或 POST（JSON / form-data / urlencoded）。
func (h *AppHandler) handleDeviceStatus(w http.ResponseWriter, r *http.Request) {
	var sn string
	switch r.Method {
	case http.MethodGet:
		sn = strings.TrimSpace(r.URL.Query().Get("sn"))
	case http.MethodPost:
		var req struct {
			SN string `json:"sn" form:"sn"`
		}
		if err := util.BindRequest(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request: "+err.Error())
			return
		}
		sn = strings.TrimSpace(req.SN)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if sn == "" {
		writeErr(w, http.StatusBadRequest, "sn required")
		return
	}
	ds, err := h.redis.GetDeviceStatusViewWithToken(r.Context(), sn)
	if err != nil {
		h.log.Error("App查询设备状态失败", zap.String("sn", sn), zap.Error(err))
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, ds)
}

// handleDeviceStatusList 批量设备状态：GET ?sns=a,b,c&sn=a&sn=b 或 POST（JSON / form-data / urlencoded）
func (h *AppHandler) handleDeviceStatusList(w http.ResponseWriter, r *http.Request) {
	sns := []string{}
	add := func(v string) {
		for _, s := range strings.Split(v, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				sns = append(sns, s)
			}
		}
	}
	switch r.Method {
	case http.MethodGet:
		if arr, ok := r.URL.Query()["sn"]; ok {
			for _, s := range arr {
				add(s)
			}
		}
		add(r.URL.Query().Get("sns"))
	case http.MethodPost:
		var req struct {
			SN  string   `json:"sn" form:"sn"`
			SNs []string `json:"sns" form:"sns"`
		}
		if err := util.BindRequest(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request: "+err.Error())
			return
		}
		add(req.SN)
		for _, s := range req.SNs {
			add(s)
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if len(sns) == 0 {
		writeErr(w, http.StatusBadRequest, "sn(s) required")
		return
	}
	seen := map[string]struct{}{}
	unique := make([]string, 0, len(sns))
	for _, s := range sns {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			unique = append(unique, s)
		}
	}
	items := make([]map[string]interface{}, 0, len(unique))
	for _, s := range unique {
		ds, err := h.redis.GetDeviceStatusView(r.Context(), s)
		if err != nil {
			h.log.Error("App批量查询设备状态失败", zap.String("sn", s), zap.Error(err))
			items = append(items, map[string]interface{}{"sn": s, "error": err.Error()})
			continue
		}
		b, _ := json.Marshal(ds)
		var row map[string]interface{}
		_ = json.Unmarshal(b, &row)
		if row == nil {
			row = map[string]interface{}{"sn": s}
		}
		items = append(items, row)
	}
	writeOK(w, map[string]interface{}{
		"items": items,
		"count": len(items),
	})
}

func limitOrDefault(limit int) int {
	if limit <= 0 || limit > 200 {
		return 50
	}
	return limit
}

func nextSinceID(list []store.AudioRecord, sinceID int64) int64 {
	maxID := sinceID
	for i := range list {
		if list[i].ID > maxID {
			maxID = list[i].ID
		}
	}
	return maxID
}

func nextSinceIDTranslate(list []store.TranslateRecord, sinceID int64) int64 {
	maxID := sinceID
	for i := range list {
		if list[i].ID > maxID {
			maxID = list[i].ID
		}
	}
	return maxID
}
