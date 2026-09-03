package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"warehouseHelper/internal/msclient/client"
)

// fakeOrderSearch — фейк OrderSearchClient, запоминает переданное имя.
type fakeOrderSearch struct {
	gotName string
	orders  []client.MSOrder
	err     error
	calls   int

	agentName string // имя, возвращаемое хопом
	agentErr  error  // ошибка хопа
	hopCalls  int
}

func (f *fakeOrderSearch) SearchCustomerOrdersByName(_ context.Context, name string) ([]client.MSOrder, error) {
	f.calls++
	f.gotName = name
	if f.err != nil {
		return nil, f.err
	}
	return f.orders, nil
}

func (f *fakeOrderSearch) FetchOrderAgentByHREF(_ context.Context, _ *client.MSOrder) (name, phone string, err error) {
	f.hopCalls++
	return f.agentName, "", f.agentErr
}

func sampleMSOrders() []client.MSOrder {
	return []client.MSOrder{
		{
			ID:   "00023557-7e97-11e7-7a34-5acf0020c748",
			Name: "03969",
			Meta: client.MSMeta{
				HREF: "https://api.moysklad.ru/api/remap/1.2/entity/customerorder/00023557-7e97-11e7-7a34-5acf0020c748?expand=agent",
			},
			Moment:                "2017-08-11 16:13:00.000",
			DeliveryPlannedMoment: "2017-08-12 16:14:00.000",
			Agent: client.MSAgent{
				Name: "ООО \"ШИРИН 2\"",
			},
		},
	}
}

func TestSearchMapsOrderRow(t *testing.T) {
	fake := &fakeOrderSearch{orders: sampleMSOrders()}
	uc := NewUseCase(fake)

	rows, err := uc.Search(context.Background(), "  03969  ")
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if fake.gotName != "03969" {
		t.Errorf("клиенту передано имя %q, want %q (обрезка пробелов)", fake.gotName, "03969")
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.Name != "03969" {
		t.Errorf("Name = %q, want 03969", r.Name)
	}
	if r.Created != "11.08.2017" {
		t.Errorf("Created = %q, want 11.08.2017 (тримминг с миллисекундами)", r.Created)
	}
	if r.DeliveryDate != "12.08.2017" {
		t.Errorf("DeliveryDate = %q, want 12.08.2017", r.DeliveryDate)
	}
	if r.AgentName != "ООО \"ШИРИН 2\"" {
		t.Errorf("AgentName = %q, want ООО \"ШИРИН 2\"", r.AgentName)
	}
	wantHref := "https://api.moysklad.ru/api/remap/1.2/entity/customerorder/00023557-7e97-11e7-7a34-5acf0020c748?expand=agent"
	if r.Href != wantHref {
		t.Errorf("Href = %q, want %q", r.Href, wantHref)
	}
}

func TestSearchEmptyName(t *testing.T) {
	fake := &fakeOrderSearch{}
	uc := NewUseCase(fake)

	for _, name := range []string{"", "   "} {
		_, err := uc.Search(context.Background(), name)
		if !errors.Is(err, ErrEmptyName) {
			t.Errorf("Search(%q) error = %v, want ErrEmptyName", name, err)
		}
	}
	if fake.calls != 0 {
		t.Errorf("клиент вызван %d раз при пустом имени, want 0", fake.calls)
	}
}

func TestSearchEmptyValuesBecomeDash(t *testing.T) {
	orders := sampleMSOrders()
	orders[0].Agent.Name = "   "
	orders[0].DeliveryPlannedMoment = ""
	orders[0].Moment = "не-дата"
	fake := &fakeOrderSearch{orders: orders}
	uc := NewUseCase(fake)

	rows, err := uc.Search(context.Background(), "03969")
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	r := rows[0]
	if r.AgentName != dash {
		t.Errorf("AgentName = %q, want «—» для пустого агента", r.AgentName)
	}
	if r.DeliveryDate != dash {
		t.Errorf("DeliveryDate = %q, want «—» для пустой даты", r.DeliveryDate)
	}
	if r.Created != dash {
		t.Errorf("Created = %q, want «—» для непарсящейся даты", r.Created)
	}
}

func TestSearchClientError(t *testing.T) {
	fake := &fakeOrderSearch{err: errors.New("429 too many requests")}
	uc := NewUseCase(fake)

	_, err := uc.Search(context.Background(), "03969")
	if err == nil {
		t.Fatal("Search() error = nil, want проброс ошибки клиента")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("ошибка не содержит текст источника: %v", err)
	}
}

func TestSearchNotFound(t *testing.T) {
	fake := &fakeOrderSearch{orders: []client.MSOrder{}}
	uc := NewUseCase(fake)

	rows, err := uc.Search(context.Background(), "00000")
	if err != nil {
		t.Fatalf("Search() error: %v, want nil", err)
	}
	if rows == nil || len(rows) != 0 {
		t.Errorf("rows = %v, want пустой не-nil слайс (не найдено — не ошибка)", rows)
	}
}

// Тесты хопа за именем контрагента: expand=agent МС игнорирует при filter=name,
// имя дотягивается отдельным запросом по agent.meta.href.

func TestSearchAgentHopFetchesName(t *testing.T) {
	orders := sampleMSOrders()
	orders[0].Agent.Name = "" // expand не сработал — имя не приехало
	orders[0].Agent.Meta.HREF = "https://api.moysklad.ru/api/remap/1.2/entity/counterparty/cp-1"
	fake := &fakeOrderSearch{orders: orders, agentName: "ООО \"ХОП\""}
	uc := NewUseCase(fake)

	rows, err := uc.Search(context.Background(), "03969")
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if fake.hopCalls != 1 {
		t.Errorf("хопов = %d, want 1 (имя не приехало в строке)", fake.hopCalls)
	}
	if rows[0].AgentName != "ООО \"ХОП\"" {
		t.Errorf("AgentName = %q, want имя из хопа", rows[0].AgentName)
	}
}

func TestSearchAgentHopSkippedWhenNamePresent(t *testing.T) {
	fake := &fakeOrderSearch{orders: sampleMSOrders()} // Agent.Name уже заполнен
	uc := NewUseCase(fake)

	if _, err := uc.Search(context.Background(), "03969"); err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if fake.hopCalls != 0 {
		t.Errorf("хопов = %d, want 0 (имя уже в строке)", fake.hopCalls)
	}
}

func TestSearchAgentHopSkippedWithoutHref(t *testing.T) {
	orders := sampleMSOrders()
	orders[0].Agent.Name = "" // href агента не задан — хопа нет
	fake := &fakeOrderSearch{orders: orders}
	uc := NewUseCase(fake)

	if _, err := uc.Search(context.Background(), "03969"); err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if fake.hopCalls != 0 {
		t.Errorf("хопов = %d, want 0 (нет href агента)", fake.hopCalls)
	}
}

func TestSearchAgentHopErrorKeepsDash(t *testing.T) {
	orders := sampleMSOrders()
	orders[0].Agent.Name = ""
	orders[0].Agent.Meta.HREF = "https://api.moysklad.ru/api/remap/1.2/entity/counterparty/cp-1"
	fake := &fakeOrderSearch{orders: orders, agentErr: errors.New("network")}
	uc := NewUseCase(fake)

	rows, err := uc.Search(context.Background(), "03969")
	if err != nil {
		t.Fatalf("Search() error: %v, want nil (хоп не роняет поиск)", err)
	}
	if fake.hopCalls != 1 {
		t.Errorf("хопов = %d, want 1", fake.hopCalls)
	}
	if rows[0].AgentName != dash {
		t.Errorf("AgentName = %q, want «—» при ошибке хопа", rows[0].AgentName)
	}
}
