// Хендлеры страницы «Варианты заказа» карточки позиции: расписание заказа
// живёт на паре (товар, поставщик) — у пары может быть несколько вариантов
// (например «Общий» — со склада поставщика на следующий день и «Под заказ
// с производства» — через неделю). Автозаказ читает эти варианты из товара,
// а не из поставщика.
package http

import (
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"log/slog"
	"warehouseHelper/internal/domain"
)

// DayCheckbox — чекбокс дня недели в форме варианта заказа.
type DayCheckbox struct {
	Day     int16 // 1..7
	Label   string
	Checked bool
}

// dayLabels — подписи дней недели, 1=Пн ... 7=Вс.
var dayLabels = map[int16]string{1: "Пн", 2: "Вт", 3: "Ср", 4: "Чт", 5: "Пт", 6: "Сб", 7: "Вс"}

func buildDayCheckboxes(days []int16) []DayCheckbox {
	selected := make(map[int16]bool, len(days))
	for _, d := range days {
		selected[d] = true
	}

	out := make([]DayCheckbox, 0, 7)
	for d := int16(1); d <= 7; d++ {
		out = append(out, DayCheckbox{
			Day:     d,
			Label:   dayLabels[d],
			Checked: selected[d],
		})
	}

	return out
}

// parseDays разбирает чекбоксы дней недели (значения "1".."7") в []int16.
// Диапазон и дубли проверяет usecase каталога.
func parseDays(vals []string) ([]int16, error) {
	days := make([]int16, 0, len(vals))
	for _, v := range vals {
		d, err := strconv.ParseInt(v, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("день %q — не число", v)
		}
		days = append(days, int16(d))
	}

	return days, nil
}

// parseNullableInt16 разбирает число или nil для пустой строки (задержка).
func parseNullableInt16(v string) (*int16, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		//nolint:nilnil // контракт: пустая строка = отсутствующее значение
		return nil, nil
	}
	n, err := strconv.ParseInt(v, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("%q — не число", v)
	}
	d := int16(n)

	return &d, nil
}

// parseCutoffTime разбирает время дедлайна заказа из формы ("HH:MM").
// Пустая строка — nil (не задано).
func parseCutoffTime(v string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		//nolint:nilnil // контракт: пустая строка = отсутствующее значение
		return nil, nil
	}
	t, err := time.Parse("15:04", v)
	if err != nil {
		return nil, fmt.Errorf("время дедлайна %q — ожидается формат ЧЧ:ММ, например 14:00", v)
	}
	// Нормализуем к фиксированной дате — в БД хранится только время (TIME).
	norm := time.Date(2000, time.January, 1, t.Hour(), t.Minute(), 0, 0, time.UTC)

	return &norm, nil
}

// goodsSchedulesTmpl — шаблон страницы вариантов заказа.
var goodsSchedulesTmpl = template.Must(template.ParseFiles(
	"../internal/delivery/web/templates/goods_schedules.html",
	"../internal/delivery/web/templates/_nav.html",
))

// ScheduleRow — строка формы «Варианты заказа» (значения подготовлены для показа).
type ScheduleRow struct {
	UID            int64  // идентификатор строки в форме (имена полей *_<uid>)
	ScheduleID     int64  // id варианта в БД; 0 — новый (вставка)
	SupplierID     string // выбранный поставщик
	Name           string // название варианта
	OrderDays      []DayCheckbox
	DeliveryDays   []DayCheckbox
	DelayDaysText  string // "" — не задано
	CutoffTimeText string // "HH:MM" или пусто
}

// GoodsSchedulesData — данные страницы вариантов заказа позиции.
type GoodsSchedulesData struct {
	Product   *domain.Product
	Suppliers []domain.Supplier
	Rows      []ScheduleRow
	OK        bool
	Error     string
}

// GoodsSchedulesPage — GET /goods/schedules?id=...: варианты заказа товара.
func (h *Handler) GoodsSchedulesPage(w http.ResponseWriter, r *http.Request) {
	productID := r.URL.Query().Get("id")

	product, err := h.goodsUC.GetProduct(r.Context(), productID)
	if err != nil {
		slog.Info("goods schedules: get product", "id", productID, "err", err)
		http.Error(w, "не удалось загрузить товар", http.StatusInternalServerError)

		return
	}
	if product == nil {
		http.Redirect(w, r, "/goods", http.StatusSeeOther)

		return
	}

	list, suppliers, err := h.goodsUC.ListOrderSchedules(r.Context(), productID)
	if err != nil {
		slog.Info("goods schedules: list", "id", productID, "err", err)
		h.renderGoodsSchedules(w, &GoodsSchedulesData{Product: product, Error: "не удалось загрузить варианты заказа"})

		return
	}

	d := &GoodsSchedulesData{
		Product:   product,
		Suppliers: suppliers,
		OK:        r.URL.Query().Get("ok") != "",
	}
	for i, item := range list {
		d.Rows = append(d.Rows, scheduleRowFromDomain(int64(i+1), item))
	}
	if len(d.Rows) == 0 {
		d.Rows = append(d.Rows, ScheduleRow{
			UID:          1,
			OrderDays:    buildDayCheckboxes(nil),
			DeliveryDays: buildDayCheckboxes(nil),
		})
	}

	h.renderGoodsSchedules(w, d)
}

// GoodsSchedulesSave — POST /goods/schedules/save: замена набора вариантов
// заказа товара (список сохраняется целиком, удалённые строки исчезают).
func (h *Handler) GoodsSchedulesSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "не удалось разобрать форму", http.StatusBadRequest)

		return
	}
	productID := r.FormValue("id")

	product, err := h.goodsUC.GetProduct(r.Context(), productID)
	if err != nil || product == nil {
		http.Redirect(w, r, "/goods", http.StatusSeeOther)

		return
	}

	list, rows, err := parseScheduleForm(r)
	if err == nil {
		err = h.goodsUC.SaveOrderSchedules(r.Context(), productID, list)
	}
	if err != nil {
		_, suppliers, lerr := h.goodsUC.ListOrderSchedules(r.Context(), productID)
		if lerr != nil {
			slog.Info("goods schedules: list after error", "id", productID, "err", lerr)
		}
		h.renderGoodsSchedules(w, &GoodsSchedulesData{
			Product:   product,
			Suppliers: suppliers,
			Rows:      rows,
			Error:     err.Error(),
		})

		return
	}

	http.Redirect(w, r, "/goods/schedules?id="+url.QueryEscape(productID)+"&ok=1", http.StatusSeeOther)
}

// scheduleRowFromDomain готовит строку формы из варианта заказа.
func scheduleRowFromDomain(uid int64, item domain.OrderSchedule) ScheduleRow {
	row := ScheduleRow{
		UID:          uid,
		ScheduleID:   item.ID,
		SupplierID:   item.SupplierID,
		Name:         item.Name,
		OrderDays:    buildDayCheckboxes(item.OrderDays),
		DeliveryDays: buildDayCheckboxes(item.DeliveryDays),
	}
	if item.DelayDays != nil {
		row.DelayDaysText = strconv.FormatInt(int64(*item.DelayDays), 10)
	}
	if item.OrderCutoffTime != nil {
		row.CutoffTimeText = item.OrderCutoffTime.Format("15:04")
	}

	return row
}

// parseScheduleForm разбирает строки формы в варианты заказа и в строки для
// повторного показа. Пустые строки (добавлены кнопкой, но не заполнены)
// пропускаются. Ошибка разбора — текст для формы; частично разобранные
// строки при этом возвращаются, чтобы введённое не потерялось.
func parseScheduleForm(r *http.Request) ([]domain.OrderSchedule, []ScheduleRow, error) {
	uids := r.Form["uid"]
	list := make([]domain.OrderSchedule, 0, len(uids))
	rows := make([]ScheduleRow, 0, len(uids))

	for i, raw := range uids {
		uid, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			uid = int64(i + 1)
		}

		orderDays, err := parseDays(r.Form[fmt.Sprintf("order_days_%d", uid)])
		if err != nil {
			return nil, rows, fmt.Errorf("вариант %d: дни заказа: %w", i+1, err)
		}
		deliveryDays, err := parseDays(r.Form[fmt.Sprintf("delivery_days_%d", uid)])
		if err != nil {
			return nil, rows, fmt.Errorf("вариант %d: дни доставки: %w", i+1, err)
		}
		delay, err := parseNullableInt16(r.FormValue(fmt.Sprintf("delay_%d", uid)))
		if err != nil {
			return nil, rows, fmt.Errorf("вариант %d: макс. дней: %w", i+1, err)
		}
		cutoff, err := parseCutoffTime(r.FormValue(fmt.Sprintf("cutoff_%d", uid)))
		if err != nil {
			return nil, rows, fmt.Errorf("вариант %d: %w", i+1, err)
		}

		supplierID := strings.TrimSpace(r.FormValue(fmt.Sprintf("supplier_%d", uid)))
		name := strings.TrimSpace(r.FormValue(fmt.Sprintf("name_%d", uid)))
		var scheduleID int64
		if raw := strings.TrimSpace(r.FormValue(fmt.Sprintf("sid_%d", uid))); raw != "" {
			if parsed, perr := strconv.ParseInt(raw, 10, 64); perr == nil && parsed > 0 {
				scheduleID = parsed
			}
		}

		// Полностью пустая строка — не вариант, а «добавь ещё» без заполнения.
		if supplierID == "" && name == "" && len(orderDays) == 0 && len(deliveryDays) == 0 && delay == nil && cutoff == nil {
			continue
		}

		list = append(list, domain.OrderSchedule{
			ID:              scheduleID,
			ProductID:       r.FormValue("id"),
			SupplierID:      supplierID,
			Name:            name,
			OrderDays:       orderDays,
			DeliveryDays:    deliveryDays,
			DelayDays:       delay,
			OrderCutoffTime: cutoff,
		})

		row := ScheduleRow{
			UID:            uid,
			ScheduleID:     scheduleID,
			SupplierID:     supplierID,
			Name:           name,
			OrderDays:      buildDayCheckboxes(orderDays),
			DeliveryDays:   buildDayCheckboxes(deliveryDays),
			DelayDaysText:  r.FormValue(fmt.Sprintf("delay_%d", uid)),
			CutoffTimeText: r.FormValue(fmt.Sprintf("cutoff_%d", uid)),
		}
		rows = append(rows, row)
	}

	if len(rows) == 0 {
		rows = append(rows, ScheduleRow{UID: 1, OrderDays: buildDayCheckboxes(nil), DeliveryDays: buildDayCheckboxes(nil)})
	}

	return list, rows, nil
}

// renderGoodsSchedules отдаёт страницу вариантов заказа (поле Options — только
// для того, чтобы шаблон не падал на пустом списке поставщиков).
func (h *Handler) renderGoodsSchedules(w http.ResponseWriter, d *GoodsSchedulesData) {
	if err := goodsSchedulesTmpl.Execute(w, d); err != nil {
		slog.Info(fmt.Sprintf("goods_schedules template: %v", err))
	}
}
