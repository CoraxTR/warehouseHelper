package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"warehouseHelper/internal/collab"
)

// Кооперативная инвентаризация: механика та же, что у совместной приёмки
// (модуль collab: комната, хозяин по cookie-ключу, гости шлют чанки, курсор seq,
// Claim/Release/Close). Отличие только в предмете: комната заводится на ВИД
// инвентаризации (products.inventory_type), потому что документа МС на момент
// открытия комнаты ещё нет, а сканы инвентаризации нигде на сервере не жили.
// Разделяемые роуты /ms/collab/* и хелперы комнаты (hostRoom, roomHost,
// setRoomHostCookie, releaseRoom) переиспользуются как есть.

// openInventoryRoom выдаёт машине комнату инвентаризации вида inventoryType:
// есть ключ живой комнаты этого вида — отдаём её же (F5 не заводит вторую), нет
// — заводим новую и выдаём ключ хозяина. Имя комнаты — сам вид инвентаризации.
func (h *Handler) openInventoryRoom(r *http.Request, w http.ResponseWriter, inventoryType string) (collab.Session, error) {
	if room, ok := hostRoom(r, h.collabUC, collab.KindInventory, inventoryType); ok {
		return room, nil
	}

	room, created, err := h.collabUC.Open(collab.KindInventory, inventoryType, inventoryType)
	if err != nil {
		return collab.Session{}, err
	}

	if created {
		setRoomHostCookie(w, room)
	}

	return room, nil
}

// claimGuestInventoryScans забирает строки гостей на проведение инвентаризации:
// одним вызовом проверяет, что комната — этот же вид, не занята и все гости
// готовы, и отдаёт снимок строк (как есть, объектами). taken=false — комнаты нет
// (перезапуск приложения/purge) или она отменена: хост проводит по своим сканам,
// отказывать оператору из-за этого нельзя. Строки возвращаются в исходном виде —
// вынимает из них штрих-коды iucase.MergeGuestScans.
func (h *Handler) claimGuestInventoryScans(sessionID, inventoryType string) (rows []json.RawMessage, taken bool, err error) {
	rows, err = h.collabUC.Claim(sessionID, inventoryType)

	switch {
	case errors.Is(err, collab.ErrNotFound), errors.Is(err, collab.ErrClosed):
		slog.Info("inventory: комнаты нет — провожу только по своим сканам",
			"session_id", sessionID, "err", err)

		return nil, false, nil
	case err != nil:
		return nil, false, err
	}

	return rows, true, nil
}

// invCollabError переводит ошибки комнаты инвентаризации в 409 с понятным
// оператору текстом: гости не готовы, комнату уже занял другой заход, вид не
// тот. Незнакомые ошибки уходят обычным путём (500 с логом).
func invCollabError(w http.ResponseWriter, err error) {
	if notReady, ok := errors.AsType[*collab.NotReadyError](err); ok {
		invWriteJSON(w, http.StatusConflict, invErrorResponse{Error: notReady.Error()})

		return
	}

	switch {
	case errors.Is(err, collab.ErrRefMismatch):
		invWriteJSON(w, http.StatusConflict, invErrorResponse{
			Error: "эта комната открыта для другого вида инвентаризации",
		})
	case errors.Is(err, collab.ErrBusy):
		invWriteJSON(w, http.StatusConflict, invErrorResponse{
			Error: "инвентаризация уже проводится — подождите пару секунд и повторите",
		})
	case errors.Is(err, collab.ErrAlreadySaved):
		invWriteJSON(w, http.StatusConflict, invErrorResponse{
			Error: "инвентаризация по этой ссылке уже проведена — второй документ не создаём",
		})
	case errors.Is(err, collab.ErrKind):
		invWriteJSON(w, http.StatusBadRequest, invErrorResponse{Error: err.Error()})
	default:
		invWriteJSONError(w, err)
	}
}
