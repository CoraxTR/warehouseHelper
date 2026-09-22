package http

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"

	"warehouseHelper/internal/receiving"
)

// ReceiveBoxLabels — POST /ms/receive/box-labels: xlsx-файл наклеек для
// принятых коробок. Коробки приходят из ответа Save (сервер их уже
// провалидировал при сохранении) — печать только формирует файл, повторного
// резолва нет. Файл — из tempdir, раздаётся как attachment (паттерн
// ReceiveLabels).
func (h *Handler) ReceiveBoxLabels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Boxes []receiving.Box `json:"boxes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "невалидный JSON запроса", http.StatusBadRequest)
		return
	}
	if len(req.Boxes) == 0 {
		http.Error(w, "нет коробок для наклеек", http.StatusBadRequest)
		return
	}

	path, err := h.receivingUC.ExportBoxLabels(req.Boxes)
	if err != nil {
		slog.Info(fmt.Sprintf("receive box labels export: %v", err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename="+filepath.Base(path))
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	http.ServeFile(w, r, path)
}
