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
// — заводим новую и выдаём ключ хозяина. Второе значение — «комнату открыла эта
// машина»: по нему страница решает, кто здесь хозяин (см. GoodsInventoryScanPage).
// Имя комнаты — сам вид инвентаризации.
func (h *Handler) openInventoryRoom(r *http.Request, w http.ResponseWriter, inventoryType string) (collab.Session, bool, error) {
	if room, ok := hostRoom(r, h.collabUC, collab.KindInventory, inventoryType); ok {
		return room, false, nil
	}

	room, created, err := h.collabUC.Open(collab.KindInventory, inventoryType, inventoryType)
	if err != nil {
		return collab.Session{}, false, err
	}

	if created {
		setRoomHostCookie(w, room)
	}

	return room, created, nil
}

// inventoryHost — пришёл ли запрос с машины, начавшей инвентаризацию. Роль
// доказывает cookie-ключ комнаты: комнаты на сервере может уже не быть (рестарт
// приложения, TTL, «Отменить») — ключ живёт дольше и продолжает работать.
func (h *Handler) inventoryHost(r *http.Request, sessionID string) bool {
	room, err := h.collabUC.State(sessionID)
	if err != nil {
		return hasHostCookie(r, sessionID)
	}

	return roomHost(r, room)
}

// guestInventoryScans отдаёт строки гостей для ПРЕДПРОСМОТРА: read-only снимок,
// комнату не занимает (Claim здесь нельзя — он блокирует проведение и требует
// готовности гостей). Комнаты нет — предпросмотр собираем по своим сканам:
// отказывать оператору из-за снесённой комнаты нельзя.
func (h *Handler) guestInventorySnapshot(sessionID string) ([]json.RawMessage, string) {
	rows, digest, err := h.collabUC.GuestSnapshot(sessionID)

	switch {
	case errors.Is(err, collab.ErrNotFound), errors.Is(err, collab.ErrClosed):
		slog.Info("inventory: комнаты нет — предпросмотр по своим сканам", "session_id", sessionID)
	case err != nil:
		// Предпросмотр — только справка: неполные строки гостей врут меньше, чем
		// отказ открыть предпросмотр. Документ всё равно собирает Conduct.
		slog.Info("inventory: строки гостей для предпросмотра не прочитаны", "session_id", sessionID, "err", err)
	default:
		return rows, digest
	}

	return nil, ""
}

// claimGuestInventoryScans забирает строки гостей на проведение инвентаризации:
// одним вызовом проверяет, что комната — этот же вид, не занята и все гости
// готовы, и отдаёт снимок строк (как есть, объектами). taken=false — комнаты нет
// (перезапуск приложения/purge) или она отменена: хост проводит по своим сканам,
// отказывать оператору из-за этого нельзя. Строки возвращаются в исходном виде —
// вынимает из них штрих-коды iucase.MergeGuestScans.
func (h *Handler) claimGuestInventoryScans(sessionID, inventoryType, digest string) (rows []json.RawMessage, taken bool, err error) {
	rows, err = h.collabUC.ClaimSnapshot(sessionID, inventoryType, digest)

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
	case errors.Is(err, collab.ErrGuestsChanged):
		// Гость вернулся и долил сканы после предпросмотра: проводить по строкам,
		// которых хозяин не видел, нельзя — страница сама перезапросит предпросмотр.
		invWriteJSON(w, http.StatusConflict, invErrorResponse{
			Error:         "гости прислали новые сканы после предпросмотра — документ соберётся по ним, проверьте количества",
			GuestsChanged: true,
		})
	default:
		invWriteJSONError(w, err)
	}
}
