// Тесты жизненного цикла фоновой пометки отгрузок РефГо (ExportOrders):
// остановка приложения должна дождаться начатых пометок в МС, а не оборвать их.
package usecase

import (
	"context"
	"sync"
	"testing"
	"time"

	"warehouseHelper/internal/domain"
)

// fakeShipper — заглушка OrdersShipper: сообщает о входе в пометку и висит,
// пока тест не отпустит. Так проверяется именно ожидание в Stop.
type fakeShipper struct {
	started chan struct{}
	release chan struct{}

	once sync.Once

	mu    sync.Mutex
	calls int
}

func (f *fakeShipper) SetOrderAsShippedToRefGo(_ context.Context, _ string) error {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()

	f.once.Do(func() { close(f.started) })

	<-f.release

	return nil
}

func (f *fakeShipper) callsCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls
}

func stopOrder() *domain.InternalOrder {
	// Способ оплаты по умолчанию не отгружаемый (IsShippablePayment == false),
	// поэтому shipmentEnsurer в тесте не нужен: проверяется пометка в МС.
	order := &domain.InternalOrder{}
	order.SetID("order-1")

	return order
}

// TestStopWaitsForShipmentProcessing — главное свойство: Stop не возвращается,
// пока фоновая пометка в МС не закончилась. Иначе процесс ушёл бы посреди
// пометок, и часть заказов осталась бы помеченной в базе, но не в МС.
func TestStopWaitsForShipmentProcessing(t *testing.T) {
	shipper := &fakeShipper{started: make(chan struct{}), release: make(chan struct{})}
	uc := &ExportToExcelUseCase{shipper: shipper}

	uc.startShipmentsProcessing([]*domain.InternalOrder{stopOrder()})

	<-shipper.started // фон дошёл до запроса в МС

	stopped := make(chan struct{})
	go func() {
		uc.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
		t.Fatal("Stop вернулся, не дождавшись пометки в МС")
	case <-time.After(50 * time.Millisecond):
	}

	close(shipper.release)

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop не дождался завершения фоновой пометки")
	}
}

// TestStartShipmentsProcessingAfterStopIsNoop — после остановки новый фон не
// стартует: экспорт, нажатый в момент остановки, пометок уже не делает
// (в МС ничего не уйдёт, а закрытый воркерпул не будет дёргаться).
func TestStartShipmentsProcessingAfterStopIsNoop(t *testing.T) {
	shipper := &fakeShipper{started: make(chan struct{}), release: make(chan struct{})}
	uc := &ExportToExcelUseCase{shipper: shipper}

	uc.Stop()
	uc.startShipmentsProcessing([]*domain.InternalOrder{stopOrder()})

	// Даём возможной горутине шанс дойти до пометки.
	time.Sleep(50 * time.Millisecond)

	if got := shipper.callsCount(); got != 0 {
		t.Errorf("пометок в МС = %d, ожидали 0: после Stop фон не должен стартовать", got)
	}

	// Повторный Stop безопасен (идемпотентность для двойного Shutdown).
	uc.Stop()
}
