package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleDeviceCommands(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/app/v1/device/commands", nil)
	rec := httptest.NewRecorder()
	HandleDeviceCommands(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Total int `json:"total"`
			List  []struct {
				Cmd string `json:"cmd"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Code != 1 {
		t.Fatalf("code=%d", resp.Code)
	}
	if resp.Data.Total < 20 || len(resp.Data.List) != resp.Data.Total {
		t.Fatalf("total=%d list=%d", resp.Data.Total, len(resp.Data.List))
	}
}

func TestHandleDeviceCommandsMethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/app/v1/device/commands", nil)
	rec := httptest.NewRecorder()
	HandleDeviceCommands(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d", rec.Code)
	}
}
