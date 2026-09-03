// Пакет usecase — сценарии модуля «Сроки»: кэш остатков (прогрев при старте),
// чтение снапшота для страниц, запись ручных скидок из UI, замена остатков
// по сканам («Обновить сроки»).
//
// Модуль — единственный владелец product_stock: все записи идут через него
// по цепочке «БД → кэш → публикация события». Приёмка и подбор будут ходить
// сюда же (AcceptStock/PickStock — адаптеры появятся вместе с их модулями,
// см. план ~/.hermes/plans/stock-module.md).
package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"warehouseHelper/internal/innercode"
	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/stock"
)

const trackPkg = "stock"

// Repository — контракт хранилища остатков, реализуется postgres-репозиторием.
type Repository interface {
	// LoadAllStock возвращает все товары каталога с их лотами остатков,
	// отсортированные по (group_name, name, best_before). Товары без остатков
	// приходят с пустым Lots — страницы «Сроки» показывают весь ассортимент,
	// клетки заполняются по мере прихода партий.
	LoadAllStock(ctx context.Context) ([]stock.Product, error)
	// SetManualDiscount обновляет ручные скидки лота по PK; строки нет — stock.ErrLotNotFound.
	SetManualDiscount(ctx context.Context, productID string, bestBefore time.Time, generalManual, telegramManual *int16) error
	// LoadProductsByCodes возвращает товары каталога по internal_code
	// (включая товары без остатков) — карта code → товар.
	LoadProductsByCodes(ctx context.Context, codes []string) (map[string]stock.Product, error)
	// LoadProductByID возвращает товар каталога по id; строки нет — stock.ErrProductNotFound.
	LoadProductByID(ctx context.Context, productID string) (stock.Product, error)
	// LoadGroupNameByCode возвращает название первой группы с кодом
	// internal_code[1:4]; пустая строка — группы нет.
	LoadGroupNameByCode(ctx context.Context, groupCode string) (string, error)
	// ReplaceStockLots применяет замену остатков товаров в одной транзакции:
	// upsert лотов (qty = целевое, produced_on = COALESCE(существующего, нового),
	// ручные скидки — целевые) и удаление лотов вне сканов.
	ReplaceStockLots(ctx context.Context, writes []stock.ProductWrite) error
	// AcceptStockLots добавляет принятые лоты в одной транзакции: upsert
	// с qty += (существующий срок увеличивается), produced_on — COALESCE.
	AcceptStockLots(ctx context.Context, lots []stock.LotIn) error
}

// Publisher — получатель событий об изменениях остатков (вебсокет-хаб).
type Publisher interface {
	PublishStockChange(e stock.Event)
}

// DayStateRecorder — наблюдатель состояния товара по дням (модуль daystate):
// вызывается после каждой записи остатков, чтобы пересчитать строку дня
// (наличие, скидки, sold_out). Ошибка наблюдателя операцию стока не роняет.
type DayStateRecorder interface {
	OnStockChanged(ctx context.Context, productID string) error
}

// maxDiscount — верхняя граница скидки в процентах (CHECK в БД дублирует).
const maxDiscount = 100

// StockUseCase — кэш остатков + сценарии записи. Кэш — read-модель:
// единственный писатель — сам usecase (репо → кэш → publish).
type StockUseCase struct {
	repo     Repository
	pub      Publisher
	dayState DayStateRecorder

	mu     sync.RWMutex
	cache  map[string]*stock.Product // product_id → товар каталога (Lots — по возрастанию best_before, может быть пустым)
	byCode map[string]string         // internal_code → product_id (шов для будущей приёмки)
}

// NewStockUseCase создаёт сценарий с хранилищем и публикатором (хаб может
// быть nil); dayState — наблюдатель состояния по дням (может быть nil).
func NewStockUseCase(repo Repository, pub Publisher, dayState DayStateRecorder) *StockUseCase {
	return &StockUseCase{repo: repo, pub: pub, dayState: dayState}
}

// notifyDayState уведомляет daystate об изменении остатков товаров (без
// дубликатов). Наблюдатель: ошибка только логируется, операция стока не
// роняется — строка дня пересчитается при следующем касании товара.
func (uc *StockUseCase) notifyDayState(ctx context.Context, productIDs ...string) {
	if uc.dayState == nil {
		return
	}
	seen := make(map[string]struct{}, len(productIDs))
	for _, pid := range productIDs {
		if pid == "" {
			continue
		}
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		if err := uc.dayState.OnStockChanged(ctx, pid); err != nil {
			slog.Info(fmt.Sprintf("stock: daystate %s: %v", pid, err))
		}
	}
}

// WarmUp прогревает кэш всем каталогом с лотами остатков при включении
// программы (товары без остатков — пустой Lots: страницы показывают весь
// ассортимент, а не только позиции в наличии).
func (uc *StockUseCase) WarmUp(ctx context.Context) error {
	done := metrics.Track(trackPkg, "WarmUp")
	defer done()
	products, err := uc.repo.LoadAllStock(ctx)
	if err != nil {
		return fmt.Errorf("warm up stock cache: %w", err)
	}

	cache := make(map[string]*stock.Product, len(products))
	byCode := make(map[string]string, len(products))
	for i := range products {
		p := &products[i]
		if p.InternalCode != "" {
			byCode[p.InternalCode] = p.ID
		}
		cache[p.ID] = p
	}

	uc.mu.Lock()
	uc.cache = cache
	uc.byCode = byCode
	uc.mu.Unlock()

	return nil
}

// Snapshot возвращает копию всего каталога с лотами остатков,
// отсортированную по (group_name, name) — клиент рендерит группы и строки
// последовательно. Лоты внутри товара — по возрастанию best_before
// (ближайший срок слева); товар без остатков — строка с пустыми клетками.
func (uc *StockUseCase) Snapshot() []stock.Product {
	done := metrics.Track(trackPkg, "Snapshot")
	defer done()
	uc.mu.RLock()
	out := make([]stock.Product, 0, len(uc.cache))
	for _, p := range uc.cache {
		cp := *p
		cp.Lots = append([]stock.Lot(nil), p.Lots...)
		out = append(out, cp)
	}
	uc.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		g := strings.ToLower(out[i].GroupName)
		h := strings.ToLower(out[j].GroupName)
		if g != h {
			return g < h
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})

	return out
}

// SetManualDiscount записывает ручные скидки лота из UI (попап по количеству).
// generalManual/telegramManual: 0..100; nil — сброс (NULL). Значения копируются
// в кэш, событие публикуется в хаб.
func (uc *StockUseCase) SetManualDiscount(ctx context.Context, productID string, bestBefore time.Time, generalManual, telegramManual *int16) error {
	done := metrics.Track(trackPkg, "SetManualDiscount")
	defer done()
	if err := validateManualDiscount("скидка сайта", generalManual); err != nil {
		return err
	}
	if err := validateManualDiscount("скидка ТГ", telegramManual); err != nil {
		return err
	}

	uc.mu.RLock()
	p, ok := uc.cache[productID]
	if !ok {
		uc.mu.RUnlock()
		return stock.ErrProductNotFound
	}
	lot := findLot(p, bestBefore)
	uc.mu.RUnlock()
	if lot == nil {
		return stock.ErrLotNotFound
	}

	if err := uc.repo.SetManualDiscount(ctx, productID, bestBefore, generalManual, telegramManual); err != nil {
		return err
	}

	uc.mu.Lock()
	p, ok = uc.cache[productID] // перечитываем под записью — кэш мог смениться
	if !ok {
		uc.mu.Unlock()
		return stock.ErrProductNotFound
	}
	lot = findLot(p, bestBefore)
	if lot == nil {
		uc.mu.Unlock()
		return stock.ErrLotNotFound
	}
	lot.GeneralManual = cloneInt16(generalManual)
	lot.TelegramManual = cloneInt16(telegramManual)
	uc.mu.Unlock()

	if uc.pub != nil {
		uc.pub.PublishStockChange(stock.Event{Kind: stock.EventLotUpsert, ProductID: productID, Lot: lot})
	}
	uc.notifyDayState(ctx, productID)

	return nil
}

// ValidLengths возвращает допустимые длины внутренних штрих-кодов — правила
// разбирателя для клиентского фильтра страницы сканирования (без дублирования
// формата в JS; значение приходит от владельца формата).
func (uc *StockUseCase) ValidLengths() []int {
	return innercode.ValidLengths()
}

// PageContext — контекст страницы «Обновить сроки»: ограничение сканов,
// баннер и ожидаемый код.
type PageContext struct {
	ProductID string // ограничение по товару (пусто — нет)
	GroupCode string // ограничение по группе (пусто — нет)
	Name      string // отображаемое имя (баннер)
	Code      string // ожидаемый internal_code (для ограничения по товару)
}

// UpdatePageContext собирает контекст страницы «Обновить сроки» по параметрам
// перехода: product_id — ограничение по товару (ожидаемый internal_code
// резолвится из каталога), group — ограничение по коду группы (3 цифры).
// Без параметров — полное обновление без ограничений.
func (uc *StockUseCase) UpdatePageContext(ctx context.Context, productID, groupCode string) (PageContext, error) {
	done := metrics.Track(trackPkg, "UpdatePageContext")
	defer done()
	pc := PageContext{}
	switch {
	case productID != "":
		p, err := uc.repo.LoadProductByID(ctx, productID)
		if err != nil {
			return PageContext{}, err
		}
		if p.InternalCode == "" {
			return PageContext{}, fmt.Errorf("у товара %q нет внутреннего кода", p.Name)
		}
		pc.ProductID, pc.Name, pc.Code = p.ID, p.Name, p.InternalCode
	case groupCode != "":
		name, err := uc.repo.LoadGroupNameByCode(ctx, groupCode)
		if err != nil {
			return PageContext{}, err
		}
		pc.GroupCode = groupCode
		pc.Name = name
	default:
		// Полное обновление — без ограничений и ожидаемого кода.
	}
	return pc, nil
}

// ReplaceRequest — замена остатков по сканам «Обновить сроки».
// Scans — сырые штрих-коды; ExpectedProductID/ExpectedGroupCode — ограничения
// страницы (пусто = нет ограничения, полное обновление).
type ReplaceRequest struct {
	Scans             []string
	ExpectedProductID string
	ExpectedGroupCode string
}

// ReplaceStock заменяет остатки сканированных товаров ровно по сканам:
// лоты из сканов апсертятся (qty = сумма сканов по лоту), лоты, которых нет
// в сканах, удаляются. Ручные скидки сохраняются у неистёкших лотов
// (best_before >= сегодня), у истёкших — сбрасываются; «просто»-скидки
// не трогаются. Валидация и запись атомарны: любая ошибка → ничего не меняется.
func (uc *StockUseCase) ReplaceStock(ctx context.Context, req ReplaceRequest) error {
	done := metrics.Track(trackPkg, "ReplaceStock")
	defer done()
	if len(req.Scans) == 0 {
		return errors.New("нет сканов")
	}

	parsed, err := parseScans(req.Scans, req.ExpectedGroupCode)
	if err != nil {
		return err
	}
	byCode, err := uc.repo.LoadProductsByCodes(ctx, uniqueCodes(parsed))
	if err != nil {
		return fmt.Errorf("load products by codes: %w", err)
	}
	byID := make(map[string]stock.Product, len(byCode))
	for _, p := range byCode {
		byID[p.ID] = p
	}
	batches, order, err := groupScans(parsed, byCode, req.ExpectedProductID)
	if err != nil {
		return err
	}
	plans, err := uc.buildReplacePlans(batches, order)
	if err != nil {
		return err
	}
	return uc.applyReplacePlans(ctx, order, plans, byID)
}

// parsedScan — разобранный штрих-код одного скана.
type parsedScan struct {
	code       string
	qty        int64
	producedOn time.Time
	bestBefore time.Time
}

// parseScans разбирает сырые штрих-коды разбирателем и проверяет
// ограничение страницы по группе (3 цифры internal_code[1:4]).
func parseScans(raws []string, expectedGroup string) ([]parsedScan, error) {
	parsed := make([]parsedScan, 0, len(raws))
	for _, raw := range raws {
		raw = strings.TrimSpace(raw)
		c, err := innercode.Parse(raw)
		if err != nil {
			if errors.Is(err, innercode.ErrNotInternal) {
				return nil, stock.ErrScanNotInternal
			}
			return nil, stock.ErrScanInvalid
		}
		if expectedGroup != "" && c.InternalCode[1:4] != expectedGroup {
			return nil, stock.ErrScanGroupMismatch
		}
		parsed = append(parsed, parsedScan{
			code:       c.InternalCode,
			qty:        int64(c.Qty),
			producedOn: c.ProdDate,
			// Нормализация к UTC-полуночи: ключи map time.Time с разными
			// зонами (кэш из БД vs разбор innercode) не совпадают — лоты
			// «теряются», скидки сбрасываются, всё уходит в deletes.
			bestBefore: normalizeDate(c.ExpDate),
		})
	}
	return parsed, nil
}

// uniqueCodes собирает уникальные internal_code в порядке появления.
func uniqueCodes(parsed []parsedScan) []string {
	seen := map[string]struct{}{}
	codes := make([]string, 0, len(parsed))
	for _, s := range parsed {
		if _, ok := seen[s.code]; !ok {
			seen[s.code] = struct{}{}
			codes = append(codes, s.code)
		}
	}
	return codes
}

// agg — сумма сканов одного лота (товар, срок).
type agg struct {
	qty        int64
	producedOn time.Time
}

// groupScans группирует сканы по (товар, срок) и проверяет ограничение
// страницы по товару. Возвращает батчи по товарам и порядок их появления.
func groupScans(parsed []parsedScan, byCode map[string]stock.Product, expectedProductID string) (batches map[string]map[time.Time]*agg, order []string, err error) {
	batches = map[string]map[time.Time]*agg{}
	for _, s := range parsed {
		p, ok := byCode[s.code]
		if !ok {
			return nil, nil, fmt.Errorf("%w: код %s", stock.ErrProductNotFound, s.code)
		}
		if expectedProductID != "" && p.ID != expectedProductID {
			return nil, nil, stock.ErrScanProductMismatch
		}
		m := batches[p.ID]
		if m == nil {
			m = map[time.Time]*agg{}
			batches[p.ID] = m
			order = append(order, p.ID)
		}
		a := m[s.bestBefore]
		if a == nil {
			a = &agg{}
			m[s.bestBefore] = a
		}
		a.qty += s.qty
		if a.producedOn.IsZero() {
			a.producedOn = s.producedOn
		}
	}
	return batches, order, nil
}

// replacePlan — целевое состояние одного товара после замены.
type replacePlan struct {
	inCache bool
	upserts []stock.Lot      // финальные лоты (кэш + события)
	writes  []stock.LotWrite // целевые значения для репозитория
	deletes []time.Time      // best_before удаляемых лотов
}

// buildReplacePlans вычисляет целевое состояние товаров: ручные скидки
// неистёкших лотов сохраняются, истёкшие сбрасываются; лоты вне сканов
// уходят в deletes. Читает кэш под RLock.
func (uc *StockUseCase) buildReplacePlans(batches map[string]map[time.Time]*agg, order []string) ([]replacePlan, error) {
	today := normalizeDate(time.Now())
	plans := make([]replacePlan, len(order))

	uc.mu.RLock()
	defer uc.mu.RUnlock()
	for i, pid := range order {
		pl := &plans[i]
		cur, ok := uc.cache[pid]
		pl.inCache = ok

		existing := map[time.Time]stock.Lot{}
		if ok {
			for _, l := range cur.Lots {
				existing[normalizeDate(l.BestBefore)] = l
			}
		}
		batch := batches[pid]
		for _, bb := range sortedDates(batch) {
			a := batch[bb]
			var ex *stock.Lot
			if cur, has := existing[bb]; has {
				ex = &cur
			}
			lot, write := targetLot(ex, a, bb, today)
			pl.upserts = append(pl.upserts, lot)
			pl.writes = append(pl.writes, write)
		}
		pl.deletes = deletedLots(existing, batch)
	}
	return plans, nil
}

// targetLot собирает финальный лот и целевое значение для репозитория:
// qty из сканов, produced_on — COALESCE(существующего, нового), ручные
// скидки сохраняются только у лотов со сроком не раньше сегодня.
// ex == nil — лота ещё нет (создаётся с нуля).
func targetLot(ex *stock.Lot, a *agg, bb, today time.Time) (lot stock.Lot, write stock.LotWrite) {
	var genMan, tgMan, general, telegram *int16
	if ex != nil {
		if !bb.Before(today) {
			genMan = cloneInt16(ex.GeneralManual)
			tgMan = cloneInt16(ex.TelegramManual)
		}
		general = cloneInt16(ex.General)
		telegram = cloneInt16(ex.Telegram)
	}
	produced := a.producedOn
	if ex != nil && ex.ProducedOn != nil {
		produced = *ex.ProducedOn
	}
	lot = stock.Lot{
		BestBefore:     bb,
		Qty:            a.qty,
		ProducedOn:     &produced,
		General:        general,
		Telegram:       telegram,
		GeneralManual:  genMan,
		TelegramManual: tgMan,
	}
	write = stock.LotWrite{
		BestBefore:     bb,
		Qty:            a.qty,
		ProducedOn:     a.producedOn,
		GeneralManual:  genMan,
		TelegramManual: tgMan,
	}
	return lot, write
}

// deletedLots — существующие лоты товара, которых нет в батче сканов.
func deletedLots(existing map[time.Time]stock.Lot, batch map[time.Time]*agg) []time.Time {
	var deletes []time.Time
	for bb := range existing {
		if _, in := batch[bb]; !in {
			deletes = append(deletes, bb)
		}
	}
	sort.Slice(deletes, func(a, b int) bool { return deletes[a].Before(deletes[b]) })
	return deletes
}

// sortedDates — сроки батча, отсортированные по возрастанию.
func sortedDates(batch map[time.Time]*agg) []time.Time {
	bbs := make([]time.Time, 0, len(batch))
	for bb := range batch {
		bbs = append(bbs, bb)
	}
	sort.Slice(bbs, func(a, b int) bool { return bbs[a].Before(bbs[b]) })
	return bbs
}

// applyReplacePlans применяет замену: одна транзакция в БД, затем кэш,
// затем события (сначала удаления, потом upsert'ы — клиент применяет
// к своему состоянию последовательно).
func (uc *StockUseCase) applyReplacePlans(ctx context.Context, order []string, plans []replacePlan, byID map[string]stock.Product) error {
	if err := uc.repo.ReplaceStockLots(ctx, replaceWrites(order, plans)); err != nil {
		return fmt.Errorf("replace stock lots: %w", err)
	}

	uc.mu.Lock()
	if err := uc.applyCacheLocked(order, plans, byID); err != nil {
		uc.mu.Unlock()
		return err
	}
	uc.mu.Unlock()
	uc.publishReplaceEvents(order, plans)
	uc.notifyDayState(ctx, order...)

	return nil
}

// replaceWrites собирает правки для репозитория.
func replaceWrites(order []string, plans []replacePlan) []stock.ProductWrite {
	writes := make([]stock.ProductWrite, 0, len(plans))
	for i := range plans {
		writes = append(writes, stock.ProductWrite{
			ProductID: order[i],
			Upserts:   plans[i].writes,
			Deletes:   plans[i].deletes,
		})
	}
	return writes
}

// applyCacheLocked применяет замену к кэшу. Вызывается только под mu.Lock.
func (uc *StockUseCase) applyCacheLocked(order []string, plans []replacePlan, byID map[string]stock.Product) error {
	for i := range plans {
		pl := &plans[i]
		pid := order[i]
		cur, ok := uc.cache[pid]
		if !ok {
			cat, ok := byID[pid]
			if !ok {
				return fmt.Errorf("%w: %s", stock.ErrProductNotFound, pid)
			}
			cp := cat
			cur = &cp
			uc.cache[pid] = cur
			if cur.InternalCode != "" {
				uc.byCode[cur.InternalCode] = pid
			}
		}
		cur.Lots = mergeLots(cur.Lots, pl)
	}
	return nil
}

// mergeLots убирает удаляемые лоты и применяет целевые (замена или добавление).
func mergeLots(lots []stock.Lot, pl *replacePlan) []stock.Lot {
	out := lots[:0]
	for _, l := range lots {
		if containsTime(pl.deletes, l.BestBefore) {
			continue
		}
		out = append(out, l)
	}
	for _, lot := range pl.upserts {
		idx := -1
		for j := range out {
			if out[j].BestBefore.Equal(lot.BestBefore) {
				idx = j
				break
			}
		}
		if idx >= 0 {
			out[idx] = lot
		} else {
			out = append(out, lot)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].BestBefore.Before(out[b].BestBefore) })
	return out
}

// publishReplaceEvents рассылает события замены: lot_delete для удалённых
// лотов, lot_upsert для целевых.
func (uc *StockUseCase) publishReplaceEvents(order []string, plans []replacePlan) {
	if uc.pub == nil {
		return
	}
	for i := range plans {
		pid := order[i]
		for _, bb := range plans[i].deletes {
			uc.pub.PublishStockChange(stock.Event{Kind: stock.EventLotDelete, ProductID: pid, BestBefore: bb})
		}
		for j := range plans[i].upserts {
			lot := plans[i].upserts[j]
			uc.pub.PublishStockChange(stock.Event{Kind: stock.EventLotUpsert, ProductID: pid, Lot: &lot})
		}
	}
}

// normalizeDate приводит дату к UTC-полуночи: единое представление DATE
// для ключей map и сравнений, независимо от зоны источника (БД, innercode).
func normalizeDate(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// containsTime проверяет наличие даты в отсортированном списке.
func containsTime(list []time.Time, t time.Time) bool {
	for _, v := range list {
		if v.Equal(t) {
			return true
		}
	}
	return false
}

// findLot ищет лот по сроку годности в товаре (без блокировок — вызывает
// только под удерживаемой блокировкой кэша).
func findLot(p *stock.Product, bestBefore time.Time) *stock.Lot {
	for i := range p.Lots {
		if p.Lots[i].BestBefore.Equal(bestBefore) {
			return &p.Lots[i]
		}
	}

	return nil
}

// validateManualDiscount проверяет ручную скидку: nil — сброс, иначе 0..100.
func validateManualDiscount(label string, v *int16) error {
	if v == nil {
		return nil
	}
	if *v < 0 || *v > maxDiscount {
		return fmt.Errorf("%s должна быть в диапазоне 0..100", label)
	}

	return nil
}

// cloneInt16 копирует значение указателя (кэш не держит указатели из запросов).
func cloneInt16(v *int16) *int16 {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

// AcceptStock — адаптер модуля приёмки: принятые партии ДОБАВЛЯЮТСЯ к
// остаткам (qty += по существующему сроку, новый срок — новая строка),
// produced_on — COALESCE (известная дата не затирается). После записи —
// кэш и события lot_upsert (клиент «Сроков» обновляется в реальном времени).
func (uc *StockUseCase) AcceptStock(ctx context.Context, lots []stock.LotIn) error {
	done := metrics.Track(trackPkg, "AcceptStock")
	defer done()
	if err := validateAcceptLots(lots); err != nil {
		return err
	}

	if err := uc.repo.AcceptStockLots(ctx, lots); err != nil {
		return fmt.Errorf("accept stock lots: %w", err)
	}

	// Товары вне кэша (не было остатков) — подгружаем из каталога до
	// блокировки кэша; ошибка не откатывает запись в БД (как в applyReplacePlans).
	byID, err := uc.loadAcceptCatalog(ctx, lots)
	if err != nil {
		return err
	}

	uc.mu.Lock()
	events, err := uc.applyAcceptCacheLocked(lots, byID)
	uc.mu.Unlock()
	if err != nil {
		return err
	}

	if uc.pub != nil {
		for _, e := range events {
			uc.pub.PublishStockChange(e)
		}
	}
	ids := make([]string, 0, len(lots))
	for _, l := range lots {
		ids = append(ids, l.ProductID)
	}
	uc.notifyDayState(ctx, ids...)

	return nil
}

// validateAcceptLots проверяет партии приёмки до записи.
func validateAcceptLots(lots []stock.LotIn) error {
	if len(lots) == 0 {
		return errors.New("нет принятых позиций")
	}
	for _, l := range lots {
		if strings.TrimSpace(l.ProductID) == "" {
			return errors.New("не указан товар")
		}
		if l.Qty <= 0 {
			return fmt.Errorf("товар %s: количество должно быть больше нуля", l.ProductID)
		}
		if l.BestBefore.IsZero() {
			return fmt.Errorf("товар %s: не указан срок годности", l.ProductID)
		}
	}
	return nil
}

// loadAcceptCatalog подгружает из каталога товары, которых нет в кэше
// (не было остатков). Вызывается до блокировки кэша.
func (uc *StockUseCase) loadAcceptCatalog(ctx context.Context, lots []stock.LotIn) (map[string]stock.Product, error) {
	uc.mu.RLock()
	var missing []string
	for _, l := range lots {
		if _, ok := uc.cache[l.ProductID]; !ok {
			missing = append(missing, l.ProductID)
		}
	}
	uc.mu.RUnlock()

	byID := make(map[string]stock.Product, len(missing))
	for _, pid := range missing {
		p, err := uc.repo.LoadProductByID(ctx, pid)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", stock.ErrProductNotFound, pid)
		}
		byID[pid] = p
	}
	return byID, nil
}

// applyAcceptCacheLocked применяет приёмку к кэшу и собирает события.
// Вызывается только под mu.Lock.
func (uc *StockUseCase) applyAcceptCacheLocked(lots []stock.LotIn, byID map[string]stock.Product) ([]stock.Event, error) {
	events := make([]stock.Event, 0, len(lots))
	for _, l := range lots {
		cur, ok := uc.cache[l.ProductID]
		if !ok {
			cat, ok := byID[l.ProductID]
			if !ok {
				return nil, fmt.Errorf("%w: %s", stock.ErrProductNotFound, l.ProductID)
			}
			cp := cat
			cur = &cp
			uc.cache[l.ProductID] = cur
			if cur.InternalCode != "" {
				uc.byCode[cur.InternalCode] = l.ProductID
			}
		}

		idx := -1
		for j := range cur.Lots {
			if cur.Lots[j].BestBefore.Equal(l.BestBefore) {
				idx = j
				break
			}
		}
		if idx >= 0 {
			cur.Lots[idx].Qty += l.Qty
			if l.ProducedOn != nil && cur.Lots[idx].ProducedOn == nil {
				cur.Lots[idx].ProducedOn = l.ProducedOn
			}
		} else {
			cur.Lots = append(cur.Lots, stock.Lot{
				BestBefore: l.BestBefore,
				Qty:        l.Qty,
				ProducedOn: l.ProducedOn,
			})
			sort.Slice(cur.Lots, func(i, j int) bool { return cur.Lots[i].BestBefore.Before(cur.Lots[j].BestBefore) })
			idx = sort.Search(len(cur.Lots), func(j int) bool { return !cur.Lots[j].BestBefore.Before(l.BestBefore) })
		}
		lot := cur.Lots[idx]
		events = append(events, stock.Event{
			Kind:       stock.EventLotUpsert,
			ProductID:  l.ProductID,
			BestBefore: l.BestBefore,
			Lot:        &lot,
		})
	}
	return events, nil
}
