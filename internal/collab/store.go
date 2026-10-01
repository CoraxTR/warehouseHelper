package collab

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
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
func (s *Store) Open(kind Kind, ref, title string) (Session, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.findLocked(kind, ref); ok {
		return existing.Clone(), false, nil
	}

	id, err := newID()
	if err != nil {
		return Session{}, false, err
	}

	// 8 знаков — 4 млрд вариантов, но занятый id молча перезаписал бы чужую
	// комнату: берём следующий свободный.
	for {
		if _, busy := s.sessions[id]; !busy {
			break
		}

		if id, err = newID(); err != nil {
			return Session{}, false, err
		}
	}

	token, err := newToken()
	if err != nil {
		return Session{}, false, err
	}

	session := &Session{
		ID:        id,
		Kind:      kind,
		Ref:       ref,
		Title:     title,
		HostToken: token,
		CreatedAt: s.now(),
	}
	s.sessions[id] = session

	return session.Clone(), true, nil
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

	session, err := s.writableLocked(id)
	if err != nil {
		return Session{}, err
	}

	now := s.now()

	if guestID != "" {
		// Отключённого хостом гостя обратно не пускаем: иначе его страница
		// зашла бы следующим же опросом и снова заблокировала кнопку хоста.
		if session.IsDropped(guestID) {
			return Session{}, ErrGuestGone
		}

		if _, ok := guestLocked(session, guestID); ok {
			touchLocked(session, guestID, now)
			activeLocked(session, now)

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
	activeLocked(session, now)

	return session.Clone(), nil
}

// Submit принимает чанк сканов гостя: строки дописываются к отправленным,
// статус становится «готов». Отправленное не меняется — новый чанк только
// добавляет строки.
func (s *Store) Submit(id, guestID string, scans []json.RawMessage, chunkID string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, err := s.writableLocked(id)
	if err != nil {
		return Session{}, err
	}

	guest, ok := guestLocked(session, guestID)
	if !ok {
		return Session{}, ErrGuestGone
	}

	// Пустой чанк проверяем после комнаты и гостя: иначе закрытая комната или
	// отключённый гость получали бы «нечего отправлять» вместо причины.
	if len(scans) == 0 {
		return Session{}, ErrEmpty
	}

	sum := chunkSum(scans)

	// Повтор захода: ответ прошлого раза не дошёл, и гость жмёт кнопку снова.
	// Строки уже приняты — второй раз не дописываем, иначе приёмка задвоится.
	if chunkID != "" && chunkID == guest.LastChunk && sum == guest.LastChunkSum {
		return session.Clone(), nil
	}

	now := s.now()

	if chunkID != "" {
		guest.LastChunk = chunkID
		guest.LastChunkSum = sum
	}

	guest.Scans = append(guest.Scans, scans...)
	guest.Chunks++
	guest.Rows += len(scans)

	// Курсор строк: сколько номеров доехало до нас. Страница гостя сверит по нему
	// своё состояние и после обрыва дошлёт только новое.
	for _, scan := range scans {
		if seq := rowSeq(scan); seq > guest.LastSeq {
			guest.LastSeq = seq
		}
	}

	guest.Status = GuestReady
	guest.SubmittedAt = now
	guest.LastSeen = now
	activeLocked(session, now)

	return session.Clone(), nil
}

// SetScanning возвращает гостя в «сканирует»: он начал новый чанк, и хосту
// рано сохранять приёмку.
func (s *Store) SetScanning(id, guestID string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, err := s.writableLocked(id)
	if err != nil {
		return Session{}, err
	}

	guest, ok := guestLocked(session, guestID)
	if !ok {
		return Session{}, ErrGuestGone
	}

	guest.Status = GuestScanning
	guest.LastSeen = s.now()
	activeLocked(session, guest.LastSeen)

	return session.Clone(), nil
}

// Touch отмечает отклик гостя. Страница гостя опрашивает состояние — этот опрос
// и есть heartbeat: по LastSeen хост видит, кто отвалился.
func (s *Store) Touch(id, guestID string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, err := s.writableLocked(id)
	if err != nil {
		return Session{}, err
	}

	if _, ok := guestLocked(session, guestID); !ok {
		return Session{}, ErrGuestGone
	}

	now := s.now()
	touchLocked(session, guestID, now)
	activeLocked(session, now)

	return session.Clone(), nil
}

// Drop отключает гостя от комнаты (обрыв связи): его отправленное пропадает,
// хост больше его не ждёт.
func (s *Store) Drop(id, guestID string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, err := s.writableLocked(id)
	if err != nil {
		return Session{}, err
	}

	for i, g := range session.Guests {
		if g.ID == guestID {
			session.Guests = append(session.Guests[:i], session.Guests[i+1:]...)
			// Запоминаем отключённого: без этого машина вернулась бы в комнату
			// следующим же опросом, а её строки уже выброшены.
			session.Dropped = append(session.Dropped, guestID)
			activeLocked(session, s.now())

			return session.Clone(), nil
		}
	}

	return Session{}, ErrGuestGone
}

// Close закрывает комнату сохранением: работа сохранена, гости увидят это по
// состоянию. Повторное закрытие — не ошибка (причину не переписываем).
func (s *Store) Close(id string) (Session, error) {
	return s.close(id, ClosedSaved)
}

// Cancel закрывает комнату отменой: совместная приёмка снята, свои строки хост
// сохранить ещё может — поэтому причина отличается от сохранения.
func (s *Store) Cancel(id string) (Session, error) {
	return s.close(id, ClosedCancelled)
}

// close закрывает комнату с причиной: сохранение или отмена. Под mu.
func (s *Store) close(id string, reason ClosedReason) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok {
		return Session{}, ErrNotFound
	}

	if session.ClosedAt.IsZero() {
		session.ClosedAt = s.now()
		session.ClosedReason = reason
	}

	session.ClaimedAt = time.Time{}
	activeLocked(session, s.now())

	return session.Clone(), nil
}

// Claim забирает строки гостей на сохранение: под мутексом проверяет готовность,
// помечает комнату «занята» и отдаёт строки одним снимком. Это единственный вход
// сохранения — иначе двойной клик по «Сохранить приёмку» сохранил бы приёмку
// дважды (проверка готовности и выдача строк — две операции, между ними окно).
//
// Ошибку сохранения хост снимает Release (комната снова открыта для повтора).
func (s *Store) Claim(id, ref string) ([]json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok {
		return nil, ErrNotFound
	}

	// Закрытая приёмка: сохранённую не переписываем (повтор после потерянного
	// ответа не должен создать вторую приёмку), отменённую — можно сохранить.
	if session.Closed() {
		if session.ClosedReason == ClosedSaved {
			return nil, ErrAlreadySaved
		}

		return nil, ErrClosed
	}

	if ref != "" && session.Ref != ref {
		return nil, ErrRefMismatch
	}

	if session.Claimed() {
		return nil, ErrBusy
	}

	if names := session.Waiting(); len(names) > 0 {
		return nil, &NotReadyError{Names: names}
	}

	now := s.now()
	session.ClaimedAt = now
	activeLocked(session, now)

	return guestScans(session), nil
}

// Release снимает «занята» после неудачного сохранения: приёмку можно повторить.
func (s *Store) Release(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok {
		return ErrNotFound
	}

	session.ClaimedAt = time.Time{}
	activeLocked(session, s.now())

	return nil
}

// GuestScans отдаёт строки всех гостей в порядке подключения (внутри гостя — в
// порядке отправки чанков): хост склеивает их со своими и сохраняет одним
// вызовом. Пустой список — гостей нет или они ничего не отправили.
func (s *Store) GuestScans(id string) ([]json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, err := s.openLocked(id)
	if err != nil {
		return nil, err
	}

	return guestScans(session), nil
}

// Waiting отдаёт имена гостей, которых ждёт хост (пусто — сохранять можно).
func (s *Store) Waiting(id string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, err := s.openLocked(id)
	if err != nil {
		return nil, err
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

// writableLocked — комната, в которую можно писать от лица гостя: открыта и не
// занята сохранением. Под mu.
func (s *Store) writableLocked(id string) (*Session, error) {
	session, err := s.openLocked(id)
	if err != nil {
		return nil, err
	}

	if session.Claimed() {
		return nil, ErrBusy
	}

	return session, nil
}

// guestScans собирает строки гостей в порядке подключения (внутри гостя — в
// порядке отправки чанков). Под mu: снимок строк для сохранения.
func guestScans(session *Session) []json.RawMessage {
	var out []json.RawMessage

	for _, g := range session.Guests {
		out = append(out, g.Scans...)
	}

	return out
}

// guestLocked ищет гостя в комнате. Вызывать под mu: указатель смотрит внутрь
// session.Guests — держать его через append в Guests нельзя.
//
// Функция, а не метод: методы Session объявлены на значении (правила комнаты
// ничего не меняют), и линтер держит набор приёмников единым.
func guestLocked(session *Session, guestID string) (*Guest, bool) {
	if guestID == "" {
		return nil, false
	}

	for i := range session.Guests {
		if session.Guests[i].ID == guestID {
			return &session.Guests[i], true
		}
	}

	return nil, false
}

// touchLocked обновляет отклик гостя. Под mu.
func touchLocked(session *Session, guestID string, now time.Time) {
	if g, ok := guestLocked(session, guestID); ok {
		g.LastSeen = now
	}
}

// activeLocked отмечает активность комнаты: по ней комната считается свежей
// (в т.ч. когда гостей нет, а хост работает). Под mu.
func activeLocked(session *Session, now time.Time) {
	session.ActiveAt = now
}

// newToken — ключ хозяина комнаты: 16 шестнадцатеричных знаков. Подделать его
// сложнее, чем угадать идентификатор комнаты.
func newToken() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}

	return hex.EncodeToString(buf), nil
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

// rowSeq — номер строки из отправленной записи (0 — страница номера не дала:
// старый шаблон в кэше браузера, тогда курсор строк не работает).
func rowSeq(raw json.RawMessage) int64 {
	var probe struct {
		Seq int64 `json:"seq"`
	}

	if err := json.Unmarshal(raw, &probe); err != nil {
		return 0
	}

	return probe.Seq
}

// chunkSum — отпечаток содержимого захода: по нему узнаём повторную отправку того
// же захода. Длина каждой строки идёт в сумму, чтобы «12» и «1»+«2» не совпали.
func chunkSum(scans []json.RawMessage) string {
	parts := make([]string, 0, len(scans)*2)
	for _, scan := range scans {
		parts = append(parts, strconv.Itoa(len(scan)), string(scan))
	}

	sum := sha256.Sum256([]byte(strings.Join(parts, ":")))

	return hex.EncodeToString(sum[:])
}
