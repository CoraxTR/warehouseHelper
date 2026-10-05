// Файл — фоновый обновитель цен товаров модуля «Продукция».
//
// В МойСклад цена — СОСТОЯНИЕ, а не событие: поллер не «догоняет события», а
// периодически переспрашивает актуальное значение. Поэтому время следующего
// прохода перезаписывается СТРОГО на момент окончания прохода — в том числе
// когда запрос к МС упал: пропущенное окно не догоняем и курсор не откатываем,
// недостающее закроет полный проход по всем id каталога.
//
// Два режима, и оба решает ОДИН тик (отдельного тикера у полного прохода нет):
//   - ПОЛНЫЙ ПРОХОД (батч): нет строки курсора (первый запуск после наката
//     схемы) ИЛИ в БД записана не сегодняшняя МСК-дата. Все id товаров режутся
//     на чанки BatchSize и спрашиваются пачками — лечит пропуски и мёртвые id;
//   - ИНКРЕМЕНТ (каждые PollInterval): только изменённые с момента курсора.
//
// Привязка полного прохода к КАЛЕНДАРЮ, а не к интервалу от старта процесса —
// осознанное решение (05.10.2026): с интервалом время прохода дрейфовало бы за
// рестартами (поднялись в 9:05 — дальше 9:05, 9:10…). Дата хранится в БД, так
// что рестарт не запускает лишний полный проход: пропущенное за время простоя
// доберёт инкремент — его окно начинается от курсора.
package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/msclient/client"
)

// mskLoc — TZ склада: МС отдаёт моменты и календарные даты в МСК, а сервер живёт
// в UTC. Календарь полного прохода считаем по МСК, иначе сутки съезжают на 3 часа.
var mskLoc = time.FixedZone("MSK", 3*60*60)

// PricesConfig — настройки поллера цен. Нулевые значения заменяются на
// дефолты в NewPricesPoller (как в sitecheck/reservewatch).
type PricesConfig struct {
	PollInterval time.Duration    // период инкремента; 0 → 10 минут
	BatchSize    int              // размер чанка полного прохода; 0 → 100
	Now          func() time.Time // шов времени для тестов; nil → time.Now
}

// PricesRepository — швы репозитория на стороне потребителя (goods):
// список id каталога, запись цен пачкой и курсор. Реализует
// *repository/postgres; интерфейс объявлен здесь, а не у репозитория.
// Имя с префиксом Prices — в пакете уже есть ProductsRepository (каталог).
type PricesRepository interface {
	// ProductIDs — все id товаров каталога (для полного прохода).
	ProductIDs(ctx context.Context) ([]string, error)
	// UpdateProductPrices — записать цены/НДС пачкой; возвращает число
	// обновлённых строк.
	UpdateProductPrices(ctx context.Context, prices []domain.ProductPrice) (int, error)
	// GetPriceCursor — момент, с которого смотреть изменения, и МСК-дата
	// последнего полного прохода ("" — прохода ещё не было); ok=false —
	// строки курсора нет (первый запуск).
	GetPriceCursor(ctx context.Context) (next time.Time, lastFullScan string, ok bool, err error)
	// SetPriceCursor — перезаписать время следующего прохода и дату полного.
	SetPriceCursor(ctx context.Context, next time.Time, lastFullScan string) error
}

// ProductPriceClient — источник цен из МС (реализует *client.MSAPIClient).
type ProductPriceClient interface {
	FetchProductPricesByIDs(ctx context.Context, ids []string) ([]client.MSProductPrice, error)
	FetchProductPricesSince(ctx context.Context, since time.Time) ([]client.MSProductPrice, error)
}

// PricesPoller — фоновый обновитель цен: полный проход по календарю плюс
// инкремент по updated. Логгер — пакетный slog (как в sitecheck), не поле.
type PricesPoller struct {
	cfg      PricesConfig
	products PricesRepository
	prices   ProductPriceClient
}

// NewPricesPoller собирает поллер; нулевые настройки заменяются дефолтами.
func NewPricesPoller(cfg PricesConfig, products PricesRepository, prices ProductPriceClient) *PricesPoller {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 10 * time.Minute
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	return &PricesPoller{cfg: cfg, products: products, prices: prices}
}

// Run — цикл поллера: каждый тик сам решает, что делать (полный проход, если
// он ещё не делался сегодня, либо инкремент). Ошибка тика логируется (slog.Error)
// и НЕ роняет поллер — следующий тик продолжит (как в sitecheck).
func (p *PricesPoller) Run(ctx context.Context) error {
	slog.Info("goods: обновитель цен запущен",
		"интервал", p.cfg.PollInterval.String(),
		"чанк", p.cfg.BatchSize,
		"календарь", "МСК")

	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()

	for {
		if err := p.tick(ctx); err != nil && ctx.Err() == nil {
			slog.Error("goods: обновление цен", "err", err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// tick — один проход поллера: полный проход, если он ещё не делался сегодня
// (МСК), иначе инкремент. Дата берётся из БД, а не из памяти процесса: рестарт
// не сдвигает календарь и не запускает лишний полный проход (изменения за время
// простоя добирает инкремент — его окно идёт от курсора).
func (p *PricesPoller) tick(ctx context.Context) error {
	now := p.cfg.Now()
	today := now.In(mskLoc).Format(time.DateOnly)

	next, lastFullScan, ok, err := p.products.GetPriceCursor(ctx)
	if err != nil {
		return fmt.Errorf("цены: чтение курсора: %w", err)
	}

	if !ok {
		// Первого запуска нет в календаре: полный проход идёт сразу, чтобы ждать
		// полуночи не пришлось. Курсор при падении запроса НЕ пишем: строки нет —
		// значит следующий тик повторит проход (инкременту пока опираться не на что).
		if err := p.tickBatch(ctx); err != nil {
			return err
		}

		if err := p.products.SetPriceCursor(ctx, now, today); err != nil {
			return fmt.Errorf("цены: запись курсора после полного прохода: %w", err)
		}

		return nil
	}

	if lastFullScan < today {
		// Новый календарный день: полный проход. Дата перезаписывается СТРОГО —
		// даже когда запрос к МС упал (ретраи уже отработали в воркерпуле, дальше
		// по стандарту: логируем и пропускаем; недостающее доберёт инкремент).
		batchErr := p.tickBatch(ctx)
		if err := p.products.SetPriceCursor(ctx, now, today); err != nil {
			return fmt.Errorf("цены: запись курсора после полного прохода: %w", err)
		}

		return batchErr
	}

	return p.tickIncrement(ctx, now, next, lastFullScan)
}

// tickBatch — полный проход: все id каталога пачками по BatchSize. Своего sleep
// между чанками нет — рейт-лимит уже держит воркерпул МС. В лог INFO идёт
// «запросили M id → вернулось K → обновили J»; K<M или J<K — WARN (мёртвые id
// или расхождение данных, чинится ресинком каталога).
func (p *PricesPoller) tickBatch(ctx context.Context) error {
	ids, err := p.products.ProductIDs(ctx)
	if err != nil {
		return fmt.Errorf("цены: список товаров: %w", err)
	}

	requested, returned, updated := 0, 0, 0
	for start := 0; start < len(ids); start += p.cfg.BatchSize {
		end := min(start+p.cfg.BatchSize, len(ids))
		chunk := ids[start:end]

		got, err := p.prices.FetchProductPricesByIDs(ctx, chunk)
		if err != nil {
			return fmt.Errorf("цены: запрос цен пачки [%d:%d]: %w", start, end, err)
		}

		n, err := p.products.UpdateProductPrices(ctx, msPricesToDomain(got))
		if err != nil {
			return fmt.Errorf("цены: запись цен пачки [%d:%d]: %w", start, end, err)
		}

		requested += len(chunk)
		returned += len(got)
		updated += n
	}

	slog.Info("goods: обновление цен (батч)",
		"запросили", requested, "вернулось", returned, "обновили", updated)
	// K<M — часть id не отдаётся МС (мёртвые/устаревшие); J<K — часть цен не
	// легла. Оба случая — признак расхождения, показываем WARN.
	if returned < requested || updated < returned {
		slog.Warn("goods: обновление цен (батч): расхождение",
			"запросили", requested, "вернулось", returned, "обновили", updated)
	}

	return nil
}

// tickIncrement — инкрементальный проход: окно [курсор..now]. Порядок «сначала
// запрос, потом перезапись времени» плюс строгая перезапись: при падении запроса
// к МС время следующего прохода ВСЁ РАВНО двигается на now (пропущенное закроет
// полный проход), а ошибка уходит вызывающему — Run её залогирует.
func (p *PricesPoller) tickIncrement(ctx context.Context, now, cursor time.Time, lastFullScan string) error {
	// ВАЖНО про время: since — момент курсора, ведётся в UTC и передаётся в МС
	// КАК ЕСТЬ. Форматирование в МСК (фильтр updated>=…) делает клиент МС;
	// модуль со временем МС не работает.
	got, fetchErr := p.prices.FetchProductPricesSince(ctx, cursor)
	if fetchErr != nil {
		// Строгая перезапись времени следующего прохода: двигаем его на now ДАЖЕ
		// при падении запроса. Окно не догоняем, курсор не откатываем — цена это
		// состояние, а не событие; недостающее закроет полный проход по каталогу.
		if setErr := p.products.SetPriceCursor(ctx, now, lastFullScan); setErr != nil {
			return fmt.Errorf("цены: запись курсора после ошибки запроса: %w", setErr)
		}

		return fmt.Errorf("цены: запрос изменений с %s: %w", cursor.UTC().Format(time.RFC3339), fetchErr)
	}

	n, err := p.products.UpdateProductPrices(ctx, msPricesToDomain(got))
	if err != nil {
		// Запись в БД не удалась — время НЕ двигаем: следующим тиком переспросим
		// то же окно, данные МС ещё доступны.
		return fmt.Errorf("цены: запись изменённых цен: %w", err)
	}

	// Перезапись времени — ТОЛЬКО после того, как запрос сделан и результат
	// записан. Значение — now, а не момент последнего товара: цена это состояние,
	// повторный опрос безвреден. Дата полного прохода не меняется.
	if err := p.products.SetPriceCursor(ctx, now, lastFullScan); err != nil {
		return fmt.Errorf("цены: запись курсора: %w", err)
	}

	slog.Info("goods: обновление цен (инкремент)",
		"с", cursor.UTC().Format(time.RFC3339),
		"запросили", len(got), "обновили", n)

	return nil
}

// msProductPrice собирает domain.ProductPrice из полного ответа МС по товару
// (выгрузка дерева и ресинк). Единое правило маппинга цен/НДС — вызывается из
// buildProduct, чтобы правило не разъехалось с фоновым обновителем.
func msProductPrice(ms client.MSProduct) domain.ProductPrice {
	return domain.ProductPrice{
		ID:           ms.ID,
		BuyPrice:     buyPriceKopecks(ms.BuyPrice),
		SalePrice:    salePriceKopecks(ms.SalePrices),
		EffectiveVat: resolveVat(ms.EffectiveVat, ms.EffectiveVatEnabled, ms.UseParentVat),
	}
}

// msPricesToDomain переводит ответ МС (батч/инкремент) в domain.ProductPrice.
// У client.MSProductPrice цены уже в копейках (*int64) — округление float→int
// делает клиент МС; здесь только решение по НДС.
func msPricesToDomain(prices []client.MSProductPrice) []domain.ProductPrice {
	out := make([]domain.ProductPrice, 0, len(prices))
	for _, p := range prices {
		out = append(out, domain.ProductPrice{
			ID:           p.ID,
			BuyPrice:     p.BuyPrice,
			SalePrice:    p.SalePrice,
			EffectiveVat: resolveVat(p.EffectiveVat, p.EffectiveVatEnabled, p.UseParentVat),
		})
	}

	return out
}

// buyPriceKopecks — закупочная цена из МС (объект buyPrice, Value в копейках)
// в *int64; nil-объект → nil (МС не отдала цену). Округление как в контракте.
func buyPriceKopecks(p *client.MSBuyPrice) *int64 {
	if p == nil {
		return nil
	}
	v := int64(math.Round(p.Value))

	return &v
}

// salePriceKopecks — цена продажи: ПЕРВЫЙ элемент salePrices (в учётке тип
// цены ровно один — «Цена продажи»). Пустой/отсутствующий массив → nil.
func salePriceKopecks(prices []client.MSSalePrice) *int64 {
	if len(prices) == 0 {
		return nil
	}
	v := int64(math.Round(prices[0].Value))

	return &v
}

// resolveVat — НДС из полей МС в нашу метку (-1 = «без НДС», nil = не отдана):
//   - UseParentVat или EffectiveVat == nil → nil (НДС наследуется от группы);
//   - vatEnabled=false при effectiveVat=0 → -1 («без НДС»);
//   - иначе → int16(*effectiveVat).
func resolveVat(effectiveVat *int, enabled *bool, useParent bool) *int16 {
	if useParent || effectiveVat == nil {
		return nil
	}
	if enabled != nil && !*enabled && *effectiveVat == 0 {
		v := int16(-1)

		return &v
	}
	v := int16(*effectiveVat)

	return &v
}
