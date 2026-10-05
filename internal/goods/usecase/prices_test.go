package usecase

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/msclient/client"
)

// captureSlog перехватывает пакетный slog в буфер (проверка сообщений поллера)
// и возвращает прежний логгер по завершении теста.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()

	buf := &bytes.Buffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	return buf
}

// cursorWrite — одна запись курсора: время следующего прохода и МСК-дата
// полного прохода. Побочный эффект, который проверяют тесты (что именно и
// сколько раз записано), поэтому храним обе части, а не только время.
type cursorWrite struct {
	next         time.Time
	lastFullScan string
}

// stubPricesRepo — заглушка PricesRepository (без БД). Мьютекс — из-за теста
// с Run в горутине (проверка, что ошибка не роняет поллер).
type stubPricesRepo struct {
	mu sync.Mutex

	ids       []string
	idsErr    error
	updateErr error
	// updateN > 0 — принудительное число РЕАЛЬНО обновлённых строк (холостые
	// UPDATE отсеяны); 0 — вернуть len(prices). J<K штатен (обновились не все
	// цены) и WARN не выставляет — проверяем это отдельно.
	updateN int

	cursor       time.Time
	lastFullScan string
	cursorOK     bool
	cursorErr    error
	setErr       error

	updated    [][]domain.ProductPrice
	setCursors []cursorWrite
}

var _ PricesRepository = (*stubPricesRepo)(nil)

func (s *stubPricesRepo) ProductIDs(_ context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.ids, s.idsErr
}

func (s *stubPricesRepo) UpdateProductPrices(_ context.Context, prices []domain.ProductPrice) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.updateErr != nil {
		return 0, s.updateErr
	}
	s.updated = append(s.updated, prices)
	if s.updateN > 0 {
		return s.updateN, nil
	}

	return len(prices), nil
}

func (s *stubPricesRepo) GetPriceCursor(_ context.Context) (time.Time, string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.cursor, s.lastFullScan, s.cursorOK, s.cursorErr
}

func (s *stubPricesRepo) SetPriceCursor(_ context.Context, next time.Time, lastFullScan string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.setErr != nil {
		return s.setErr
	}
	s.cursor = next
	s.lastFullScan = lastFullScan
	s.cursorOK = true
	s.setCursors = append(s.setCursors, cursorWrite{next: next, lastFullScan: lastFullScan})

	return nil
}

func (s *stubPricesRepo) updatedTotal() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := 0
	for _, batch := range s.updated {
		n += len(batch)
	}

	return n
}

// writes — снимок всех записей курсора (копия, чтобы тест не держал мьютекс).
func (s *stubPricesRepo) writes() []cursorWrite {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]cursorWrite(nil), s.setCursors...)
}

// cursorNow — текущее состояние курсора в заглушке (последнее, что записали).
func (s *stubPricesRepo) cursorNow() (time.Time, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.cursor, s.lastFullScan, s.cursorOK
}

// stubPriceClient — заглушка ProductPriceClient (без сети).
type stubPriceClient struct {
	byIDsErr   error
	sinceErr   error
	byIDsResp  func(ids []string) []client.MSProductPrice
	sinceResp  func(since time.Time) []client.MSProductPrice
	onSince    func() // хук на каждый запрос инкремента (синхронизация Run-теста)
	mu         sync.Mutex
	byIDChunks [][]string
	sinceCalls []time.Time
}

var _ ProductPriceClient = (*stubPriceClient)(nil)

func (s *stubPriceClient) FetchProductPricesByIDs(_ context.Context, ids []string) ([]client.MSProductPrice, error) {
	s.mu.Lock()
	s.byIDChunks = append(s.byIDChunks, append([]string(nil), ids...))
	s.mu.Unlock()

	if s.byIDsErr != nil {
		return nil, s.byIDsErr
	}
	if s.byIDsResp == nil {
		out := make([]client.MSProductPrice, 0, len(ids))
		for _, id := range ids {
			out = append(out, client.MSProductPrice{ID: id})
		}

		return out, nil
	}

	return s.byIDsResp(ids), nil
}

func (s *stubPriceClient) FetchProductPricesSince(_ context.Context, since time.Time) ([]client.MSProductPrice, error) {
	s.mu.Lock()
	s.sinceCalls = append(s.sinceCalls, since)
	onSince := s.onSince
	s.mu.Unlock()

	if onSince != nil {
		onSince()
	}
	if s.sinceErr != nil {
		return nil, s.sinceErr
	}
	if s.sinceResp == nil {
		return nil, nil
	}

	return s.sinceResp(since), nil
}

func (s *stubPriceClient) chunks() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.byIDChunks
}

func (s *stubPriceClient) sinceTimes() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]time.Time(nil), s.sinceCalls...)
}

// assertCursorWrite — проверяет, что курсор записан ровно один раз и именно
// парой (next, lastFullScan): побочный эффект тика, а не только возврат.
func assertCursorWrite(t *testing.T, repo *stubPricesRepo, wantNext time.Time, wantDate string) {
	t.Helper()

	writes := repo.writes()
	if len(writes) != 1 {
		t.Fatalf("записей курсора: %d, want 1: %+v", len(writes), writes)
	}
	if !writes[0].next.Equal(wantNext) {
		t.Errorf("next = %v, want %v", writes[0].next, wantNext)
	}
	if writes[0].lastFullScan != wantDate {
		t.Errorf("lastFullScan = %q, want %q", writes[0].lastFullScan, wantDate)
	}
}

// TestPriceTickCalendar — календарная привязка полного прохода (МСК): решение
// «батч или инкремент» принимает ОДИН тик по МСК-дате последнего полного
// прохода, прочитанной из БД. Условие прохода — lastFullScan != сегодня (НЕ
// «< сегодня»): дата из будущего при «<» подавляла бы проход НАВСЕГДА.
// Закрепляем:
//   - нет строки курсора → полный проход сразу + запись курсора (next=now,
//     дата=сегодня МСК); при падении запроса курсор НЕ пишем — следующий тик
//     повторит проход (инкременту опираться не на что);
//   - дата = сегодня МСК → полного прохода нет, инкремент;
//   - дата != сегодня МСК (вчера ИЛИ будущее) → полный проход, дата
//     перезаписана СТРОГО (даже если запрос цен упал — ошибка наружу, но
//     SetPriceCursor вызван);
//   - при падении полного прохода в новый день дата всё равно двигается на
//     сегодня, а next_scan_at остаётся СТАРЫМ: окно [старый курсор..now]
//     остаётся открытым, и следующий инкремент доберёт пропущенное (при
//     успехе next_scan_at = now);
//   - сутки считаются по МСК, а не UTC (граница полуночи).
func TestPriceTickCalendar(t *testing.T) {
	ids := []string{"p1", "p2"}

	tests := []struct {
		name string

		cursorOK     bool
		cursor       time.Time
		lastFullScan string
		now          time.Time
		fetchErr     error

		wantFull     bool      // ждём полный проход (запрос по всем id)
		wantIncrem   bool      // ждём инкремент (запрос по окну)
		wantWrites   int       // сколько раз записан курсор
		wantNext     time.Time // время следующего прохода в последней записи
		wantFullDate string    // МСК-дата полного прохода в последней записи
		wantErr      bool
	}{
		{
			name:         "нет строки курсора → полный проход и запись курсора",
			cursorOK:     false,
			now:          time.Date(2026, time.October, 5, 10, 0, 0, 0, time.UTC), // 13:00 МСК
			wantFull:     true,
			wantWrites:   1,
			wantNext:     time.Date(2026, time.October, 5, 10, 0, 0, 0, time.UTC),
			wantFullDate: "2026-10-05",
		},
		{
			name:         "курсор есть, дата сегодня (МСК) → инкремент",
			cursorOK:     true,
			cursor:       time.Date(2026, time.October, 5, 9, 0, 0, 0, time.UTC),
			lastFullScan: "2026-10-05",
			now:          time.Date(2026, time.October, 5, 10, 0, 0, 0, time.UTC),
			wantIncrem:   true,
			wantWrites:   1,
			wantNext:     time.Date(2026, time.October, 5, 10, 0, 0, 0, time.UTC),
			wantFullDate: "2026-10-05",
		},
		{
			name:         "курсор есть, дата вчера → полный проход, дата перезаписана",
			cursorOK:     true,
			cursor:       time.Date(2026, time.October, 4, 9, 0, 0, 0, time.UTC),
			lastFullScan: "2026-10-04",
			now:          time.Date(2026, time.October, 5, 10, 0, 0, 0, time.UTC),
			wantFull:     true,
			wantWrites:   1,
			wantNext:     time.Date(2026, time.October, 5, 10, 0, 0, 0, time.UTC),
			wantFullDate: "2026-10-05",
		},
		{
			// Без строки курсора инкременту опираться не на что: при падении
			// запроса строку НЕ создаём, следующий тик повторит полный проход.
			name:     "нет строки, запрос цен упал → курсор НЕ пишем",
			cursorOK: false,
			now:      time.Date(2026, time.October, 5, 10, 0, 0, 0, time.UTC),
			fetchErr: errMC,
			wantFull: true,
			wantErr:  true,
		},
		{
			// Строгая перезапись ДАТЫ на новом дне: даже упавший запрос не
			// оставляет вчерашнюю дату — иначе тик за тиком крутил бы полный
			// проход. Но next_scan_at остаётся СТАРЫМ (прежним курсором): окно
			// [старый курсор..now] не закрывается, и следующий инкремент
			// доберёт всё, что изменилось за время простоя.
			name:         "новый день, запрос цен упал → дата перезаписана, next остаётся старым",
			cursorOK:     true,
			cursor:       time.Date(2026, time.October, 4, 9, 0, 0, 0, time.UTC),
			lastFullScan: "2026-10-04",
			now:          time.Date(2026, time.October, 5, 10, 0, 0, 0, time.UTC),
			fetchErr:     errMC,
			wantFull:     true,
			wantWrites:   1,
			wantNext:     time.Date(2026, time.October, 4, 9, 0, 0, 0, time.UTC), // окно не закрыто
			wantFullDate: "2026-10-05",
			wantErr:      true,
		},
		{
			// Регресс условия «!= сегодня»: дата из БУДУЩЕГО (разъехались часы
			// сервера / ручная правка БД) при прежнем «<» навсегда подавляла
			// полный проход. Теперь проход идёт, а дата нормализуется на сегодня.
			name:         "дата в будущем → полный проход, а не вечное подавление",
			cursorOK:     true,
			cursor:       time.Date(2026, time.October, 5, 9, 0, 0, 0, time.UTC),
			lastFullScan: "2026-10-09",
			now:          time.Date(2026, time.October, 5, 10, 0, 0, 0, time.UTC),
			wantFull:     true,
			wantWrites:   1,
			wantNext:     time.Date(2026, time.October, 5, 10, 0, 0, 0, time.UTC),
			wantFullDate: "2026-10-05",
		},
		{
			// МСК-полночь: 21:30 UTC — это уже 00:30 СЛЕДУЮЩЕГО дня в МСК,
			// значит вчерашняя (по UTC) дата устарела → полный проход, и в
			// хранилище должна уйти МСК-дата 06-го, а не UTC 05-е.
			name:         "МСК-сутки: 21:30 UTC (00:30 МСК) → полный проход",
			cursorOK:     true,
			cursor:       time.Date(2026, time.October, 5, 21, 0, 0, 0, time.UTC),
			lastFullScan: "2026-10-05", // дата по UTC — в МСК уже 06-е
			now:          time.Date(2026, time.October, 5, 21, 30, 0, 0, time.UTC),
			wantFull:     true,
			wantWrites:   1,
			wantNext:     time.Date(2026, time.October, 5, 21, 30, 0, 0, time.UTC),
			wantFullDate: "2026-10-06",
		},
		{
			// Обратная сторона той же границы: 20:30 UTC = 23:30 МСК — ещё
			// сегодня в МСК, полного прохода нет (сутки не перекатились).
			name:         "МСК-сутки: 20:30 UTC (23:30 МСК) → инкремент",
			cursorOK:     true,
			cursor:       time.Date(2026, time.October, 5, 20, 0, 0, 0, time.UTC),
			lastFullScan: "2026-10-05",
			now:          time.Date(2026, time.October, 5, 20, 30, 0, 0, time.UTC),
			wantIncrem:   true,
			wantWrites:   1,
			wantNext:     time.Date(2026, time.October, 5, 20, 30, 0, 0, time.UTC),
			wantFullDate: "2026-10-05",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &stubPricesRepo{
				ids:          ids,
				cursorOK:     tt.cursorOK,
				cursor:       tt.cursor,
				lastFullScan: tt.lastFullScan,
			}
			cl := &stubPriceClient{byIDsErr: tt.fetchErr, sinceErr: tt.fetchErr}
			p := NewPricesPoller(PricesConfig{Now: func() time.Time { return tt.now }}, repo, cl)

			err := p.tick(context.Background())
			if tt.wantErr && err == nil {
				t.Fatal("tick вернул nil, ожидалась ошибка")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("tick: %v", err)
			}

			// Полный проход и инкремент различимы по тому, какой метод клиента
			// МС реально вызван (это и есть наблюдаемый побочный эффект выбора).
			gotFull := len(cl.chunks()) > 0
			gotIncrem := len(cl.sinceTimes()) > 0
			if gotFull != tt.wantFull {
				t.Errorf("полный проход: got %v, want %v (чанков %d)", gotFull, tt.wantFull, len(cl.chunks()))
			}
			if gotIncrem != tt.wantIncrem {
				t.Errorf("инкремент: got %v, want %v (запросов окна %d)", gotIncrem, tt.wantIncrem, len(cl.sinceTimes()))
			}

			// На успешном полном проходе в МС обязан уйти весь каталог.
			if tt.wantFull && tt.fetchErr == nil {
				total := 0
				for _, chunk := range cl.chunks() {
					total += len(chunk)
				}
				if total != len(ids) {
					t.Errorf("в МС ушло %d id, want %d", total, len(ids))
				}
			}

			writes := repo.writes()
			if len(writes) != tt.wantWrites {
				t.Fatalf("записей курсора: %d, want %d: %+v", len(writes), tt.wantWrites, writes)
			}
			if tt.wantWrites > 0 {
				last := writes[len(writes)-1]
				if !last.next.Equal(tt.wantNext) {
					t.Errorf("next = %v, want %v", last.next, tt.wantNext)
				}
				if last.lastFullScan != tt.wantFullDate {
					t.Errorf("lastFullScan = %q, want %q", last.lastFullScan, tt.wantFullDate)
				}
			}
		})
	}
}

// TestPriceFullScanFallbackWindow — провал полного прохода в новый день НЕ
// закрывает окно инкремента: дата двигается на сегодня, а next_scan_at остаётся
// старым, и следующий тик (дата уже сегодня → инкремент) спрашивает МС со
// СТАРОГО курсора, добирая всё, что изменилось за простой. Ровно ради этого
// окно [старый курсор..now] оставляют открытым.
func TestPriceFullScanFallbackWindow(t *testing.T) {
	now := time.Date(2026, time.October, 5, 10, 0, 0, 0, time.UTC)
	staleCursor := time.Date(2026, time.October, 4, 9, 0, 0, 0, time.UTC)

	repo := &stubPricesRepo{ids: []string{"a", "b"}, cursor: staleCursor, cursorOK: true, lastFullScan: "2026-10-04"}
	cl := &stubPriceClient{byIDsErr: errMC}
	p := NewPricesPoller(PricesConfig{Now: func() time.Time { return now }}, repo, cl)

	// Тик 1: новый день, полный проход падает.
	if err := p.tick(context.Background()); err == nil {
		t.Fatal("тик 1: ожидалась ошибка полного прохода")
	}
	// Дата перезаписана на сегодня, время — старое (окно осталось открытым).
	cur, date, ok := repo.cursorNow()
	if !ok || !cur.Equal(staleCursor) || date != "2026-10-05" {
		t.Fatalf("курсор после падения: next=%v date=%q (ok=%v), want next=%v date=2026-10-05",
			cur, date, ok, staleCursor)
	}

	// Тик 2: дата = сегодня → инкремент, и запрошен СТАРЫЙ курсор (окно не закрыто).
	if err := p.tick(context.Background()); err != nil {
		t.Fatalf("тик 2 (инкремент): %v", err)
	}
	since := cl.sinceTimes()
	if len(since) != 1 || !since[0].Equal(staleCursor) {
		t.Fatalf("инкремент запрошен с %v, want %v (окно [старый курсор..now] должно остаться открытым)",
			since, staleCursor)
	}

	// Успешный инкремент закрывает окно: next двигается на now.
	writes := repo.writes()
	if len(writes) != 2 {
		t.Fatalf("записей курсора: %d, want 2: %+v", len(writes), writes)
	}
	if !writes[1].next.Equal(now) || writes[1].lastFullScan != "2026-10-05" {
		t.Errorf("после инкремента next=%v date=%q, want next=%v date=2026-10-05",
			writes[1].next, writes[1].lastFullScan, now)
	}
}

// TestPriceTickIncrement — инкремент (полный проход уже делался сегодня МСК): окно
// [курсор..now], строгая перезапись времени на now и разные судьбы курсора при
// ошибках. Проверяем именно побочные эффекты: с каким since ушли в МС, сколько
// записали и что именно легло в курсор (время И дата полного прохода).
func TestPriceTickIncrement(t *testing.T) {
	now := time.Date(2026, time.October, 5, 10, 0, 0, 0, time.UTC)
	today := "2026-10-05" // МСК-дата now
	cursor := now.Add(-time.Hour)

	t.Run("успех: окно от курсора, курсор=now, дата сохраняется", func(t *testing.T) {
		repo := &stubPricesRepo{cursor: cursor, cursorOK: true, lastFullScan: today}
		cl := &stubPriceClient{
			sinceResp: func(time.Time) []client.MSProductPrice {
				return []client.MSProductPrice{{ID: "p1"}, {ID: "p2"}}
			},
		}
		p := NewPricesPoller(PricesConfig{Now: func() time.Time { return now }}, repo, cl)

		if err := p.tick(context.Background()); err != nil {
			t.Fatalf("tick: %v", err)
		}

		since := cl.sinceTimes()
		if len(since) != 1 || !since[0].Equal(cursor) {
			t.Fatalf("окно запрошено с %v, want %v", since, cursor)
		}
		if got := repo.updatedTotal(); got != 2 {
			t.Errorf("записано товаров: %d, want 2", got)
		}
		// Дата полного прохода сохраняется как была, время двигается на now.
		assertCursorWrite(t, repo, now, today)
	})

	t.Run("ошибка запроса МС: время=now, дата сохраняется, ошибка наружу", func(t *testing.T) {
		repo := &stubPricesRepo{cursor: cursor, cursorOK: true, lastFullScan: today}
		cl := &stubPriceClient{sinceErr: errMC}
		p := NewPricesPoller(PricesConfig{Now: func() time.Time { return now }}, repo, cl)

		if err := p.tick(context.Background()); err == nil {
			t.Fatal("tick вернул nil, ожидалась ошибка запроса")
		}
		// Строгая перезапись: окно не догоняем, но время двигаем; дата полного
		// прохода не трогается.
		assertCursorWrite(t, repo, now, today)
		if got := repo.updatedTotal(); got != 0 {
			t.Errorf("при ошибке запроса записано товаров: %d, want 0", got)
		}
	})

	t.Run("ошибка записи цен в БД: курсор НЕ двигается", func(t *testing.T) {
		repo := &stubPricesRepo{cursor: cursor, cursorOK: true, lastFullScan: today, updateErr: errMC}
		cl := &stubPriceClient{
			sinceResp: func(time.Time) []client.MSProductPrice {
				return []client.MSProductPrice{{ID: "p1"}}
			},
		}
		p := NewPricesPoller(PricesConfig{Now: func() time.Time { return now }}, repo, cl)

		if err := p.tick(context.Background()); err == nil {
			t.Fatal("tick вернул nil, ожидалась ошибка записи цен")
		}
		if writes := repo.writes(); len(writes) != 0 {
			t.Fatalf("курсор записан при ошибке БД: %+v", writes)
		}
		// Следующим тиком переспросим то же окно: прежний курсор не тронут.
		cur, date, ok := repo.cursorNow()
		if !ok || !cur.Equal(cursor) || date != today {
			t.Errorf("курсор сдвинулся при ошибке БД: %v %q (ok=%v)", cur, date, ok)
		}
	})
}

// TestPriceTickBatchChunking — полный проход режет id на чанки BatchSize и
// спрашивает МС пачками. Побочные эффекты: ровно такие чанки (по порядку и
// целиком), все id уходят в хранилище. Пустой каталог — ни одного запроса.
func TestPriceTickBatchChunking(t *testing.T) {
	ids := make([]string, 250)
	for i := range ids {
		ids[i] = fmt.Sprintf("id-%03d", i)
	}

	tests := []struct {
		name      string
		batchSize int
		ids       []string
		wantSizes []int
	}{
		{"пустой каталог → ни одного запроса", 100, nil, nil},
		{"меньше чанка → один запрос", 100, ids[:50], []int{50}},
		{"ровно чанк → один запрос", 100, ids[:100], []int{100}},
		{"250 при BatchSize=100 → 100/100/50", 100, ids, []int{100, 100, 50}},
		{"BatchSize=200 → 200/50", 200, ids, []int{200, 50}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &stubPricesRepo{ids: tt.ids}
			cl := &stubPriceClient{}
			p := NewPricesPoller(PricesConfig{BatchSize: tt.batchSize}, repo, cl)

			if err := p.tickBatch(context.Background()); err != nil {
				t.Fatalf("tickBatch: %v", err)
			}

			chunks := cl.chunks()
			if len(chunks) != len(tt.wantSizes) {
				t.Fatalf("чанков: %d, want %d", len(chunks), len(tt.wantSizes))
			}
			for i, want := range tt.wantSizes {
				if len(chunks[i]) != want {
					t.Errorf("чанк %d: %d id, want %d", i, len(chunks[i]), want)
				}
			}
			// Порядок и содержимое: склейка чанков — ровно исходный каталог.
			if flat := slices.Concat(chunks...); !slices.Equal(flat, tt.ids) {
				t.Errorf("чанки не совпали с каталогом: %v", flat)
			}
			if got := repo.updatedTotal(); got != len(tt.ids) {
				t.Errorf("в хранилище ушло %d товаров, want %d", got, len(tt.ids))
			}
		})
	}
}

// TestPriceBatchLogs — батч логирует «запросили/вернулось/обновили».
func TestPriceBatchLogs(t *testing.T) {
	ids := make([]string, 250)
	for i := range ids {
		ids[i] = "id"
	}
	repo := &stubPricesRepo{ids: ids}
	cl := &stubPriceClient{}

	// Первым тиком (нет курсора) идёт полный проход — заодно сверим лог батча.
	now := time.Date(2026, time.October, 5, 10, 0, 0, 0, time.UTC)
	p := NewPricesPoller(PricesConfig{Now: func() time.Time { return now }}, repo, cl)

	buf := captureSlog(t)
	if err := p.tickBatch(context.Background()); err != nil {
		t.Fatalf("tickBatch: %v", err)
	}

	log := buf.String()
	for _, want := range []string{"запросили=250", "вернулось=250", "обновили=250"} {
		if !strings.Contains(log, want) {
			t.Errorf("в логе нет %q: %s", want, log)
		}
	}
}

// TestPriceBatchShortfallWarns — WARN о «расхождении» ровно при returned <
// requested (МС не отдала часть запрошенных id — потеря покрытия, лечится
// ресинком каталога). J<K (UpdateProductPrices вернул меньше присланного) —
// ШТАТНО: метод возвращает число ИЗМЕНИВШИХСЯ строк, холостые UPDATE отсеяны
// по IS DISTINCT FROM, поэтому WARN по нему не выставляется.
func TestPriceBatchShortfallWarns(t *testing.T) {
	t.Run("returned < requested → WARN", func(t *testing.T) {
		buf := captureSlog(t)

		// Спросили три id, МС вернула две строки — расхождение.
		repo := &stubPricesRepo{ids: []string{"a", "b", "c"}}
		cl := &stubPriceClient{byIDsResp: func([]string) []client.MSProductPrice {
			return []client.MSProductPrice{{ID: "a"}, {ID: "b"}}
		}}
		p := NewPricesPoller(PricesConfig{}, repo, cl)

		if err := p.tickBatch(context.Background()); err != nil {
			t.Fatalf("tickBatch: %v", err)
		}

		log := buf.String()
		if !strings.Contains(log, "расхождение") || !strings.Contains(log, "level=WARN") {
			t.Errorf("ожидался WARN о расхождении: %s", log)
		}
	})

	t.Run("J<K (цены не изменились) → без WARN", func(t *testing.T) {
		buf := captureSlog(t)

		// Вернулось 3, легло 1: холостые UPDATE отсеяны, покрытие полное.
		repo := &stubPricesRepo{ids: []string{"a", "b", "c"}, updateN: 1}
		cl := &stubPriceClient{}
		p := NewPricesPoller(PricesConfig{}, repo, cl)

		if err := p.tickBatch(context.Background()); err != nil {
			t.Fatalf("tickBatch: %v", err)
		}

		log := buf.String()
		if strings.Contains(log, "расхождение") || strings.Contains(log, "level=WARN") {
			t.Errorf("J<K штатен, WARN не ожидался: %s", log)
		}
	})
}

// TestPriceIncrementFetchErrorDoesNotStopPoller — ошибка тика залогирована, но Run
// не падает и продолжает крутиться (как в sitecheck). Полный проход уже сделан
// сегодня (дата совпадает) — значит крутится именно инкремент.
func TestPriceIncrementFetchErrorDoesNotStopPoller(t *testing.T) {
	called := make(chan struct{}, 4)

	repo := &stubPricesRepo{
		cursor:       time.Date(2026, time.January, 1, 3, 0, 0, 0, time.UTC),
		cursorOK:     true,
		lastFullScan: "2026-01-01", // МСК-дата now (04:00 UTC)
	}
	now := time.Date(2026, time.January, 1, 4, 0, 0, 0, time.UTC)
	cl := &stubPriceClient{
		sinceErr: errMC,
		onSince: func() {
			select {
			case called <- struct{}{}:
			default: // тест перестал читать — не блокируем Run
			}
		},
	}

	buf := captureSlog(t)

	p := NewPricesPoller(PricesConfig{
		PollInterval: 5 * time.Millisecond,
		Now:          func() time.Time { return now },
	}, repo, cl)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	// Дожидаемся двух тиков инкремента: поллер обязан продолжать после ошибок.
	for i := 0; i < 2; i++ {
		select {
		case <-called:
		case <-time.After(2 * time.Second):
			t.Fatal("поллер не выполнил тик инкремента")
		}
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run вернул ошибку, поллер должен продолжать: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run не остановился после отмены контекста")
	}

	if !strings.Contains(buf.String(), errMC.Error()) {
		t.Errorf("ошибка запроса не залогирована: %q", buf.String())
	}

	cur, _, ok := repo.cursorNow()
	if !ok || !cur.Equal(now) {
		t.Errorf("курсор не перезаписан на now: %v (ok=%v)", cur, ok)
	}
}

// TestPriceResolveVat — решение по НДС из полей МС.
func TestPriceResolveVat(t *testing.T) {
	zero, ten := 0, 10
	disabled, enabled := false, true
	minusOne, ten16 := int16(-1), int16(10)

	tests := []struct {
		name      string
		vat       *int
		enabled   *bool
		useParent bool
		want      *int16
	}{
		{"выключен при 0 → без НДС", &zero, &disabled, false, &minusOne},
		{"включён → процент", &ten, &enabled, false, &ten16},
		{"не отдана → nil", nil, &enabled, false, nil},
		{"наследуется от группы → nil", &ten, &enabled, true, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveVat(tt.vat, tt.enabled, tt.useParent)
			switch {
			case tt.want == nil && got != nil:
				t.Fatalf("resolveVat = %d, want nil", *got)
			case tt.want != nil && got == nil:
				t.Fatalf("resolveVat = nil, want %d", *tt.want)
			case tt.want != nil && *got != *tt.want:
				t.Fatalf("resolveVat = %d, want %d", *got, *tt.want)
			}
		})
	}
}

// TestExportProductWithoutPrices — товар без цен/НДС не попадает под жёсткую
// валидацию выгрузки: пишется как раньше, цены остаются nil.
func TestExportProductWithoutPrices(t *testing.T) {
	prod := msProduct("p1", "11110001", "Товар без цен", uomKgHref, fullProductAttrs()...)

	pc := &stubProductClient{
		byPath:   map[string][]client.MSProduct{testGroupA: {prod}},
		uomNames: map[string]string{uomKgHref: "кг"},
	}
	repo := &stubProductsRepo{}
	uc := NewGoodsUseCase(&stubProductFolderClient{}, pc, repo, nil)

	errs, err := uc.ExportProducts(context.Background(), []ExportItem{{ProductID: "p1", GroupPath: testGroupA}})
	if err != nil {
		t.Fatalf("ExportProducts: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("товар без цен/НДС должен выгружаться, ошибки: %v", errs)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("выгружено товаров: %d, want 1", len(repo.saved))
	}

	got := repo.saved[0]
	if got.BuyPrice != nil || got.SalePrice != nil || got.EffectiveVat != nil {
		t.Errorf("пустые цены/НДС МС должны остаться nil: buy=%v sale=%v vat=%v",
			got.BuyPrice, got.SalePrice, got.EffectiveVat)
	}
}

// TestExportProductPricesMapping — общий хелпер маппит цены (копейки округление)
// и НДС из ответа МС в каталог.
func TestExportProductPricesMapping(t *testing.T) {
	prod := msProduct("p1", "11110001", "Товар с ценой", uomKgHref, fullProductAttrs()...)
	prod.BuyPrice = &client.MSBuyPrice{Value: 12345.4}
	prod.SalePrices = []client.MSSalePrice{{Value: 20000.6}}
	vat := 20
	vatEnabled := true
	prod.EffectiveVat = &vat
	prod.EffectiveVatEnabled = &vatEnabled

	pc := &stubProductClient{
		byPath:   map[string][]client.MSProduct{testGroupA: {prod}},
		uomNames: map[string]string{uomKgHref: "кг"},
	}
	repo := &stubProductsRepo{}
	uc := NewGoodsUseCase(&stubProductFolderClient{}, pc, repo, nil)

	if _, err := uc.ExportProducts(context.Background(), []ExportItem{{ProductID: "p1", GroupPath: testGroupA}}); err != nil {
		t.Fatalf("ExportProducts: %v", err)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("выгружено товаров: %d, want 1", len(repo.saved))
	}

	got := repo.saved[0]
	if got.BuyPrice == nil || *got.BuyPrice != 12345 {
		t.Errorf("BuyPrice = %v, want 12345", got.BuyPrice)
	}
	if got.SalePrice == nil || *got.SalePrice != 20001 {
		t.Errorf("SalePrice = %v, want 20001", got.SalePrice)
	}
	if got.EffectiveVat == nil || *got.EffectiveVat != 20 {
		t.Errorf("EffectiveVat = %v, want 20", got.EffectiveVat)
	}
}

// errMC — имитация сбоя запроса к МойСклад в тестах поллера.
var errMC = errors.New("МС упал")
