package collab

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// ErrEmpty — попытка отправить пустой чанк: гостю нечего присылать, а хосту
// нечего ждать.
var ErrEmpty = errors.New("нечего отправлять: пустой список сканов")

// Store — комнаты в памяти процесса. Все методы потокобезопасны; наружу
// отдаются только копии (`Session.Clone`), внутренние слайсы не текут.
//
// Часы — швом (`now`), чтобы правила «готов/заброшена» проверялись тестами без
// ожидания реального времени.
type Store struct {
	mu       sync.Mutex
	now      func() time.Time
	sessions map[string]*Session
}

// NewStore создаёт пустое хранилище. Пустой now — реальные часы.
func NewStore(now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}

	return &Store{now: now, sessions: make(map[string]*Session)}
}

// Open отдаёт открытую комнату по (kind, ref), а если её нет — создаёт новую.
// Идемпотентно: возврат хоста на страницу (F5, «Продолжить хостить») не плодит
// комнаты; для одного поставщика открытая комната всегда одна.
func (s *Store) Open(kind Kind, ref, title string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.findLocked(kind, ref); ok {
		return existing.Clone(), nil
	}

	id, err := newID()
	if err != nil {
		return Session{}, err
	}

	session := &Session{
		ID:        id,
		Kind:      kind,
		Ref:       ref,
		Title:     title,
		CreatedAt: s.now(),
	}
	s.sessions[id] = session

	return session.Clone(), nil
}

// Get возвращает комнату по идентификатору (копию).
func (s *Store) Get(id string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok {
		return Session{}, ErrNotFound
	}

	return session.Clone(), nil
}

// List возвращает открытые и не заброшенные комнаты вида kind, свежие первыми.
func (s *Store) List(kind Kind, ttl time.Duration) []Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()

	out := make([]Session, 0, len(s.sessions))

	for _, session := range s.sessions {
		if session.Kind != kind || session.Stale(now, ttl) {
			continue
		}

		out = append(out, session.Clone())
	}

	// Свежие первыми: комната, открытая минуту назад, — наверху списка.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].CreatedAt.After(out[j-1].CreatedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}

	return out
}

// Join подключает машину к комнате.
//
//   - пустой guestID — новый гость;
//   - известный guestID — возврат того же гостя (F5 на странице гостя не создаёт
//     «Гостя 3»): отправленное и статус сохраняются, обновляется LastSeen;
//   - неизвестный guestID (хост отключил, приложение перезапустили) — новый
//     гость с этим идентификатором, чтобы страница не теряла себя при перезагрузке.
func (s *Store) Join(id, guestID string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, err := s.openLocked(id)
	if err != nil {
		return Session{}, err
	}

	now := s.now()

	if guestID != "" {
		if _, ok := session.guestLocked(guestID); ok {
			session.touchLocked(guestID, now)

			return session.Clone(), nil
		}
	} else {
		guestID, err = newID()
		if err != nil {
			return Session{}, err
		}
	}

	session.GuestSeq++
	session.Guests = append(session.Guests, Guest{
		ID:       guestID,
		Name:     GuestName(session.GuestSeq),
		Status:   GuestScanning,
		JoinedAt: now,
		LastSeen: now,
	})

	return session.Clone(), nil
}

// Submit принимает чанк сканов гостя: строки дописываются к отправленным,
// статус становится «готов». Отправленное не меняется — новый чанк только
// добавляет строки.
func (s *Store) Submit(id, guestID string, scans []json.RawMessage) (Session, error) {
	if len(scans) == 0 {
		return Session{}, ErrEmpty
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	session, err := s.openLocked(id)
	if err != nil {
		return Session{}, err
	}

	guest, ok := session.guestLocked(guestID)
	if !ok {
		return Session{}, ErrGuestGone
	}

	now := s.now()
	guest.Scans = append(guest.Scans, scans...)
	guest.Chunks++
	guest.Rows += len(scans)
	guest.Status = GuestReady
	guest.SubmittedAt = now
	guest.LastSeen = now

	return session.Clone(), nil
}

// SetScanning возвращает гостя в «сканирует»: он начал новый чанк, и хосту
// рано сохранять приёмку.
func (s *Store) SetScanning(id, guestID string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, err := s.openLocked(id)
	if err != nil {
		return Session{}, err
	}

	guest, ok := session.guestLocked(guestID)
	if !ok {
		return Session{}, ErrGuestGone
	}

	guest.Status = GuestScanning
	guest.LastSeen = s.now()

	return session.Clone(), nil
}

// Touch отмечает отклик гостя. Страница гостя опрашивает состояние — этот опрос
// и есть heartbeat: по LastSeen хост видит, кто отвалился.
func (s *Store) Touch(id, guestID string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, err := s.openLocked(id)
	if err != nil {
		return Session{}, err
	}

	if _, ok := session.guestLocked(guestID); !ok {
		return Session{}, ErrGuestGone
	}

	session.touchLocked(guestID, s.now())

	return session.Clone(), nil
}

// Drop отключает гостя от комнаты (обрыв связи): его отправленное пропадает,
// хост больше его не ждёт.
func (s *Store) Drop(id, guestID string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, err := s.openLocked(id)
	if err != nil {
		return Session{}, err
	}

	for i, g := range session.Guests {
		if g.ID == guestID {
			session.Guests = append(session.Guests[:i], session.Guests[i+1:]...)

			return session.Clone(), nil
		}
	}

	return Session{}, ErrGuestGone
}

// Close закрывает комнату: работа сохранена, гости увидят это по состоянию.
// Повторное закрытие — не ошибка.
func (s *Store) Close(id string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok {
		return Session{}, ErrNotFound
	}

	if session.ClosedAt.IsZero() {
		session.ClosedAt = s.now()
	}

	return session.Clone(), nil
}

// GuestScans отдаёт строки всех гостей в порядке подключения (внутри гостя — в
// порядке отправки чанков): хост склеивает их со своими и сохраняет одним
// вызовом. Пустой список — гостей нет или они ничего не отправили.
func (s *Store) GuestScans(id string) ([]json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok {
		return nil, ErrNotFound
	}

	var out []json.RawMessage
	for _, g := range session.Guests {
		out = append(out, g.Scans...)
	}

	return out, nil
}

// Waiting отдаёт имена гостей, которых ждёт хост (пусто — сохранять можно).
func (s *Store) Waiting(id string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok {
		return nil, ErrNotFound
	}

	return session.Waiting(), nil
}

// Purge убирает закрытые и заброшенные комнаты (молчат дольше ttl): без него
// память росла бы с каждой забытой приёмкой. Возвращает, сколько убрано.
func (s *Store) Purge(ttl time.Duration) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()

	removed := 0

	for id, session := range s.sessions {
		if session.Stale(now, ttl) {
			delete(s.sessions, id)

			removed++
		}
	}

	return removed
}

// findLocked ищет открытую комнату вида kind по ref. Вызывать под mu.
func (s *Store) findLocked(kind Kind, ref string) (*Session, bool) {
	for _, session := range s.sessions {
		if session.Kind == kind && session.Ref == ref && !session.Closed() {
			return session, true
		}
	}

	return nil, false
}

// openLocked возвращает комнату по id, если она есть и не закрыта. Под mu.
func (s *Store) openLocked(id string) (*Session, error) {
	session, ok := s.sessions[id]
	if !ok {
		return nil, ErrNotFound
	}

	if session.Closed() {
		return nil, ErrClosed
	}

	return session, nil
}

// guestLocked ищет гостя в комнате. Вызывать под mu: указатель смотрит внутрь
// session.Guests — держать его через append в Guests нельзя.
func (s *Session) guestLocked(guestID string) (*Guest, bool) {
	if guestID == "" {
		return nil, false
	}

	for i := range s.Guests {
		if s.Guests[i].ID == guestID {
			return &s.Guests[i], true
		}
	}

	return nil, false
}

// touchLocked обновляет отклик гостя. Под mu.
func (s *Session) touchLocked(guestID string, now time.Time) {
	if g, ok := s.guestLocked(guestID); ok {
		g.LastSeen = now
	}
}

// newID — идентификатор комнаты: 8 шестнадцатеричных знаков. Это не «код для
// оператора» (кодов подключения у нас нет, решение владельца 01.10.2026), а
// адрес комнаты в списке открытых работ и в ссылке страницы гостя.
func newID() (string, error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}

	return hex.EncodeToString(buf), nil
}
