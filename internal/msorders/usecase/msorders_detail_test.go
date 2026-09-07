package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"warehouseHelper/internal/msclient/client"
)

// fakeOrderDetail — фейк OrderClient для Detail: заказ, позиции, агент-хоп.
type fakeOrderDetail struct {
	order          *client.MSOrder
	positions      []client.MSPosition
	orderErr       error
	positionsErr   error
	agentName      string
	agentPhone     string
	agentErr       error
	fetchByIDCalls int
}

func (f *fakeOrderDetail) SearchCustomerOrdersByName(context.Context, string) ([]client.MSOrder, error) {
	return nil, errors.New("SearchCustomerOrdersByName не нужен в Detail-тестах")
}

func (f *fakeOrderDetail) FetchOrderAgentByHREF(_ context.Context, _ *client.MSOrder) (name, phone string, err error) {
	return f.agentName, f.agentPhone, f.agentErr
}

func (f *fakeOrderDetail) FetchOrderByID(_ context.Context, _ string) (*client.MSOrder, error) {
	f.fetchByIDCalls++
	if f.orderErr != nil {
		return nil, f.orderErr
	}
	return f.order, nil
}

func (f *fakeOrderDetail) FetchOrderPositionsByHREF(_ context.Context, _ *client.MSOrder) ([]client.MSPosition, error) {
	if f.positionsErr != nil {
		return nil, f.positionsErr
	}
	return f.positions, nil
}

// fakeCatalog — фейк CatalogReader.
type fakeCatalog struct {
	byCode map[string]CatalogProduct
	err    error
}

func (f *fakeCatalog) LoadCatalogProductsByCodes(_ context.Context, _ []string) (map[string]CatalogProduct, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byCode, nil
}

func detailOrder() *client.MSOrder {
	return &client.MSOrder{
		ID:                    "053b3dfc-926b-11f1-0a80-135d00113455",
		Name:                  "05685",
		DeliveryPlannedMoment: "2026-09-13 17:19:00.000",
		Description:           "самовывозом заберёт",
		ShipmentAddress:       "старый адрес строкой",
		ShipmentAddressFull:   client.MSAddressFull{AddInfo: "микрорайон Дедешино-8, 14-1", Comment: "2п,16эт,77кв"},
		Agent: client.MSAgent{Meta: client.MSMeta{
			HREF: "https://api.moysklad.ru/api/remap/1.2/entity/counterparty/cp-1",
		}},
	}
}

// position собирает позицию заказа для теста.
func position(id, code, name string, qty, price, reserve float64) client.MSPosition {
	return client.MSPosition{
		ID:       id,
		Quantity: qty,
		Price:    price,
		Reserve:  reserve,
		Assortment: client.MSAssortment{
			Meta: client.MSMeta{HREF: "https://api.moysklad.ru/api/remap/1.2/entity/product/" + id},
			Code: code,
			Name: name,
		},
	}
}

func TestDetailHeader(t *testing.T) {
	fake := &fakeOrderDetail{order: detailOrder(), agentName: "ООО Ромашка", agentPhone: "+7 900 123-45-67"}
	uc := NewUseCase(fake, &fakeCatalog{})

	o, err := uc.Detail(context.Background(), detailOrder().ID)
	if err != nil {
		t.Fatalf("Detail() error: %v", err)
	}

	if o.Name != "05685" {
		t.Errorf("Name = %q, want 05685", o.Name)
	}
	if o.AgentName != "ООО Ромашка" || o.AgentPhone != "+7 900 123-45-67" {
		t.Errorf("агент = %q / %q, want имя и телефон из хопа", o.AgentName, o.AgentPhone)
	}
	if o.Address != "микрорайон Дедешино-8, 14-1" {
		t.Errorf("Address = %q, want addInfo полного адреса", o.Address)
	}
	if o.AddressComment != "2п,16эт,77кв" {
		t.Errorf("AddressComment = %q, want comment полного адреса", o.AddressComment)
	}
	if o.DeliveryDate != "13.09.2026" {
		t.Errorf("DeliveryDate = %q, want 13.09.2026", o.DeliveryDate)
	}
	if o.Comment != "самовывозом заберёт" {
		t.Errorf("Comment = %q", o.Comment)
	}
}

func TestDetailAddressFallbackToShipmentAddress(t *testing.T) {
	order := detailOrder()
	order.ShipmentAddressFull = client.MSAddressFull{} // полный адрес пуст
	fake := &fakeOrderDetail{order: order}
	uc := NewUseCase(fake, &fakeCatalog{})

	o, err := uc.Detail(context.Background(), order.ID)
	if err != nil {
		t.Fatalf("Detail() error: %v", err)
	}
	if o.Address != "старый адрес строкой" {
		t.Errorf("Address = %q, want shipmentAddress (fallback)", o.Address)
	}
}

func TestDetailRowsSortingGroupsAndFormatting(t *testing.T) {
	positions := []client.MSPosition{
		position("p5", "", "Фарш (старый товар)", 3, 10000, 0),                        // без кода → пассивная, в конец
		position("p3", "00210006", "Стейк Нью-Йорк DryAged", 0.288, 1129000, 0.288),   // резерв = весь объём → пассивная
		position("p1", "00220002", "Стейк Нью-Йорк АМТ", 0.367, 279000, 0),            // активная
		position("p4", "00310030", "Стейк Мираторг (вне каталога)", 0.416, 459000, 0), // код не найден → пассивная
		position("p2", "00220002", "Стейк Нью-Йорк АМТ", 0.4, 279000, 0),              // та же группа, что p1
	}
	catalog := &fakeCatalog{byCode: map[string]CatalogProduct{
		"00220002": {InternalCode: "00220002", Weighted: true},
		"00210006": {InternalCode: "00210006", Weighted: true},
	}}
	uc := NewUseCase(&fakeOrderDetail{order: detailOrder(), positions: positions}, catalog)

	o, err := uc.Detail(context.Background(), "id")
	if err != nil {
		t.Fatalf("Detail() error: %v", err)
	}

	rows := o.Rows
	if len(rows) != 5 {
		t.Fatalf("len(rows) = %d, want 5", len(rows))
	}

	// Порядок: HasCode по (code, price): 00210006, затем 00220002×2, потом без кода в исходном порядке (p5, p4).
	if rows[0].ID != "p3" || rows[1].ID != "p1" || rows[2].ID != "p2" || rows[3].ID != "p5" || rows[4].ID != "p4" {
		t.Errorf("порядок строк = %s, want [p3 p1 p2 p5 p4]", ids(rows))
	}

	// Группы склейки: p1+p2 (00220002, одна цена) — группа 1 размером 2; p3 — одиночная.
	if rows[1].Group != rows[2].Group || rows[1].Group == 0 {
		t.Errorf("p1/p2 не в одной группе: %d vs %d", rows[1].Group, rows[2].Group)
	}
	if rows[1].GroupSize != 2 {
		t.Errorf("GroupSize группы p1/p2 = %d, want 2", rows[1].GroupSize)
	}
	if rows[0].GroupSize != 1 {
		t.Errorf("GroupSize p3 = %d, want 1", rows[0].GroupSize)
	}
	if rows[3].Group != 0 || rows[4].Group != 0 {
		t.Errorf("строки без кода должны быть вне групп: p5=%d p4=%d", rows[3].Group, rows[4].Group)
	}

	// Активность: HasCode и резерв.
	if !rows[1].HasCode || !rows[1].Weighted {
		t.Errorf("p1: HasCode=%v Weighted=%v, want активную весовую", rows[1].HasCode, rows[1].Weighted)
	}
	if rows[1].Reserve != 0 {
		t.Errorf("p1 Reserve = %v, want 0 (активная)", rows[1].Reserve)
	}
	if !rows[0].HasCode || rows[0].Reserve != 0.288 {
		t.Errorf("p3: HasCode=%v Reserve=%v, want код с резервом", rows[0].HasCode, rows[0].Reserve)
	}
	if rows[4].HasCode {
		t.Error("p4: HasCode = true, want false (код вне каталога)")
	}
	if rows[3].HasCode {
		t.Error("p5: HasCode = true, want false (кода нет)")
	}
}

// TestDetailRowFormatting — форматы количества и цены (запятая, обрезка
// хвостовых нулей у килограммов, целые у штучных).
func TestDetailRowFormatting(t *testing.T) {
	positions := []client.MSPosition{
		position("p1", "00220002", "Стейк АМТ", 0.367, 279000, 0),
		position("p2", "00220002", "Стейк АМТ", 0.4, 279000, 0),
		position("p5", "", "Фарш (без кода)", 3, 10000, 0),
	}
	catalog := &fakeCatalog{byCode: map[string]CatalogProduct{
		"00220002": {InternalCode: "00220002", Weighted: true},
	}}
	uc := NewUseCase(&fakeOrderDetail{order: detailOrder(), positions: positions}, catalog)

	o, err := uc.Detail(context.Background(), "id")
	if err != nil {
		t.Fatalf("Detail() error: %v", err)
	}
	rows := o.Rows
	if len(rows) != 3 {
		t.Fatalf("len(rows) = %d, want 3", len(rows))
	}

	if rows[0].QtyText != "0,367 кг" {
		t.Errorf("QtyText p1 = %q, want «0,367 кг»", rows[0].QtyText)
	}
	if rows[1].QtyText != "0,4 кг" {
		t.Errorf("QtyText p2 = %q, want «0,4 кг» (хвостовые нули обрезаны)", rows[1].QtyText)
	}
	if rows[2].QtyText != "3 шт" {
		t.Errorf("QtyText p5 = %q, want «3 шт»", rows[2].QtyText)
	}
	if rows[0].PriceText != "2790,00" {
		t.Errorf("PriceText p1 = %q, want «2790,00»", rows[0].PriceText)
	}
}

func TestDetailResolveRequiresCatalog(t *testing.T) {
	positions := []client.MSPosition{position("p1", "00220002", "Стейк", 0.367, 279000, 0)}
	catalog := &fakeCatalog{err: errors.New("db down")}
	uc := NewUseCase(&fakeOrderDetail{order: detailOrder(), positions: positions}, catalog)

	if _, err := uc.Detail(context.Background(), "id"); err == nil {
		t.Fatal("Detail() error = nil, want ошибку каталога (нельзя показать строки без резолва)")
	}
}

func TestDetailPositionsError(t *testing.T) {
	fake := &fakeOrderDetail{order: detailOrder(), positionsErr: errors.New("network")}
	uc := NewUseCase(fake, &fakeCatalog{})

	if _, err := uc.Detail(context.Background(), "id"); err == nil {
		t.Fatal("Detail() error = nil, want ошибку клиента по позициям")
	}
}

func TestDetailEmptyID(t *testing.T) {
	uc := NewUseCase(&fakeOrderDetail{}, &fakeCatalog{})

	if _, err := uc.Detail(context.Background(), "   "); !errors.Is(err, ErrEmptyOrderID) {
		t.Fatalf("Detail(' ') error = %v, want ErrEmptyOrderID", err)
	}
}

func TestDetailAgentHopErrorKeepsDash(t *testing.T) {
	fake := &fakeOrderDetail{order: detailOrder(), agentErr: errors.New("network")}
	uc := NewUseCase(fake, &fakeCatalog{})

	o, err := uc.Detail(context.Background(), "id")
	if err != nil {
		t.Fatalf("Detail() error: %v, want nil (хоп не роняет страницу)", err)
	}
	if o.AgentName != dash || o.AgentPhone != dash {
		t.Errorf("агент = %q/%q, want «—» при ошибке хопа", o.AgentName, o.AgentPhone)
	}
}

func ids(rows []OrderItem) string {
	var sb strings.Builder
	for i, r := range rows {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(r.ID)
	}
	return sb.String()
}
