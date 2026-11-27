package usecase

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"sync"
	"time"

	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/receiving"
)

// Приёмка живёт в памяти процесса: после «Завершить» она остаётся на странице
// приёмок до 21:00 МСК того дня, когда её завершили, — вход в неё даёт отчёт и
// наклейки, но не редактирование. Перезапуск приложения стирает их: это
// сознательное решение владельца (27.11.2026) — отдельная таблица не нужна,
// наклейку всегда можно перепечатать сканом кода («Перепечатать наклейку»).
//
// Черновик (до «Завершить») остатков НЕ трогает: остатки, веса и уведомления
// пишет только FinishDraft.
var (
	// ErrDraftNotFound — приёмка не найдена: истёк срок хранения или приложение
	// перезапускали.
	ErrDraftNotFound = errors.New("приёмка не найдена: срок хранения прошёл или приложение перезапускали")
	// ErrDraftFinished — приёмка закрыта для правки (уже завершена).
	ErrDraftFinished = errors.New("приёмка уже завершена: править нельзя, доступны отчёт и наклейки")
	// ErrDraftHost — правка/завершение не с той машины, что вела приёмку.
	ErrDraftHost = errors.New("править и завершать приёмку может только машина, которая её вела")
)

// receiveCloseHour — час МСК, до которого завершённая приёмка остаётся на
// странице приёмок (21:00 — решение владельца 27.11.2026).
const receiveCloseHour = 21

// unfinishedTTL — сколько живёт незавершённый черновик. Тянется от последней
// правки, поэтому работа, начатая вечером, не пропадает в 21:00 на полпути.
const unfinishedTTL = 12 * time.Hour

// msk — часовой пояс склада: срок «до 21:00» считается по Москве, а сервер
// живёт в UTC.
var msk = time.FixedZone("MSK", 3*60*60)

// closeOfDay — 21:00 МСК дня, в который попадает t.
func closeOfDay(t time.Time) time.Time {
	local := t.In(msk)
	return time.Date(local.Year(), local.Month(), local.Day(), receiveCloseHour, 0, 0, 0, msk)
}

// Draft — приёмка в памяти: сканы «как отсканировали» (из них страница
// восстанавливает строки для правки) и посчитанный отчёт (куски и коробки для
// отчёта и наклеек).
type Draft struct {
	ID           string
	SupplierID   string
	SupplierName string
	// HostToken — ключ машины-хозяина: правит и завершает только она.
	HostToken string
	Scans     []receiving.ScanEntry
	Result    *receiving.SaveResult
	CreatedAt time.Time
	UpdatedAt time.Time
	// FinishedAt — момент «Завершить»; nil — черновик, ещё правится.
	FinishedAt *time.Time
	// ExpiresAt — когда приёмка пропадает со страницы приёмок.
	ExpiresAt time.Time
	// finishing — «Завершить» уже в работе: остатки пишутся прямо сейчас,
	// второй клик их не задваивает.
	finishing bool
}

// Finished — приёмка закрыта для правки.
func (d Draft) Finished() bool { return d.FinishedAt != nil }

// EndsAt — «до 21:00» для страницы: пусто у незавершённого черновика.
func (d Draft) EndsAt() string {
	if d.FinishedAt == nil {
		return ""
	}
	return d.ExpiresAt.In(msk).Format("15:04")
}

// FinishedAtText — время завершения для списка (МСК); пусто у черновика.
func (d Draft) FinishedAtText() string {
	if d.FinishedAt == nil {
		return ""
	}
	return d.FinishedAt.In(msk).Format("15:04")
}

// draftStore — приёмки в памяти. Все методы потокобезопасны, наружу уходят
// только копии: сканы и отчёт не должны меняться из-под читателя.
type draftStore struct {
	mu    sync.Mutex
	now   func() time.Time
	items map[string]*Draft
}

func newDraftStore(now func() time.Time) *draftStore {
	if now == nil {
		now = time.Now
	}
	return &draftStore{now: now, items: make(map[string]*Draft)}
}

// Put сохраняет приёмку как черновик. id пустой — новая приёмка: заводим id и
// ключ хозяина. id есть — правим существующую, и только со своим ключом.
func (s *draftStore) Put(id, token, supplierID, supplierName string, scans []receiving.ScanEntry, result *receiving.SaveResult) (Draft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()

	now := s.now()

	if id == "" {
		newID, err := s.newIDLocked()
		if err != nil {
			return Draft{}, err
		}
		newHostToken, err := newToken()
		if err != nil {
			return Draft{}, err
		}
		d := &Draft{
			ID:           newID,
			SupplierID:   supplierID,
			SupplierName: supplierName,
			HostToken:    newHostToken,
			Scans:        scans,
			Result:       result,
			CreatedAt:    now,
			UpdatedAt:    now,
			ExpiresAt:    now.Add(unfinishedTTL),
		}
		s.items[newID] = d
		return d.clone(), nil
	}

	d, ok := s.items[id]
	if !ok {
		return Draft{}, ErrDraftNotFound
	}
	if d.Finished() {
		return Draft{}, ErrDraftFinished
	}
	if token == "" || token != d.HostToken {
		return Draft{}, ErrDraftHost
	}

	d.Scans = scans
	d.Result = result
	d.UpdatedAt = now
	d.ExpiresAt = now.Add(unfinishedTTL)
	return d.clone(), nil
}

// Claim отдаёт черновик под завершение и сразу помечает его занятым: двойной
// клик по «Завершить» не должен второй раз писать остатки.
func (s *draftStore) Claim(id, token string) (Draft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()

	d, ok := s.items[id]
	if !ok {
		return Draft{}, ErrDraftNotFound
	}
	if d.Finished() || d.finishing {
		return Draft{}, ErrDraftFinished
	}
	if token == "" || token != d.HostToken {
		return Draft{}, ErrDraftHost
	}
	d.finishing = true
	return d.clone(), nil
}

// Finish закрывает приёмку: отчёт фиксируется, срок — 21:00 МСК этого дня.
// Завершили после 21:00 — держим до 21:00 следующего дня, иначе приёмка
// исчезла бы сразу после завершения.
func (s *draftStore) Finish(id string, result *receiving.SaveResult) (Draft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	d, ok := s.items[id]
	if !ok {
		return Draft{}, ErrDraftNotFound
	}

	now := s.now()
	deadline := closeOfDay(now)
	if !deadline.After(now) {
		deadline = closeOfDay(now.Add(24 * time.Hour))
	}

	d.finishing = false
	d.FinishedAt = &now
	d.UpdatedAt = now
	d.ExpiresAt = deadline
	if result != nil {
		d.Result = result
	}
	return d.clone(), nil
}

// Abort снимает занятость черновика: завершение не удалось (нет остатков,
// ошибка записи) — приёмка снова правится.
func (s *draftStore) Abort(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.items[id]; ok {
		d.finishing = false
	}
}

// Get отдаёт приёмку по id (копию).
func (s *draftStore) Get(id string) (Draft, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	d, ok := s.items[id]
	if !ok {
		return Draft{}, false
	}
	return d.clone(), true
}

// List отдаёт живые приёмки: сначала незавершённые (правятся), затем
// завершённые — свежие сверху.
func (s *draftStore) List() []Draft {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()

	out := make([]Draft, 0, len(s.items))
	for _, d := range s.items {
		out = append(out, d.clone())
	}
	slices.SortFunc(out, func(a, b Draft) int {
		if a.Finished() != b.Finished() {
			if a.Finished() {
				return 1
			}
			return -1
		}
		if a.Finished() {
			return b.FinishedAt.Compare(*a.FinishedAt)
		}
		return b.UpdatedAt.Compare(a.UpdatedAt)
	})
	return out
}

// pruneLocked выкидывает приёмки с истёкшим сроком; вызывается под мьютексом.
func (s *draftStore) pruneLocked() {
	now := s.now()
	for id, d := range s.items {
		if !d.ExpiresAt.After(now) {
			delete(s.items, id)
		}
	}
}

// newIDLocked подбирает свободный id приёмки (8 знаков).
func (s *draftStore) newIDLocked() (string, error) {
	for range 100 {
		buf := make([]byte, 4)
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		id := hex.EncodeToString(buf)
		if _, busy := s.items[id]; !busy {
			return id, nil
		}
	}
	return "", errors.New("не удалось подобрать свободный номер приёмки")
}

// newToken — ключ машины-хозяина (12 байт).
func newToken() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// clone отдаёт копию приёмки: сканы и отчёт наружу не меняются.
func (d Draft) clone() Draft {
	out := d
	out.Scans = append([]receiving.ScanEntry(nil), d.Scans...)
	if d.Result != nil {
		res := *d.Result
		out.Result = &res
	}
	if d.FinishedAt != nil {
		fin := *d.FinishedAt
		out.FinishedAt = &fin
	}
	return out
}

// SaveDraft сохраняет приёмку как черновик: отчёт считается, остатки и веса НЕ
// пишутся. draftID пустой — новая приёмка (вернётся её id и ключ хозяина),
// иначе правится уже открытая — только со своим ключом.
func (uc *ReceivingUseCase) SaveDraft(ctx context.Context, req receiving.SaveRequest, draftID, hostToken, supplierName string) (*receiving.SaveResult, Draft, error) {
	result, err := uc.Preview(ctx, req.SupplierID, req.Scans)
	if err != nil {
		return nil, Draft{}, err
	}

	draft, err := uc.drafts.Put(draftID, hostToken, req.SupplierID, supplierName, req.Scans, result)
	if err != nil {
		return nil, Draft{}, err
	}

	return result, draft, nil
}

// FinishDraft закрывает приёмку: пишет остатки (с уведомлениями о наличии и
// скидках), веса и отчёт, после чего приёмка живёт до 21:00 МСК только на
// просмотр и печать наклеек.
func (uc *ReceivingUseCase) FinishDraft(ctx context.Context, id, hostToken string) (Draft, error) {
	done := metrics.Track(trackPkg, "FinishDraft")
	defer done()

	draft, err := uc.drafts.Claim(id, hostToken)
	if err != nil {
		return Draft{}, err
	}

	result, err := uc.Save(ctx, receiving.SaveRequest{SupplierID: draft.SupplierID, Scans: draft.Scans})
	if err != nil {
		// Завершение не прошло — приёмка снова правится, чтобы можно было
		// исправить и повторить.
		uc.drafts.Abort(id)

		return Draft{}, err
	}

	return uc.drafts.Finish(id, result)
}

// Drafts — живые приёмки для страницы приёмок: сначала черновики, затем
// завершённые до 21:00 (свежие сверху).
func (uc *ReceivingUseCase) Drafts() []Draft { return uc.drafts.List() }

// DraftByID — приёмка по id (для страницы финала и продолжения правки).
func (uc *ReceivingUseCase) DraftByID(id string) (Draft, bool) { return uc.drafts.Get(id) }
