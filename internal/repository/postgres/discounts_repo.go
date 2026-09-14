package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"warehouseHelper/internal/discounts"

	"github.com/jackc/pgx/v5"
)

// discountInputColumns — колонки снапшота входа расчёта; порядок обязан
// совпадать с порядком Scan в scanDiscountInput.
//
// Последние пять колонок — текущие скидки лота: «простые» (plain — их пишет
// движок расчёта), ручные (manual — UI сроков) и метка источника plain-значения
// (discount_source). Расчёту они нужны, чтобы знать, что уже стоит в БД:
// general автоматика поднимает только вверх, ручная перекрывает plain, а по
// метке видно, что снимать при уходе пары из избытка (`0 = NULL`: NULL здесь —
// «скидка не задана»).
const discountInputColumns = `
    s.product_id, p.name, p.group_name, p.short_list, p.shelf_life, p.track_weekly,
    s.best_before, s.qty,
    s.discount_general, s.discount_telegram,
    s.discount_general_manual, s.discount_telegram_manual, s.discount_source`

// discountInputQuery — снапшот целиком: лоты стока с товарными признаками.
//
// Оборота здесь НЕТ намеренно: это данные модуля средних продаж, и расчёт
// получает их его методами (шов Turnover: RefreshCurrent для кандидатов,
// Averages для остальных) — читать чужие таблицы своим SQL модуль скидок не
// должен. От products берём только товарный признак периода (track_weekly →
// дни периода: недельный ряд 7, месячный 30), сам оборот — не отсюда.
const discountInputQuery = `
    SELECT ` + discountInputColumns + `
    FROM product_stock s
    JOIN products p ON p.id = s.product_id
    ORDER BY s.product_id, s.best_before`

// Дни периода оборота — Input.PeriodDays: недельный ряд 7 дней, месячный 30.
const (
	weeklyPeriodDays  = 7
	monthlyPeriodDays = 30
)

// digestPairColumns — колонки пары (товар, срок) позиций рассылки; порядок
// совпадает со scanLotPair. Префикс i — алиас discount_telegram_digest_item.
const digestPairColumns = `i.product_id, i.best_before`

// latestSentDigestSQL — подзапрос «последняя отправленная рассылка КАНАЛА»
// (по дню плана, затем по id — в один день бывает два слота: дайджест 09:00 в
// общий чат и план 14:00 в чат склада). $1 — канал (chat_kind).
//
// Канал обязателен: без него «последней рассылкой» для слота оказывался бы
// утренний дайджест общего чата — в нём все позиции со скидками, и антидубль
// вычистил бы слот целиком. Отправка важна: собранная, но не отправленная
// рассылка (sent_at IS NULL) историей публикации не является — позиции такого
// слота человек не видел.
const latestSentDigestSQL = `
    SELECT id FROM discount_telegram_digest
    WHERE sent_at IS NOT NULL AND chat_kind = $1
    ORDER BY planned_at DESC, id DESC
    LIMIT 1`

// LoadDiscountInput читает вход расчёта: все лоты product_stock с товарными
// признаками из products. Оборот в снапшот не входит — расчёт берёт его
// методами модуля средних продаж (шов Turnover).
//
// today — локальная дата склада (начало дня расчёта): шов её принимает (по ней
// расчёт обнуляет день и помечает сбой в журнале), но в самом запросе параметров
// больше нет — окон оборота в снапшоте не осталось.
//
// Фильтров по количеству нет: обнулённые лоты сток удаляет сам (DELETE в
// ReplaceStockLots), а что делать с нулём остатка, решает расчёт.
func (pg *PGClient) LoadDiscountInput(ctx context.Context, today time.Time) ([]discounts.Input, error) {
	rows, err := pg.Pool.Query(ctx, discountInputQuery)
	if err != nil {
		return nil, fmt.Errorf("load discount input %s: %w", today.Format(time.DateOnly), err)
	}
	defer rows.Close()

	inputs := make([]discounts.Input, 0, 256)
	for rows.Next() {
		in, err := scanDiscountInput(rows)
		if err != nil {
			return nil, fmt.Errorf("load discount input scan: %w", err)
		}
		inputs = append(inputs, in)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load discount input %s: %w", today.Format(time.DateOnly), err)
	}
	return inputs, nil
}

// scanDiscountInput сканирует строку снапшота в discounts.Input (порядок
// discountInputColumns). Дни периода оборота задаёт товарный признак
// track_weekly (7 или 30). Скидки лота переносятся как есть (NULL → nil, метка
// источника — строка): трактовку «0 = NULL» и приоритеты источников держит
// домен (discounts.Resolve), а не репозиторий.
//
// Две колонки снапшота nullable TEXT — products.group_name (товар без группы) и
// product_stock.discount_source (метки нет): читаются через *string и textValue.
// Прямой Scan в string падал на первой же строке без группы или без метки
// («can't scan into dest[12] (col: discount_source): cannot scan NULL into
// *string») — то есть на почти любой строке живого склада, и весь пересчёт
// скидок падал целиком.
func scanDiscountInput(row pgx.Row) (discounts.Input, error) {
	var (
		in             discounts.Input
		groupName      *string // NULL — товар без группы (products.group_name)
		discountSource *string // NULL — метки источника нет (product_stock.discount_source)
	)
	if err := row.Scan(
		&in.ProductID, &in.Name, &groupName, &in.ShortList, &in.ShelfLife, &in.TrackWeekly,
		&in.BestBefore, &in.Qty,
		&in.GeneralPlain, &in.TelegramPlain, &in.GeneralManual, &in.TelegramManual, &discountSource,
	); err != nil {
		return discounts.Input{}, fmt.Errorf("scan discount input: %w", err)
	}
	in.GroupName = textValue(groupName)
	in.DiscountSource = textValue(discountSource)

	in.PeriodDays = monthlyPeriodDays
	if in.TrackWeekly {
		in.PeriodDays = weeklyPeriodDays
	}
	return in, nil
}

// SaveDigest сохраняет рассылку вместе с позициями одной транзакцией: строку
// discount_telegram_digest (id присваивает БД) и строки её позиций. Пустой слот
// допустим — рассылка есть, позиций нет (публиковать нечего), отдельной ошибки
// на это нет.
//
// Позиции проверяются до вставки (checkDigestItems): CHECK-констрейнт из БД не
// говорит, какая строка его нарушила.
func (pg *PGClient) SaveDigest(ctx context.Context, d discounts.DigestRecord, items []discounts.DigestItem) error {
	if err := checkDigestItems(items); err != nil {
		return err
	}

	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("save digest begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // после Commit — no-op

	var digestID int64
	if err := tx.QueryRow(ctx, `
        INSERT INTO discount_telegram_digest (planned_at, chat_kind, sent_at)
        VALUES ($1, $2, $3)
        RETURNING id`,
		d.PlannedAt, d.ChatKind, d.SentAt,
	).Scan(&digestID); err != nil {
		return fmt.Errorf("insert digest %s %s: %w", d.PlannedAt.Format(time.DateOnly), d.ChatKind, err)
	}

	for _, it := range items {
		if _, err := tx.Exec(ctx, `
            INSERT INTO discount_telegram_digest_item
                (digest_id, product_id, best_before, percent, coeff, reason)
            VALUES ($1, $2, $3, $4, $5, $6)`,
			digestID, it.ProductID, it.BestBefore, it.Percent, it.Coeff, it.Reason,
		); err != nil {
			return fmt.Errorf("insert digest item %s %s: %w", it.ProductID, it.BestBefore.Format(time.DateOnly), err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("save digest %s %s commit: %w", d.PlannedAt.Format(time.DateOnly), d.ChatKind, err)
	}
	return nil
}

// checkDigestItems проверяет позиции против CHECK-констрейнтов БД: причина —
// одна из Reason* (слова совпадают с Source.String()), скидка — 0..100.
// Порядок позиций не важен; дубли лотов PK отсечёт сам, отдельной ошибки нет.
func checkDigestItems(items []discounts.DigestItem) error {
	for _, it := range items {
		switch it.Reason {
		case discounts.ReasonManual, discounts.ReasonExpiry, discounts.ReasonSurplus:
		default:
			return fmt.Errorf("%w: причина %q у лота %s %s",
				discounts.ErrBadDigestItem, it.Reason, it.ProductID, it.BestBefore.Format(time.DateOnly))
		}
		if it.Percent < 0 || it.Percent > 100 {
			return fmt.Errorf("%w: скидка %d%% у лота %s %s",
				discounts.ErrBadDigestItem, it.Percent, it.ProductID, it.BestBefore.Format(time.DateOnly))
		}
	}
	return nil
}

// LastDigestPairs читает пары (товар, срок) позиций последней отправленной
// рассылки — антидубль «не было в предыдущей рассылке» (по лоту, не по товару).
//
// Рассылки в истории нет — пустая карта, не ошибка: первый слот публикует всё.
func (pg *PGClient) LastDigestPairs(ctx context.Context) (map[discounts.LotKey]struct{}, error) {
	rows, err := pg.Pool.Query(ctx, `
        SELECT `+digestPairColumns+`
        FROM discount_telegram_digest_item i
        WHERE i.digest_id = (`+latestSentDigestSQL+`)`,
		discounts.ChatWarehouse,
	)
	if err != nil {
		return nil, fmt.Errorf("last digest pairs: %w", err)
	}
	defer rows.Close()

	pairs, err := collectLotPairs(rows)
	if err != nil {
		return nil, fmt.Errorf("last digest pairs: %w", err)
	}
	return pairs, nil
}

// collectLotPairs собирает пары (товар, срок) в набор: у лота в рассылке одна
// строка, дубликаты (если появятся) схлопываются. Выборку не закрывает — это
// делает вызывающий. Пустая выборка даёт пустую (не nil) карту.
func collectLotPairs(rows pgx.Rows) (map[discounts.LotKey]struct{}, error) {
	pairs := map[discounts.LotKey]struct{}{}
	for rows.Next() {
		key, err := scanLotPair(rows)
		if err != nil {
			return nil, err
		}
		pairs[key] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("digest pairs: %w", err)
	}
	return pairs, nil
}

// scanLotPair сканирует строку выборки в discounts.LotKey (порядок
// digestPairColumns).
func scanLotPair(row pgx.Row) (discounts.LotKey, error) {
	var key discounts.LotKey
	if err := row.Scan(&key.ProductID, &key.BestBefore); err != nil {
		return discounts.LotKey{}, fmt.Errorf("scan lot pair: %w", err)
	}
	return key, nil
}

// MarkGeneralRaised фиксирует подъём general до telegram (16:00) по позициям
// плана: general_raised_at = at у строк последней отправленной рассылки (плана
// 14:00). Уже поднятые строки не трогаются, распроданные до 16:00 в pairs просто
// не приходят — ноль обновлённых строк не ошибка.
//
// Отправленной рассылки в истории нет — discounts.ErrNoDigest (поднимать не по
// чему). Пустой список пар — не ошибка и запроса не делает.
func (pg *PGClient) MarkGeneralRaised(ctx context.Context, pairs []discounts.LotKey, at time.Time) error {
	if len(pairs) == 0 {
		return nil
	}

	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mark general raised begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // после Commit — no-op

	var digestID int64
	err = tx.QueryRow(ctx, latestSentDigestSQL, discounts.ChatWarehouse).Scan(&digestID)
	if errors.Is(err, pgx.ErrNoRows) {
		return discounts.ErrNoDigest
	}
	if err != nil {
		return fmt.Errorf("mark general raised digest: %w", err)
	}

	for _, p := range pairs {
		if _, err := tx.Exec(ctx, `
            UPDATE discount_telegram_digest_item
            SET general_raised_at = $4
            WHERE digest_id = $1 AND product_id = $2 AND best_before = $3
              AND general_raised_at IS NULL`,
			digestID, p.ProductID, p.BestBefore, at,
		); err != nil {
			return fmt.Errorf("mark general raised %s %s: %w", p.ProductID, p.BestBefore.Format(time.DateOnly), err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("mark general raised commit: %w", err)
	}
	return nil
}

// dayFlagColumns — белый список маркеров дня: маркер → колонка
// discount_day_flags. Имя колонки подставляется в SQL, поэтому берётся ТОЛЬКО
// из этой карты: маркер не из списка — ошибка, а не запрос.
var dayFlagColumns = map[discounts.DayFlag]string{
	discounts.FlagSurplus:        "surplus_done",
	discounts.FlagExpiry:         "expiry_done",
	discounts.FlagDigestSent:     "digest_sent",
	discounts.FlagPlan:           "tg_plan_done",
	discounts.FlagRaise:          "tg_raise_done",
	discounts.FlagTurnoverWindow: "turnover_window_done",
}

// dayFlagColumn — колонка маркера дня из белого списка; неизвестный маркер —
// discounts.ErrBadDayFlag (в SQL ничего не подставляется).
func dayFlagColumn(flag discounts.DayFlag) (string, error) {
	col, ok := dayFlagColumns[flag]
	if !ok {
		return "", fmt.Errorf("%w: %q", discounts.ErrBadDayFlag, string(flag))
	}
	return col, nil
}

// DayFlagDone — сделан ли шаг дня (маркеры «догона после сна»: после рестарта
// шаг дорабатывается). Строки дня в БД нет или флаг false → (false, nil):
// неотмеченный шаг нужно выполнить, это не ошибка.
func (pg *PGClient) DayFlagDone(ctx context.Context, date time.Time, flag discounts.DayFlag) (bool, error) {
	col, err := dayFlagColumn(flag)
	if err != nil {
		return false, err
	}

	var done bool
	err = pg.Pool.QueryRow(ctx,
		`SELECT `+col+` FROM discount_day_flags WHERE date = $1::date`, date,
	).Scan(&done)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("day flag %s %s: %w", date.Format(time.DateOnly), flag, err)
	}
	return done, nil
}

// MarkDayFlag — отметить шаг дня сделанным. Идемпотентно: строка дня создаётся
// при первой отметке, повторная отметка (в том числе после рестарта) значение
// не меняет — защита от повторной рассылки/повторного подъёма.
func (pg *PGClient) MarkDayFlag(ctx context.Context, date time.Time, flag discounts.DayFlag) error {
	col, err := dayFlagColumn(flag)
	if err != nil {
		return err
	}

	if _, err := pg.Pool.Exec(ctx, `
        INSERT INTO discount_day_flags (date, `+col+`)
        VALUES ($1::date, true)
        ON CONFLICT (date) DO UPDATE SET `+col+` = true`, date); err != nil {
		return fmt.Errorf("mark day flag %s %s: %w", date.Format(time.DateOnly), flag, err)
	}
	return nil
}

// todaySlotSQL — позиции последнего ОТПРАВЛЕННОГО слота дня (лот → скидка
// плана). Слотов в дне два — дайджест 09:00 в общий чат и план 14:00 в чат
// склада; слотом дня считаем последний по id в СВОЁМ канале (план 14:00): по
// нему держат эскалацию 10→20 до конца дня и по нему же поднимают general в
// 16:00. $1 — день плана, $2 — канал (chat_kind). Отправка важна: собранную,
// но не отправленную рассылку человек не видел.
const todaySlotSQL = `
    SELECT i.product_id, i.best_before, i.percent
    FROM discount_telegram_digest_item i
    WHERE i.digest_id = (
        SELECT id FROM discount_telegram_digest
        WHERE sent_at IS NOT NULL AND planned_at = $1::date AND chat_kind = $2
        ORDER BY id DESC
        LIMIT 1
    )`

// TodaySlot — позиции отправленного сегодня слота (лот → скидка плана).
// Пустой день (слота не было) — пустая (не nil) карта, не ошибка: первый день
// работы модуля и дни без публикации — обычное состояние.
func (pg *PGClient) TodaySlot(ctx context.Context, date time.Time) (map[discounts.LotKey]int16, error) {
	rows, err := pg.Pool.Query(ctx, todaySlotSQL, date, discounts.ChatWarehouse)
	if err != nil {
		return nil, fmt.Errorf("today slot %s: %w", date.Format(time.DateOnly), err)
	}
	defer rows.Close()

	slot, err := collectTodaySlot(rows)
	if err != nil {
		return nil, fmt.Errorf("today slot %s: %w", date.Format(time.DateOnly), err)
	}
	return slot, nil
}

// collectTodaySlot собирает позиции слота в карту «лот → скидка плана».
// Выборку не закрывает — это делает вызывающий. Пустая выборка даёт пустую
// (не nil) карту: дня без публикации — обычное состояние.
func collectTodaySlot(rows pgx.Rows) (map[discounts.LotKey]int16, error) {
	slot := map[discounts.LotKey]int16{}
	for rows.Next() {
		var (
			key     discounts.LotKey
			percent int16
		)
		if err := rows.Scan(&key.ProductID, &key.BestBefore, &percent); err != nil {
			return nil, fmt.Errorf("today slot scan: %w", err)
		}
		slot[key] = percent
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("today slot rows: %w", err)
	}
	return slot, nil
}

// MarkDigestSent фиксирует отправку рассылки: sent_at = at у строки дня плана и
// канала, ещё не отмеченной. id наружу не отдаём — история пишется один раз за
// день и ищется по каналу и дню плана; уже отмеченную рассылку вызов не трогает
// (повторная отправка после рестарта — не ошибка, а ничего не делающий вызов).
//
// Собранной рассылки за этот день и канал в истории нет — discounts.ErrNoDigest:
// отправлять нечего, отметка без рассылки молчать не должна.
func (pg *PGClient) MarkDigestSent(ctx context.Context, chatKind string, plannedAt, at time.Time) error {
	tag, err := pg.Pool.Exec(ctx, `
        UPDATE discount_telegram_digest
        SET sent_at = $3
        WHERE chat_kind = $1 AND planned_at = $2::date AND sent_at IS NULL`,
		chatKind, plannedAt, at)
	if err != nil {
		return fmt.Errorf("mark digest sent %s %s: %w", plannedAt.Format(time.DateOnly), chatKind, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s %s", discounts.ErrNoDigest, plannedAt.Format(time.DateOnly), chatKind)
	}
	return nil
}
