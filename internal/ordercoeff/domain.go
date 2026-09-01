// Пакет ordercoeff — модуль «Коэффициент изменения заказа».
//
// Владеет таблицей product_period_coeff: накопленный коэффициент по периодам
// (неделя/месяц) для коррекции динамики объёма заказов. События шлют модули-
// владельцы фактов (сток, каталог, скидки, расформирование заказов) через
// публичные методы usecase; коэффициент — чистая функция от последовательности
// событий (правило ApplyEvent), хранится «календарём»: одна строка на
// событийный период, значение переносится между соседними событийными
// периодами и обнуляется у предыдущего.
//
// Правила (согласовано с владельцем):
//   - скидка −1, «закончился» +1, заморозка −2, недоступен 0 (держит цепочку);
//   - повтор события = повторное наложение того же значения;
//   - откаты (возврат/отмена скидки) — событие ТЕКУЩЕГО периода: снимают 1
//     с coeff и со счётчика своего типа, только если в текущем периоде есть
//     живое событие этого типа; иначе no-op («возвращать нечего»);
//   - чистая неделя (без событий) рвёт цепочку: накопленное остаётся на
//     последней событийной неделе и перестаёт учитываться, когда та выходит
//     из окна расчёта (окно задаёт потребитель, хранение неограниченное).
package ordercoeff

import "time"

// PeriodType — гранулярность отрезка отслеживания товара.
type PeriodType int16

const (
	PeriodWeek  PeriodType = 1 // неделя (понедельник — начало)
	PeriodMonth PeriodType = 2 // месяц (1-е число — начало)
)

// EventType — событие, влияющее на коэффициент.
type EventType int16

const (
	EventSoldOut          EventType = 1 // товар закончился: +1
	EventDiscount         EventType = 2 // попадание в скидки: −1
	EventFrozen           EventType = 3 // товар был заморожен: −2
	EventUnavailable      EventType = 4 // недоступен для заказа: 0, держит цепочку
	EventRollbackSoldOut  EventType = 5 // возврат после «закончился»: −1 к coeff, требует живого +1
	EventRollbackDiscount EventType = 6 // отмена скидки: +1 к coeff, требует живой −1
)

// PeriodCoeff — состояние коэффициента периода (одна строка «календаря»).
// Строка существует только для событийного периода; чистые периоды строк
// не имеют. Coeff — накопленное значение цепочки, «сидящее» на этом периоде;
// счётчики — живые события периода (факты, нужны для предусловий откатов
// и пересчёта coeff).
type PeriodCoeff struct {
	ProductID   string
	PeriodType  PeriodType
	PeriodStart time.Time
	Coeff       int16
	SoldOut     int16
	Discount    int16
	Frozen      int16
	Unavailable int16
}

// transfer возвращает строку текущего периода (создаёт при первом событии)
// и предыдущий период с обнулённым Coeff, если произошёл перенос.
func transfer(cur, prev *PeriodCoeff) (newCur, newPrev *PeriodCoeff) {
	if cur != nil {
		return cur, nil
	}
	cur = &PeriodCoeff{}
	if prev == nil {
		return cur, nil
	}
	cur.Coeff = prev.Coeff
	prev.Coeff = 0
	return cur, prev
}

// ApplyEvent — чистое правило цепочки: применяет событие к текущему периоду.
//
// cur — состояние текущего периода (nil = период чистый, строки нет);
// prev — состояние предыдущего периода (nil = предыдущий период чистый).
// Возвращает:
//   - newCur — новое состояние текущего периода (при cur == nil создаётся
//     новая строка, PK заполняет хранилище);
//   - newPrev — предыдущий период с обнулённым Coeff, если произошёл перенос
//     (иначе nil);
//   - applied — false только для отката, которому нечего отменять (живого
//     события нужного типа в текущем периоде нет): ничего писать не нужно.
func ApplyEvent(cur, prev *PeriodCoeff, ev EventType) (newCur, newPrev *PeriodCoeff, applied bool) {
	switch ev {
	case EventSoldOut:
		newCur, newPrev = transfer(cur, prev)
		newCur.SoldOut++
		newCur.Coeff++
		return newCur, newPrev, true

	case EventDiscount:
		newCur, newPrev = transfer(cur, prev)
		newCur.Discount++
		newCur.Coeff--
		return newCur, newPrev, true

	case EventFrozen:
		newCur, newPrev = transfer(cur, prev)
		newCur.Frozen++
		newCur.Coeff -= 2
		return newCur, newPrev, true

	case EventUnavailable:
		newCur, newPrev = transfer(cur, prev)
		newCur.Unavailable++
		return newCur, newPrev, true

	case EventRollbackSoldOut:
		if cur == nil || cur.SoldOut == 0 {
			return cur, nil, false
		}
		cur.SoldOut--
		cur.Coeff--
		return cur, nil, true

	case EventRollbackDiscount:
		if cur == nil || cur.Discount == 0 {
			return cur, nil, false
		}
		cur.Discount--
		cur.Coeff++
		return cur, nil, true

	default:
		return cur, nil, false
	}
}
