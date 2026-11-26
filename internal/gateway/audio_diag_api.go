package gateway

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	redisx "pet-translate-gateway/internal/redis"
	"pet-translate-gateway/internal/store"
)

// audioDiagItem 上传诊断 + 入库后的翻译/原音记录（可对上 URL、类型、状态）。
type audioDiagItem struct {
	redisx.AudioSessionCycle
	Translate *store.TranslateRecord `json:"translate,omitempty"`
}

// registerAudioDiagRoutes 音频会话诊断（Redis）查询接口。
func (m *ConnManager) registerAudioDiagRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/audio/diag/stats", m.handleAudioDiagStats)
	mux.HandleFunc("/api/v1/audio/diag/list", m.handleAudioDiagList)
	mux.HandleFunc("/api/v1/audio/diag/get", m.handleAudioDiagGet)
	mux.HandleFunc("/api/v1/audio/diag/query", m.handleAudioDiagQuery)
	mux.HandleFunc("/api/v1/audio/diag/purge", m.handleAudioDiagPurge)
}

func (m *ConnManager) handleAudioDiagStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeAdminErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if m.redis == nil {
		writeAdminErr(w, http.StatusServiceUnavailable, "redis unavailable")
		return
	}
	st, err := m.redis.GetAudioDiagStats(r.Context())
	if err != nil {
		writeAdminErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAdminOK(w, st)
}

func (m *ConnManager) handleAudioDiagList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeAdminErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if m.redis == nil {
		writeAdminErr(w, http.StatusServiceUnavailable, "redis unavailable")
		return
	}
	ensureForm(r)
	sn := strings.TrimSpace(firstNonEmpty(r.FormValue("sn"), r.URL.Query().Get("sn")))
	limit, _ := strconv.Atoi(firstNonEmpty(r.URL.Query().Get("limit"), r.FormValue("limit")))
	problemsOnly := r.URL.Query().Get("problems_only") == "1" || r.FormValue("problems_only") == "1"
	list, err := m.redis.ListAudioCycles(r.Context(), sn, limit, problemsOnly)
	if err != nil {
		writeAdminErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := m.attachTranslateToCycles(r, sn, list, false)
	writeAdminOK(w, map[string]interface{}{
		"items": items,
		"count": len(items),
		"sn":    sn,
	})
}

// handleAudioDiagQuery 按设备号一次返回：全局累计 + 该 SN 汇总 + 上传周期（含翻译/原音 URL）。
func (m *ConnManager) handleAudioDiagQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeAdminErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if m.redis == nil {
		writeAdminErr(w, http.StatusServiceUnavailable, "redis unavailable")
		return
	}
	ensureForm(r)
	sn := strings.TrimSpace(firstNonEmpty(r.FormValue("sn"), r.URL.Query().Get("sn")))
	if sn == "" {
		writeAdminErr(w, http.StatusBadRequest, "sn required")
		return
	}
	limit, _ := strconv.Atoi(firstNonEmpty(r.URL.Query().Get("limit"), r.FormValue("limit")))
	if limit <= 0 {
		limit = 100
	}
	problemsOnly := r.URL.Query().Get("problems_only") == "1" || r.FormValue("problems_only") == "1"

	global, err := m.redis.GetAudioDiagStats(r.Context())
	if err != nil {
		writeAdminErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	all, err := m.redis.ListAudioCycles(r.Context(), sn, limit, false)
	if err != nil {
		writeAdminErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	deviceStats := redisx.SummarizeAudioCycles(all)
	items := m.attachTranslateToCycles(r, sn, all, true)
	if problemsOnly {
		filtered := make([]audioDiagItem, 0, len(items))
		for _, it := range items {
			if it.HasProblem {
				filtered = append(filtered, it)
			}
		}
		items = filtered
	}
	writeAdminOK(w, map[string]interface{}{
		"sn":           sn,
		"global_stats": global,
		"device_stats": deviceStats,
		"items":        items,
		"count":        len(items),
		"total_for_sn": len(all),
	})
}

// handleAudioDiagPurge 清除 N 天前的音频诊断 Redis 记录。
// GET/POST ?days=3&reset_stats=1 ；days=0 表示清除全部 cycle（按 ended_at<=now）。
func (m *ConnManager) handleAudioDiagPurge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodDelete {
		writeAdminErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if m.redis == nil {
		writeAdminErr(w, http.StatusServiceUnavailable, "redis unavailable")
		return
	}
	ensureForm(r)
	daysStr := firstNonEmpty(r.URL.Query().Get("days"), r.FormValue("days"))
	if daysStr == "" {
		daysStr = "7"
	}
	days, err := strconv.Atoi(daysStr)
	if err != nil || days < 0 {
		writeAdminErr(w, http.StatusBadRequest, "days must be >= 0 integer")
		return
	}
	resetStats := r.URL.Query().Get("reset_stats") == "1" || r.FormValue("reset_stats") == "1"
	n, err := m.redis.PurgeAudioCycles(r.Context(), days, resetStats)
	if err != nil {
		writeAdminErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAdminOK(w, map[string]interface{}{
		"removed":     n,
		"days":        days,
		"reset_stats": resetStats,
	})
}

func (m *ConnManager) handleAudioDiagGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeAdminErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if m.redis == nil {
		writeAdminErr(w, http.StatusServiceUnavailable, "redis unavailable")
		return
	}
	ensureForm(r)
	sn := strings.TrimSpace(firstNonEmpty(r.FormValue("sn"), r.URL.Query().Get("sn")))
	tsStr := firstNonEmpty(r.FormValue("device_ts"), r.URL.Query().Get("device_ts"))
	kindStr := firstNonEmpty(r.FormValue("kind"), r.URL.Query().Get("kind"))
	deviceTS, _ := strconv.ParseInt(tsStr, 10, 64)
	kind, _ := strconv.Atoi(kindStr)
	if sn == "" || deviceTS == 0 {
		writeAdminErr(w, http.StatusBadRequest, "sn and device_ts required")
		return
	}
	cycle, err := m.redis.GetAudioCycle(r.Context(), sn, deviceTS, kind)
	if err != nil {
		writeAdminErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if cycle == nil {
		// Redis 无诊断时，仍尝试返回库内翻译记录
		if m.store != nil {
			tr, terr := m.store.GetTranslateBySNDeviceTS(r.Context(), sn, deviceTS)
			if terr == nil && tr != nil {
				item := synthItemFromTranslate(tr)
				m.fillChunkStatsOne(r, &item)
				writeAdminOK(w, item)
				return
			}
		}
		writeAdminErr(w, http.StatusNotFound, "cycle not found")
		return
	}
	item := audioDiagItem{AudioSessionCycle: *cycle}
	if m.store != nil {
		if tr, terr := m.store.GetTranslateBySNDeviceTS(r.Context(), sn, deviceTS); terr == nil && tr != nil {
			item.Translate = tr
			fillCycleFromTranslate(&item.AudioSessionCycle, tr)
		}
	}
	m.fillChunkStatsOne(r, &item)
	writeAdminOK(w, item)
}

// attachTranslateToCycles 用 a_emb_translate 按 device_ts 对齐；并从 audio_chunk 回填帧统计。
func (m *ConnManager) attachTranslateToCycles(r *http.Request, sn string, cycles []redisx.AudioSessionCycle, mergeDBOnly bool) []audioDiagItem {
	var byTS map[int64]*store.TranslateRecord
	var byChunk map[store.SessionChunkKey]store.SessionChunkStats
	if m.store != nil && sn != "" {
		if m2, err := m.store.MapTranslateByDeviceTS(r.Context(), sn, 200); err == nil {
			byTS = m2
		}
		if m3, err := m.store.MapSessionChunkStatsBySN(r.Context(), sn, 200); err == nil {
			byChunk = m3
		}
	}
	seen := make(map[int64]struct{}, len(cycles))
	out := make([]audioDiagItem, 0, len(cycles)+8)
	for _, c := range cycles {
		it := audioDiagItem{AudioSessionCycle: c}
		seen[c.DeviceTS] = struct{}{}
		if byTS != nil {
			if tr := byTS[c.DeviceTS]; tr != nil {
				it.Translate = tr
				fillCycleFromTranslate(&it.AudioSessionCycle, tr)
			}
		}
		fillCycleFromChunkStats(&it.AudioSessionCycle, byChunk)
		out = append(out, it)
	}
	if mergeDBOnly && byTS != nil {
		for ts, tr := range byTS {
			if _, ok := seen[ts]; ok || tr == nil {
				continue
			}
			it := synthItemFromTranslate(tr)
			fillCycleFromChunkStats(&it.AudioSessionCycle, byChunk)
			out = append(out, it)
		}
		sortDiagItems(out)
	}
	return out
}

func (m *ConnManager) fillChunkStatsOne(r *http.Request, it *audioDiagItem) {
	if m.store == nil || it == nil || it.SN == "" || it.DeviceTS == 0 {
		return
	}
	st, err := m.store.GetSessionChunkStats(r.Context(), it.SN, it.DeviceTS, it.AudioKind)
	if err != nil || st.Chunks == 0 && st.Total == 0 {
		// kind 可能不准：再试不按 kind
		st2, err2 := m.store.GetSessionChunkStats(r.Context(), it.SN, it.DeviceTS, -1)
		if err2 != nil {
			return
		}
		st = st2
	}
	applyChunkStats(&it.AudioSessionCycle, st)
}

func fillCycleFromChunkStats(c *redisx.AudioSessionCycle, by map[store.SessionChunkKey]store.SessionChunkStats) {
	if c == nil || by == nil {
		return
	}
	if st, ok := by[store.SessionChunkKey{DeviceTS: c.DeviceTS, AudioKind: c.AudioKind}]; ok {
		applyChunkStats(c, st)
		return
	}
	// kind 对不上时，同 device_ts 任取一段
	for k, st := range by {
		if k.DeviceTS == c.DeviceTS {
			applyChunkStats(c, st)
			if c.AudioKind == 0 {
				c.AudioKind = k.AudioKind
			}
			return
		}
	}
}

func applyChunkStats(c *redisx.AudioSessionCycle, st store.SessionChunkStats) {
	if c == nil {
		return
	}
	if st.Chunks > 0 {
		c.StoreChunks = st.Chunks
		if c.AssembleChunks == 0 {
			c.AssembleChunks = st.Chunks
		}
	}
	if st.Total > 0 {
		c.DeviceTotal = st.Total
	}
	if st.HasEnd || st.Chunks > 0 {
		c.StoreReady = st.Ready()
		c.StoreIncomplete = st.Incomplete()
	}
	if c.MissingCount == 0 && st.Total > 0 && st.Chunks < st.Total {
		c.MissingCount = st.Total - st.Chunks
	}
}

func fillCycleFromTranslate(c *redisx.AudioSessionCycle, tr *store.TranslateRecord) {
	if c == nil || tr == nil {
		return
	}
	if c.OriginURL == "" && tr.OriginAudioURL != "" {
		c.OriginURL = tr.OriginAudioURL
	}
	if c.KindLabel == "" {
		c.KindLabel = tr.AudioKindLabel
	}
	if c.AudioKind == 0 && tr.AudioKind != 0 {
		c.AudioKind = tr.AudioKind
	}
}

func synthItemFromTranslate(tr *store.TranslateRecord) audioDiagItem {
	ended := tr.ResultTime
	if ended == 0 {
		ended = tr.CreateTime
	}
	c := redisx.AudioSessionCycle{
		ID:          tr.EmbSN + ":" + strconv.FormatInt(tr.DeviceTS, 10) + ":" + strconv.Itoa(tr.AudioKind),
		SN:          tr.EmbSN,
		DeviceTS:    tr.DeviceTS,
		AudioKind:   tr.AudioKind,
		KindLabel:   tr.AudioKindLabel,
		StartedAt:   tr.CreateTime,
		EndedAt:     ended,
		UpdatedAt:   ended,
		OriginURL:   tr.OriginAudioURL,
		Verdict:     "db_only:" + tr.Status,
		HasProblem:  tr.Status == "fail",
		Fingerprint: "db_" + strconv.FormatInt(tr.ID, 10),
	}
	if tr.Status == "fail" && tr.ErrorMsg != "" {
		c.Problems = []string{tr.ErrorMsg}
	}
	return audioDiagItem{AudioSessionCycle: c, Translate: tr}
}

func sortDiagItems(items []audioDiagItem) {
	sort.SliceStable(items, func(i, j int) bool {
		ai, aj := items[i].EndedAt, items[j].EndedAt
		if ai == 0 {
			ai = items[i].StartedAt
		}
		if aj == 0 {
			aj = items[j].StartedAt
		}
		return ai > aj
	})
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimSpace(a)
	}
	return strings.TrimSpace(b)
}
