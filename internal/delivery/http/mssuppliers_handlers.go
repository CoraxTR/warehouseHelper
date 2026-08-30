// Пакет http — хендлеры веб-слоя модуля «МойСклад»: хаб и справочник
// поставщиков (список, создание, редактирование, удаление).
package http

import (
	"errors"
	"fmt"
	"html/template"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"warehouseHelper/internal/domain"
	msu "warehouseHelper/internal/mssuppliers/usecase"
	"warehouseHelper/internal/receiving"
)

// SuppliersListData — данные страницы списка поставщиков.
type SuppliersListData struct {
	Suppliers []domain.Supplier
	Error     string
}

// DayCheckbox — чекбокс дня недели в форме поставщика.
type DayCheckbox struct {
	Day     int16 // 1..7
	Label   string
	Checked bool
}

// SupplierFormData — данные формы создания/редактирования поставщика.
// Текстовые поля подготовлены для показа (не копейки/массивы, а строки),
// чтобы шаблон оставался глупым.
type SupplierFormData struct {
	Supplier *domain.Supplier
	IsEdit   bool
	RawID    string // создание: исходная ссылка пользователя (uuid извлекается при сохранении)

	DecodeRulesText      string
	BoxDecodeRulesText   string
	OrderDays            []DayCheckbox
	DeliveryDays         []DayCheckbox
	SpecialOrderDays     []DayCheckbox
	SpecialDeliveryDays  []DayCheckbox
	DelayDaysText        string
	SpecialDelayDaysText string
	MinOrderAmountText   string

	Barcodes []receiving.BarcodeRef // виджет «Внешние коды» (приёмка)

	Error string
}

// Шаблоны модуля «МойСклад», парсятся один раз при старте.
var (
	msIndexTmpl         = template.Must(template.ParseFiles("../internal/delivery/web/templates/ms_index.html"))
	msSuppliersListTmpl = template.Must(template.ParseFiles("../internal/delivery/web/templates/ms_suppliers_list.html"))
	msSupplierFormTmpl  = template.Must(template.ParseFiles("../internal/delivery/web/templates/ms_suppliers_form.html"))
)

// MsPage — GET /ms: хаб модуля «МойСклад» (кнопки подмодулей).
func (h *Handler) MsPage(w http.ResponseWriter, _ *http.Request) {
	if err := msIndexTmpl.Execute(w, nil); err != nil {
		log.Printf("ms_index template: %v", err)
	}
}

// SuppliersList — GET /ms/suppliers: список поставщиков по алфавиту.
func (h *Handler) SuppliersList(w http.ResponseWriter, r *http.Request) {
	d := &SuppliersListData{}
	if err := r.URL.Query().Get("err"); err != "" {
		d.Error = err
	}

	suppliers, err := h.msUC.List(r.Context())
	if err != nil {
		log.Printf("list suppliers: %v", err)
		d.Error = "не удалось загрузить список поставщиков"
	} else {
		d.Suppliers = suppliers
	}

	if err := msSuppliersListTmpl.Execute(w, d); err != nil {
		log.Printf("ms_suppliers_list template: %v", err)
	}
}

// SupplierNew — GET /ms/suppliers/new: пустая форма создания.
func (h *Handler) SupplierNew(w http.ResponseWriter, _ *http.Request) {
	h.renderSupplierForm(w, buildSupplierFormData(nil, false, "", ""))
}

// SupplierEdit — GET /ms/suppliers/edit?id=...: форма редактирования.
func (h *Handler) SupplierEdit(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	s, err := h.msUC.Get(r.Context(), id)
	if err != nil {
		log.Printf("get supplier %s: %v", id, err)
		h.renderSupplierForm(w, buildSupplierFormData(nil, true, "", "не удалось загрузить поставщика"))
		return
	}
	if s == nil {
		http.Redirect(w, r, "/ms/suppliers", http.StatusSeeOther)
		return
	}

	d := buildSupplierFormData(s, true, "", "")
	d.Barcodes = h.loadSupplierBarcodes(r, s.ID)

	h.renderSupplierForm(w, d)
}

// loadSupplierBarcodes читает связки «внешний код → товар» для виджета;
// ошибка чтения не ломает форму поставщика (вторичные данные), в лог — да.
func (h *Handler) loadSupplierBarcodes(r *http.Request, supplierID string) []receiving.BarcodeRef {
	barcodes, err := h.msUC.ListBarcodes(r.Context(), supplierID)
	if err != nil {
		log.Printf("list supplier barcodes %s: %v", supplierID, err)

		return nil
	}

	return barcodes
}

// SupplierSave — POST /ms/suppliers/save: создание или обновление.
// Имя контрагента запрашивается из МС до записи в БД; при ошибке (сеть,
// WAF, несуществующий id) форма остаётся с заполненными данными и сообщением.
func (h *Handler) SupplierSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "не удалось разобрать форму", http.StatusBadRequest)
		return
	}

	mode := r.FormValue("mode")
	isEdit := mode == "edit"
	rawID := r.FormValue("id")

	id, err := msu.ExtractCounterpartyID(rawID)
	if err != nil {
		h.renderSupplierForm(w, buildSupplierFormData(nil, isEdit, rawID, err.Error()))
		return
	}

	s := &domain.Supplier{ID: id}
	if isEdit {
		// Текущее имя — только для показа в форме; при сохранении перезапросится из МС.
		existing, err := h.msUC.Get(r.Context(), id)
		if err != nil {
			log.Printf("get supplier %s: %v", id, err)
			h.renderSupplierForm(w, buildSupplierFormData(nil, true, "", "не удалось загрузить поставщика"))
			return
		}
		if existing != nil {
			s.Name = existing.Name
		}
	}

	// Остальные поля — вручную из формы.
	s.DecodeRules = strings.Split(r.FormValue("decode_rules"), "\n")
	s.BoxDecodeRules = strings.Split(r.FormValue("box_decode_rules"), "\n")

	if s.OrderDays, err = parseDays(r.Form["order_days"]); err != nil {
		h.renderSupplierForm(w, buildSupplierFormData(s, isEdit, rawID, err.Error()))
		return
	}
	if s.DeliveryDays, err = parseDays(r.Form["delivery_days"]); err != nil {
		h.renderSupplierForm(w, buildSupplierFormData(s, isEdit, rawID, err.Error()))
		return
	}
	if s.SpecialOrderDays, err = parseDays(r.Form["special_order_days"]); err != nil {
		h.renderSupplierForm(w, buildSupplierFormData(s, isEdit, rawID, err.Error()))
		return
	}
	if s.SpecialDeliveryDays, err = parseDays(r.Form["special_delivery_days"]); err != nil {
		h.renderSupplierForm(w, buildSupplierFormData(s, isEdit, rawID, err.Error()))
		return
	}
	if s.DelayDays, err = parseNullableInt16(r.FormValue("delay_days")); err != nil {
		h.renderSupplierForm(w, buildSupplierFormData(s, isEdit, rawID, "макс. дней между заказом и доставкой: "+err.Error()))
		return
	}
	if s.SpecialDelayDays, err = parseNullableInt16(r.FormValue("special_delay_days")); err != nil {
		h.renderSupplierForm(w, buildSupplierFormData(s, isEdit, rawID, "спец. макс. дней: "+err.Error()))
		return
	}
	if s.MinOrderAmount, err = parseRublesToKopecks(r.FormValue("min_order_amount")); err != nil {
		h.renderSupplierForm(w, buildSupplierFormData(s, isEdit, rawID, err.Error()))
		return
	}

	if isEdit {
		err = h.msUC.Update(r.Context(), s)
	} else {
		err = h.msUC.Create(r.Context(), s)
	}

	switch {
	case err == nil:
		http.Redirect(w, r, "/ms/suppliers", http.StatusSeeOther)
	case errors.Is(err, domain.ErrSupplierExists):
		// Пользователь добавил ссылку на уже заведённого поставщика — открываем его.
		http.Redirect(w, r, "/ms/suppliers/edit?id="+s.ID, http.StatusSeeOther)
	case errors.Is(err, msu.ErrCounterpartyNameFetch):
		h.renderSupplierForm(w, buildSupplierFormData(s, isEdit, rawID,
			"не удалось получить имя контрагента из МойСклад — нажмите «Сохранить» ещё раз"))
	case errors.Is(err, msu.ErrWikiSync):
		// Поставщик уже сохранён — показываем сообщение на списке.
		http.Redirect(w, r, "/ms/suppliers?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
	default:
		h.renderSupplierForm(w, buildSupplierFormData(s, isEdit, rawID, err.Error()))
	}
}

// SupplierDelete — POST /ms/suppliers/delete: удаление поставщика.
// Каскады (barcodes/prices — CASCADE, wiki — SET NULL) выполняет БД.
func (h *Handler) SupplierDelete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "не удалось разобрать форму", http.StatusBadRequest)
		return
	}

	id := r.FormValue("id")
	if err := h.msUC.Delete(r.Context(), id); err != nil {
		log.Printf("delete supplier %s: %v", id, err)
		http.Redirect(w, r, "/ms/suppliers?err="+url.QueryEscape("не удалось удалить поставщика: "+err.Error()), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/ms/suppliers", http.StatusSeeOther)
}

func (h *Handler) renderSupplierForm(w http.ResponseWriter, d *SupplierFormData) {
	if err := msSupplierFormTmpl.Execute(w, d); err != nil {
		log.Printf("ms_suppliers_form template: %v", err)
	}
}

// buildSupplierFormData подготавливает данные формы для показа; nil — пустая форма.
func buildSupplierFormData(s *domain.Supplier, isEdit bool, rawID, errMsg string) *SupplierFormData {
	if s == nil {
		s = &domain.Supplier{}
	}

	d := &SupplierFormData{
		Supplier: s,
		IsEdit:   isEdit,
		RawID:    rawID,
		Error:    errMsg,
	}

	d.DecodeRulesText = strings.Join(s.DecodeRules, "\n")
	d.BoxDecodeRulesText = strings.Join(s.BoxDecodeRules, "\n")
	d.OrderDays = buildDayCheckboxes(s.OrderDays)
	d.DeliveryDays = buildDayCheckboxes(s.DeliveryDays)
	d.SpecialOrderDays = buildDayCheckboxes(s.SpecialOrderDays)
	d.SpecialDeliveryDays = buildDayCheckboxes(s.SpecialDeliveryDays)
	if s.DelayDays != nil {
		d.DelayDaysText = strconv.FormatInt(int64(*s.DelayDays), 10)
	}
	if s.SpecialDelayDays != nil {
		d.SpecialDelayDaysText = strconv.FormatInt(int64(*s.SpecialDelayDays), 10)
	}
	if s.MinOrderAmount != nil {
		d.MinOrderAmountText = kopecksToRubles(*s.MinOrderAmount)
	}

	return d
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
// Диапазон и дубли проверяет usecase (normalizeDays).
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

// parseNullableInt16 разбирает число или nil для пустой строки (задержки).
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

// parseRublesToKopecks переводит сумму в рублях (форма) в копейки (БД).
// Пустая строка — nil.
func parseRublesToKopecks(v string) (*int64, error) {
	v = strings.TrimSpace(strings.ReplaceAll(v, ",", "."))
	if v == "" {
		//nolint:nilnil // контракт: пустая строка = отсутствующее значение
		return nil, nil
	}
	rub, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return nil, fmt.Errorf("мин. сумма заказа %q — не число", v)
	}
	if rub < 0 {
		return nil, errors.New("мин. сумма заказа не может быть отрицательной")
	}
	k := int64(math.Round(rub * 100))
	return &k, nil
}

// kopecksToRubles форматирует копейки в строку рублей для поля формы.
func kopecksToRubles(k int64) string {
	rub := k / 100
	kop := k % 100
	if kop == 0 {
		return strconv.FormatInt(rub, 10)
	}
	return fmt.Sprintf("%d.%02d", rub, kop)
}
