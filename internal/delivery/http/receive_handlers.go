package http

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// ReceiveBarcodesAdd — POST /ms/receive/barcodes/add: добавление связки
// «внешний код → товар» поставщика (виджет на карточке поставщика).
// После операции — редирект на форму поставщика (PRG), ошибки — в query err.
func (h *Handler) ReceiveBarcodesAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "не удалось разобрать форму", http.StatusBadRequest)

		return
	}

	supplierID := strings.TrimSpace(r.FormValue("supplier_id"))
	externalCode := strings.TrimSpace(r.FormValue("external_code"))
	productID := strings.TrimSpace(r.FormValue("product_id"))

	if err := h.receiveUC.Add(r.Context(), supplierID, externalCode, productID); err != nil {
		slog.Info(fmt.Sprintf("receive: добавить код %q: %v", externalCode, err))
		http.Redirect(w, r, "/ms/suppliers/edit?id="+url.QueryEscape(supplierID)+"&err="+url.QueryEscape(err.Error()), http.StatusSeeOther)

		return
	}

	http.Redirect(w, r, "/ms/suppliers/edit?id="+url.QueryEscape(supplierID), http.StatusSeeOther)
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
		http.Redirect(w, r, "/ms/suppliers/edit?id="+url.QueryEscape(supplierID)+"&err="+url.QueryEscape(err.Error()), http.StatusSeeOther)

		return
	}

	http.Redirect(w, r, "/ms/suppliers/edit?id="+url.QueryEscape(supplierID), http.StatusSeeOther)
}
