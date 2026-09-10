package http

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"warehouseHelper/internal/receiving"
)

// ReceiveBarcodesAdd — POST /ms/receive/barcodes/add: батчевое добавление
// связок «внешний код → товар» поставщика (виджет на карточке поставщика).
//
// Форма шлёт ПАРАЛЛЕЛЬНЫЕ массивы external_code[] и product_id[] по строкам
// виджета (supplier_id — одиночное поле). Старый одиночный POST продолжает
// работать: одиночное значение приходит массивом из одного элемента.
// Строки паруются чистой receiving.PairCodeRows, валидные пишутся по одной,
// ошибки строк агрегируются и не глотаются — сколько добавлено и сколько с
// ошибками видно и в логе, и в err-сообщении страницы (PRG).
func (h *Handler) ReceiveBarcodesAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "не удалось разобрать форму", http.StatusBadRequest)

		return
	}

	supplierID := strings.TrimSpace(r.FormValue("supplier_id"))
	if supplierID == "" {
		http.Redirect(w, r, "/ms/suppliers?err="+url.QueryEscape("не указан поставщик"), http.StatusSeeOther)

		return
	}

	rows := receiving.PairCodeRows(r.Form["external_code"], r.Form["product_id"])
	if len(rows) == 0 {
		http.Redirect(w, r, supplierBarcodesRedirect(supplierID, "",
			"не передано ни одной строки с внешним кодом"), http.StatusSeeOther)

		return
	}

	added := 0

	problems := make([]string, 0, len(rows))

	for _, row := range rows {
		if !row.Valid() {
			problems = append(problems, fmt.Sprintf("строка %d: %s", row.Row, strings.Join(row.Errors, "; ")))

			continue
		}

		if err := h.receiveUC.Add(r.Context(), supplierID, row.ExternalCode, row.ProductID); err != nil {
			slog.Info(fmt.Sprintf("receive: добавить код %q поставщику %s: %v", row.ExternalCode, supplierID, err))
			problems = append(problems, fmt.Sprintf("строка %d (код %q): %v", row.Row, row.ExternalCode, err))

			continue
		}

		added++
	}

	// Агрегат — в лог: сколько строк батча записано, сколько отклонено.
	slog.Info(fmt.Sprintf("receive: батч внешних кодов поставщика %s: добавлено %d, с ошибкой %d",
		supplierID, added, len(problems)))

	msg := fmt.Sprintf("Добавлено кодов: %d", added)
	errMsg := ""
	if len(problems) > 0 {
		errMsg = fmt.Sprintf("добавлено %d, с ошибкой %d: %s", added, len(problems), strings.Join(problems, "; "))
	}

	http.Redirect(w, r, supplierBarcodesRedirect(supplierID, msg, errMsg), http.StatusSeeOther)
}

// supplierBarcodesRedirect собирает PRG-адрес формы поставщика: раскрывает
// виджет кодов (barcodes=1) и якорем ставит прокрутку на него, чтобы оператор
// не спускался заново после каждого батча.
func supplierBarcodesRedirect(supplierID, msg, errMsg string) string {
	q := url.Values{}
	q.Set("id", supplierID)
	q.Set("barcodes", "1")
	if msg != "" {
		q.Set("msg", msg)
	}
	if errMsg != "" {
		q.Set("err", errMsg)
	}

	return "/ms/suppliers/edit?" + q.Encode() + "#barcodes-section"
}

// ReceiveBarcodesDelete — POST /ms/receive/barcodes/delete: удаление связки
// (со сносом тегов вики, если это был последний код товара у поставщика).
func (h *Handler) ReceiveBarcodesDelete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "не удалось разобрать форму", http.StatusBadRequest)

		return
	}

	supplierID := strings.TrimSpace(r.FormValue("supplier_id"))
	externalCode := strings.TrimSpace(r.FormValue("external_code"))

	if err := h.receiveUC.Remove(r.Context(), supplierID, externalCode); err != nil {
		slog.Info(fmt.Sprintf("receive: удалить код %q: %v", externalCode, err))
		http.Redirect(w, r, supplierBarcodesRedirect(supplierID, "", err.Error()), http.StatusSeeOther)

		return
	}

	http.Redirect(w, r, supplierBarcodesRedirect(supplierID, "Код удалён", ""), http.StatusSeeOther)
}
