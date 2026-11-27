package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"warehouseHelper/internal/receiving"
)

// newDraftTest — приёмка с подменными остатками и весами: черновик не должен
// трогать ни остатки, ни средний вес — это делает только «Завершить».
func newDraftTest() (*ReceivingUseCase, *stubStockAccepter, *stubWeightRecorder) {
	repo := testCacheRepo()
	stock := &stubStockAccepter{}
	weights := &stubWeightRecorder{}

	return NewReceivingUseCase(repo, stock, weights), stock, weights
}

// saveOneDraft сохраняет черновик из одного куска и отдаёт его.
func saveOneDraft(t *testing.T, uc *ReceivingUseCase) Draft {
	t.Helper()

	_, draft, err := uc.SaveDraft(context.Background(), receiving.SaveRequest{
		SupplierID: "sup-1",
		Scans:      []receiving.ScanEntry{{Raw: itemBarcode}},
	}, "", "", "Говядина ООО")
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	return draft
}

// Черновик считает отчёт, но остатки, веса и уведомления не пишет: до
// «Завершить» склад остаётся прежним (решение владельца 27.11.2026).
func TestSaveDraft_WritesNothing(t *testing.T) {
	uc, stock, weights := newDraftTest()

	draft := saveOneDraft(t, uc)

	if draft.ID == "" || draft.HostToken == "" {
		t.Fatalf("черновик без id/ключа: %+v", draft)
	}
	if draft.Finished() {
		t.Error("только что сохранённый черновик не должен быть завершён")
	}
	if draft.Result == nil || len(draft.Result.Rows) != 1 {
		t.Fatalf("отчёт черновика: %+v", draft.Result)
	}
	if len(stock.lots) != 0 {
		t.Fatalf("черновик записал остатки: %+v", stock.lots)
	}
	if len(weights.recorded) != 0 {
		t.Fatalf("черновик записал веса: %+v", weights.recorded)
	}
	if !draft.ExpiresAt.After(time.Now()) {
		t.Errorf("черновик должен жить дальше: expires=%v", draft.ExpiresAt)
	}
}

// «Завершить» пишет остатки и веса ровно один раз и закрывает приёмку для правки.
func TestFinishDraft_WritesStockOnceAndCloses(t *testing.T) {
	uc, stock, weights := newDraftTest()
	draft := saveOneDraft(t, uc)

	done, err := uc.FinishDraft(context.Background(), draft.ID, draft.HostToken)
	if err != nil {
		t.Fatalf("FinishDraft: %v", err)
	}
	if !done.Finished() {
		t.Error("приёмка не закрыта после «Завершить»")
	}
	if len(stock.lots) != 1 {
		t.Fatalf("остатки после завершения: %d лотов, want 1", len(stock.lots))
	}
	if len(weights.recorded) != 1 {
		t.Fatalf("веса после завершения: %d записей, want 1", len(weights.recorded))
	}
	if done.Result == nil || len(done.Result.Rows) != 1 {
		t.Fatalf("отчёт завершённой приёмки: %+v", done.Result)
	}

	// Второй клик по «Завершить» остатки не задваивает.
	if _, err := uc.FinishDraft(context.Background(), draft.ID, draft.HostToken); !errors.Is(err, ErrDraftFinished) {
		t.Fatalf("повторное завершение: %v, want ErrDraftFinished", err)
	}
	if len(stock.lots) != 1 {
		t.Fatalf("остатки задвоились: %d лотов", len(stock.lots))
	}

	// И правка закрытой приёмки уже не проходит.
	if _, _, err := uc.SaveDraft(context.Background(), receiving.SaveRequest{
		SupplierID: "sup-1",
		Scans:      []receiving.ScanEntry{{Raw: itemBarcode}},
	}, draft.ID, draft.HostToken, "Говядина ООО"); !errors.Is(err, ErrDraftFinished) {
		t.Fatalf("правка завершённой приёмки: %v, want ErrDraftFinished", err)
	}
}

// Правит и завершает только машина-хозяин: у чужой машины ключа нет.
func TestDraft_EditingNeedsHostKey(t *testing.T) {
	uc, _, _ := newDraftTest()
	draft := saveOneDraft(t, uc)

	if _, _, err := uc.SaveDraft(context.Background(), receiving.SaveRequest{
		SupplierID: "sup-1",
		Scans:      []receiving.ScanEntry{{Raw: itemBarcode}},
	}, draft.ID, "чужой-ключ", "Говядина ООО"); !errors.Is(err, ErrDraftHost) {
		t.Fatalf("правка с чужим ключом: %v, want ErrDraftHost", err)
	}
	if _, err := uc.FinishDraft(context.Background(), draft.ID, ""); !errors.Is(err, ErrDraftHost) {
		t.Fatalf("завершение без ключа: %v, want ErrDraftHost", err)
	}
	if _, _, err := uc.SaveDraft(context.Background(), receiving.SaveRequest{
		SupplierID: "sup-1",
		Scans:      []receiving.ScanEntry{{Raw: itemBarcode}},
	}, "нет-такой", "", "Говядина ООО"); !errors.Is(err, ErrDraftNotFound) {
		t.Fatalf("правка неизвестной приёмки: %v, want ErrDraftNotFound", err)
	}
}

// Правка сохраняет то, что оператор ввёл руками: восстановление строк идёт из
// сканов приёмки, а не из повторного разбора кода.
func TestSaveDraft_KeepsManualInput(t *testing.T) {
	uc, _, _ := newDraftTest()
	bb := d(time.November, 28)
	pd := d(time.November, 20)

	_, draft, err := uc.SaveDraft(context.Background(), receiving.SaveRequest{
		SupplierID: "sup-1",
		Scans: []receiving.ScanEntry{{
			Raw:              itemBarcode,
			ManualWeightG:    ptrInt64(777),
			ManualProducedOn: &pd,
			ManualBestBefore: &bb,
		}},
	}, "", "", "Говядина ООО")
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	got := draft.Scans[0]
	if got.ManualWeightG == nil || *got.ManualWeightG != 777 {
		t.Fatalf("ручной вес потерян: %+v", got)
	}
	if got.ManualBestBefore == nil || !got.ManualBestBefore.Equal(bb) {
		t.Fatalf("ручной срок потерян: %+v", got.ManualBestBefore)
	}
	if got.ManualProducedOn == nil || !got.ManualProducedOn.Equal(pd) {
		t.Fatalf("ручная выработка потеряна: %+v", got.ManualProducedOn)
	}
}

// Завершённая приёмка живёт до 21:00 МСК дня завершения, потом пропадает.
func TestDraft_ExpiresAt21MSK(t *testing.T) {
	uc, _, _ := newDraftTest()
	// 27.11.2026 12:00 UTC = 15:00 МСК.
	uc.drafts.now = func() time.Time { return time.Date(2026, time.November, 27, 12, 0, 0, 0, time.UTC) }

	draft := saveOneDraft(t, uc)

	done, err := uc.FinishDraft(context.Background(), draft.ID, draft.HostToken)
	if err != nil {
		t.Fatalf("FinishDraft: %v", err)
	}

	want := time.Date(2026, time.November, 27, 21, 0, 0, 0, msk)
	if !done.ExpiresAt.Equal(want) {
		t.Fatalf("срок приёмки = %v, want %v", done.ExpiresAt, want)
	}
	if done.EndsAt() != "21:00" {
		t.Errorf("EndsAt = %q, want 21:00", done.EndsAt())
	}

	// 21:01 МСК — приёмки в списке уже нет.
	uc.drafts.now = func() time.Time { return want.Add(time.Minute) }
	if got := uc.Drafts(); len(got) != 0 {
		t.Fatalf("после 21:00 в списке %d приёмок, want 0", len(got))
	}
	if _, ok := uc.DraftByID(draft.ID); ok {
		t.Error("после 21:00 приёмка должна пропасть")
	}
}

// Завершили после 21:00 — держим до 21:00 следующего дня: иначе приёмка
// исчезла бы сразу, и наклейки было бы не напечатать.
func TestDraft_FinishedAfterCloseHourLivesTillNextDay(t *testing.T) {
	uc, _, _ := newDraftTest()
	// 27.11.2026 19:30 UTC = 22:30 МСК.
	uc.drafts.now = func() time.Time { return time.Date(2026, time.November, 27, 19, 30, 0, 0, time.UTC) }

	draft := saveOneDraft(t, uc)

	done, err := uc.FinishDraft(context.Background(), draft.ID, draft.HostToken)
	if err != nil {
		t.Fatalf("FinishDraft: %v", err)
	}

	want := time.Date(2026, time.November, 28, 21, 0, 0, 0, msk)
	if !done.ExpiresAt.Equal(want) {
		t.Fatalf("срок приёмки = %v, want %v", done.ExpiresAt, want)
	}
	if len(uc.Drafts()) != 1 {
		t.Fatal("приёмка должна остаться в списке до 21:00 следующего дня")
	}
}

// Повторное сохранение той же приёмки заменяет набор сканов, а не копит его:
// «вернуться к правке» и убрать лишнюю позицию — рабочая ветка.
func TestSaveDraft_ReplacesScans(t *testing.T) {
	uc, _, _ := newDraftTest()

	_, draft, err := uc.SaveDraft(context.Background(), receiving.SaveRequest{
		SupplierID: "sup-1",
		Scans: []receiving.ScanEntry{
			{Raw: itemBarcode},
			{Raw: itemBarcode},
		},
	}, "", "", "Говядина ООО")
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if len(draft.Scans) != 2 || len(draft.Result.Units) != 2 {
		t.Fatalf("первое сохранение: сканов %d, кусков %d", len(draft.Scans), len(draft.Result.Units))
	}

	result, draft, err := uc.SaveDraft(context.Background(), receiving.SaveRequest{
		SupplierID: "sup-1",
		Scans:      []receiving.ScanEntry{{Raw: itemBarcode}},
	}, draft.ID, draft.HostToken, "Говядина ООО")
	if err != nil {
		t.Fatalf("правка: %v", err)
	}
	if len(result.Rows) != 1 || len(draft.Scans) != 1 {
		t.Fatalf("после правки: строк %d, сканов %d, want 1/1", len(result.Rows), len(draft.Scans))
	}
}

// ptrInt64 — указатель на число: ручные поля скана — указатели.
func ptrInt64(v int64) *int64 { return new(v) }
