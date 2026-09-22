package http

import (
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"

	"warehouseHelper/internal/boxlabel"
)

// ReceiveBreakLabel — GET /ms/receive/break-label: xlsx-наклейка спец-кода
// закрытия коробки (666).
func (h *Handler) ReceiveBreakLabel(w http.ResponseWriter, r *http.Request) {
	serveMarker(w, r, boxlabel.ExportBreakMarker, "break label")
}

// ReceiveOpenBoxLabel — GET /ms/receive/open-box-label: xlsx-наклейка спец-кода
// открытия коробки (555) — скан равен нажатию «+ Коробка» на странице приёмки.
func (h *Handler) ReceiveOpenBoxLabel(w http.ResponseWriter, r *http.Request) {
	serveMarker(w, r, boxlabel.ExportOpenBoxMarker, "open box label")
}

// serveMarker отдаёт файл наклейки спец-кода как attachment (паттерн этикеток).
func serveMarker(w http.ResponseWriter, r *http.Request, export func() (string, error), what string) {
	path, err := export()
	if err != nil {
		slog.Info(fmt.Sprintf("receive %s export: %v", what, err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename="+filepath.Base(path))
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	http.ServeFile(w, r, path)
}
