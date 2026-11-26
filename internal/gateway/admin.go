package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"pet-translate-gateway/internal/api"
	"pet-translate-gateway/internal/model"
	"pet-translate-gateway/internal/util"
)

// RegisterAdminRoutes 注册联调管理接口。
func (m *ConnManager) RegisterAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1,
			"data": map[string]interface{}{
				"status": "ok",
				"online": m.Count(),
			},
		})
	})

	mux.HandleFunc("/api/v1/device/commands", api.HandleDeviceCommands)
	mux.HandleFunc("/api/v1/device/cmd", m.handleSendCmd)
	mux.HandleFunc("/api/v1/device/tts", m.handleSendTTS)
	mux.HandleFunc("/api/v1/device/tts/replay", m.handleReplayTTS)
	mux.HandleFunc("/api/v1/device/status", m.handleDeviceStatus)
	mux.HandleFunc("/api/v1/device/status/list", m.handleDeviceStatusList)
	m.registerAudioDiagRoutes(mux)
}

// ensureForm 确保 GET 以外的请求都能通过 r.FormValue 读到参数：
// 对 urlencoded / multipart form-data 都自动 Parse；JSON/其他 Content-Type 不处理（仍走 r.Body 原路径）。
func ensureForm(r *http.Request) {
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

// handleSendCmd POST：JSON / form-data / urlencoded 三种。
// JSON：{"sn":"...","cmd":"play_cry","data":"0xCB"}（data 可为 string / number / object；兼容 type 作命令名）
// FORM：sn=...&cmd=...&data=<可选：JSON 值或纯字符串，如 0xCB / {"lat":1}>
func (m *ConnManager) handleSendCmd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var sn, cmd string
	var data interface{} = map[string]interface{}{}
	ct := r.Header.Get("Content-Type")
	if strings.Contains(ct, "application/json") {
		var req model.AdminCmdRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAdminErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		sn = req.SN
		cmd = req.Cmd
		if cmd == "" {
			cmd = req.Type
		}
		// data 保持原类型：play_cry 为 string，set_fence 为 object，无参命令缺省 {}
		if req.Data != nil {
			data = req.Data
		}
	} else {
		ensureForm(r)
		sn = strings.TrimSpace(r.FormValue("sn"))
		cmd = strings.TrimSpace(r.FormValue("cmd"))
		if cmd == "" {
			cmd = strings.TrimSpace(r.FormValue("type"))
		}
		rawData := strings.TrimSpace(r.FormValue("data"))
		if rawData != "" {
			var parsed interface{}
			if err := json.Unmarshal([]byte(rawData), &parsed); err == nil {
				data = parsed
			} else {
				// 非 JSON（如裸 0xCB）按字符串下发
				data = rawData
			}
		}
	}

	if sn == "" || cmd == "" {
		writeAdminErr(w, http.StatusBadRequest, "sn and cmd required")
		return
	}
	payload := BuildCommand(model.CmdType(cmd), data)
	if err := m.SendCmdToDevice(sn, payload); err != nil {
		m.log.Error("管理接口下发失败", zap.Error(err))
		writeAdminErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := map[string]interface{}{
		"cmd":  payload.Cmd,
		"type": payload.Type,
		"data": payload.Data,
	}
	if strings.EqualFold(payload.Cmd, string(model.CmdOTAUpdate)) && m.store != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		if rec, err := m.store.GetPendingOTABySN(ctx, sn); err == nil && rec != nil {
			out["ota_log_id"] = rec.ID
			out["ota_from_fw"] = rec.FromFW
			out["ota_target_fw"] = rec.TargetFW
			out["ota_status"] = rec.Status
		}
		cancel()
	}
	writeAdminOK(w, out)
}

// handleSendTTS POST：JSON / form-data / urlencoded 三种。
// JSON：{"sn":"...","pid?":"...","opus_data":"<base64>"}
// FORM：sn=...&pid=<可选>&opus_data=<base64 字符串>（Postman 选 form-data/text，粘贴 base64）
func (m *ConnManager) handleSendTTS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var sn, pid string
	var opus []byte
	ct := r.Header.Get("Content-Type")
	if strings.Contains(ct, "application/json") {
		var req model.AdminTTSRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAdminErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		sn = req.SN
		pid = req.PID
		opus = req.OpusData
	} else {
		ensureForm(r)
		sn = strings.TrimSpace(r.FormValue("sn"))
		pid = strings.TrimSpace(r.FormValue("pid"))
		opusData := strings.TrimSpace(r.FormValue("opus_data"))
		if opusData != "" {
			if b, err := base64.StdEncoding.DecodeString(opusData); err == nil {
				opus = b
			}
		}
		// Postman 用户可能直接传 file：opus_file 字段（二进制），也兜底读（base64 会慢）
		if len(opus) == 0 && strings.Contains(ct, "multipart/form-data") {
			if fh, _, err := r.FormFile("opus_file"); err == nil && fh != nil {
				raw, rerr := io.ReadAll(io.LimitReader(fh, 8<<20))
				fh.Close()
				if rerr == nil && len(raw) > 0 {
					opus = raw
				}
			}
		}
	}

	if sn == "" || len(opus) == 0 {
		writeAdminErr(w, http.StatusBadRequest, "sn and opus_data required")
		return
	}

	var err error
	if pid != "" {
		err = m.SendTTSToDeviceWithPID(sn, pid, opus)
	} else {
		err = m.SendTTSToDevice(sn, opus)
	}
	if err != nil {
		m.log.Error("TTS 下发失败", zap.Error(err))
		writeAdminErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeAdminOK(w, map[string]interface{}{
		"opus_len": len(opus),
		"pid":      pid,
	})
}

// handleReplayTTS POST：按 a_emb_translate.id 取出已存设备 Opus，再次下发。
// 支持 JSON / form-data / urlencoded：{"id":676} / id=676&sn=可选
func (m *ConnManager) handleReplayTTS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if m.store == nil {
		writeAdminErr(w, http.StatusServiceUnavailable, "database disabled")
		return
	}

	var req struct {
		ID          util.Int64Flex `json:"id" form:"id"`
		TranslateID util.Int64Flex `json:"translate_id" form:"translate_id"`
		SN          string         `json:"sn" form:"sn"`
	}
	if err := util.BindRequest(r, &req); err != nil {
		writeAdminErr(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	id := req.ID.Int64()
	if id <= 0 {
		id = req.TranslateID.Int64()
	}
	snOverride := strings.TrimSpace(req.SN)
	if id <= 0 {
		writeAdminErr(w, http.StatusBadRequest, "id required")
		return
	}

	rec, err := m.store.GetTranslate(r.Context(), id)
	if err != nil {
		writeAdminErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rec == nil {
		writeAdminErr(w, http.StatusNotFound, "translate not found")
		return
	}
	frames, err := m.store.LoadTranslateDeviceOpus(r.Context(), id)
	if err != nil {
		writeAdminErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(frames) == 0 {
		writeAdminErr(w, http.StatusBadRequest, "no device_opus for this translate id")
		return
	}
	sn := strings.TrimSpace(snOverride)
	if sn == "" {
		sn = rec.EmbSN
	}
	if sn == "" {
		writeAdminErr(w, http.StatusBadRequest, "sn required")
		return
	}
	if err := m.SendTTSFrames(sn, frames); err != nil {
		m.log.Error("TTS replay 下发失败", zap.Int64("id", id), zap.String("sn", sn), zap.Error(err))
		writeAdminErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	m.log.Info("TTS replay 已下发", zap.Int64("id", id), zap.String("sn", sn), zap.Int("frames", len(frames)))
	writeAdminOK(w, map[string]interface{}{
		"id":                 id,
		"sn":                 sn,
		"frames":             len(frames),
		"translate_text":     rec.TranslateText,
		"has_device_opus":    true,
		"device_opus_frames": rec.DeviceOpusFrames,
	})
}

// handleDeviceStatus GET/POST：query、JSON、form-data、urlencoded 都认 sn。
func (m *ConnManager) handleDeviceStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		SN string `json:"sn" form:"sn"`
	}
	if err := util.BindRequest(r, &req); err != nil {
		writeAdminErr(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	sn := strings.TrimSpace(req.SN)
	if sn == "" {
		sn = strings.TrimSpace(r.URL.Query().Get("sn"))
	}
	if sn == "" {
		writeAdminErr(w, http.StatusBadRequest, "sn required")
		return
	}
	ds, err := m.redis.GetDeviceStatusViewWithToken(r.Context(), sn)
	if err != nil {
		m.log.Error("查询设备状态失败", zap.String("sn", sn), zap.Error(err))
		writeAdminErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAdminOK(w, ds)
}

// handleDeviceStatusList GET/POST：?sns=a,b,c&sn=a&sn=b... 或 form sns=...&sn=... 或 body {"sns":[...],"sn":"..."}
// 支持批量查询；返回 items[] 按请求顺序对齐。
func (m *ConnManager) handleDeviceStatusList(w http.ResponseWriter, r *http.Request) {
	sns := []string{}
	add := func(v string) {
		for _, s := range strings.Split(v, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				sns = append(sns, s)
			}
		}
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Method == http.MethodPost {
		var req struct {
			SN  string   `json:"sn" form:"sn"`
			SNs []string `json:"sns" form:"sns"`
		}
		if err := util.BindRequest(r, &req); err != nil {
			writeAdminErr(w, http.StatusBadRequest, "invalid request: "+err.Error())
			return
		}
		add(req.SN)
		for _, s := range req.SNs {
			add(s)
		}
	}
	if r.Method == http.MethodGet || len(sns) == 0 {
		if arr, ok := r.URL.Query()["sn"]; ok {
			for _, s := range arr {
				add(s)
			}
		}
		add(r.URL.Query().Get("sns"))
	}
	if len(sns) == 0 {
		writeAdminErr(w, http.StatusBadRequest, "sn(s) required")
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
		ds, err := m.redis.GetDeviceStatusViewWithToken(r.Context(), s)
		if err != nil {
			m.log.Error("批量查询设备状态失败", zap.String("sn", s), zap.Error(err))
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
	writeAdminOK(w, map[string]interface{}{
		"items": items,
		"count": len(items),
	})
}

func writeAdminOK(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 1, "data": data})
}

func writeAdminErr(w http.ResponseWriter, httpCode int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpCode)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "msg": msg})
}
