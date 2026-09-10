// Тесты обёртки печати бланков РефГо: сам экспорт/слияние живёт в нижнем слое
// pdfexport (там же его тесты), здесь — швы обёртки: остановка фоновой
// предзагрузки, проброс пути и списка неполученных бланков, ошибки.
package usecase

import (
	"context"
	"errors"
	"testing"
)

// fakeForms — заглушка PDFForms.
type fakeForms struct {
	path      string
	orderPath string
	skipped   []string
	err       error
	calls     int
}

func (f *fakeForms) GetOrderPDF(_ context.Context, _ string) (string, error) {
	f.calls++

	return f.orderPath, f.err
}

func (f *fakeForms) GetMultipleOrdersPDF(_ context.Context, _ []string) (string, []string, error) {
	f.calls++

	return f.path, f.skipped, f.err
}

// fakePreloader считает остановки предзагрузки.
type fakePreloader struct {
	stops int
}

func (p *fakePreloader) StopPreloading() { p.stops++ }

func TestGetMultipleOrdersPDF_StopsPreloaderAndPassesSkipped(t *testing.T) {
	forms := &fakeForms{path: "merged.pdf", skipped: []string{"a", "c"}}
	pre := &fakePreloader{}
	uc := NewExportOrderPDFUseCase(forms, pre)

	path, skipped, err := uc.GetMultipleOrdersPDF(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("GetMultipleOrdersPDF() error = %v", err)
	}

	if path != "merged.pdf" {
		t.Errorf("path = %q, want merged.pdf", path)
	}
	if len(skipped) != 2 || skipped[0] != "a" || skipped[1] != "c" {
		t.Errorf("skipped = %v, want [a c]", skipped)
	}
	if pre.stops != 1 {
		t.Errorf("остановок предзагрузки = %d, want 1", pre.stops)
	}
}

func TestGetOrderPDF_StopsPreloader(t *testing.T) {
	forms := &fakeForms{orderPath: "exported.pdf"}
	pre := &fakePreloader{}
	uc := NewExportOrderPDFUseCase(forms, pre)

	path, err := uc.GetOrderPDF(context.Background(), "abc")
	if err != nil {
		t.Fatalf("GetOrderPDF() error = %v", err)
	}
	if path != "exported.pdf" {
		t.Errorf("path = %q, want exported.pdf", path)
	}
	if pre.stops != 1 {
		t.Errorf("остановок предзагрузки = %d, want 1", pre.stops)
	}
}

func TestGetMultipleOrdersPDF_PropagatesError(t *testing.T) {
	wantErr := errors.New("МойСклад недоступен")
	forms := &fakeForms{err: wantErr}
	uc := NewExportOrderPDFUseCase(forms, &fakePreloader{})

	if _, _, err := uc.GetMultipleOrdersPDF(context.Background(), []string{"a"}); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestGetOrderPDF_PropagatesError(t *testing.T) {
	wantErr := errors.New("МойСклад недоступен")
	forms := &fakeForms{err: wantErr}
	uc := NewExportOrderPDFUseCase(forms, &fakePreloader{})

	if _, err := uc.GetOrderPDF(context.Background(), "abc"); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}
