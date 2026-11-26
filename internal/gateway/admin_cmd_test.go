package gateway

import (
	"bytes"
	"encoding/json"
	"testing"

	"pet-translate-gateway/internal/model"
)

func TestBuildCommandPreservesStringData(t *testing.T) {
	c := BuildCommand(model.CmdPlayCry, "0xCB")
	if c.Data != "0xCB" {
		t.Fatalf("got %#v", c.Data)
	}
	b, _ := json.Marshal(c)
	if !bytes.Contains(b, []byte(`"data":"0xCB"`)) {
		t.Fatalf("json=%s", b)
	}
}

func TestAdminCmdJSONDataKeepsString(t *testing.T) {
	body := `{"sn":"PT-68EE8F6557B8","type":"play_cry","data":"0xCB"}`
	var req model.AdminCmdRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	cmd := req.Cmd
	if cmd == "" {
		cmd = req.Type
	}
	var data interface{} = map[string]interface{}{}
	if req.Data != nil {
		data = req.Data
	}
	payload := BuildCommand(model.CmdType(cmd), data)
	if payload.Cmd != "play_cry" {
		t.Fatalf("cmd=%s", payload.Cmd)
	}
	if payload.Data != "0xCB" {
		t.Fatalf("data got %#v want string 0xCB (旧逻辑会静默变成 {})", payload.Data)
	}
}
