package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"pet-translate-gateway/config"
)

func TestAdminAlwaysRegistered(t *testing.T) {
	h := NewAdminHandler(&config.Config{}, nil, nil, nil, nil, zap.NewNop())
	mux := http.NewServeMux()
	h.Register(mux)

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /admin status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "后台") && !strings.Contains(rr.Body.String(), "admin") {
		t.Fatalf("unexpected body: %s", rr.Body.String())
	}
}
