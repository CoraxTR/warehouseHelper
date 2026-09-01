package usecase

import (
	"context"
	"time"

	"warehouseHelper/internal/averagesales"
	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/msclient/client"
)

// stubSales — заглушка отчёта прибыльности: считает вызовы, строки по rowsFn.
type stubSales struct {
	rowsFn  func(from, to time.Time, interval string, filter client.ProfitFilter) []client.ProfitRow
	err     error
	calls   int
	froms   []time.Time
	filters []client.ProfitFilter
}

func (s *stubSales) FetchProfitTurnover(_ context.Context, from, to time.Time, interval string, filter client.ProfitFilter) ([]client.ProfitRow, error) {
	s.calls++
	s.froms = append(s.froms, from)
	s.filters = append(s.filters, filter)
	if s.err != nil {
		return nil, s.err
	}
	if s.rowsFn == nil {
		return nil, nil
	}
	return s.rowsFn(from, to, interval, filter), nil
}

// stubRepo — заглушка хранилища оборотов.
type stubRepo struct {
	monthly  []averagesales.TurnoverRow // возвращается из LastMonthlyTurnover
	weekly   []averagesales.TurnoverRow
	upsM     []averagesales.TurnoverRow
	upsW     []averagesales.TurnoverRow
	hasM     bool
	hasW     bool
	missingM []string // дыры в месячном окне (селекция дозаливки)
	missingW []string // дыры в недельном окне
	err      error
}

func (r *stubRepo) UpsertMonthlyTurnover(_ context.Context, rows []averagesales.TurnoverRow) error {
	if r.err != nil {
		return r.err
	}
	r.upsM = append(r.upsM, rows...)
	return nil
}

func (r *stubRepo) UpsertWeeklyTurnover(_ context.Context, rows []averagesales.TurnoverRow) error {
	if r.err != nil {
		return r.err
	}
	r.upsW = append(r.upsW, rows...)
	return nil
}

func (r *stubRepo) LastMonthlyTurnover(_ context.Context, _ string, _ int) ([]averagesales.TurnoverRow, error) {
	return r.monthly, nil
}

func (r *stubRepo) LastWeeklyTurnover(_ context.Context, _ string, _ int) ([]averagesales.TurnoverRow, error) {
	return r.weekly, nil
}

func (r *stubRepo) HasMonthlyTurnover(_ context.Context, _ string) (bool, error) {
	return r.hasM, nil
}

func (r *stubRepo) HasWeeklyTurnover(_ context.Context, _ string) (bool, error) {
	return r.hasW, nil
}

func (r *stubRepo) ProductsMissingMonthlyTurnover(_ context.Context, _ []string) ([]string, error) {
	return r.missingM, nil
}

func (r *stubRepo) ProductsMissingWeeklyTurnover(_ context.Context, _ []string) ([]string, error) {
	return r.missingW, nil
}

// stubProducts — заглушка каталога.
type stubProducts struct {
	byID map[string]averagesales.TurnoverProduct
	err  error
}

func (p *stubProducts) TurnoverProduct(_ context.Context, id string) (*averagesales.TurnoverProduct, error) {
	if p.err != nil {
		return nil, p.err
	}
	prod, ok := p.byID[id]
	if !ok {
		return nil, domain.ErrProductNotFound
	}
	return &prod, nil
}

func (p *stubProducts) TurnoverProductsByIDs(_ context.Context, ids []string) ([]averagesales.TurnoverProduct, error) {
	if p.err != nil {
		return nil, p.err
	}
	out := make([]averagesales.TurnoverProduct, 0, len(ids))
	for _, id := range ids {
		if prod, ok := p.byID[id]; ok {
			out = append(out, prod)
		}
	}
	return out, nil
}

// profitRow — строка отчёта для товара (штучный, продажи 10/возврат 1).
func profitRow(productID string, sell, ret float64) client.ProfitRow {
	var r client.ProfitRow
	r.Assortment.Meta.Href = "https://api.moysklad.ru/api/remap/1.2/entity/product/" + productID
	r.Assortment.UOM.Name = "шт"
	r.SellQuantity = sell
	r.ReturnQuantity = ret
	return r
}
