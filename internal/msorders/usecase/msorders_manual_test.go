package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"warehouseHelper/internal/msclient/client"
)

// Тесты статуса «Вес подобран» (PUT заказа) и ручного подтверждения подбора
// (POST /ms/orders/{id}/submit-manual): решения владельца 15.09.2026.

// Статус «Вес подобран» уезжает тем же PUT, что и позиции — но ТОЛЬКО если
// заказ в статусе «Получен» (решение владельца 24.09.2026); пустой id в
// конфиге — PUT без смены статуса (прод .env правится руками).
func TestSubmitWeightPickedState(t *testing.T) {
	const weightState = "eb28c8c8-c53c-11e6-7a69-97110013c338"

	cases := []struct {
		name       string
		stateID    string // id статуса из конфига
		orderState string // статус заказа в МС (order.StateID)
		want       string
	}{
		{name: "статус из конфига", stateID: weightState, orderState: stateIDReceived, want: weightState},
		{name: "пустой id — без статуса", stateID: "", orderState: stateIDReceived, want: ""},
		{name: "заказ не «Получен» — статус не меняем", stateID: weightState, orderState: "eb28c8c8-c53c-11e6-7a69-97110013c339", want: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake, o := submitOrder()
			o.StateID = c.orderState
			uc := NewUseCase(fake, submitCatalog(), &fakePicker{}, nil, nil)
			uc.SetWeightPickedState(c.stateID)

			if _, err := uc.Submit(context.Background(), o.ID, SubmitRequest{Rows: []SubmitRow{
				{IDs: []string{"pos-2"}, Records: []ScanRecord{{WeightG: 1250, BB: "01102026"}}},
			}}); err != nil {
				t.Fatalf("Submit: %v", err)
			}

			if len(fake.putStates) != 1 {
				t.Fatalf("PUT-ов %d, want 1", len(fake.putStates))
			}
			if fake.putStates[0] != c.want {
				t.Errorf("state = %q, want %q", fake.putStates[0], c.want)
			}
		})
	}
}

// Ручное подтверждение: quantity = reserve = введённое значение, сроки НЕ
// списываются (PickStock не зовём), складу уходит уведомление о пересчёте,
// статус «Вес подобран» ставится. Ненабранная активная строка — как в Submit.
func TestSubmitManualPiece(t *testing.T) {
	fake, o := submitOrder()
	picker := &fakePicker{}
	notify := &fakeNotifier{}
	uc := NewUseCase(fake, submitCatalog(), picker, nil, notify)
	uc.SetWeightPickedState("state-weight")

	res, err := uc.SubmitManual(context.Background(), o.ID, ManualRequest{Rows: []ManualRow{
		{IDs: []string{"pos-1"}, Qty: 2}, // штучная: убрали одну единицу (3 → 2)
	}})
	if err != nil {
		t.Fatalf("SubmitManual: %v", err)
	}

	rows := decodePutPositions(t, fake.putBody[0])
	q, rs := rowQty(t, rows, "pos-1")
	if q != 2 || rs != 2 {
		t.Errorf("pos-1 qty/reserve = %v/%v, want 2/2", q, rs)
	}
	// Весовая не подтверждена вручную — остаётся заглушкой обычного подбора.
	q2, rs2 := rowQty(t, rows, "pos-2")
	if q2 != 0.0001 || rs2 != 0 {
		t.Errorf("pos-2 qty/reserve = %v/%v, want 0,0001/0 (не покрыта вручную)", q2, rs2)
	}
	if picker.calls != 0 {
		t.Errorf("PickStock вызван %d раз, want 0: ручной вес сроки не списывает", picker.calls)
	}
	if len(notify.texts) != 1 {
		t.Fatalf("уведомлений складу %d, want 1", len(notify.texts))
	}
	if !strings.Contains(notify.texts[0], "Соус терияки") {
		t.Errorf("в уведомлении нет позиции: %q", notify.texts[0])
	}
	if res.StockWarn != "" {
		t.Errorf("StockWarn = %q, want пусто (уведомление ушло)", res.StockWarn)
	}
	if len(fake.putStates) != 1 || fake.putStates[0] != "state-weight" {
		t.Errorf("state = %v, want [state-weight]", fake.putStates)
	}
}

// Ручное подтверждение весовой строки: введённый вес идёт и в quantity, и в
// reserve (полное резервирование), сроки не списываются.
func TestSubmitManualWeighted(t *testing.T) {
	fake, o := submitOrder()
	picker := &fakePicker{}
	uc := NewUseCase(fake, submitCatalog(), picker, nil, &fakeNotifier{})

	if _, err := uc.SubmitManual(context.Background(), o.ID, ManualRequest{Rows: []ManualRow{
		{IDs: []string{"pos-2"}, Qty: 2.1},
	}}); err != nil {
		t.Fatalf("SubmitManual: %v", err)
	}

	rows := decodePutPositions(t, fake.putBody[0])
	q, rs := rowQty(t, rows, "pos-2")
	if q != 2.1 || rs != 2.1 {
		t.Errorf("pos-2 qty/reserve = %v/%v, want 2.1/2.1", q, rs)
	}
	if picker.calls != 0 {
		t.Errorf("PickStock вызван %d раз, want 0", picker.calls)
	}
}

// Смёрженная группа: подтверждаем вручную по живой строке — хвосты группы
// выбывают из positions (их количество входит в живую строку).
func TestSubmitManualMergedGroup(t *testing.T) {
	fake, o := submitOrder()
	// Две весовые строки одной группы склейки (как после «Объединить») + штучная.
	fake.positions = []client.MSPosition{
		position("pos-1", "21110001", "Соус терияки", 3, 50000, 0),
		position("pos-2", "00220002", "Стейк Нью-Йорк", 0.5, 279000, 0),
		position("pos-3", "00220002", "Стейк Нью-Йорк", 0.4, 279000, 0),
	}
	uc := NewUseCase(fake, submitCatalog(), &fakePicker{}, nil, &fakeNotifier{})

	if _, err := uc.SubmitManual(context.Background(), o.ID, ManualRequest{Rows: []ManualRow{
		{IDs: []string{"pos-2", "pos-3"}, Qty: 0.85},
	}}); err != nil {
		t.Fatalf("SubmitManual: %v", err)
	}

	rows := decodePutPositions(t, fake.putBody[0])
	q, rs := rowQty(t, rows, "pos-2")
	if q != 0.85 || rs != 0.85 {
		t.Errorf("pos-2 qty/reserve = %v/%v, want 0.85/0.85", q, rs)
	}
	for _, r := range rows {
		if r["id"] == "pos-3" {
			t.Errorf("смёрженный хвост pos-3 остался в positions: %v", r)
		}
	}
}

// Валидация ручного подтверждения: пустые строки, неположительное значение,
// дробные штуки, неизвестная позиция.
func TestSubmitManualValidation(t *testing.T) {
	cases := []struct {
		name string
		req  ManualRequest
		want error
	}{
		{name: "нет строк", req: ManualRequest{}, want: ErrManualEmptyRows},
		{name: "нулевое значение", req: ManualRequest{Rows: []ManualRow{{IDs: []string{"pos-1"}, Qty: 0}}}, want: ErrManualBadQty},
		{name: "отрицательное значение", req: ManualRequest{Rows: []ManualRow{{IDs: []string{"pos-1"}, Qty: -1}}}, want: ErrManualBadQty},
		{name: "дробные штуки", req: ManualRequest{Rows: []ManualRow{{IDs: []string{"pos-1"}, Qty: 1.5}}}, want: ErrManualBadQty},
		{name: "позиции нет в заказе", req: ManualRequest{Rows: []ManualRow{{IDs: []string{"pos-9"}, Qty: 1}}}, want: ErrSubmitRowMissing},
		{name: "нет id", req: ManualRequest{Rows: []ManualRow{{Qty: 1}}}, want: ErrSubmitBadRow},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake, o := submitOrder()
			picker := &fakePicker{}
			uc := NewUseCase(fake, submitCatalog(), picker, nil, &fakeNotifier{})

			_, err := uc.SubmitManual(context.Background(), o.ID, c.req)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if len(fake.putBody) != 0 {
				t.Errorf("PUT ушёл при ошибке валидации (%d тел)", len(fake.putBody))
			}
			if picker.calls != 0 {
				t.Error("PickStock вызван при ошибке валидации")
			}
		})
	}
}

// Без шва уведомлений заказ всё равно обновляется, но оператор получает
// предупреждение, что складу не сообщили о пересчёте.
func TestSubmitManualWithoutNotifier(t *testing.T) {
	fake, o := submitOrder()
	uc := NewUseCase(fake, submitCatalog(), &fakePicker{}, nil, nil)

	res, err := uc.SubmitManual(context.Background(), o.ID, ManualRequest{Rows: []ManualRow{
		{IDs: []string{"pos-1"}, Qty: 1},
	}})
	if err != nil {
		t.Fatalf("SubmitManual: %v", err)
	}
	if res.StockWarn == "" {
		t.Error("StockWarn пуст, want предупреждение о складе")
	}
	if len(fake.putBody) != 1 {
		t.Errorf("PUT-ов %d, want 1", len(fake.putBody))
	}
}
