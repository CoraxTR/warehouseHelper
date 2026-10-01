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
	"time"
)

// Kind — вид работы, результат которой собирает комната.
type Kind string

// KindReceive — совместная приёмка. Единственный вид, который умеет открывать
// клиент; Kind.Valid — точка расширения для инвентаризации.
const KindReceive Kind = "receive"

// Valid сообщает, умеет ли система открывать комнаты этого вида.
func (k Kind) Valid() bool {
	return k == KindReceive
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

var (
	// ErrNotFound — комнаты нет (в т.ч. после перезапуска приложения).
	ErrNotFound = errors.New("комната не найдена")
	// ErrClosed — комната уже закрыта сохранением.
	ErrClosed = errors.New("комната закрыта")
	// ErrGuestGone — гостя нет в комнате (отключён хостом или комната новая).
	ErrGuestGone = errors.New("гость не найден")
	// ErrKind — вид работы, который открывать не умеем.
	ErrKind = errors.New("неизвестный вид совместной работы")
)

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
	ID        string
	Kind      Kind
	Ref       string
	Title     string
	CreatedAt time.Time
	ClosedAt  time.Time
	GuestSeq  int
	Guests    []Guest
}

// Closed сообщает, закрыта ли комната (сохранена работа).
func (s Session) Closed() bool {
	return !s.ClosedAt.IsZero()
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

// LastActive — время последней активности комнаты: создание, подключение,
// отправка, отклик любого гостя.
func (s Session) LastActive() time.Time {
	last := s.CreatedAt

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

	return cp
}

// GuestName — автоимя участника: «Гость 1», «Гость 2», … Решение владельца
// 01.10.2026: имена не вводим, склад маленький.
func GuestName(seq int) string {
	return fmt.Sprintf("Гость %d", seq)
}
