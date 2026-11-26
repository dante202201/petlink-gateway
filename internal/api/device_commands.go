package api

import (
	"net/http"

	"pet-translate-gateway/internal/model"
)

// HandleDeviceCommands GET 列出云端支持的全部设备下行 cmd。
func HandleDeviceCommands(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeOK(w, model.ListDeviceCommands())
}
