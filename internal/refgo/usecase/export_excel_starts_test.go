// Регрессия #100: экспорт заказов обязан запускать фоновую пометку их
// отгруженными в Реф и создание отгрузок. Без вызова фон не стартовал, и
// симптом на складе выглядел так: таблица-выгрузка создана, а заказы в МС
// статус не поменяли и не отгрузились (при этом ошибок в логе нет вовсе).
package usecase

import (
	"context"
	"testing"
	"time"

	"warehouseHelper/internal/domain"
)

type startFakeExporter struct{}

func (startFakeExporter) ExportOrdersToExcel([]*domain.InternalOrder) (string, error) {
	return "temp/orders.xlsx", nil
}

type startFakeOrders struct{ orders []*domain.InternalOrder }

func (f startFakeOrders) GetAllOrders(context.Context) ([]*domain.InternalOrder, error) {
	return f.orders, nil
}

type startFakeCleaner struct{}

func (startFakeCleaner) CleanOlderThan(time.Duration) error { return nil }

func TestExportOrdersStartsShipmentProcessing(t *testing.T) {
	shipper := &fakeShipper{started: make(chan struct{}), release: make(chan struct{})}
	close(shipper.release) // пометку не держим: проверяем сам факт запуска

	uc := NewExportToExcelUseCase(
		startFakeExporter{},
		startFakeOrders{orders: []*domain.InternalOrder{stopOrder()}},
		shipper,
		nil, // отгрузку в тесте не обеспечиваем: способ оплаты не отгружаемый
		startFakeCleaner{},
		time.Hour,
	)

	if _, err := uc.ExportOrders(context.Background()); err != nil {
		t.Fatalf("ExportOrders: %v", err)
	}

	uc.Stop() // ждём фон: после Stop пометки гарантированно закончены

	if got := shipper.callsCount(); got != 1 {
		t.Errorf("пометок в МС = %d, want 1: экспорт обязан запускать фоновую пометку отгрузок", got)
	}
}
