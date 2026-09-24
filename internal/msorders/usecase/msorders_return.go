// Возврат в сроки при переподборе позиций подобранного заказа: склад
// возвращает физически отложенные куски (резерв строки) с номерами сроков,
// пересчитанными по новым этикеткам. Сверка — по ЖИВЫМ данным заказа: заказ
// перечитывается из МС при каждом вызове (кэша и журнала у модуля нет),
// ожидания собираются по резерву строки, а правила сверки сканов берутся из
// общего ядра internal/scanmatch — те же, что на странице «Возврат в продажу»
// (строгий вес, построчное гашение, одинаковые тексты отказов оператору).
//
// Два выхода: SavePickReturn принимает вернувшиеся единицы в остатки
// (AcceptStock по сроку этикетки), ClosePickReturn — ручной оверрайд: в остатки
// ничего не пишет, а уведомляет чат склада о пересчёте сроков по незакрытым
// строкам (куски потеряны/списаны либо вес не совпал с резервом — «позиция
// слита» при подборе).
package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"warehouseHelper/internal/innercode"
	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/scanmatch"
)

// stubScanWeightG — вес-заглушка этикетки для штучной строки: сверка штучной
// идёт по количеству, вес этикетки в ней не участвует, но внутренний код
// куска не существует без веса (формат жёсткий).
const stubScanWeightG = 1

// Ошибки валидации возврата в сроки (400 на клиенте). ErrEmptyOrderID,
// ErrSubmitRowMissing, ErrSubmitBadBB и ErrSubmitBadWeight общие с отправкой
// подбора: тексты те же, клиент показывает их одной веткой.
var (
	ErrReturnEmptyRows   = errors.New("нет строк возврата — отсканируйте хотя бы одну позицию")
	ErrReturnNoScans     = errors.New("нет сканов в строке возврата")
	ErrReturnCodeMissing = errors.New("код позиции не найден в каталоге склада — возврат в сроки невозможен")
	ErrReturnCodeChanged = errors.New("код строки не совпал с заказом — обновите страницу")
	ErrReturnNoReserve   = errors.New("в строке нет резерва — возвращать нечего")
	ErrReturnOverReserve = errors.New("отсканировано больше единиц, чем в резерве строки")
)

// Ошибки подключения швов (500): приём остатков и уведомление склада — суть
// операции, молча их пропускать нельзя (в отличие от StockWarn при Submit).
var (
	errReturnNoStock  = errors.New("msorders: шов остатков не подключён")
	errReturnNoNotify = errors.New("msorders: уведомления складу не подключены")
)

// PickReturnRequest — тело POST /ms/orders/{id}/return и .../return/close:
// по строке на логическую строку заказа (как на странице подбора).
type PickReturnRequest struct {
	Rows []PickReturnRow `json:"rows"`
}

// PickReturnRow — одна строка возврата в сроки: ids — позиции МС строки
// (первая — «живая», остальные — смёрженные хвосты группы), code — внутренний
// код склада строки (подсказка клиента: сервер сверяет его с живым заказом и
// каталогом — страница могла устареть), weighted — тип учёта на момент
// отрисовки страницы (справочно: тип решает каталог), scans — отсканированные
// куски (w — вес в граммах, у штучных 0; bb — срок годности ДДММГГГГ).
type PickReturnRow struct {
	IDs      []string     `json:"ids"`
	Code     string       `json:"code"`
	Weighted bool         `json:"weighted"`
	Scans    []ScanRecord `json:"scans"`
}

// SavedReturn — сводка принятого возврата: сколько единиц записано в остатки и
// по каким строкам (для сообщения оператору; в остатках единицы склеиваются по
// (товар, срок этикетки) — в сводке они по строкам заказа).
type SavedReturn struct {
	Units int64      `json:"units"`
	Rows  []SavedRow `json:"rows"`
}

// SavedRow — строка сводки: код склада, название товара и число принятых
// единиц.
type SavedRow struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Units int64  `json:"units"`
}

// returnPlan — строка возврата, сверенная с живым заказом: ожидание ядра
// сверки (scanmatch) и записи сканов этой строки.
type returnPlan struct {
	expected scanmatch.Expected
	scans    []ScanRecord
}

// SavePickReturn сохраняет возврат в сроки: сверяет отсканированные куски с
// ожиданиями живого заказа (правила scanmatch: весовую строку гасит ровно один
// скан того же веса, штучную — ExpectedQty сканов, любое несоответствие —
// ValidationError) и принимает вернувшиеся единицы в остатки (AcceptStock:
// qty += по (товар, срок этикетки), каждая единица — один лот).
//
// Ожидания: весовая строка — резерв строки в граммах (QtyInt: строго, без
// допуска — вес куска обязан совпасть с резервом), штучная — N сканов того же
// кода, где 1 ≤ N ≤ round(резерв строки): вернуться может и часть строки.
// Несоответствие — 400, возврат не производится, выход один — ClosePickReturn.
func (uc *UseCase) SavePickReturn(ctx context.Context, id string, req PickReturnRequest) (SavedReturn, error) {
	done := metrics.Track(trackPkg, "SavePickReturn")
	defer done()

	id = strings.TrimSpace(id)
	if id == "" {
		return SavedReturn{}, ErrEmptyOrderID
	}
	if len(req.Rows) == 0 {
		return SavedReturn{}, ErrReturnEmptyRows
	}

	// Свежее чтение: резерв строки — источник истины на момент сканирования
	// (менеджер мог поправить заказ после отрисовки страницы).
	_, entry, err := uc.fetchForSubmit(ctx, id)
	if err != nil {
		return SavedReturn{}, err
	}

	plans, err := buildReturnPlans(req.Rows, entry)
	if err != nil {
		return SavedReturn{}, err
	}

	expected := make([]scanmatch.Expected, 0, len(plans))
	for i := range plans {
		expected = append(expected, plans[i].expected)
	}

	labels, err := returnScanLabels(plans)
	if err != nil {
		return SavedReturn{}, err
	}

	units, err := scanmatch.Match(labels, expected)
	if err != nil {
		return SavedReturn{}, err
	}

	if uc.acceptor == nil {
		return SavedReturn{}, fmt.Errorf("%w (заказ %s)", errReturnNoStock, id)
	}
	if err := uc.acceptor.AcceptStock(ctx, scanmatch.AggregateLots(units)); err != nil {
		return SavedReturn{}, fmt.Errorf("accept stock for order %s: %w", id, err)
	}

	slog.Info("msorders: возврат в сроки принят", "order", id, "units", len(units))

	// Вернувшиеся куски ушли в остатки — их даты убираем и из журнала заказа
	// (та же единица не может числиться и на складе, и в заказе). Ошибка журнала
	// не откатывает приём остатков: даты пересчитывает склад (текст — в лог).
	uc.removeJournalUnits(ctx, id, units)

	return savedReturn(units), nil
}

// ClosePickReturn закрывает возврат в сроки вручную (оверрайд): куски не
// вернулись — потеряны/списаны, либо ни один скан не гасит строку (вес не
// совпал с резервом — «позиция слита» при подборе). В остатки НЕ пишется
// (принимать нечего), в МС ничего не меняется: в чат склада уходит список
// незакрытых строк — склад пересчитывает по ним сроки построчно. Сканы строки
// нужны только для состава списка: авторитетной сверки здесь нет (мягкий
// разбор, как в ручном закрытии возврата в продажу).
func (uc *UseCase) ClosePickReturn(ctx context.Context, id string, req PickReturnRequest) error {
	done := metrics.Track(trackPkg, "ClosePickReturn")
	defer done()

	id = strings.TrimSpace(id)
	if id == "" {
		return ErrEmptyOrderID
	}
	if len(req.Rows) == 0 {
		return ErrReturnEmptyRows
	}

	_, entry, err := uc.fetchForSubmit(ctx, id)
	if err != nil {
		return err
	}

	unclosed := unclosedReturnRows(req.Rows, entry)

	// Ручное закрытие обходит обычный путь возврата: куски в остатки не пишутся,
	// поэтому их даты неизвестны — строки журнала по позициям запроса убираем
	// (пересчёт сроков идёт по уведомлению складу ниже).
	uc.clearJournalPositions(ctx, id, returnRowPositions(req.Rows))

	if len(unclosed) == 0 {
		return nil // все строки закрыты сканами — пересчитывать нечего
	}

	if uc.notify == nil {
		return fmt.Errorf("%w (заказ %s)", errReturnNoNotify, id)
	}
	if err := uc.notify.NotifyWarehouse(recountText(unclosed)); err != nil {
		return fmt.Errorf("notify warehouse about order %s: %w", id, err)
	}

	slog.Info("msorders: возврат в сроки закрыт вручную", "order", id, "rows", len(unclosed))

	return nil
}

// buildReturnPlans сверяет строки запроса с живым заказом и собирает ожидания
// для scanmatch. ids обязаны найтись в заказе (первая позиция — живая строка,
// остальные — смёрженные хвосты группы), код строки — резолвиться в каталоге
// склада (иначе 400: товар без кода склада в остатки не пишется). Резерв строки
// — сумма резервов позиций группы (как в Submit: хвосты группы несут свой
// резерв). Весовая строка ждёт ровно один скан своего веса (вес = резерв
// строки в граммах), штучная — N сканов (N = число сканов, 1 ≤ N ≤ резерв).
func buildReturnPlans(rows []PickReturnRow, entry *submitEntry) ([]returnPlan, error) {
	_, metaByID, err := orderRowMetas(entry)
	if err != nil {
		return nil, err
	}

	plans := make([]returnPlan, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		live, reserve, err := liveReturnRow(i, row, metaByID)
		if err != nil {
			return nil, err
		}

		product, err := returnRowProduct(i, row, live, entry.catalog)
		if err != nil {
			return nil, err
		}
		if err := validateReturnScans(i, product, row.Scans); err != nil {
			return nil, err
		}

		plan := returnPlan{
			scans: row.Scans,
			expected: scanmatch.Expected{
				Idx:          len(plans),
				ProductID:    product.ProductID,
				InternalCode: product.InternalCode,
				Name:         rowName(live),
				Weighted:     product.Weighted,
			},
		}

		if product.Weighted {
			// Весовая строка: один кусок с весом, равным резерву строки
			// (строго, без допуска) — второй скан того же веса отвергнется.
			grams := scanmatch.QtyInt(reserve, scanmatch.QtyGrams)
			if grams <= 0 {
				return nil, fmt.Errorf("строка %d (%s): %w", i+1, product.InternalCode, ErrReturnNoReserve)
			}
			plan.expected.ExpectedQty = grams
			plans = append(plans, plan)

			continue
		}

		// Штучная строка: возвращается столько единиц, сколько отсканировано,
		// но не больше резерва строки (вернуться может и часть строки).
		scanned := int64(len(row.Scans))
		if scanned == 0 {
			return nil, fmt.Errorf("строка %d (%s): %w", i+1, product.InternalCode, ErrReturnNoScans)
		}
		if limit := scanmatch.QtyInt(reserve, scanmatch.QtyPieces); scanned > limit {
			return nil, fmt.Errorf("строка %d (%s): %w: сканов %d, в резерве %d",
				i+1, product.InternalCode, ErrReturnOverReserve, scanned, limit)
		}
		plan.expected.ExpectedQty = scanned
		plans = append(plans, plan)
	}

	return plans, nil
}

// liveReturnRow находит живую позицию строки запроса и считает резерв всей
// строки (живая позиция + смёрженные хвосты группы). Пустые ids и позиции, не
// найденные в заказе, — 400.
func liveReturnRow(num int, row *PickReturnRow, metaByID map[string]*submitRowMeta) (*submitRowMeta, float64, error) {
	if len(row.IDs) == 0 {
		return nil, 0, fmt.Errorf("строка %d: %w", num+1, ErrSubmitBadRow)
	}

	liveID := strings.TrimSpace(row.IDs[0])
	live, ok := metaByID[liveID]
	if !ok {
		return nil, 0, fmt.Errorf("%w: %s", ErrSubmitRowMissing, liveID)
	}

	reserve := live.reserve
	for _, rawID := range row.IDs[1:] {
		id := strings.TrimSpace(rawID)
		tail, ok := metaByID[id]
		if !ok {
			return nil, 0, fmt.Errorf("%w: %s", ErrSubmitRowMissing, id)
		}
		reserve += tail.reserve
	}

	return live, reserve, nil
}

// returnRowProduct отдаёт товар каталога для строки возврата. Код строки —
// код живой позиции заказа (страница могла устареть: код из запроса — только
// подсказка, расхождение отклоняется); позиция без кода склада и код вне
// каталога — 400.
func returnRowProduct(num int, row *PickReturnRow, live *submitRowMeta, catalog map[string]CatalogProduct) (CatalogProduct, error) {
	code := live.code
	if !live.hasCode || code == "" {
		return CatalogProduct{}, fmt.Errorf("строка %d: %w: %q", num+1, ErrReturnCodeMissing, code)
	}
	if asked := strings.TrimSpace(row.Code); asked != "" && asked != code {
		return CatalogProduct{}, fmt.Errorf("строка %d: %w: %q, в заказе %q", num+1, ErrReturnCodeChanged, asked, code)
	}

	product, ok := catalog[code]
	if !ok || product.InternalCode == "" {
		return CatalogProduct{}, fmt.Errorf("строка %d: %w: %q", num+1, ErrReturnCodeMissing, code)
	}

	return product, nil
}

// validateReturnScans проверяет записи сканов строки до сверки: дата срока
// ДДММГГГГ и вес 0..99999 г, у весового товара — строго больше нуля (куска без
// веса не существует, сверка идёт по весу). Тексты ошибок общие с Submit.
func validateReturnScans(num int, product CatalogProduct, scans []ScanRecord) error {
	for _, rec := range scans {
		if rec.WeightG < 0 || rec.WeightG > maxScanWeightG || (product.Weighted && rec.WeightG == 0) {
			return fmt.Errorf("строка %d: %w: %d", num+1, ErrSubmitBadWeight, rec.WeightG)
		}
		if _, err := parseBB(rec.BB); err != nil {
			return fmt.Errorf("строка %d: %w: %q", num+1, ErrSubmitBadBB, rec.BB)
		}
	}

	return nil
}

// returnScanLabels собирает этикетки кусков (29 цифр) из записей запроса:
// клиент присылает уже разобранные вес и срок (та же форма, что в Submit), а
// правила сверки scanmatch работают с этикеткой целиком — кодируем её обратно
// каноническим кодировщиком innercode (код проходит те же проверки, что
// отсканированная этикетка). Порядок сканов сохраняется.
func returnScanLabels(plans []returnPlan) ([]string, error) {
	labels := make([]string, 0, len(plans))
	for i := range plans {
		for _, rec := range plans[i].scans {
			label, err := returnScanLabel(&plans[i].expected, rec)
			if err != nil {
				return nil, err
			}
			labels = append(labels, label)
		}
	}

	return labels, nil
}

// returnScanLabel кодирует одну запись скана в этикетку куска: у весовой строки
// берётся вес записи, у штучной — вес-заглушка (сверка по количеству). В
// этикетке — срок годности записи и её выработка; выработку клиент присылает
// отдельным срезом кода (PD), а если не прислал (старая версия страницы) —
// выработкой идёт срок годности, как было до появления PD: лоту остатков нужен
// только срок (produced_on уходит в COALESCE и известную дату не затирает).
// Ошибка кодирования возможна только на коде склада, не проходящем формат
// внутреннего кода (8 цифр) — это 400: код позиции не складской.
func returnScanLabel(expected *scanmatch.Expected, rec ScanRecord) (string, error) {
	weightG := int64(rec.WeightG)
	if !expected.Weighted {
		weightG = stubScanWeightG
	}

	// Даты и вес проверены validateReturnScans — ошибки здесь невозможны.
	bb, err := parseBB(rec.BB)
	if err != nil {
		return "", fmt.Errorf("строка %d: %w: %q", expected.Idx+1, ErrSubmitBadBB, rec.BB)
	}
	produced := bb
	if pd := parsePD(rec.PD); pd != nil {
		produced = *pd
	}
	label, err := innercode.EncodeItem(expected.InternalCode, weightG, produced, bb)
	if err != nil {
		return "", fmt.Errorf("строка %d: %w: %q: %w", expected.Idx+1, ErrReturnCodeMissing, expected.InternalCode, err)
	}

	return label, nil
}

// unclosedReturnRows — строки запроса, которые не гасятся присланными сканами
// (для уведомления о пересчёте сроков): мягкий разбор без отказов — строки, не
// нашедшиеся в заказе, позиции без резерва и чужие сканы закрытие не блокируют,
// а ошибка разбора строк заказа просто оставляет список пустым. Ожидание строки
// — её резерв (весовой — граммы, штучный — штуки): строку гасит скан того же
// кода (правила scanmatch).
func unclosedReturnRows(rows []PickReturnRow, entry *submitEntry) []scanmatch.Expected {
	_, metaByID, err := orderRowMetas(entry)
	if err != nil {
		slog.Warn("msorders: строки заказа недоступны для пересчёта", "err", err)
		return nil
	}

	expected := make([]scanmatch.Expected, 0, len(rows))
	codes := make([]string, len(rows))
	for i := range rows {
		live, reserve, err := liveReturnRow(i, &rows[i], metaByID)
		if err != nil {
			slog.Warn("msorders: строка пересчёта не найдена в заказе", "err", err)
			continue
		}

		unit := scanmatch.QtyPieces
		if live.weighted {
			unit = scanmatch.QtyGrams
		}
		qty := scanmatch.QtyInt(reserve, unit)
		if qty <= 0 {
			continue // резерва нет — пересчитывать нечего
		}

		codes[i] = live.code
		expected = append(expected, scanmatch.Expected{
			Idx:          len(expected),
			ProductID:    live.productID,
			InternalCode: live.code,
			Name:         rowName(live),
			Weighted:     live.weighted,
			ExpectedQty:  qty,
		})
	}

	progress := make([]int64, len(expected))
	for i := range rows {
		if codes[i] == "" {
			continue
		}
		for _, rec := range rows[i].Scans {
			idx := scanmatch.PickRow(expected, progress, codes[i], int64(rec.WeightG))
			if idx < 0 {
				continue // чужой скан — в мягком разборе игнор
			}
			if expected[idx].Weighted {
				progress[idx] = expected[idx].ExpectedQty
				continue
			}
			progress[idx]++
		}
	}

	out := make([]scanmatch.Expected, 0, len(expected))
	for i := range expected {
		if progress[i] != expected[i].ExpectedQty {
			out = append(out, expected[i])
		}
	}

	return out
}

// recountText — текст уведомления складу о пересчёте сроков: позиции в порядке
// отчёта, дубли названий сводятся в «×N строк» (пересчитывать надо построчно).
// Формулировки те же, что в ручном закрытии возврата в продажу
// (internal/returns: recountText не экспортируется, поэтому текст повторён
// дословно — оператор и склад видят одно и то же сообщение).
func recountText(rows []scanmatch.Expected) string {
	type group struct {
		name string
		code string
		n    int
	}
	groups := make([]group, 0, len(rows))
	byKey := make(map[string]int, len(rows))
	for _, r := range rows {
		key := r.Name + "\x00" + r.InternalCode
		if i, ok := byKey[key]; ok {
			groups[i].n++
			continue
		}
		byKey[key] = len(groups)
		groups = append(groups, group{name: r.Name, code: r.InternalCode, n: 1})
	}

	var sb strings.Builder
	sb.WriteString("Необходимо пересчитать сроки по позициям:")
	for _, g := range groups {
		sb.WriteString("\n— ")
		sb.WriteString(g.name)
		if g.code != "" {
			sb.WriteString(" (")
			sb.WriteString(g.code)
			sb.WriteByte(')')
		}
		if g.n > 1 {
			fmt.Fprintf(&sb, " ×%d строк", g.n)
		}
	}
	sb.WriteString("\nпострочно")

	return sb.String()
}

// savedReturn собирает сводку принятого возврата: единицы по строкам запроса в
// порядке сканов (склейка (товар, срок) — это остатки, сводка показывает, что
// вернулось по строкам).
func savedReturn(units []scanmatch.ScannedUnit) SavedReturn {
	byIdx := make(map[int]SavedRow, len(units))
	order := make([]int, 0, len(units))
	for _, u := range units {
		row, ok := byIdx[u.Row.Idx]
		if !ok {
			row = SavedRow{Code: u.Row.InternalCode, Name: u.Row.Name}
			order = append(order, u.Row.Idx)
		}
		row.Units++
		byIdx[u.Row.Idx] = row
	}

	rows := make([]SavedRow, 0, len(order))
	for _, idx := range order {
		rows = append(rows, byIdx[idx])
	}

	return SavedReturn{Units: int64(len(units)), Rows: rows}
}

// rowName — название строки для текстов сверки: имя позиции заказа, при пустом
// — код склада (в отказе оператору лучше код, чем пустое место).
func rowName(meta *submitRowMeta) string {
	if name := strings.TrimSpace(meta.name); name != "" {
		return name
	}

	return meta.code
}
