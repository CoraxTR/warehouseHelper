package http

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	"warehouseHelper/internal/receiving"
)

// ReceiveQRLabels — POST /ms/receive/qr-labels: xlsx-файл QR-наклеек для
// принятых кусков. Куски приходят из ответа Save (сервер их уже провалидировал
// при сохранении) — печать только формирует файл, повторного резолва нет.
// Файл — из tempdir, раздаётся как attachment (паттерн ReceiveLabels).
func (h *Handler) ReceiveQRLabels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Units []receiving.Unit `json:"units"`

		// DraftID — печать по сохранённой приёмке: страница финала не
		// помнит единицы, сервер берёт их из самой приёмки.
		DraftID string `json:"draft_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "невалидный JSON запроса", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.DraftID) != "" {
		units, _, err := h.draftLabelData(strings.TrimSpace(req.DraftID))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		req.Units = units
	}

	if len(req.Units) == 0 {
		http.Error(w, "нет единиц для QR-наклеек", http.StatusBadRequest)
		return
	}

	path, err := h.receivingUC.ExportQRLabels(req.Units)
	if err != nil {
		slog.Info(fmt.Sprintf("receive qr labels export: %v", err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename="+filepath.Base(path))
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	http.ServeFile(w, r, path)
}
