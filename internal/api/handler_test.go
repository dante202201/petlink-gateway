package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"pet-translate-gateway/config"
)

// TestActivateValidation 无 Redis 时至少校验请求参数。
func TestActivateValidation(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.PublicBaseURL = "wss://example.com"
	cfg.Server.WSPath = "/ws"
	cfg.Device.TokenBytes = 8
	h := New(cfg, nil, nil, zap.NewNop(), nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/device/activate", nil)
	rr := httptest.NewRecorder()
	h.handleActivate(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 got %d", rr.Code)
	}
}

func TestWSServerURL(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.PublicBaseURL = "wss://gw.example.com/"
	cfg.Server.WSPath = "/ws"
	if got := cfg.WSServerURL(); got != "wss://gw.example.com/ws" {
		t.Fatalf("got %s", got)
	}
}

func TestActivateJSONShape(t *testing.T) {
	// 仅验证响应结构体序列化字段名与固件一致
	resp := activateResp{Token: "abc", ServerURL: "wss://x/ws"}
	b, _ := json.Marshal(resp)
	var m map[string]string
	_ = json.Unmarshal(b, &m)
	if m["token"] != "abc" || m["server_url"] != "wss://x/ws" {
		t.Fatalf("bad json: %s", b)
	}
}
