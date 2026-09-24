package discounts

import (
	"errors"
	"time"
)

// История ТГ-слотов (задачи 7–8 плана): типы строк discount_telegram_digest и
// discount_telegram_digest_item. Живут в доменном пакете, потому что ими же
// подписан интерфейс репозитория в usecase модуля, а репозиторий
// (internal/repository/postgres) только читает и пишет их (образец —
// daystate.DayState).
//
// Решение владельца 14.09.2026: в БД персистентна только история рассылок ТГ
// (реестр активных скидок живёт снапшотом в памяти), таблицы эпизодов и
// created_at/updated_at не заводим.

// Каналы рассылки — discount_telegram_digest.chat_kind. В БД это свободный
// текст без CHECK (каналы могут добавиться); согласованы два значения.
const (
	ChatWarehouse = "warehouse" // чат склада: план ТГ-слота 14:00
	ChatGeneral   = "general"   // общий чат: дайджест 09:00
)

// Причины скидки в истории — discount_telegram_digest_item.reason (CHECK в БД).
// Те же слова даёт Source.String(): одна причина и у строки истории, и у
// источника скидки в расчёте.
const (
	ReasonManual  = "manual"  // ручная скидка менеджера
	ReasonExpiry  = "expiry"  // лестница по сроку годности
	ReasonSurplus = "surplus" // избыток остатка к скорости продаж
)

// ErrBadDigestItem — позиция рассылки не пройдёт CHECK в БД: percent вне 0..100
// или неизвестная причина. Репозиторий отсекает такое до запроса: ошибка
// констрейнта из БД не говорит, какая именно строка его нарушила.
var ErrBadDigestItem = errors.New("discounts: неверная позиция рассылки")

// ErrNoDigest — отправленной рассылки в истории нет: поднимать general (16:00)
// не по чему. Штатно это значит, что план ТГ-слота не сформирован. Той же
// ошибкой отвечает отметка отправки (MarkDigestSent), если собранной рассылки
// за день и канал в истории нет.
var ErrNoDigest = errors.New("discounts: отправленной рассылки нет")

// ErrBadDayFlag — неизвестный маркер дня (discount_day_flags). Имя колонки
// подставляется в SQL, поэтому репозиторий берёт его только из своего белого
// списка: маркер не из списка — ошибка, а не запрос.
var ErrBadDayFlag = errors.New("discounts: неизвестный маркер дня")

// LotKey — ключ лота в истории рассылок: пара (товар, срок годности).
// Антидубль «не было в предыдущей рассылке» считается ПО ЛОТУ, а не по товару:
// у одного товара бывает два лота с разными сроками.
type LotKey struct {
	ProductID  string
	BestBefore time.Time
}

// DigestRecord — сохранённая рассылка (строка discount_telegram_digest).
type DigestRecord struct {
	PlannedAt time.Time  // день плана: локальная дата склада, для которой собирали слот
	ChatKind  string     // канал рассылки: ChatWarehouse / ChatGeneral
	SentAt    *time.Time // факт отправки; nil — собрано, но не отправлено
}

// SlotItem — позиция отправленного плана дня (14:00): скидка плана плюс
// контроль «не продано», по которому вечером (16:00) решается, поднимать ли
// скидку сайта. InitialQty/PlanQty заполнены только у позиции добора из
// избытка (см. DigestItem): пара продана, если остаток к 16:00 опустился до
// InitialQty−PlanQty или ниже. У сроковых позиций контроль — «пара не
// обнулилась», поэтому оба поля nil.
type SlotItem struct {
	LotKey
	Percent    int16
	Reason     string
	InitialQty *int64
	PlanQty    *int64
}

// DigestItem — позиция рассылки (лот + скидка + причина).
// Percent — скидка дня, %; Coeff — коэффициент избытка Q/(v×D) для источника
// «избыток» (nil — позиция не избыточная).
//
// InitialQty/PlanQty — контроль вечернего подъёма (16:00), решение владельца
// 24.09.2026. У позиции добора из избытка заранее считается, сколько штук надо
// продать, чтобы коэффициент стал < 1 у всех пар группы: InitialQty — остаток
// пары на момент плана (14:00), PlanQty — сколько с неё надо продать. Пара
// считается проданной, если её остаток к 16:00 опустился до InitialQty−PlanQty
// или ниже; у сроковых позиций план продаж не считается (nil) — там контроль
// проще: пара не должна обнулиться.
type DigestItem struct {
	ProductID  string
	BestBefore time.Time
	Percent    int16
	Coeff      *float64
	Reason     string
	InitialQty *int64
	PlanQty    *int64
}
