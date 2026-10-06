// Пакет collab — совместное сканирование: «комната», в которой одну и ту же
// работу (приёмку, позже — инвентаризацию) с разных машин собирают несколько
// операторов.
//
// Приложение одно и живёт на одном адресе в локальной сети, все машины ходят в
// него браузером. Поэтому никакой передачи данных между машинами нет: «хост» —
// роль вкладки, состояние комнаты лежит в памяти сервера, гости пишут и читают
// теми же HTTP-роутами. Хранилище — в памяти (`store.go`), таблиц и миграций
// нет: решение владельца 01.10.2026 (перезапуск приложения = гости подключаются
// заново).
//
// Сканы гостей хранятся как есть — в формате сохраняющего модуля (для приёмки
// это `receiveSaveScan`), чтобы модуль collab не знал домена приёмки. Склейку
// «хостовые + гостевые» и один вызов Save делает слой доставки (см.
// `delivery/http/receive_page.go`).
package collab

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Kind — вид работы, результат которой собирает комната.
type Kind string

// KindReceive — совместная приёмка.
const KindReceive Kind = "receive"

// KindInventory — совместная инвентаризация. Комната заводится на ВИД
// инвентаризации (products.inventory_type): документа МойСклад на момент
// открытия комнаты ещё нет, а вид — это и есть работа (решение владельца
// 06.10.2026: отдельного «совместного» режима не плодим — комната всегда).
const KindInventory Kind = "inventory"

// Valid сообщает, умеет ли система открывать комнаты этого вида.
func (k Kind) Valid() bool {
	return k == KindReceive || k == KindInventory
}

// GuestStatus — состояние участника-гостя.
type GuestStatus string

const (
	// GuestScanning — гость ещё ничего не отправил (или начал новый чанк):
	// хост его ждёт.
	GuestScanning GuestStatus = "scanning"
	// GuestReady — отправленное ушло на хост и не меняется: сохранять можно.
	GuestReady GuestStatus = "ready"
)

// HostName — имя участника-хоста: у хоста нет записи в списке гостей, он всегда
// первый и единственный владелец кнопки сохранения.
const HostName = "Хост"

// ClosedReason — почему комната закрыта. Разница важна сохранению: после «уже
// сохранено» повтор не пропускаем (иначе вторая приёмка того же поставщика), а
// после отмены приёмку сохранить можно — свои строки остались на странице.
type ClosedReason string

const (
	// ClosedSaved — работа сохранена.
	ClosedSaved ClosedReason = "saved"
	// ClosedCancelled — хозяин отменил совместную приёмку.
	ClosedCancelled ClosedReason = "cancelled"
)

var (
	// ErrNotFound — комнаты нет (в т.ч. после перезапуска приложения).
	ErrNotFound = errors.New("комната не найдена")
	// ErrClosed — комната уже закрыта сохранением.
	ErrClosed = errors.New("комната закрыта")
	// ErrGuestGone — гостя нет в комнате (отключён хостом или комната новая).
	ErrGuestGone = errors.New("гость не найден")
	// ErrKind — вид работы, который открывать не умеем.
	ErrKind = errors.New("неизвестный вид совместной работы")
	// ErrBusy — хост уже забирает строки на сохранение: второй раз брать нечего.
	ErrBusy = errors.New("приёмка уже сохраняется")
	// ErrRefMismatch — комната открыта для другой работы: строки чужого
	// поставщика в текущее сохранение не попадут.
	ErrRefMismatch = errors.New("комната открыта для другого поставщика")
	// ErrAlreadySaved — приёмка по этой ссылке уже сохранена: повторное
	// сохранение создало бы вторую приёмку того же поставщика (ответ прошлого
	// раза мог не дойти до страницы — владелец, 01.10.2026).
	ErrAlreadySaved = errors.New("приёмка по этой ссылке уже сохранена")
)

// NotReadyError — сохранение не начинается: хост ещё ждёт гостей. Имена уходят
// оператору в тексте ошибки как есть.
type NotReadyError struct{ Names []string }

func (e *NotReadyError) Error() string {
	return "не все гости готовы: " + strings.Join(e.Names, ", ")
}

// Guest — подключённая к комнате машина.
//
// Scans — плоский список отправленных строк в формате сохраняющего модуля
// (для приёмки — объекты `receiveSaveScan`), в порядке отправки чанков.
type Guest struct {
	ID          string
	Name        string
	Status      GuestStatus
	Chunks      int
	Rows        int
	Scans       []json.RawMessage
	JoinedAt    time.Time
	LastSeen    time.Time
	SubmittedAt time.Time

	// Заход гостя опознаётся идентификатором, а содержимое — отпечатком строк:
	// повторная отправка того же захода (оборвалась сеть, ответ не дошёл)
	// распознаётся и не задваивает строки приёмки.
	LastChunk    string
	LastChunkSum string

	// LastSeq — наибольший номер строки, принятый от этого гостя. Страница
	// гостя нумерует строки и по этому номеру понимает, что уже у хозяина:
	// повтор после обрыва отправляет только новое (владелец, 01.10.2026).
	LastSeq int64
}

// Sent сообщает, отправлял ли гость хоть что-то.
func (g Guest) Sent() bool {
	return g.Chunks > 0
}

// IsReady сообщает, что гость прислал свои сканы и хост его не ждёт. Русскую
// подпись («готов»/«сканирует») держит шаблон: текст — не правило домена.
func (g Guest) IsReady() bool {
	return g.Status == GuestReady
}

// Session — комната: хост + подключённые гости.
//
// Ref — идентификатор сохраняемой работы (для приёмки — ID поставщика), по нему
// комната открывается идемпотентно. Title — человекочитаемое имя для списка
// открытых работ (имя поставщика).
type Session struct {
	ID    string
	Kind  Kind
	Ref   string
	Title string

	// HostToken — ключ хозяина приёмки: приложение выдаёт его машине, открывшей
	// приёмку, и та держит его у себя (cookie комнаты). По адресу машины хозяина
	// не опознать — весь склад ходит через VPN (владелец, 01.10.2026), поэтому
	// роль решает ключ. Хост не меняется: с чужим ключом страница — гость.
	HostToken string
	CreatedAt time.Time
	ClosedAt  time.Time

	// ClosedReason — сохранением комната закрыта или отменой: после сохранения
	// повтор не пропускаем (ErrAlreadySaved), после отмены — сохранить можно.
	ClosedReason ClosedReason
	GuestSeq     int
	Guests       []Guest

	// ActiveAt — последняя активность комнаты: действия гостей и хоста. Нужна,
	// чтобы комната, где хост работает один (опросил, отключил гостя), не
	// считалась заброшенной и не сносилась очисткой.
	ActiveAt time.Time

	// ClaimedAt — хост забрал строки на сохранение: второй Save по этой комнате
	// не пойдёт (иначе двойной клик сохранил бы приёмку дважды). Снимается
	// закрытием (успех) или Release (ошибка сохранения).
	ClaimedAt time.Time

	// Dropped — id отключённых хостом гостей: комната помнит их, иначе страница
	// гостя зашла бы обратно следующим же опросом и снова заблокировала кнопку.
	Dropped []string
}

// Closed сообщает, закрыта ли комната (сохранена работа).
func (s Session) Closed() bool {
	return !s.ClosedAt.IsZero()
}

// Claimed сообщает, что идёт сохранение по этой комнате.
func (s Session) Claimed() bool {
	return !s.ClaimedAt.IsZero()
}

// IsDropped сообщает, отключал ли хост эту машину от комнаты.
func (s Session) IsDropped(guestID string) bool {
	return slices.Contains(s.Dropped, guestID)
}

// Ready сообщает, можно ли сохранять: все подключённые гости прислали свои
// сканы. Гостей нет — хост может сохранять один.
func (s Session) Ready() bool {
	for _, g := range s.Guests {
		if g.Status != GuestReady {
			return false
		}
	}

	return true
}

// Waiting — имена гостей, которых хост ещё ждёт (для подсказки у кнопки).
func (s Session) Waiting() []string {
	var names []string

	for _, g := range s.Guests {
		if g.Status != GuestReady {
			names = append(names, g.Name)
		}
	}

	return names
}

// RowsSent — сколько строк прислали гости (для панели хоста).
func (s Session) RowsSent() int {
	total := 0

	for _, g := range s.Guests {
		total += g.Rows
	}

	return total
}

// LastActive — время последней активности комнаты: создание, действия хоста,
// подключение, отправка, отклик любого гостя.
func (s Session) LastActive() time.Time {
	last := s.CreatedAt

	if s.ActiveAt.After(last) {
		last = s.ActiveAt
	}

	for _, g := range s.Guests {
		if g.LastSeen.After(last) {
			last = g.LastSeen
		}
	}

	return last
}

// Stale сообщает, что комната заброшена: не закрыта и молчит дольше ttl.
// Такие комнаты не показываем в списке открытых работ.
func (s Session) Stale(now time.Time, ttl time.Duration) bool {
	if s.Closed() {
		return true
	}

	if ttl <= 0 {
		return false
	}

	return now.Sub(s.LastActive()) > ttl
}

// Clone возвращает копию комнаты: хранилище отдаёт наружу только копии, чтобы
// читатель не гонял с писателем по внутренним слайсам. Байты сканов не
// копируются — они пишутся один раз и не меняются.
func (s Session) Clone() Session {
	cp := s

	if s.Guests != nil {
		cp.Guests = make([]Guest, len(s.Guests))
		copy(cp.Guests, s.Guests)

		for i := range cp.Guests {
			if g := s.Guests[i]; g.Scans != nil {
				cp.Guests[i].Scans = make([]json.RawMessage, len(g.Scans))
				copy(cp.Guests[i].Scans, g.Scans)
			}
		}
	}

	if s.Dropped != nil {
		cp.Dropped = make([]string, len(s.Dropped))
		copy(cp.Dropped, s.Dropped)
	}

	return cp
}

// GuestName — автоимя участника: «Гость 1», «Гость 2», … Решение владельца
// 01.10.2026: имена не вводим, склад маленький.
func GuestName(seq int) string {
	return fmt.Sprintf("Гость %d", seq)
}

// HostTokenMatches — пришёл ли запрос с ключом хозяина этой приёмки. Пустой ключ
// хозяйским не считается: без ключа роль не выдаётся.
func (s Session) HostTokenMatches(token string) bool {
	return token != "" && s.HostToken != "" && token == s.HostToken
}

// hostCookiePrefix — начало имени cookie хозяина комнаты.
const hostCookiePrefix = "collab_host_"

// HostCookieName — имя cookie комнаты: у каждой приёмки своё, чтобы одна машина
// могла вести две приёмки в разных вкладках.
func HostCookieName(roomID string) string {
	return hostCookiePrefix + roomID
}

// RoomIDFromHostCookie — идентификатор комнаты из имени cookie хозяина. Пусто —
// cookie не про наш ключ (чужие cookie машины игнорируем).
func RoomIDFromHostCookie(name string) string {
	if !strings.HasPrefix(name, hostCookiePrefix) {
		return ""
	}

	return strings.TrimPrefix(name, hostCookiePrefix)
}

// HostKeyLooksLike — имя cookie принадлежит комнате roomID и значение непустое.
// Нужно, когда комнаты на сервере уже нет (рестарт приложения, TTL, «Отменить»):
// сам ключ хозяина живёт дольше комнаты (12 ч против 6 ч) и отличает машину,
// начавшую работу, от гостя — у гостя в session_id та же комната, но ключа нет.
func HostKeyLooksLike(cookieName, cookieValue, roomID string) bool {
	return cookieValue != "" && RoomIDFromHostCookie(cookieName) == roomID
}
