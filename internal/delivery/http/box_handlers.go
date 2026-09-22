package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	retucase "warehouseHelper/internal/returns/usecase"
)

// Страница «Создать коробку» (хаб «Продукция»): сканирование ярлыков кусков
// (29 цифр) и наклейка коробки — GET /goods/box + POST /goods/box/save.
// Коробка не меняет ни остатки, ни МойСклад: ответ на сохранение — xlsx-файл
// наклейки, который печатают и клеят на коробку. Маршруты — в router.go
// (добавляет владелец ветки).

var boxTmpl = template.Must(template.ParseFiles("../internal/delivery/web/templates/goods_box.html", "../internal/delivery/web/templates/_nav.html"))

// GoodsBoxPage — GET /goods/box: страница сканирования кусков в коробку.
func (h *Handler) GoodsBoxPage(w http.ResponseWriter, _ *http.Request) {
	if err := boxTmpl.Execute(w, nil); err != nil {
		slog.Error(fmt.Sprintf("box template: %v", err))
	}
}

// GoodsBoxSave — POST /goods/box/save: собрать коробку по сканам. body:
// {"scans":[...]}. 200 — xlsx-наклейка как attachment; 400 — батч отклонён
// целиком (текст причины); 500 — сбой. Предупреждения (расхождение
// выработки) уходят заголовком X-Box-Warnings: тело ответа занято файлом.
func (h *Handler) GoodsBoxSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Scans []string `json:"scans"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Scans) == 0 {
		http.Error(w, "нет сканов", http.StatusBadRequest)

		return
	}

	path, warnings, err := h.returnsUC.CreateBox(r.Context(), req.Scans)
	if err != nil {
		var ve *retucase.ValidationError
		switch {
		case errors.As(err, &ve):
			http.Error(w, ve.Error(), http.StatusBadRequest)
		default:
			slog.Error(fmt.Sprintf("create box: %v", err))
			http.Error(w, "не удалось собрать коробку — попробуйте позже", http.StatusInternalServerError)
		}

		return
	}

	if len(warnings) > 0 {
		w.Header().Set("X-Box-Warnings", url.QueryEscape(strings.Join(warnings, "; ")))
	}
	w.Header().Set("Content-Disposition", "attachment; filename="+filepath.Base(path))
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	http.ServeFile(w, r, path)
}
