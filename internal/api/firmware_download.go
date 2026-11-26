package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

// fwProxyClient 网关自己跟随 OSS 跳转，对设备只吐 200/206 文件流。
// 设备 HTTP 客户端默认不跟随 302，不能把重定向暴露给设备。
var fwProxyClient = &http.Client{
	Timeout: 15 * time.Minute,
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DisableCompression:    true, // 固件必须是原始字节，否则设备 MD5 对不上
		ForceAttemptHTTP2:     false,
		ResponseHeaderTimeout: 30 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   8,
	},
}

// handleFirmwareDownload 固件包下载代理（GET/HEAD）。鉴权后直接回 200/206，支持 Range。
// 禁止 302：设备端不跟随重定向，收到 302 会当成 HTTP error。
func (h *Handler) handleFirmwareDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sn, token, _ := parseDeviceAuth(r, false)
	if sn == "" || token == "" {
		writeErr(w, http.StatusBadRequest, "sn and token required")
		return
	}
	ok, err := h.redis.ValidateDeviceToken(r.Context(), sn, token)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "auth service unavailable")
		return
	}
	if !ok {
		writeErr(w, http.StatusUnauthorized, "token invalid")
		return
	}

	fw, _, err := h.resolveCurrentFirmware(r.Context())
	if err != nil || fw.URL == "" {
		writeErr(w, http.StatusNotFound, "firmware not configured")
		return
	}

	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, fw.URL, nil)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "bad upstream url")
		return
	}
	for _, k := range []string{"Range", "If-Range", "If-None-Match", "If-Modified-Since"} {
		if v := r.Header.Get(k); v != "" {
			upReq.Header.Set(k, v)
		}
	}
	upReq.Header.Set("User-Agent", "PetLinkGateway-OTA/1.0")

	upResp, err := fwProxyClient.Do(upReq)
	if err != nil {
		h.log.Warn("固件上游下载失败", zap.String("sn", sn), zap.String("url", fw.URL), zap.Error(err))
		writeErr(w, http.StatusBadGateway, "upstream download failed")
		return
	}
	defer upResp.Body.Close()

	copyHeader := func(k string) {
		if v := upResp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	for _, k := range []string{
		"Content-Type", "Content-Length", "Content-Range",
		"Accept-Ranges", "ETag", "Last-Modified", "Cache-Control",
	} {
		copyHeader(k)
	}
	if w.Header().Get("Accept-Ranges") == "" {
		w.Header().Set("Accept-Ranges", "bytes")
	}
	if w.Header().Get("ETag") == "" && fw.MD5 != "" {
		w.Header().Set("ETag", `"`+fw.MD5+`"`)
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	// 告诉 Nginx 不要缓冲整段，否则慢速设备十几秒读超时。
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Cache-Control", "no-transform")
	if fw.Version != "" {
		w.Header().Set("X-Firmware-Version", fw.Version)
	}
	if fw.MD5 != "" {
		w.Header().Set("X-Firmware-MD5", fw.MD5)
	}
	if fw.Size > 0 && w.Header().Get("Content-Length") == "" && upResp.StatusCode == http.StatusOK {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", fw.Size))
	}

	w.WriteHeader(upResp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}

	// 分块写出并 Flush，避免反代/网关缓冲整段导致慢客户端十几秒被掐断。
	buf := make([]byte, 32*1024)
	var written int64
	flusher, canFlush := w.(http.Flusher)
	for {
		nr, er := upResp.Body.Read(buf)
		if nr > 0 {
			nw, ew := w.Write(buf[:nr])
			written += int64(nw)
			if canFlush {
				flusher.Flush()
			}
			if ew != nil {
				h.log.Warn("固件下载写出中断",
					zap.String("sn", sn),
					zap.Int64("written", written),
					zap.Int("upstream_status", upResp.StatusCode),
					zap.Error(ew),
				)
				return
			}
			if nw != nr {
				h.log.Warn("固件下载写出中断",
					zap.String("sn", sn),
					zap.Int64("written", written),
					zap.Int("upstream_status", upResp.StatusCode),
					zap.Error(io.ErrShortWrite),
				)
				return
			}
		}
		if er != nil {
			if er != io.EOF {
				h.log.Warn("固件下载写出中断",
					zap.String("sn", sn),
					zap.Int64("written", written),
					zap.Int("upstream_status", upResp.StatusCode),
					zap.Error(er),
				)
			} else {
				h.log.Info("固件下载完成/分段",
					zap.String("sn", sn),
					zap.Int("status", upResp.StatusCode),
					zap.String("range", r.Header.Get("Range")),
					zap.Int64("bytes", written),
				)
			}
			return
		}
	}
}

// parseDeviceAuth 从 Query / Header / Form（urlencoded/multipart）/（可选）JSON Body 取 sn、token、fw_ver。
func parseDeviceAuth(r *http.Request, readJSONBody bool) (sn, token, fwVer string) {
	sn = r.URL.Query().Get("sn")
	token = r.URL.Query().Get("token")
	fwVer = r.URL.Query().Get("fw_ver")
	if sn == "" {
		sn = r.Header.Get("X-Device-SN")
	}
	if token == "" {
		token = r.Header.Get("X-Device-Token")
	}
	if r.Method == http.MethodPost {
		ct := r.Header.Get("Content-Type")
		if strings.Contains(ct, "application/x-www-form-urlencoded") || strings.Contains(ct, "multipart/form-data") {
			ensureFormAPI(r)
			if sn == "" {
				sn = strings.TrimSpace(r.FormValue("sn"))
			}
			if token == "" {
				token = strings.TrimSpace(r.FormValue("token"))
			}
			if fwVer == "" {
				fwVer = strings.TrimSpace(r.FormValue("fw_ver"))
				if fwVer == "" {
					fwVer = strings.TrimSpace(r.FormValue("fw"))
				}
			}
		} else if readJSONBody && r.Body != nil && strings.Contains(ct, "application/json") {
			var body firmwareReq
			_ = json.NewDecoder(r.Body).Decode(&body)
			if sn == "" {
				sn = body.SN
			}
			if token == "" {
				token = body.Token
			}
			if fwVer == "" {
				fwVer = body.FWVer
			}
		}
	}
	return sn, token, fwVer
}

func publicHTTPBase(r *http.Request) string {
	proto := "https"
	if v := r.Header.Get("X-Forwarded-Proto"); v != "" {
		proto = strings.Split(v, ",")[0]
		proto = strings.TrimSpace(proto)
	} else if r.TLS == nil {
		proto = "http"
	}
	host := r.Host
	if v := r.Header.Get("X-Forwarded-Host"); v != "" {
		host = strings.TrimSpace(strings.Split(v, ",")[0])
	}
	return proto + "://" + host
}
