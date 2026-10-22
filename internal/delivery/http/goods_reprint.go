package http

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"path/filepath"

	rucase "warehouseHelper/internal/receiving/usecase"
)

// Страница «Перепечатать наклейку» (хаб «Продукция»): сканирование внутренних
// кодов (29 — кусок, 33 — коробка) и выгрузка xlsx с наклейками на выбор —
// длинные (одна на печатную страницу, с подписью), короткие (две в ряд, только
// код и QR) или наклейки коробок (75×120). Данные наклейки целиком в коде:
// остатки и МойСклад не читаются и не пишутся, каталог нужен только ради
// наименования. Маршруты — в router.go.

var reprintTmpl = template.Must(template.ParseFiles("../internal/delivery/web/templates/goods_reprint.html", "../internal/delivery/web/templates/_nav.html"))

// GoodsReprintPage — GET /goods/reprint: страница перепечатки наклеек.
func (h *Handler) GoodsReprintPage(w http.ResponseWriter, _ *http.Request) {
	if err := reprintTmpl.Execute(w, nil); err != nil {
		slog.Error(fmt.Sprintf("reprint template: %v", err))
	}
}

// GoodsReprintSave — POST /goods/reprint/save: xlsx с наклейками по сканам.
// body: {"scans":[...], "variant":"long"|"short"|"box"}. 200 — xlsx как
// attachment; 400 — батч отклонён целиком (текст причины: неразобранный скан,
// код не из каталога); 500 — сбой.
func (h *Handler) GoodsReprintSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Scans   []string `json:"scans"`
		Variant string   `json:"variant"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Scans) == 0 {
		http.Error(w, "нет сканов", http.StatusBadRequest)

		return
	}

	variant := rucase.ReprintVariant(req.Variant)

	path, err := h.receivingUC.ExportReprint(r.Context(), req.Scans, variant)
	if err != nil {
		slog.Info(fmt.Sprintf("reprint labels %q: %v", req.Variant, err))
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename="+filepath.Base(path))
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	http.ServeFile(w, r, path)
}
