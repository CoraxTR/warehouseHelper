package usecase

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"warehouseHelper/internal/config"
	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/refgo/registry"

	"github.com/xuri/excelize/v2"
)

// stubRefGoRepo — заглушка репозитория заказов для сверки.
type stubRefGoRepo struct {
	orders map[string]domain.InternalRefGoCheckAgainstOrder
	err    error
}

func (s *stubRefGoRepo) GetRefGoCheckOrdersByDateRange(_ context.Context, _, _ string) (map[string]domain.InternalRefGoCheckAgainstOrder, error) {
	if s.err != nil {
		return nil, s.err
	}

	return s.orders, nil
}

func buildCheckRegistry(t *testing.T, rows [][]any) []byte {
	t.Helper()

	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	sheet := f.GetSheetName(0)
	for i, row := range rows {
		for j, v := range row {
			cell, err := excelize.CoordinatesToCellName(j+1, i+1)
			if err != nil {
				t.Fatalf("cell name: %v", err)
			}

			if err := f.SetCellValue(sheet, cell, v); err != nil {
				t.Fatalf("set cell %s: %v", cell, err)
			}
		}
	}

	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatalf("write buffer: %v", err)
	}

	return buf.Bytes()
}

func TestRefGoCheckAgainstUseCaseCheck(t *testing.T) {
	parser := registry.NewxlsxImporter()

	dbOrders := map[string]domain.InternalRefGoCheckAgainstOrder{
		"1001": {RefGoNumber: "1001", PaymentMethod: "Наличные", Sum: 1000.0, Weight: 10, RefGoZone: "Зеленая"},
		"1002": {RefGoNumber: "1002", PaymentMethod: "Терминал", Sum: 500.0, Weight: 30.5, RefGoZone: "Зеленая"},
		"1003": {RefGoNumber: "1003", PaymentMethod: "Наличные", Sum: 1000.0, Weight: 10, RefGoZone: "Зеленая"},
		// 1004 есть в базе, но отсутствует в реестре — попадёт в «не обнаружен в сверке».
		"1004": {RefGoNumber: "1004", PaymentMethod: "Наличные", Sum: 100.0, Weight: 5, RefGoZone: "Зеленая"},
	}

	uc := NewRefGoCheckAgainstUseCase(&stubRefGoRepo{orders: dbOrders}, parser, &config.RefGoConfig{
		CheckAgainstModule: true,
		RGGreenzonePrice:   500,
		RGWeightlimit:      20,
		RGCashtax:          5,
		RGCardtax:          5,
	})

	file := buildCheckRegistry(t, [][]any{
		{"Реестр"},
		{"Номер"},
		{"", "", "", "", "Забор 1"},
		{"", "", "", "", "1001", "", "", "", "", "", "", "", "1000,00", "", "500,00", "", "", "", "", "", "", "50,00"},
		{"", "", "", "", "1002", "", "", "", "", "", "", "", "500,00", "", "1 000,00", "", "", "", "", "", "", "25,00"},
		{"", "", "", "", "1003", "", "", "", "", "", "", "", "900,00", "", "300,00", "", "", "", "", "200,00", "100,00", "999,00"},
		{"", "", "", "", "9999", "", "", "", "", "", "", "", "1,00", "", "1,00"},
	})

	result, err := uc.Check(context.Background(), "03.08.2026", "05.08.2026", file)
	if err != nil {
		t.Fatalf("Check error: %v", err)
	}

	if result.RetrievesCount != 1 {
		t.Errorf("RetrievesCount = %d, want 1", result.RetrievesCount)
	}

	// 1001 и 1002 — корректные заказы, 1003 — 5 ошибок, 9999 — не найден,
	// 1004 — остался в мапе БД (нет в реестре).
	wantErrors := []string{
		"Ошибка на строке 6: Сумма оплаты не совпадает с базой",
		"Ошибка на строке 6: Некорректная обработка д/с",
		"Ошибка на строке 6: Проверь сокращение интервала",
		"Ошибка на строке 6: Проверь доп. услуги",
		"Ошибка на строке 6: Неверная стоимость доставки",
		"Ошибка на строке 7: Этот заказ не найден в базе",
		"Заказ 1004 не обнаружен в сверке",
	}

	if !reflect.DeepEqual(result.Errors, wantErrors) {
		t.Errorf("errors mismatch:\n got: %q\nwant: %q", result.Errors, wantErrors)
	}
}

func TestRefGoCheckAgainstUseCaseUnknownZone(t *testing.T) {
	parser := registry.NewxlsxImporter()

	dbOrders := map[string]domain.InternalRefGoCheckAgainstOrder{
		"1001": {RefGoNumber: "1001", PaymentMethod: "Наличные", Sum: 1000.0, Weight: 10, RefGoZone: "Марс"},
	}

	uc := NewRefGoCheckAgainstUseCase(&stubRefGoRepo{orders: dbOrders}, parser, &config.RefGoConfig{
		CheckAgainstModule: true,
		RGGreenzonePrice:   500,
		RGWeightlimit:      20,
		RGCashtax:          5,
		RGCardtax:          5,
	})

	file := buildCheckRegistry(t, [][]any{
		{"Реестр"},
		{"Номер"},
		{"", "", "", "", "1001", "", "", "", "", "", "", "", "1000,00", "", "500,00", "", "", "", "", "", "", "50,00"},
	})

	result, err := uc.Check(context.Background(), "03.08.2026", "05.08.2026", file)
	if err != nil {
		t.Fatalf("Check error: %v", err)
	}

	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "Неизвестная зона доставки") {
		t.Errorf("want unknown zone error, got: %v", result.Errors)
	}
}

func TestRefGoCheckAgainstUseCaseRepoError(t *testing.T) {
	parser := registry.NewxlsxImporter()

	uc := NewRefGoCheckAgainstUseCase(&stubRefGoRepo{err: context.DeadlineExceeded}, parser, &config.RefGoConfig{
		CheckAgainstModule: true,
	})

	file := buildCheckRegistry(t, [][]any{
		{"Реестр"},
		{"Номер"},
		{"", "", "", "", "1001"},
	})

	_, err := uc.Check(context.Background(), "03.08.2026", "05.08.2026", file)
	if err == nil {
		t.Fatal("expected error when repository fails")
	}
}

func TestZonePrice(t *testing.T) {
	uc := NewRefGoCheckAgainstUseCase(nil, nil, &config.RefGoConfig{
		RGGreenzonePrice:  500,
		RGYellowzonePrice: 600,
		RGOrangezonePrice: 700,
		RGRedzonePrice:    800,
		RGBluezonePrice:   900,
	})

	tests := []struct {
		zone string
		want float64
		ok   bool
	}{
		{"Зеленая", 500, true},
		{"Зелёная", 500, true},
		{"Желтая", 600, true},
		{"Оранжевая", 700, true},
		{"Красная", 800, true},
		{"Голубая", 900, true},
		{"Марс", 0, false},
		{"", 0, false},
	}

	for _, tt := range tests {
		got, ok := uc.zonePrice(tt.zone)
		if got != tt.want || ok != tt.ok {
			t.Errorf("zonePrice(%q) = (%v, %v), want (%v, %v)", tt.zone, got, ok, tt.want, tt.ok)
		}
	}
}
