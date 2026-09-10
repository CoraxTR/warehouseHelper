package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"warehouseHelper/internal/msclient/client"
)

// fakeOrderDetail — фейк OrderClient для Detail/Submit: заказ, позиции,
// агент-хоп, сырьё (эхо для PUT) и перехват PUT.
type fakeOrderDetail struct {
	order          *client.MSOrder
	positions      []client.MSPosition
	orderErr       error
	positionsErr   error
	agentName      string
	agentPhone     string
	agentErr       error
	fetchByIDCalls int
	orderRaw       json.RawMessage   // сырое тело GET заказа (nil — из order)
	rowsRaw        []json.RawMessage // сырые строки positions (nil — из positions)
	putBody        []json.RawMessage // тела PUT (UpdateCustomerOrder)
	putErr         error
}

func (f *fakeOrderDetail) SearchCustomerOrdersByName(context.Context, string) ([]client.MSOrder, error) {
	return nil, errors.New("SearchCustomerOrdersByName не нужен в Detail-тестах")
}

func (f *fakeOrderDetail) FetchOrderAgentByHREF(_ context.Context, _ *client.MSOrder) (name, phone string, err error) {
	return f.agentName, f.agentPhone, f.agentErr
}

func (f *fakeOrderDetail) FetchOrderByID(_ context.Context, _ string) (*client.MSOrder, json.RawMessage, error) {
	f.fetchByIDCalls++
	if f.orderErr != nil {
		return nil, nil, f.orderErr
	}
	raw, err := f.rawOrder()
	if err != nil {
		return nil, nil, err
	}
	return f.order, raw, nil
}

func (f *fakeOrderDetail) FetchOrderPositionsByHREF(_ context.Context, _ *client.MSOrder) ([]client.MSPosition, []json.RawMessage, error) {
	if f.positionsErr != nil {
		return nil, nil, f.positionsErr
	}
	raw, err := f.rawRows()
	if err != nil {
		return nil, nil, err
	}
	return f.positions, raw, nil
}

func (f *fakeOrderDetail) UpdateCustomerOrder(_ context.Context, _ string, body json.RawMessage) error {
	if f.putErr != nil {
		return f.putErr
	}
	f.putBody = append(f.putBody, body)
	return nil
}

// rawOrder — сырое тело GET заказа (явное или из модели).
func (f *fakeOrderDetail) rawOrder() (json.RawMessage, error) {
	if f.orderRaw != nil {
		return f.orderRaw, nil
	}
	return json.Marshal(f.order)
}

// rawRows — сырые строки positions (явные или из моделей позиций).
func (f *fakeOrderDetail) rawRows() ([]json.RawMessage, error) {
	if f.rowsRaw != nil {
		return f.rowsRaw, nil
	}
	out := make([]json.RawMessage, 0, len(f.positions))
	for i := range f.positions {
		b, err := json.Marshal(&f.positions[i])
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// fakeCatalog — фейк CatalogReader.
type fakeCatalog struct {
	byCode map[string]CatalogProduct
	err    error
	// avgWeights — средние веса (кг) по products.id для LoadProductAverageWeights.
	avgWeights map[string]float64
	avgErr     error
	// avgIDs — id, переданные в последний вызов LoadProductAverageWeights
	// (проверка, что весовые позиции не запрашиваются).
	avgIDs []string
}

func (f *fakeCatalog) LoadCatalogProductsByCodes(_ context.Context, _ []string) (map[string]CatalogProduct, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byCode, nil
}

func (f *fakeCatalog) LoadProductAverageWeights(_ context.Context, productIDs []string) (map[string]float64, error) {
	f.avgIDs = productIDs
	if f.avgErr != nil {
		return nil, f.avgErr
	}
	if f.avgWeights == nil {
		return map[string]float64{}, nil
	}
	return f.avgWeights, nil
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
	uc := NewUseCase(fake, &fakeCatalog{}, &fakePicker{})

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
	uc := NewUseCase(fake, &fakeCatalog{}, &fakePicker{})

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
	uc := NewUseCase(&fakeOrderDetail{order: detailOrder(), positions: positions}, catalog, &fakePicker{})

	o, err := uc.Detail(context.Background(), "id")
	if err != nil {
		t.Fatalf("Detail() error: %v", err)
	}

	rows := o.Rows
	if len(rows) != 5 {
		t.Fatalf("len(rows) = %d, want 5", len(rows))
	}

	// Порядок: HasCode по (code, price): 00210006, затем 00220002×2, потом без кода в исходном порядке (p5, p4).
	if got := ids(rows); got != "p3 p1 p2 p5 p4" {
		t.Errorf("порядок строк = %s, want [p3 p1 p2 p5 p4]", got)
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
	uc := NewUseCase(&fakeOrderDetail{order: detailOrder(), positions: positions}, catalog, &fakePicker{})

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
	uc := NewUseCase(&fakeOrderDetail{order: detailOrder(), positions: positions}, catalog, &fakePicker{})

	if _, err := uc.Detail(context.Background(), "id"); err == nil {
		t.Fatal("Detail() error = nil, want ошибку каталога (нельзя показать строки без резолва)")
	}
}

func TestDetailPositionsError(t *testing.T) {
	fake := &fakeOrderDetail{order: detailOrder(), positionsErr: errors.New("network")}
	uc := NewUseCase(fake, &fakeCatalog{}, &fakePicker{})

	if _, err := uc.Detail(context.Background(), "id"); err == nil {
		t.Fatal("Detail() error = nil, want ошибку клиента по позициям")
	}
}

func TestDetailEmptyID(t *testing.T) {
	uc := NewUseCase(&fakeOrderDetail{}, &fakeCatalog{}, &fakePicker{})

	if _, err := uc.Detail(context.Background(), "   "); !errors.Is(err, ErrEmptyOrderID) {
		t.Fatalf("Detail(' ') error = %v, want ErrEmptyOrderID", err)
	}
}

func TestDetailAgentHopErrorKeepsDash(t *testing.T) {
	fake := &fakeOrderDetail{order: detailOrder(), agentErr: errors.New("network")}
	uc := NewUseCase(fake, &fakeCatalog{}, &fakePicker{})

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

// TestDetailRowTopupStates — состояния строк при частичном резерве: штучная
// 0 < reserve < qty активна как добор (счёт стартует с резерва), полностью
// зарезервированная штучная и любая весовая с резервом — «Переподобрать»,
// товар вне каталога пассивен.
func TestDetailRowTopupStates(t *testing.T) {
	o := detailOrder()
	o.MSPositions = client.MSPositions{
		Meta: client.MSMeta{HREF: "https://api.moysklad.ru/api/remap/1.2/entity/customerorder/" + o.ID + "/positions"},
	}
	fake := &fakeOrderDetail{
		order: o,
		positions: []client.MSPosition{
			position("p-part", "21110001", "Хлеб", 5, 30000, 2),       // 0 < r < qty — добор
			position("p-full", "21110001", "Хлеб", 2, 30000, 2),       // r == qty — переподобрать
			position("p-zero", "21110001", "Хлеб", 5, 30000, 0),       // подбор с нуля
			position("p-wgh", "00220002", "Стейк", 0.5, 279000, 0.48), // весовая с резервом — не трогаем
			position("p-wgh0", "00220002", "Стейк", 0.5, 279000, 0),   // весовая активная
			position("p-for", "99999999", "Чужое", 1, 10000, 0),       // вне каталога
		},
	}
	uc := NewUseCase(fake, submitCatalog(), &fakePicker{})
	order, err := uc.Detail(context.Background(), o.ID)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}

	byID := make(map[string]OrderItem, len(order.Rows))
	for _, r := range order.Rows {
		byID[r.ID] = r
	}

	want := []struct {
		id        string
		active    bool
		canRepick bool
		reserve   float64
	}{
		{"p-zero", true, false, 0},   // reserve 0 — подбор с нуля
		{"p-part", true, false, 2},   // 0 < reserve < qty — добор
		{"p-full", false, true, 2},   // reserve == qty — «Переподобрать»
		{"p-wgh0", true, false, 0},   // весовая без резерва — активна
		{"p-wgh", false, true, 0.48}, // весовая с резервом — CanRepick
		{"p-for", false, false, 0},   // вне каталога — пассивна
	}
	for _, w := range want {
		r, ok := byID[w.id]
		if !ok {
			t.Errorf("строка %s не найдена в Rows", w.id)
			continue
		}
		if r.Active != w.active || r.CanRepick != w.canRepick {
			t.Errorf("%s: Active/CanRepick = %v/%v, want %v/%v", w.id, r.Active, r.CanRepick, w.active, w.canRepick)
		}
		if r.Reserve != w.reserve {
			t.Errorf("%s: Reserve = %v, want %v (data-reserve для клиента)", w.id, r.Reserve, w.reserve)
		}
	}
}

// sumCatalog — каталог тестов сумм: штучный p1 (код 21110001) и весовой p2
// (код 00220002).
func sumCatalog() *fakeCatalog {
	return &fakeCatalog{byCode: map[string]CatalogProduct{
		"21110001": {ProductID: "p1", InternalCode: "21110001"},
		"00220002": {ProductID: "p2", InternalCode: "00220002", Weighted: true},
	}}
}

// TestDetailSumsWeightsAndTotals — сумма строки (Price×Qty, копейки через
// moneyInt), общий вес (Σ кг весовых + Σ шт×средний вес) и общая сумма.
func TestDetailSumsWeightsAndTotals(t *testing.T) {
	pieceAndWeighted := []client.MSPosition{
		position("piece", "21110001", "Хлеб", 3, 1116000, 0),   // 3 шт × 11160,00 = 33480,00
		position("wgh", "00220002", "Стейк", 0.367, 279000, 0), // 0,367 кг × 2790,00 = 1023,93
	}

	tests := []struct {
		name            string
		positions       []client.MSPosition
		avgWeights      map[string]float64
		avgErr          error
		wantSums        map[string]string // id строки → SumText
		wantTotalSum    string
		wantTotalWeight string
		wantMissing     int
	}{
		{
			name:            "штучная и весовая: средний вес есть",
			positions:       pieceAndWeighted,
			avgWeights:      map[string]float64{"p1": 0.15},
			wantSums:        map[string]string{"piece": "33480,00", "wgh": "1023,93"},
			wantTotalSum:    "34503,93",
			wantTotalWeight: "0,817 кг", // 3×0,15 + 0,367
			wantMissing:     0,
		},
		{
			name:            "штучная без среднего веса в вес не входит",
			positions:       pieceAndWeighted,
			avgWeights:      map[string]float64{},
			wantSums:        map[string]string{"piece": "33480,00", "wgh": "1023,93"},
			wantTotalSum:    "34503,93",
			wantTotalWeight: "0,367 кг",
			wantMissing:     1,
		},
		{
			name:            "ошибка чтения веса не роняет страницу",
			positions:       pieceAndWeighted,
			avgErr:          errors.New("db down"),
			wantSums:        map[string]string{"piece": "33480,00", "wgh": "1023,93"},
			wantTotalSum:    "34503,93",
			wantTotalWeight: "0,367 кг",
			wantMissing:     1,
		},
		{
			name: "нормализация копеек (float-хвосты)",
			positions: []client.MSPosition{
				position("w1", "00220002", "Стейк", 0.2, 279000, 0), // 55800.00000000001 → 558,00
				position("w2", "00220002", "Стейк", 0.2, 279000, 0),
			},
			avgWeights:      map[string]float64{},
			wantSums:        map[string]string{"w1": "558,00", "w2": "558,00"},
			wantTotalSum:    "1116,00",
			wantTotalWeight: "0,4 кг",
			wantMissing:     0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			catalog := sumCatalog()
			catalog.avgWeights = tc.avgWeights
			catalog.avgErr = tc.avgErr
			uc := NewUseCase(&fakeOrderDetail{order: detailOrder(), positions: tc.positions}, catalog, &fakePicker{})

			order, err := uc.Detail(context.Background(), "id")
			if err != nil {
				t.Fatalf("Detail() error: %v", err)
			}

			byID := make(map[string]OrderItem, len(order.Rows))
			for i := range order.Rows {
				byID[order.Rows[i].ID] = order.Rows[i]
			}
			for id, want := range tc.wantSums {
				if got := byID[id].SumText; got != want {
					t.Errorf("SumText[%s] = %q, want %q", id, got, want)
				}
			}
			if order.TotalSumText != tc.wantTotalSum {
				t.Errorf("TotalSumText = %q, want %q", order.TotalSumText, tc.wantTotalSum)
			}
			if order.TotalWeightText != tc.wantTotalWeight {
				t.Errorf("TotalWeightText = %q, want %q", order.TotalWeightText, tc.wantTotalWeight)
			}
			if order.WeightMissingPositions != tc.wantMissing {
				t.Errorf("WeightMissingPositions = %d, want %d", order.WeightMissingPositions, tc.wantMissing)
			}
		})
	}
}

// TestDetailAverageWeightOnlyForPieces — AverageWeightKg проставляется только
// по штучным строкам; весовые id в чтение веса не уходят.
func TestDetailAverageWeightOnlyForPieces(t *testing.T) {
	catalog := sumCatalog()
	catalog.avgWeights = map[string]float64{"p1": 0.15}
	positions := []client.MSPosition{
		position("piece", "21110001", "Хлеб", 3, 1116000, 0),
		position("wgh", "00220002", "Стейк", 0.367, 279000, 0),
	}
	uc := NewUseCase(&fakeOrderDetail{order: detailOrder(), positions: positions}, catalog, &fakePicker{})

	order, err := uc.Detail(context.Background(), "id")
	if err != nil {
		t.Fatalf("Detail() error: %v", err)
	}
	if len(catalog.avgIDs) != 1 || catalog.avgIDs[0] != "p1" {
		t.Errorf("LoadProductAverageWeights ids = %v, want [p1] (весовые не запрашиваются)", catalog.avgIDs)
	}

	byID := make(map[string]OrderItem, len(order.Rows))
	for i := range order.Rows {
		byID[order.Rows[i].ID] = order.Rows[i]
	}
	if aw := byID["piece"].AverageWeightKg; aw == nil || *aw != 0.15 {
		t.Errorf("piece.AverageWeightKg = %v, want 0.15", aw)
	}
	if aw := byID["wgh"].AverageWeightKg; aw != nil {
		t.Errorf("wgh.AverageWeightKg = %v, want nil (весовой вес уже в Qty)", *aw)
	}
}

// TestDetailGroupSubtotals — подытог группы: Σ Qty по всем строкам группы,
// GroupLast только у последней строки группы (под ней рендерится «итого»).
func TestDetailGroupSubtotals(t *testing.T) {
	positions := []client.MSPosition{
		position("g1", "00220002", "Стейк", 0.367, 279000, 0),
		position("g2", "00220002", "Стейк", 0.4, 279000, 0),
		position("s1", "21110001", "Хлеб", 2, 30000, 0),
		position("s2", "21110001", "Хлеб", 3, 30000, 0),
	}
	uc := NewUseCase(&fakeOrderDetail{order: detailOrder(), positions: positions}, sumCatalog(), &fakePicker{})

	order, err := uc.Detail(context.Background(), "id")
	if err != nil {
		t.Fatalf("Detail() error: %v", err)
	}
	byID := make(map[string]OrderItem, len(order.Rows))
	for i := range order.Rows {
		byID[order.Rows[i].ID] = order.Rows[i]
	}

	want := []struct {
		id        string
		qtyText   string
		qty       float64
		groupLast bool
		groupSize int
	}{
		{"g1", "0,767 кг", 0.767, false, 2},
		{"g2", "0,767 кг", 0.767, true, 2},
		{"s1", "5 шт", 5, false, 2},
		{"s2", "5 шт", 5, true, 2},
	}
	for _, w := range want {
		row, ok := byID[w.id]
		if !ok {
			t.Fatalf("строка %s не найдена", w.id)
		}
		if row.GroupQtyText != w.qtyText {
			t.Errorf("%s: GroupQtyText = %q, want %q", w.id, row.GroupQtyText, w.qtyText)
		}
		if math.Abs(row.GroupQty-w.qty) > 1e-9 {
			t.Errorf("%s: GroupQty = %v, want %v", w.id, row.GroupQty, w.qty)
		}
		if row.GroupLast != w.groupLast {
			t.Errorf("%s: GroupLast = %v, want %v", w.id, row.GroupLast, w.groupLast)
		}
		if row.GroupSize != w.groupSize {
			t.Errorf("%s: GroupSize = %d, want %d", w.id, row.GroupSize, w.groupSize)
		}
	}
}
