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
// не по чему. Штатно это значит, что план ТГ-слота не сформирован.
var ErrNoDigest = errors.New("discounts: отправленной рассылки нет")

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

// DigestItem — позиция рассылки (лот + скидка + причина).
// Percent — скидка дня, %; Coeff — коэффициент избытка Q/(v×D) для источника
// «избыток» (nil — позиция не избыточная).
type DigestItem struct {
	ProductID  string
	BestBefore time.Time
	Percent    int16
	Coeff      *float64
	Reason     string
}
