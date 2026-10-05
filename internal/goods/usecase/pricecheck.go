package usecase

import (
	"context"
	"errors"
	"fmt"

	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/metrics"
)

// ErrVATOutOfRange — процент НДС вне диапазона 0..100.
var ErrVATOutOfRange = errors.New("НДС должен быть от 0 до 100 %")

// ErrPriceNegative — отрицательная цена (песочница принимает цены в копейках).
var ErrPriceNegative = errors.New("цена не может быть отрицательной")

// Подписи «чего не хватает» для страницы «Проверка цен»: неполные данные не
// считаем, а показываем вместо наценки, каких значений нет.
const (
	missingSalePrice = "цена продажи"
	missingBuyPrice  = "закупочная цена"
	missingVatOut    = "наш НДС"
	missingVatIn     = "входящий НДС"
)

// MarkupResult — результат расчёта наценки. Percent имеет смысл только когда
// Missing пуст (решение владельца 05.10.2026: неполные данные не отдаём).
type MarkupResult struct {
	Percent float64
	Missing []string
}

// PriceCheckItem — строка страницы «Проверка цен»: значения базы, наценка по
// ним и последний снапшот песочницы (если человек что-то в ней менял).
type PriceCheckItem struct {
	ID           string
	InternalCode string
	Name         string
	GroupName    string
	SalePrice    *int64 // цена продажи из МС, копейки
	BuyPrice     *int64 // закупочная из МС, копейки
	EffectiveVat *int16 // наш НДС из МС, %; -1 — «без НДС»
	VATIncoming  *int16 // входящий НДС, %; NULL — не задан
	Markup       MarkupResult
	// Sandbox — снапшот песочницы товара (nil — не заполнялась);
	// SandboxMarkup — наценка по значениям снапшота (нужна только вместе с ним).
	Sandbox       *domain.PriceSandbox
	SandboxMarkup MarkupResult
}

// computeMarkup — наценка по ценам без НДС (формула владельца, 05.10.2026):
//
//	(цена продажи − (НДС исходящий − НДС входящий) − закупочная) / закупочная × 100,
//
// где НДС исходящий = цена продажи / 100 × наш НДС,
// НДС входящий = закупочная / 100 × НДС входящий.
//
// Это ЕДИНСТВЕННАЯ реализация формулы: страница «Проверка цен» не повторяет её
// в JS, а получает готовое число от сервера (эндпоинты .../vat и .../sandbox).
//
// Неполные данные не считаем: Missing перечисляет, чего не хватает (подписи для
// страницы), и тогда Percent не определён. НДС = -1 («без НДС», метка синка из
// МС) — значение ЗАПОЛНЕННОЕ, в формуле участвует как 0 %.
func computeMarkup(salePrice, buyPrice *int64, vatOut, vatIn *int16) MarkupResult {
	var missing []string

	if salePrice == nil {
		missing = append(missing, missingSalePrice)
	}
	if buyPrice == nil || *buyPrice <= 0 {
		// Закупочная 0 — делить не на что, для формулы это такое же «нет данных».
		missing = append(missing, missingBuyPrice)
	}
	if vatOut == nil {
		missing = append(missing, missingVatOut)
	}
	if vatIn == nil {
		missing = append(missing, missingVatIn)
	}
	if len(missing) > 0 {
		return MarkupResult{Missing: missing}
	}

	sale := float64(*salePrice)
	buy := float64(*buyPrice)
	out := float64(vatPercent(*vatOut))
	in := float64(*vatIn)

	return MarkupResult{Percent: (sale - (sale/100*out - buy/100*in) - buy) / buy * 100}
}

// vatPercent — НДС каталога в процентах для формулы: -1 («без НДС») читается как 0 %.
func vatPercent(vat int16) int16 {
	if vat < 0 {
		return 0
	}

	return vat
}

// PriceCheckList — товары каталога для страницы «Проверка цен»: цены и НДС из
// products, наценка по значениям базы и снапшот песочницы каждого товара.
// Порядок — как у каталога (group_name, name), страница режет его на группы.
func (uc *GoodsUseCase) PriceCheckList(ctx context.Context) ([]PriceCheckItem, error) {
	done := metrics.Track(trackPkg, "PriceCheckList")
	defer done()

	products, err := uc.repo.LoadAllProducts(ctx)
	if err != nil {
		return nil, fmt.Errorf("каталог: %w", err)
	}
	boxes, err := uc.repo.LoadPriceSandboxes(ctx)
	if err != nil {
		return nil, fmt.Errorf("песочница цен: %w", err)
	}

	items := make([]PriceCheckItem, 0, len(products))
	for _, p := range products {
		item := PriceCheckItem{
			ID:           p.ID,
			InternalCode: p.InternalCode,
			Name:         p.Name,
			GroupName:    p.GroupName,
			SalePrice:    p.SalePrice,
			BuyPrice:     p.BuyPrice,
			EffectiveVat: p.EffectiveVat,
			VATIncoming:  p.VATIncoming,
			Markup:       computeMarkup(p.SalePrice, p.BuyPrice, p.EffectiveVat, p.VATIncoming),
		}
		if box, ok := boxes[p.ID]; ok {
			item.Sandbox = &box
			item.SandboxMarkup = computeMarkup(box.SalePrice, box.BuyPrice, box.EffectiveVat, box.VATIncoming)
		}
		items = append(items, item)
	}

	return items, nil
}

// SetIncomingVAT записывает входящий НДС товара (products.vat_incoming, %):
// ручное поле страницы «Проверка цен»; nil сбрасывает в NULL.
func (uc *GoodsUseCase) SetIncomingVAT(ctx context.Context, productID string, vat *int16) error {
	done := metrics.Track(trackPkg, "SetIncomingVAT")
	defer done()

	if err := checkVAT(vat); err != nil {
		return err
	}

	return uc.repo.SetProductIncomingVAT(ctx, productID, vat)
}

// PriceCheckMarkup — наценка товара по значениям БАЗЫ (пересчёт строки после
// записи входящего НДС: формула обязана жить в одном месте — здесь).
func (uc *GoodsUseCase) PriceCheckMarkup(ctx context.Context, productID string) (MarkupResult, error) {
	done := metrics.Track(trackPkg, "PriceCheckMarkup")
	defer done()

	p, err := uc.repo.GetProduct(ctx, productID)
	if err != nil {
		return MarkupResult{}, err
	}

	return computeMarkup(p.SalePrice, p.BuyPrice, p.EffectiveVat, p.VATIncoming), nil
}

// SavePriceSandbox сохраняет снапшот песочницы товара и возвращает наценку по
// нему. В products НЕ пишет: песочница «пофантазировать» — отдельная таблица
// состояния страницы, цены каталога она не меняет.
func (uc *GoodsUseCase) SavePriceSandbox(ctx context.Context, s domain.PriceSandbox) (MarkupResult, error) {
	done := metrics.Track(trackPkg, "SavePriceSandbox")
	defer done()

	if s.ProductID == "" {
		return MarkupResult{}, domain.ErrProductNotFound
	}
	if err := checkVAT(s.EffectiveVat); err != nil {
		return MarkupResult{}, err
	}
	if err := checkVAT(s.VATIncoming); err != nil {
		return MarkupResult{}, err
	}
	if err := checkPrice(s.SalePrice); err != nil {
		return MarkupResult{}, err
	}
	if err := checkPrice(s.BuyPrice); err != nil {
		return MarkupResult{}, err
	}
	if err := uc.repo.UpsertPriceSandbox(ctx, s); err != nil {
		return MarkupResult{}, err
	}

	return computeMarkup(s.SalePrice, s.BuyPrice, s.EffectiveVat, s.VATIncoming), nil
}

// checkVAT — диапазон процента НДС; nil («не задано») допустим.
func checkVAT(vat *int16) error {
	if vat != nil && (*vat < 0 || *vat > 100) {
		return ErrVATOutOfRange
	}

	return nil
}

// checkPrice — цена в копейках; nil («не задано») допустим, отрицательная — нет.
func checkPrice(price *int64) error {
	if price != nil && *price < 0 {
		return ErrPriceNegative
	}

	return nil
}
