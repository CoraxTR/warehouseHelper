package http

import (
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"

	"warehouseHelper/internal/boxlabel"
)

// ReceiveBreakLabel — GET /ms/receive/break-label: xlsx-наклейка спец-кода
// окончания коробки (666). Код перехватывает страница приёмки до резолва, так
// что оператор закрывает коробку сканом наклейки, а не вводом с клавиатуры.
// Файл — из tempdir, раздаётся как attachment (паттерн ReceiveLabels).
func (h *Handler) ReceiveBreakLabel(w http.ResponseWriter, r *http.Request) {
	path, err := boxlabel.ExportBreakMarker()
	if err != nil {
		slog.Info(fmt.Sprintf("receive break label export: %v", err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename="+filepath.Base(path))
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	http.ServeFile(w, r, path)
}
