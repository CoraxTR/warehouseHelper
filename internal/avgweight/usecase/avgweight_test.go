package usecase

import (
	"context"
	"errors"
	"testing"

	"warehouseHelper/internal/avgweight"
)

// --- стабы ---

type stubAvgRepo struct {
	inserted []avgweight.WeightRow
	trimmed  map[string]int
	avgBy    map[string]float64

	insertErr error
	trimErr   error
	avgErr    error
}

func (s *stubAvgRepo) InsertReceivedWeights(_ context.Context, rows []avgweight.WeightRow) error {
	if s.insertErr != nil {
		return s.insertErr
	}
	s.inserted = append(s.inserted, rows...)
	return nil
}

func (s *stubAvgRepo) TrimReceivedWeights(_ context.Context, productID string, keep int) error {
	if s.trimErr != nil {
		return s.trimErr
	}
	if s.trimmed == nil {
		s.trimmed = make(map[string]int)
	}
	s.trimmed[productID] = keep
	return nil
}

func (s *stubAvgRepo) AverageWeightGrams(_ context.Context, productID string) (float64, error) {
	if s.avgErr != nil {
		return 0, s.avgErr
	}
	return s.avgBy[productID], nil
}

type stubProductUpdater struct {
	updated []struct {
		ProductID string
		AvgKg     float64
	}
	err error
}

func (s *stubProductUpdater) UpdateAverageWeight(_ context.Context, productID string, avgKg float64) error {
	if s.err != nil {
		return s.err
	}
	s.updated = append(s.updated, struct {
		ProductID string
		AvgKg     float64
	}{productID, avgKg})
	return nil
}

type stubWikiUpdater struct {
	updated []struct {
		ProductID     string
		AverageWeight string
	}
	err error
}

func (s *stubWikiUpdater) UpdateProductAverageWeight(_ context.Context, productID, averageWeight string) error {
	if s.err != nil {
		return s.err
	}
	s.updated = append(s.updated, struct {
		ProductID     string
		AverageWeight string
	}{productID, averageWeight})
	return nil
}

// --- фикстуры ---

func newTestUseCase() (*UseCase, *stubAvgRepo, *stubProductUpdater, *stubWikiUpdater) {
	repo := &stubAvgRepo{avgBy: map[string]float64{"p1": 250, "p2": 1000}}
	products := &stubProductUpdater{}
	wiki := &stubWikiUpdater{}
	return NewUseCase(repo, products, wiki, 100), repo, products, wiki
}

// --- тесты ---

func TestRecordWeights(t *testing.T) {
	uc, repo, products, wiki := newTestUseCase()

	warnings, err := uc.RecordWeights(context.Background(), []avgweight.WeightRow{
		{ProductID: "p1", WeightG: 250},
		{ProductID: "p1", WeightG: 300},
		{ProductID: "p2", WeightG: 1000},
	})
	if err != nil {
		t.Fatalf("RecordWeights: %v", err)
	}
	if warnings != nil {
		t.Fatalf("предупреждений быть не должно: %v", warnings)
	}

	// Все веса записаны поштучно, одной партией.
	if len(repo.inserted) != 3 {
		t.Fatalf("записано весов: %d, want 3", len(repo.inserted))
	}

	// Trim по уникальным товарам партии с лимитом.
	if len(repo.trimmed) != 2 || repo.trimmed["p1"] != 100 || repo.trimmed["p2"] != 100 {
		t.Fatalf("trim: %+v", repo.trimmed)
	}

	// Каталог: среднее в кг (граммы → /1000).
	if len(products.updated) != 2 {
		t.Fatalf("каталог обновлён: %d, want 2", len(products.updated))
	}
	if products.updated[0].ProductID != "p1" || products.updated[0].AvgKg != 0.25 {
		t.Fatalf("каталог p1: %+v", products.updated[0])
	}
	if products.updated[1].ProductID != "p2" || products.updated[1].AvgKg != 1.0 {
		t.Fatalf("каталог p2: %+v", products.updated[1])
	}

	// Вики: то же значение строкой (кг).
	if len(wiki.updated) != 2 {
		t.Fatalf("вики обновлена: %d, want 2", len(wiki.updated))
	}
	if wiki.updated[0].ProductID != "p1" || wiki.updated[0].AverageWeight != "0.25" {
		t.Fatalf("вики p1: %+v", wiki.updated[0])
	}
	if wiki.updated[1].ProductID != "p2" || wiki.updated[1].AverageWeight != "1" {
		t.Fatalf("вики p2: %+v", wiki.updated[1])
	}
}

func TestRecordWeightsEmpty(t *testing.T) {
	uc, repo, products, wiki := newTestUseCase()

	warnings, err := uc.RecordWeights(context.Background(), nil)
	if err != nil {
		t.Fatalf("RecordWeights: %v", err)
	}
	if warnings != nil || repo.inserted != nil || repo.trimmed != nil {
		t.Fatalf("пустая партия не должна ничего делать: %+v %+v", repo.inserted, repo.trimmed)
	}
	if products.updated != nil || wiki.updated != nil {
		t.Fatalf("пустая партия не должна трогать синки")
	}
}

func TestRecordWeightsInsertFailure(t *testing.T) {
	uc, repo, _, _ := newTestUseCase()
	repo.insertErr = errors.New("база недоступна")

	_, err := uc.RecordWeights(context.Background(), []avgweight.WeightRow{
		{ProductID: "p1", WeightG: 250},
	})
	if err == nil {
		t.Fatal("ожидалась ошибка записи")
	}
	if repo.trimmed != nil {
		t.Fatalf("trim не должен вызываться при падении INSERT: %+v", repo.trimmed)
	}
}

func TestRecordWeightsTrimFailure(t *testing.T) {
	uc, repo, products, _ := newTestUseCase()
	repo.trimErr = errors.New("конфликт блокировок")

	_, err := uc.RecordWeights(context.Background(), []avgweight.WeightRow{
		{ProductID: "p1", WeightG: 250},
	})
	if err == nil {
		t.Fatal("ожидалась ошибка trim")
	}
	if products.updated != nil {
		t.Fatalf("каталог не должен обновляться при падении trim")
	}
}

func TestRecordWeightsAvgFailure(t *testing.T) {
	uc, repo, products, wiki := newTestUseCase()
	repo.avgErr = errors.New("чтение не удалось")

	_, err := uc.RecordWeights(context.Background(), []avgweight.WeightRow{
		{ProductID: "p1", WeightG: 250},
	})
	if err == nil {
		t.Fatal("ожидалась ошибка расчёта среднего")
	}
	if products.updated != nil || wiki.updated != nil {
		t.Fatalf("синки не должны вызываться без среднего")
	}
}

func TestRecordWeightsSyncWarnings(t *testing.T) {
	uc, _, products, wiki := newTestUseCase()
	products.err = errors.New("товар не найден в каталоге")
	wiki.err = errors.New("страница занята")

	warnings, err := uc.RecordWeights(context.Background(), []avgweight.WeightRow{
		{ProductID: "p1", WeightG: 250},
	})
	if err != nil {
		t.Fatalf("сбой синка не должен ронять запись: %v", err)
	}
	if len(warnings) != 2 {
		t.Fatalf("предупреждений: %d, want 2 (%v)", len(warnings), warnings)
	}
	// Оба синка пытались (каталог упал — вики всё равно вызвана).
	if len(products.updated) != 0 || len(wiki.updated) != 0 {
		t.Fatalf("упавшие синки не записывают вызовы")
	}
}

func TestRecordWeightsNilAdapters(t *testing.T) {
	repo := &stubAvgRepo{avgBy: map[string]float64{"p1": 250}}
	uc := NewUseCase(repo, nil, nil, 100)

	warnings, err := uc.RecordWeights(context.Background(), []avgweight.WeightRow{
		{ProductID: "p1", WeightG: 250},
	})
	if err != nil {
		t.Fatalf("RecordWeights: %v", err)
	}
	if warnings != nil {
		t.Fatalf("без адаптеров предупреждений нет: %v", warnings)
	}
}
