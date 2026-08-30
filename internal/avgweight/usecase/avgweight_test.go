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

// testEnv — стабы одного теста (repo + оба синка).
type testEnv struct {
	repo     *stubAvgRepo
	products *stubProductUpdater
	wiki     *stubWikiUpdater
}

func newTestUseCase() (*UseCase, *testEnv) {
	repo := &stubAvgRepo{avgBy: map[string]float64{"p1": 250, "p2": 1000}}
	products := &stubProductUpdater{}
	wiki := &stubWikiUpdater{}
	return NewUseCase(repo, products, wiki, 100), &testEnv{repo: repo, products: products, wiki: wiki}
}

// --- тесты ---

func TestRecordWeights(t *testing.T) {
	uc, env := newTestUseCase()

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
	if len(env.repo.inserted) != 3 {
		t.Fatalf("записано весов: %d, want 3", len(env.repo.inserted))
	}

	// Trim по уникальным товарам партии с лимитом.
	if len(env.repo.trimmed) != 2 || env.repo.trimmed["p1"] != 100 || env.repo.trimmed["p2"] != 100 {
		t.Fatalf("trim: %+v", env.repo.trimmed)
	}

	// Каталог: среднее в кг (граммы → /1000).
	if len(env.products.updated) != 2 {
		t.Fatalf("каталог обновлён: %d, want 2", len(env.products.updated))
	}
	if env.products.updated[0].ProductID != "p1" || env.products.updated[0].AvgKg != 0.25 {
		t.Fatalf("каталог p1: %+v", env.products.updated[0])
	}
	if env.products.updated[1].ProductID != "p2" || env.products.updated[1].AvgKg != 1.0 {
		t.Fatalf("каталог p2: %+v", env.products.updated[1])
	}

	// Вики: то же значение строкой (кг).
	if len(env.wiki.updated) != 2 {
		t.Fatalf("вики обновлена: %d, want 2", len(env.wiki.updated))
	}
	if env.wiki.updated[0].ProductID != "p1" || env.wiki.updated[0].AverageWeight != "0.25" {
		t.Fatalf("вики p1: %+v", env.wiki.updated[0])
	}
	if env.wiki.updated[1].ProductID != "p2" || env.wiki.updated[1].AverageWeight != "1" {
		t.Fatalf("вики p2: %+v", env.wiki.updated[1])
	}
}

func TestRecordWeightsEmpty(t *testing.T) {
	uc, env := newTestUseCase()

	warnings, err := uc.RecordWeights(context.Background(), nil)
	if err != nil {
		t.Fatalf("RecordWeights: %v", err)
	}
	if warnings != nil || env.repo.inserted != nil || env.repo.trimmed != nil {
		t.Fatalf("пустая партия не должна ничего делать: %+v %+v", env.repo.inserted, env.repo.trimmed)
	}
	if env.products.updated != nil || env.wiki.updated != nil {
		t.Fatal("пустая партия не должна трогать синки")
	}
}

func TestRecordWeightsInsertFailure(t *testing.T) {
	uc, env := newTestUseCase()
	env.repo.insertErr = errors.New("база недоступна")

	_, err := uc.RecordWeights(context.Background(), []avgweight.WeightRow{
		{ProductID: "p1", WeightG: 250},
	})
	if err == nil {
		t.Fatal("ожидалась ошибка записи")
	}
	if env.repo.trimmed != nil {
		t.Fatalf("trim не должен вызываться при падении INSERT: %+v", env.repo.trimmed)
	}
}

func TestRecordWeightsTrimFailure(t *testing.T) {
	uc, env := newTestUseCase()
	env.repo.trimErr = errors.New("конфликт блокировок")

	_, err := uc.RecordWeights(context.Background(), []avgweight.WeightRow{
		{ProductID: "p1", WeightG: 250},
	})
	if err == nil {
		t.Fatal("ожидалась ошибка trim")
	}
	if env.products.updated != nil {
		t.Fatal("каталог не должен обновляться при падении trim")
	}
}

func TestRecordWeightsAvgFailure(t *testing.T) {
	uc, env := newTestUseCase()
	env.repo.avgErr = errors.New("чтение не удалось")

	_, err := uc.RecordWeights(context.Background(), []avgweight.WeightRow{
		{ProductID: "p1", WeightG: 250},
	})
	if err == nil {
		t.Fatal("ожидалась ошибка расчёта среднего")
	}
	if env.products.updated != nil || env.wiki.updated != nil {
		t.Fatal("синки не должны вызываться без среднего")
	}
}

func TestRecordWeightsSyncWarnings(t *testing.T) {
	uc, env := newTestUseCase()
	env.products.err = errors.New("товар не найден в каталоге")
	env.wiki.err = errors.New("страница занята")

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
	if len(env.products.updated) != 0 || len(env.wiki.updated) != 0 {
		t.Fatal("упавшие синки не записывают вызовы")
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
