package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"warehouseHelper/internal/msclient/client"
)

const (
	stateIDCancelled = "st-cancelled"
	stateIDPreparing = "st-preparing"

	orderIDFirst  = "order-1"
	orderIDSecond = "order-2"
)

// fakeFormsClient — заглушка FormsClient: отдаёт заданные заказы и справочник
// статусов, запоминает день выборки.
type fakeFormsClient struct {
	orders     []client.MSOrder
	states     map[string]string
	ordersErr  error
	statesErr  error
	stateCalls int
	gotDay     time.Time
}

func (f *fakeFormsClient) FetchOrdersByDeliveryDate(_ context.Context, day time.Time) ([]client.MSOrder, error) {
	f.gotDay = day

	return f.orders, f.ordersErr
}

func (f *fakeFormsClient) FetchOrderStates(_ context.Context) (map[string]string, error) {
	f.stateCalls++

	return f.states, f.statesErr
}

// fakePrinter — заглушка FormPrinter: запоминает ids и отдаёт заданный результат.
type fakePrinter struct {
	path    string
	skipped []string
	err     error
	gotIDs  []string
	calls   int
}

func (p *fakePrinter) GetMultipleOrdersPDF(_ context.Context, ids []string) (string, []string, error) {
	p.calls++
	p.gotIDs = ids

	return p.path, p.skipped, p.err
}

// formsOrders — три заказа дня: имя статуса в строке, только id статуса (МС
// проигнорировал expand=state) и без статуса вовсе; номера — для проверки
// числовой сортировки.
func formsOrders() []client.MSOrder {
	return []client.MSOrder{
		{
			ID:                    orderIDFirst,
			Name:                  "10",
			DeliveryPlannedMoment: "2026-09-10 12:00:00.000",
			State:                 client.MSState{Name: "Отменен"},
		},
		{
			ID:                    orderIDSecond,
			Name:                  "9",
			DeliveryPlannedMoment: "2026-09-10 09:00:00.000",
			State:                 client.MSState{Meta: client.MSMeta{HREF: "…/states/" + stateIDPreparing}},
			StateID:               stateIDPreparing,
		},
		{
			ID:   "order-3",
			Name: "101",
			State: client.MSState{
				Meta: client.MSMeta{HREF: "…/states/" + stateIDCancelled},
			},
			StateID: stateIDCancelled,
		},
	}
}

func TestFormsByDate_RowsFilledSortedAndStatesResolved(t *testing.T) {
	ms := &fakeFormsClient{
		orders: formsOrders(),
		states: map[string]string{stateIDPreparing: "Подготовка"},
	}
	uc := NewFormsUseCase(ms, &fakePrinter{})

	day := time.Date(2026, time.September, 10, 0, 0, 0, 0, time.UTC)
	rows, err := uc.FormsByDate(context.Background(), day)
	if err != nil {
		t.Fatalf("FormsByDate() error = %v", err)
	}

	if !ms.gotDay.Equal(day) {
		t.Errorf("в клиент ушёл день %v, want %v", ms.gotDay, day)
	}
	if ms.stateCalls != 1 {
		t.Errorf("запросов справочника статусов = %d, want 1", ms.stateCalls)
	}

	// Числовая сортировка по номеру: 9, 10, 101 (а не «10, 101, 9»).
	wantNames := []string{"9", "10", "101"}
	if len(rows) != len(wantNames) {
		t.Fatalf("строк = %d, want %d", len(rows), len(wantNames))
	}
	for i, want := range wantNames {
		if rows[i].Name != want {
			t.Errorf("строка %d: номер %q, want %q", i, rows[i].Name, want)
		}
	}

	// Статус: из строки, из справочника по id, и «—» — когда нет вовсе.
	wantStates := []string{"Подготовка", "Отменен", dash}
	for i, want := range wantStates {
		if rows[i].State != want {
			t.Errorf("строка %d (%s): статус %q, want %q", i, rows[i].Name, rows[i].State, want)
		}
	}

	if rows[0].ID != orderIDSecond {
		t.Errorf("id строки 0 = %q, want %q", rows[0].ID, orderIDSecond)
	}
	if rows[0].Delivery != "10.09.2026" {
		t.Errorf("дата доставки = %q, want 10.09.2026", rows[0].Delivery)
	}
	if rows[2].Delivery != dash {
		t.Errorf("пустая дата доставки = %q, want %q", rows[2].Delivery, dash)
	}
}

func TestFormsByDate_NoStatesRequestWhenNamesInRows(t *testing.T) {
	orders := formsOrders()
	orders[1].State = client.MSState{Name: "Подготовка"}
	orders[2].State = client.MSState{Name: "Отменен"}

	ms := &fakeFormsClient{orders: orders}
	uc := NewFormsUseCase(ms, &fakePrinter{})

	rows, err := uc.FormsByDate(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("FormsByDate() error = %v", err)
	}
	if ms.stateCalls != 0 {
		t.Errorf("справочник статусов запрошен %d раз, want 0 (имена пришли в строках)", ms.stateCalls)
	}
	if len(rows) != 3 {
		t.Fatalf("строк = %d, want 3", len(rows))
	}
}

func TestFormsByDate_StatesCatalogErrorKeepsRows(t *testing.T) {
	// Справочник статусов — вторичные данные: его ошибка не роняет список,
	// статус остаётся «—» (клиент залогировал).
	ms := &fakeFormsClient{orders: formsOrders(), statesErr: errors.New("МС недоступен")}
	uc := NewFormsUseCase(ms, &fakePrinter{})

	rows, err := uc.FormsByDate(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("FormsByDate() error = %v, want nil", err)
	}
	if len(rows) != 3 {
		t.Fatalf("строк = %d, want 3", len(rows))
	}
	if rows[0].State != dash {
		t.Errorf("статус без справочника = %q, want %q", rows[0].State, dash)
	}
}

func TestFormsByDate_FetchError(t *testing.T) {
	wantErr := errors.New("МС недоступен")
	ms := &fakeFormsClient{ordersErr: wantErr}
	uc := NewFormsUseCase(ms, &fakePrinter{})

	if _, err := uc.FormsByDate(context.Background(), time.Now()); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestFormsByDate_NoOrders(t *testing.T) {
	uc := NewFormsUseCase(&fakeFormsClient{}, &fakePrinter{})

	rows, err := uc.FormsByDate(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("FormsByDate() error = %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("строк = %d, want 0", len(rows))
	}
}

func TestPrintForms_CleansIDsAndReportsSkipped(t *testing.T) {
	printer := &fakePrinter{path: "merged.pdf", skipped: []string{orderIDSecond}}
	uc := NewFormsUseCase(&fakeFormsClient{}, printer)

	path, skipped, err := uc.PrintForms(context.Background(),
		[]string{" " + orderIDFirst + " ", "", orderIDSecond, orderIDFirst})
	if err != nil {
		t.Fatalf("PrintForms() error = %v", err)
	}

	if path != "merged.pdf" {
		t.Errorf("path = %q, want merged.pdf", path)
	}
	if len(skipped) != 1 || skipped[0] != orderIDSecond {
		t.Errorf("skipped = %v, want [%s]", skipped, orderIDSecond)
	}

	// Пустые id выброшены, дубликат схлопнут, порядок выделения сохранён.
	wantIDs := []string{orderIDFirst, orderIDSecond}
	if len(printer.gotIDs) != len(wantIDs) {
		t.Fatalf("в печать ушло %v, want %v", printer.gotIDs, wantIDs)
	}
	for i, want := range wantIDs {
		if printer.gotIDs[i] != want {
			t.Errorf("id %d = %q, want %q", i, printer.gotIDs[i], want)
		}
	}
}

func TestPrintForms_NoSelectedOrders(t *testing.T) {
	printer := &fakePrinter{}
	uc := NewFormsUseCase(&fakeFormsClient{}, printer)

	_, _, err := uc.PrintForms(context.Background(), []string{"", "   "})
	if !errors.Is(err, ErrNoOrdersSelected) {
		t.Fatalf("error = %v, want ErrNoOrdersSelected", err)
	}
	if printer.calls != 0 {
		t.Errorf("печать вызвана %d раз, want 0", printer.calls)
	}
}

func TestPrintForms_PrinterError(t *testing.T) {
	wantErr := errors.New("бланки не получены")
	uc := NewFormsUseCase(&fakeFormsClient{}, &fakePrinter{err: wantErr})

	if _, _, err := uc.PrintForms(context.Background(), []string{orderIDFirst}); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}
