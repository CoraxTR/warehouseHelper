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
	// GetPriceCursor — курсор обновителя цен: момент следующего инкремента и
	// МСК-дата последнего полного прохода (Exists=false — строки курсора нет,
	// первый запуск).
	GetPriceCursor(ctx context.Context) (domain.ProductPriceCursor, error)
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

	cur, err := p.products.GetPriceCursor(ctx)
	if err != nil {
		return fmt.Errorf("цены: чтение курсора: %w", err)
	}

	if !cur.Exists {
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

	if cur.LastFullScan != today {
		// Новый календарный день — полный проход. Сравнение строго на НЕРАВЕНСТВО
		// (не «<»): дата в курсоре из будущего — сдвиг часов сервера или ручная
		// правка БД — при «<» навсегда подавила бы полный проход.
		//
		// ДАТУ перезаписываем строго даже при падении запроса — иначе упавший
		// проход ретраился бы каждые 10 минут. А время следующего инкремента при
		// падении НЕ двигаем: окно [старый курсор..now] осталось непрочитанным, и
		// инкремент следующим тиком закроет пропущенное сам. Иначе правки,
		// сделанные между старым курсором и now, не увидел бы никто до завтра.
		batchErr := p.tickBatch(ctx)

		nextScan := now
		if batchErr != nil {
			nextScan = cur.Next
		}

		if err := p.products.SetPriceCursor(ctx, nextScan, today); err != nil {
			return fmt.Errorf("цены: запись курсора после полного прохода: %w", err)
		}

		return batchErr
	}

	return p.tickIncrement(ctx, now, cur.Next, cur.LastFullScan)
}

// tickBatch — полный проход: все id каталога пачками по BatchSize. Своего sleep
// между чанками нет — рейт-лимит уже держит воркерпул МС. В лог INFO идёт
// «запросили M id → вернулось K → обновили J»; K<M — МС не отдала часть
// запрошенных id (мёртвые/устаревшие), это WARN и лечится ресинком каталога.
// J<K — НЕ расхождение: UpdateProductPrices возвращает число реально
// изменившихся строк, а постоянные цены не пишутся вообще.
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
	// K<M — МС не отдала часть запрошенных id (мёртвые/устаревшие): признак
	// расхождения. J<K проверять нельзя: J — число ИЗМЕНИВШИХСЯ строк, поэтому
	// на установившемся каталоге он меньше K штатно и давал бы ложный WARN.
	if returned < requested {
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
// (выгрузка дерева и ресинк). Перевод «сырой МС → цены в копейках» живёт в
// клиенте МС (client.ProductPriceFrom) — та же функция, которой пользуется
// фоновый обновитель: правило маппинга одно на оба пути, разъехаться не может.
// Здесь добавляется только наше решение по НДС.
func msProductPrice(ms client.MSProduct) domain.ProductPrice {
	return msPriceToDomain(client.ProductPriceFrom(ms))
}

// msPriceToDomain — цены одного товара из ответа МС в домен: копейки посчитаны
// клиентом (*int64), здесь — решение по НДС (resolveVat).
func msPriceToDomain(p client.MSProductPrice) domain.ProductPrice {
	return domain.ProductPrice{
		ID:           p.ID,
		BuyPrice:     p.BuyPrice,
		SalePrice:    p.SalePrice,
		EffectiveVat: resolveVat(p),
	}
}

// msPricesToDomain — то же для пачки (батч и инкремент обновителя).
func msPricesToDomain(prices []client.MSProductPrice) []domain.ProductPrice {
	out := make([]domain.ProductPrice, 0, len(prices))
	for _, p := range prices {
		out = append(out, msPriceToDomain(p))
	}

	return out
}

// resolveVat — НДС из полей МС в нашу метку (-1 = «без НДС», nil = не отдана):
//   - UseParentVat или EffectiveVat == nil → nil (НДС наследуется от группы);
//   - vatEnabled=false при effectiveVat=0 → -1 («без НДС»);
//   - EffectiveVat вне диапазона int16 → nil (см. проверку ниже);
//   - иначе → int16(*effectiveVat).
func resolveVat(p client.MSProductPrice) *int16 {
	if p.UseParentVat || p.EffectiveVat == nil {
		return nil
	}
	if p.EffectiveVatEnabled != nil && !*p.EffectiveVatEnabled && *p.EffectiveVat == 0 {
		v := int16(-1)

		return &v
	}
	// НДС МС — int в JSON, а у нас метка int16: конверсию делаем только после
	// явной проверки диапазона (G115), иначе переполнение молча исказило бы
	// ставку. Практически МС отдаёт 10/22, но защищаемся: вне int16 считаем,
	// что ставка не отдана (nil), и пишем предупреждение.
	if *p.EffectiveVat < math.MinInt16 || *p.EffectiveVat > math.MaxInt16 {
		slog.Warn("goods: НДС МС вне диапазона int16 — считаем, что не отдана", "значение", *p.EffectiveVat)

		return nil
	}
	v := int16(*p.EffectiveVat)

	return &v
}
