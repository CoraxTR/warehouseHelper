package collab

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// clock — управляемые часы: правила «готов/заброшена» проверяются без ожидания
// реального времени.
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func newClock() *clock {
	return &clock{t: time.Date(2026, time.October, 1, 9, 0, 0, 0, time.UTC)}
}

// scan собирает строку скана в формате сохраняющего модуля.
func scan(raw string) json.RawMessage {
	return json.RawMessage(`{"raw":"` + raw + `"}`)
}

func mustOpen(t *testing.T, s *Store, ref string) Session {
	t.Helper()

	session, err := s.Open(KindReceive, ref, "Поставщик "+ref)
	if err != nil {
		t.Fatalf("Open(%q): %v", ref, err)
	}

	return session
}

func join(t *testing.T, s *Store, id, guestID string) Session {
	t.Helper()

	session, err := s.Join(id, guestID)
	if err != nil {
		t.Fatalf("Join(%q, %q): %v", id, guestID, err)
	}

	return session
}

func submit(t *testing.T, s *Store, id, guestID string, scans ...json.RawMessage) Session {
	t.Helper()

	session, err := s.Submit(id, guestID, scans)
	if err != nil {
		t.Fatalf("Submit(%q, %q): %v", id, guestID, err)
	}

	return session
}

// TestOpenIdempotent — на одну работу одна открытая комната: возврат хоста на
// страницу не плодит комнаты, другая работа — другая комната, а закрытая работа
// открывается заново.
func TestOpenIdempotent(t *testing.T) {
	c := newClock()
	s := NewStore(c.now)

	first := mustOpen(t, s, "sup-1")
	again := mustOpen(t, s, "sup-1")
	if again.ID != first.ID {
		t.Errorf("повторное открытие дало новую комнату: %q → %q", first.ID, again.ID)
	}

	other := mustOpen(t, s, "sup-2")
	if other.ID == first.ID {
		t.Errorf("разные работы получили одну комнату: %q", other.ID)
	}

	if _, err := s.Close(first.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := mustOpen(t, s, "sup-1")
	if reopened.ID == first.ID {
		t.Errorf("после закрытия комната не переоткрылась: %q", reopened.ID)
	}

	if len(reopened.ID) != 8 {
		t.Errorf("идентификатор комнаты = %q, ожидались 8 знаков", reopened.ID)
	}
}

// TestJoinNamesAndRejoin — автоимена по порядку подключения; знакомый guestID
// возвращает того же гостя, а не создаёт нового.
func TestJoinNamesAndRejoin(t *testing.T) {
	s := NewStore(newClock().now)
	room := mustOpen(t, s, "sup-1")

	room = join(t, s, room.ID, "")
	if len(room.Guests) != 1 || room.Guests[0].Name != "Гость 1" {
		t.Fatalf("первый гость: %+v", room.Guests)
	}

	firstID := room.Guests[0].ID
	if firstID == "" {
		t.Fatal("новому гостю не выдан идентификатор")
	}

	room = join(t, s, room.ID, "")
	if len(room.Guests) != 2 || room.Guests[1].Name != "Гость 2" {
		t.Fatalf("второй гость: %+v", room.Guests)
	}

	// Перезагрузка страницы гостя: тот же идентификатор — тот же гость.
	room = join(t, s, room.ID, firstID)
	if len(room.Guests) != 2 {
		t.Errorf("возврат гостя создал новую запись: %+v", room.Guests)
	}

	if room.Guests[0].ID != firstID || room.Guests[0].Name != "Гость 1" {
		t.Errorf("возврат гостя изменил запись: %+v", room.Guests[0])
	}

	// Идентификатор, которого в комнате нет (хост отключил), — новый гость с
	// этим идентификатором: страница сохраняет себя.
	room = join(t, s, room.ID, "deadbeef")
	if len(room.Guests) != 3 || room.Guests[2].ID != "deadbeef" || room.Guests[2].Name != "Гость 3" {
		t.Errorf("подключение после отключения: %+v", room.Guests)
	}
}

// TestReadyGate — хост сохраняет только когда все подключённые гости готовы;
// новый чанк возвращает гостя в «сканирует».
func TestReadyGate(t *testing.T) {
	s := NewStore(newClock().now)
	room := mustOpen(t, s, "sup-1")

	if !room.Ready() || len(room.Waiting()) != 0 {
		t.Error("комната без гостей должна быть готова к сохранению")
	}

	room = join(t, s, room.ID, "")
	if room.Ready() {
		t.Error("комната с неотправившим гостем не готова")
	}

	room = submit(t, s, room.ID, room.Guests[0].ID, scan("111"))
	if !room.Ready() || len(room.Waiting()) != 0 {
		t.Errorf("после отправки комната готова: ready=%v waiting=%v", room.Ready(), room.Waiting())
	}

	if _, err := s.SetScanning(room.ID, room.Guests[0].ID); err != nil {
		t.Fatalf("SetScanning: %v", err)
	}

	room, err := s.Get(room.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if room.Ready() {
		t.Error("гость начал новый чанк — комната снова не готова")
	}

	if got := room.Waiting(); len(got) != 1 || got[0] != "Гость 1" {
		t.Errorf("Waiting = %v, ожидался [Гость 1]", got)
	}

	// Второй гость готов, первый — нет: хост ждёт только первого.
	room = join(t, s, room.ID, "")
	room = submit(t, s, room.ID, room.Guests[1].ID, scan("222"))

	if got := room.Waiting(); len(got) != 1 || got[0] != "Гость 1" {
		t.Errorf("Waiting = %v, ожидался [Гость 1]", got)
	}
}

// TestSubmitAccumulates — чанки гостя накапливаются: строки не теряются, статус
// «готов», счётчики растут.
func TestSubmitAccumulates(t *testing.T) {
	s := NewStore(newClock().now)
	room := mustOpen(t, s, "sup-1")
	room = join(t, s, room.ID, "")
	guestID := room.Guests[0].ID

	room = submit(t, s, room.ID, guestID, scan("111"), scan("222"))
	room = submit(t, s, room.ID, guestID, scan("333"))

	stored := room.Guests[0]
	if stored.Chunks != 2 {
		t.Errorf("Chunks = %d, ожидалось 2", stored.Chunks)
	}

	if stored.Rows != 3 {
		t.Errorf("Rows = %d, ожидалось 3", stored.Rows)
	}

	if len(stored.Scans) != 3 {
		t.Fatalf("Scans = %d, ожидалось 3", len(stored.Scans))
	}

	if stored.Status != GuestReady || !stored.Sent() {
		t.Errorf("статус после отправки: %+v", stored)
	}

	scans, err := s.GuestScans(room.ID)
	if err != nil {
		t.Fatalf("GuestScans: %v", err)
	}

	want := []string{"111", "222", "333"}
	if len(scans) != len(want) {
		t.Fatalf("склеенных строк %d, ожидалось %d", len(scans), len(want))
	}

	for i, raw := range want {
		if !strings.Contains(string(scans[i]), raw) {
			t.Errorf("строка %d = %s, ожидалась %q", i, scans[i], raw)
		}
	}

	if room.RowsSent() != 3 {
		t.Errorf("RowsSent = %d, ожидалось 3", room.RowsSent())
	}
}

// TestGuestScansOrder — строки склеиваются в порядке подключения гостей: хост
// получит детерминированный список, а не порядок карты.
func TestGuestScansOrder(t *testing.T) {
	s := NewStore(newClock().now)
	room := mustOpen(t, s, "sup-1")

	room = join(t, s, room.ID, "")
	first := room.Guests[0].ID
	room = join(t, s, room.ID, "")
	second := room.Guests[1].ID

	// Отправка в обратном порядке: склейка всё равно по подключению.
	room = submit(t, s, room.ID, second, scan("bbb"))
	room = submit(t, s, room.ID, first, scan("aaa"), scan("aaa2"))

	scans, err := s.GuestScans(room.ID)
	if err != nil {
		t.Fatalf("GuestScans: %v", err)
	}

	got := make([]string, 0, len(scans))
	for _, raw := range scans {
		got = append(got, string(raw))
	}

	want := []string{`{"raw":"aaa"}`, `{"raw":"aaa2"}`, `{"raw":"bbb"}`}

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("порядок склейки = %v, ожидался %v", got, want)
	}
}

// TestDropGuest — отключённый гость не виден в комнате, его строки в склейку не
// идут, комната снова готова, а обращения к нему — ErrGuestGone.
func TestDropGuest(t *testing.T) {
	s := NewStore(newClock().now)
	room := mustOpen(t, s, "sup-1")
	room = join(t, s, room.ID, "")
	guestID := room.Guests[0].ID
	room = submit(t, s, room.ID, guestID, scan("111"))

	room, err := s.Drop(room.ID, guestID)
	if err != nil {
		t.Fatalf("Drop: %v", err)
	}

	if len(room.Guests) != 0 {
		t.Errorf("после отключения гости: %+v", room.Guests)
	}

	if !room.Ready() {
		t.Error("без гостей комната готова к сохранению")
	}

	scans, err := s.GuestScans(room.ID)
	if err != nil {
		t.Fatalf("GuestScans: %v", err)
	}

	if len(scans) != 0 {
		t.Errorf("строки отключённого гостя попали в склейку: %v", scans)
	}

	if _, err := s.Submit(room.ID, guestID, []json.RawMessage{scan("222")}); !errors.Is(err, ErrGuestGone) {
		t.Errorf("Submit отключённого гостя: %v, ожидалась ErrGuestGone", err)
	}

	if _, err := s.Drop(room.ID, guestID); !errors.Is(err, ErrGuestGone) {
		t.Errorf("повторный Drop: %v, ожидалась ErrGuestGone", err)
	}
}

// TestCloneIsolation — хранилище отдаёт копии: правка полученной комнаты не
// меняет состояние комнаты.
func TestCloneIsolation(t *testing.T) {
	s := NewStore(newClock().now)
	room := mustOpen(t, s, "sup-1")
	room = join(t, s, room.ID, "")
	room = submit(t, s, room.ID, room.Guests[0].ID, scan("111"))

	got, err := s.Get(room.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	got.Guests[0].Name = "хакер"
	got.Guests = append(got.Guests, Guest{ID: "ghost", Name: "Гость 9"})
	got.Guests[0].Scans[0] = scan("подмена")
	got.Guests[0].Status = GuestScanning

	fresh, err := s.Get(room.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if len(fresh.Guests) != 1 {
		t.Fatalf("в комнате %d гостей, ожидался 1", len(fresh.Guests))
	}

	if fresh.Guests[0].Name != "Гость 1" {
		t.Errorf("имя гостя изменилось: %q", fresh.Guests[0].Name)
	}

	if fresh.Guests[0].Status != GuestReady {
		t.Errorf("статус гостя изменился: %q", fresh.Guests[0].Status)
	}

	if !strings.Contains(string(fresh.Guests[0].Scans[0]), "111") {
		t.Errorf("строка скана изменилась: %s", fresh.Guests[0].Scans[0])
	}
}

// TestErrors — таблица отказов: нет комнаты, пустой чанк, закрытая комната.
func TestErrors(t *testing.T) {
	c := newClock()
	s := NewStore(c.now)
	open := mustOpen(t, s, "sup-1")

	empty := mustOpen(t, s, "sup-2")
	emptyGuest := join(t, s, empty.ID, "").Guests[0].ID
	closed := mustOpen(t, s, "sup-3")
	if _, err := s.Close(closed.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}

	tests := []struct {
		name string
		call func() error
		want error
	}{
		{"Get без комнаты", func() error { _, err := s.Get("нет"); return err }, ErrNotFound},
		{"Join без комнаты", func() error { _, err := s.Join("нет", ""); return err }, ErrNotFound},
		{"GuestScans без комнаты", func() error { _, err := s.GuestScans("нет"); return err }, ErrNotFound},
		{"Waiting без комнаты", func() error { _, err := s.Waiting("нет"); return err }, ErrNotFound},
		{"пустой чанк от живого гостя", func() error { _, err := s.Submit(empty.ID, emptyGuest, nil); return err }, ErrEmpty},
		{"пустой чанк у незнакомого гостя", func() error { _, err := s.Submit(empty.ID, "кто-то", nil); return err }, ErrGuestGone},
		{"Submit в закрытую", func() error {
			_, err := s.Submit(closed.ID, "кто-то", []json.RawMessage{scan("111")})

			return err
		}, ErrClosed},
		{"Submit в закрытую с пустым чанком", func() error { _, err := s.Submit(closed.ID, "кто-то", nil); return err }, ErrClosed},
		{"Join в закрытую", func() error { _, err := s.Join(closed.ID, ""); return err }, ErrClosed},
		{"Touch в закрытую", func() error { _, err := s.Touch(closed.ID, "кто-то"); return err }, ErrClosed},
		{"SetScanning в закрытую", func() error { _, err := s.SetScanning(closed.ID, "кто-то"); return err }, ErrClosed},
		{"Claim в закрытую", func() error { _, err := s.Claim(closed.ID, "sup-3"); return err }, ErrClosed},
		{"Claim чужой работы", func() error { _, err := s.Claim(open.ID, "sup-9"); return err }, ErrRefMismatch},
		{"GuestScans в закрытую", func() error { _, err := s.GuestScans(closed.ID); return err }, ErrClosed},
		{"Waiting в закрытую", func() error { _, err := s.Waiting(closed.ID); return err }, ErrClosed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); !errors.Is(err, tt.want) {
				t.Errorf("ошибка = %v, ожидалась %v", err, tt.want)
			}
		})
	}

	// Закрытая комната читается (гостю нужно увидеть «приёмка сохранена»), но в
	// список открытых работ не попадает.
	got, err := s.Get(closed.ID)
	if err != nil || !got.Closed() {
		t.Errorf("закрытая комната: session=%+v err=%v", got, err)
	}

	if list := s.List(KindReceive, 6*time.Hour); len(list) != 2 {
		t.Errorf("в списке открытых %d комнат, ожидалось 2", len(list))
	}

	if !open.Ready() {
		t.Error("чистая комната готова к сохранению")
	}
}

// TestStaleAndPurge — заброшенная и закрытая комнаты уходят из списка и из
// памяти; молчание меньше TTL комнату не убивает.
func TestStaleAndPurge(t *testing.T) {
	c := newClock()
	s := NewStore(c.now)
	ttl := 6 * time.Hour

	live := mustOpen(t, s, "sup-1")
	stale := mustOpen(t, s, "sup-2")
	closed := mustOpen(t, s, "sup-3")

	live = join(t, s, live.ID, "")
	c.add(2 * time.Hour)
	// Отклик гостя продлевает жизнь комнаты.
	if _, err := s.Touch(live.ID, live.Guests[0].ID); err != nil {
		t.Fatalf("Touch: %v", err)
	}

	if _, err := s.Close(closed.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}

	c.add(5 * time.Hour)

	if got := s.List(KindReceive, ttl); len(got) != 1 || got[0].ID != live.ID {
		t.Errorf("список открытых = %+v, ожидалась только живая комната", got)
	}

	if removed := s.Purge(ttl); removed != 2 {
		t.Errorf("Purge убрал %d комнат, ожидалось 2 (закрытая и заброшенная)", removed)
	}

	if _, err := s.Get(stale.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("заброшенная комната осталась: %v", err)
	}

	if live.Stale(c.now(), 0) {
		t.Error("нулевой TTL не должен считать комнату заброшенной")
	}
}

// TestConcurrentUse — комната под нагрузкой: параллельные подключения,
// отправки и отклики не рвут состояние (гонки ловит -race).
func TestConcurrentUse(t *testing.T) {
	s := NewStore(newClock().now)
	room := mustOpen(t, s, "sup-1")

	const guests = 8

	var wg sync.WaitGroup

	for range guests {
		wg.Go(func() {
			joined, err := s.Join(room.ID, "")
			if err != nil {
				t.Errorf("Join: %v", err)

				return
			}

			guestID := joined.Guests[len(joined.Guests)-1].ID

			for range 20 {
				if _, err := s.Submit(room.ID, guestID, []json.RawMessage{scan("111")}); err != nil {
					t.Errorf("Submit: %v", err)

					return
				}

				if _, err := s.Touch(room.ID, guestID); err != nil {
					t.Errorf("Touch: %v", err)

					return
				}

				if _, err := s.Get(room.ID); err != nil {
					t.Errorf("Get: %v", err)

					return
				}
			}
		})
	}

	wg.Wait()

	final, err := s.Get(room.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if len(final.Guests) != guests {
		t.Errorf("подключённых гостей %d, ожидалось %d", len(final.Guests), guests)
	}

	if got := final.RowsSent(); got != guests*20 {
		t.Errorf("присланных строк %d, ожидалось %d", got, guests*20)
	}

	if !final.Ready() {
		t.Error("после отправок комната готова к сохранению")
	}
}

// TestClaimSavesOnce — строки гостей забираются на сохранение одним снимком:
// двойной клик по «Сохранить приёмку» не сохранит приёмку дважды (находка ревью
// 01.10.2026: проверка готовности и выдача строк были раздельными операциями).
// Ошибку сохранения снимает Release, успех — Close.
func TestClaimSavesOnce(t *testing.T) {
	s := NewStore(newClock().now)

	room := join(t, s, mustOpen(t, s, "sup-1").ID, "")
	guestID := room.Guests[0].ID

	if _, err := s.Submit(room.ID, guestID, []json.RawMessage{scan("111"), scan("222")}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	scans, err := s.Claim(room.ID, "sup-1")
	if err != nil || len(scans) != 2 {
		t.Fatalf("Claim: строк %d, err=%v", len(scans), err)
	}

	// Второй заход по той же комнате — уже занята: приёмка не сохранится дважды.
	if _, err := s.Claim(room.ID, "sup-1"); !errors.Is(err, ErrBusy) {
		t.Errorf("повторный Claim: %v, ожидалась ErrBusy", err)
	}

	// Пока комната занята, гости в неё не пишут.
	if _, err := s.Submit(room.ID, guestID, []json.RawMessage{scan("333")}); !errors.Is(err, ErrBusy) {
		t.Errorf("Submit в занятую: %v, ожидалась ErrBusy", err)
	}

	if _, err := s.Join(room.ID, ""); !errors.Is(err, ErrBusy) {
		t.Errorf("Join в занятую: %v, ожидалась ErrBusy", err)
	}

	// Ошибка сохранения: хост освобождает комнату — строки на месте, повтор можно.
	if err := s.Release(room.ID); err != nil {
		t.Fatalf("Release: %v", err)
	}

	if scans, err := s.Claim(room.ID, "sup-1"); err != nil || len(scans) != 2 {
		t.Errorf("Claim после Release: строк %d, err=%v", len(scans), err)
	}

	// Успех: комната закрыта, третий заход отбит.
	if _, err := s.Close(room.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := s.Claim(room.ID, "sup-1"); !errors.Is(err, ErrClosed) {
		t.Errorf("Claim после Close: %v, ожидалась ErrClosed", err)
	}
}

// TestClaimGates — гейт готовности: неготового гостя не сохраняем, но комнату и
// не занимаем (иначе хост не смог бы сохранить после того, как гость дослал).
func TestClaimGates(t *testing.T) {
	s := NewStore(newClock().now)

	room := join(t, s, mustOpen(t, s, "sup-1").ID, "")
	guestID := room.Guests[0].ID

	_, err := s.Claim(room.ID, "sup-1")

	var notReady *NotReadyError
	if !errors.As(err, &notReady) {
		t.Fatalf("Claim с неготовым гостем: %v, ожидалась NotReadyError", err)
	}

	if len(notReady.Names) != 1 || notReady.Names[0] != GuestName(1) {
		t.Errorf("имена ожидаемых гостей = %v", notReady.Names)
	}

	if _, err := s.Submit(room.ID, guestID, []json.RawMessage{scan("111")}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Отбитый заход не занял комнату: после готовности сохранение проходит.
	if scans, err := s.Claim(room.ID, "sup-1"); err != nil || len(scans) != 1 {
		t.Errorf("Claim после готовности: строк %d, err=%v", len(scans), err)
	}
}

// TestDropSticks — отключённый гость не возвращается сам (его страница опрашивает
// состояние и зашла бы обратно), его строки выброшены, а действие хоста продлевает
// жизнь комнаты.
func TestDropSticks(t *testing.T) {
	c := newClock()
	s := NewStore(c.now)

	room := join(t, s, mustOpen(t, s, "sup-1").ID, "")
	guestID := room.Guests[0].ID

	if _, err := s.Submit(room.ID, guestID, []json.RawMessage{scan("111")}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	c.add(2 * time.Hour)

	dropped, err := s.Drop(room.ID, guestID)
	if err != nil {
		t.Fatalf("Drop: %v", err)
	}

	if len(dropped.Guests) != 0 || !dropped.IsDropped(guestID) {
		t.Errorf("после Drop: гостей %d, отключённые %v", len(dropped.Guests), dropped.Dropped)
	}

	if _, err := s.Join(room.ID, guestID); !errors.Is(err, ErrGuestGone) {
		t.Errorf("возврат отключённого гостя: %v, ожидалась ErrGuestGone", err)
	}

	if scans, err := s.GuestScans(room.ID); err != nil || len(scans) != 0 {
		t.Errorf("строки отключённого идут в сохранение: %d, err=%v", len(scans), err)
	}

	// Отключение — действие хоста: комната не считается заброшенной.
	if dropped.Stale(c.now(), time.Hour) {
		t.Error("отключение гостя хостом должно продлевать жизнь комнаты")
	}
}

// TestHeartbeatAndNewChunk — отклик готового гостя не сбивает готовность, а новый
// заход не теряет отправленное раньше (инварианты, найденные ревью 01.10.2026).
func TestHeartbeatAndNewChunk(t *testing.T) {
	s := NewStore(newClock().now)

	room := join(t, s, mustOpen(t, s, "sup-1").ID, "")
	guestID := room.Guests[0].ID

	if _, err := s.Submit(room.ID, guestID, []json.RawMessage{scan("111")}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	touched, err := s.Touch(room.ID, guestID)
	if err != nil {
		t.Fatalf("Touch: %v", err)
	}

	if !touched.Ready() || touched.Guests[0].Rows != 1 || touched.Guests[0].Chunks != 1 {
		t.Errorf("отклик сбил состояние гостя: %+v", touched.Guests[0])
	}

	scanning, err := s.SetScanning(room.ID, guestID)
	if err != nil {
		t.Fatalf("SetScanning: %v", err)
	}

	if scanning.Ready() {
		t.Error("новый заход должен снова закрывать кнопку хоста")
	}

	if _, err := s.Claim(room.ID, "sup-1"); err == nil {
		t.Error("Claim с недосланным заходом не должен проходить")
	}

	after, err := s.Submit(room.ID, guestID, []json.RawMessage{scan("222")})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if len(after.Guests[0].Scans) != 2 || after.Guests[0].Chunks != 2 || after.Guests[0].Rows != 2 || !after.Ready() {
		t.Errorf("новый заход потерял отправленное: %+v", after.Guests[0])
	}
}
