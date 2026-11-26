package util

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestInt64FlexJSON(t *testing.T) {
	var v Int64Flex
	if err := json.Unmarshal([]byte(`676`), &v); err != nil || v.Int64() != 676 {
		t.Fatalf("number: %v %d", err, v)
	}
	if err := json.Unmarshal([]byte(`"677"`), &v); err != nil || v.Int64() != 677 {
		t.Fatalf("string: %v %d", err, v)
	}
}

func TestBindRequestJSONAndForm(t *testing.T) {
	type replayReq struct {
		ID Int64Flex `json:"id" form:"id"`
		SN string    `json:"sn" form:"sn"`
	}

	t.Run("json", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"id":676,"sn":"PT-1"}`))
		r.Header.Set("Content-Type", "application/json")
		var req replayReq
		if err := BindRequest(r, &req); err != nil {
			t.Fatal(err)
		}
		if req.ID.Int64() != 676 || req.SN != "PT-1" {
			t.Fatalf("%+v", req)
		}
	})

	t.Run("json_id_string", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"id":"676"}`))
		r.Header.Set("Content-Type", "application/json")
		var req replayReq
		if err := BindRequest(r, &req); err != nil {
			t.Fatal(err)
		}
		if req.ID.Int64() != 676 {
			t.Fatalf("%+v", req)
		}
	})

	t.Run("json_no_content_type", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"id":680,"sn":"PT-2"}`))
		var req replayReq
		if err := BindRequest(r, &req); err != nil {
			t.Fatal(err)
		}
		if req.ID.Int64() != 680 || req.SN != "PT-2" {
			t.Fatalf("%+v", req)
		}
	})

	t.Run("form_urlencoded", func(t *testing.T) {
		form := url.Values{"id": {"681"}, "sn": {"PT-3"}}
		r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		var req replayReq
		if err := BindRequest(r, &req); err != nil {
			t.Fatal(err)
		}
		if req.ID.Int64() != 681 || req.SN != "PT-3" {
			t.Fatalf("%+v", req)
		}
	})

	t.Run("multipart", func(t *testing.T) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("id", "682")
		_ = mw.WriteField("sn", "PT-4")
		ct := mw.FormDataContentType()
		_ = mw.Close()
		r := httptest.NewRequest(http.MethodPost, "/x", &buf)
		r.Header.Set("Content-Type", ct)
		var req replayReq
		if err := BindRequest(r, &req); err != nil {
			t.Fatal(err)
		}
		if req.ID.Int64() != 682 || req.SN != "PT-4" {
			t.Fatalf("%+v", req)
		}
	})
}
