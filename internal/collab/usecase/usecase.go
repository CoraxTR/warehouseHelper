// Пакет usecase — сценарии совместного сканирования: открыть комнату, показать
// открытые, подключить гостя, принять чанк, отключить, закрыть.
//
// Слой тонкий намеренно: правила «готов/заброшена» живут в домене, состояние — в
// хранилище (`collab.Store`), здесь — проверки входа, TTL заброшенных комнат и
// журнал. Тяжёлых зависимостей нет: комната ничего не сохраняет сама, сохранение
// делает модуль-владелец (приёмка), получив строки гостей через GuestScans.
package usecase

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"warehouseHelper/internal/collab"
)

// DefaultTTL — комната без активности столько времени считается заброшенной:
// её не показываем в списке и убираем из памяти. «Забытая» приёмка не должна
// висеть в списке открытых работ неделю.
const DefaultTTL = 6 * time.Hour

// ErrNeedRef — работа не указана: без идентификатора (поставщик, документ) нет
// ни идемпотентного открытия, ни смысла в комнате.
var ErrNeedRef = errors.New("не указана работа для совместного сканирования")

// UseCase — сценарии комнат.
type UseCase struct {
	store *collab.Store
	ttl   time.Duration
}

// NewUseCase собирает сценарии. Пустой TTL — DefaultTTL.
func NewUseCase(store *collab.Store, ttl time.Duration) *UseCase {
	if ttl <= 0 {
		ttl = DefaultTTL
	}

	return &UseCase{store: store, ttl: ttl}
}

// Open открывает комнату вида kind для работы ref (идемпотентно: одна открытая
// комната на работу). title — имя для списка открытых работ.
func (uc *UseCase) Open(kind collab.Kind, ref, title string) (collab.Session, bool, error) {
	if !kind.Valid() {
		return collab.Session{}, false, collab.ErrKind
	}

	ref = strings.TrimSpace(ref)
	if ref == "" {
		return collab.Session{}, false, ErrNeedRef
	}

	// Висящие комнаты убираем и здесь: их должно убивать время, а не чей-то
	// заход в список открытых работ.
	uc.store.Purge(uc.ttl)

	session, created, err := uc.store.Open(kind, ref, strings.TrimSpace(title))
	if err != nil {
		return collab.Session{}, false, err
	}

	slog.Info("collab: комната открыта",
		"session", session.ID, "kind", session.Kind, "ref", ref, "title", session.Title,
		"created", created)

	return session, created, nil
}

// List отдаёт открытые работы вида kind (свежие первыми), попутно убирая
// заброшенные.
func (uc *UseCase) List(kind collab.Kind) []collab.Session {
	uc.store.Purge(uc.ttl)

	return uc.store.List(kind, uc.ttl)
}

// State отдаёт состояние комнаты — им пользуются и хост, и гость (гость ещё и
// отмечается в LastSeen — это heartbeat).
func (uc *UseCase) State(id string) (collab.Session, error) {
	// Опрос идёт с каждой страницы: он же и убирает заброшенные комнаты.
	uc.store.Purge(uc.ttl)

	return uc.store.Get(id)
}

// Join подключает машину: пустой guestID — новый гость с автоименем, знакомый
// guestID — возврат того же гостя.
func (uc *UseCase) Join(id, guestID string) (collab.Session, error) {
	session, err := uc.store.Join(id, guestID)
	if err != nil {
		return collab.Session{}, err
	}

	if g, ok := guestOf(session, guestID); ok {
		slog.Info("collab: гость подключён",
			"session", session.ID, "guest", g.Name, "sent", g.Sent())
	}

	return session, nil
}

// Submit принимает чанк сканов гостя.
func (uc *UseCase) Submit(id, guestID string, scans []json.RawMessage, chunkID string) (collab.Session, error) {
	session, err := uc.store.Submit(id, guestID, scans, chunkID)
	if err != nil {
		return collab.Session{}, err
	}

	if g, ok := guestOf(session, guestID); ok {
		slog.Info("collab: гость прислал сканы",
			"session", session.ID, "guest", g.Name, "rows", len(scans), "total", g.Rows)
	}

	return session, nil
}

// SetScanning возвращает гостя в «сканирует» — он начал новый чанк.
func (uc *UseCase) SetScanning(id, guestID string) (collab.Session, error) {
	return uc.store.SetScanning(id, guestID)
}

// Touch отмечает отклик гостя (heartbeat опроса).
func (uc *UseCase) Touch(id, guestID string) (collab.Session, error) {
	return uc.store.Touch(id, guestID)
}

// Drop отключает гостя от комнаты: хост снимает того, у кого оборвалась связь.
func (uc *UseCase) Drop(id, guestID string) (collab.Session, error) {
	session, err := uc.store.Drop(id, guestID)
	if err != nil {
		return collab.Session{}, err
	}

	slog.Info("collab: гость отключён хостом", "session", session.ID, "guest", guestID)

	return session, nil
}

// Close закрывает комнату после успешного сохранения работы.
func (uc *UseCase) Close(id string) (collab.Session, error) {
	session, err := uc.store.Close(id)
	if err != nil {
		return collab.Session{}, err
	}

	slog.Info("collab: комната закрыта (работа сохранена)",
		"session", session.ID, "ref", session.Ref, "guests", len(session.Guests))

	return session, nil
}

// Cancel закрывает комнату отменой: совместная приёмка снята, но свои строки
// хозяин сохранить ещё может — поэтому причина закрытия отличается от сохранения.
func (uc *UseCase) Cancel(id string) (collab.Session, error) {
	session, err := uc.store.Cancel(id)
	if err != nil {
		return collab.Session{}, err
	}

	slog.Info("collab: совместная приёмка отменена хозяином",
		"session", session.ID, "ref", session.Ref)

	return session, nil
}

// GuestScans отдаёт строки всех гостей — модуль-владелец склеивает их со своими
// и сохраняет одним вызовом.
func (uc *UseCase) GuestScans(id string) ([]json.RawMessage, error) {
	return uc.store.GuestScans(id)
}

// Claim забирает строки гостей на сохранение: гейт «все готовы» и снимок строк —
// одна операция под мутексом хранилища, поэтому двойной клик по «Сохранить
// приёмку» не сохраняет работу дважды (повтор получает ErrBusy).
func (uc *UseCase) Claim(id, ref string) ([]json.RawMessage, error) {
	scans, err := uc.store.Claim(id, ref)
	if err != nil {
		return nil, err
	}

	slog.Info("collab: строки гостей забраны на сохранение", "session", id, "rows", len(scans))

	return scans, nil
}

// Release снимает «занята» после неудачного сохранения: хост может повторить.
func (uc *UseCase) Release(id string) error {
	if err := uc.store.Release(id); err != nil {
		return err
	}

	slog.Info("collab: комната освобождена после неудачного сохранения", "session", id)

	return nil
}

// Waiting отдаёт имена гостей, которых ждёт хост. Непустой список — сохранять
// нельзя (гейт кнопки у хоста и защита от устаревшей страницы на сервере).
func (uc *UseCase) Waiting(id string) ([]string, error) {
	return uc.store.Waiting(id)
}

// TTL отдаёт время забвения комнаты (для тестов и подсказок страницы).
func (uc *UseCase) TTL() time.Duration {
	return uc.ttl
}

// guestOf ищет гостя в копии комнаты по идентификатору.
func guestOf(session collab.Session, guestID string) (collab.Guest, bool) {
	for _, g := range session.Guests {
		if g.ID == guestID {
			return g, true
		}
	}

	return collab.Guest{}, false
}
