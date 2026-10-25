// Пакет http — хендлеры веб-слоя модуля «МойСклад»: хаб и справочник
// поставщиков (список, создание, редактирование, удаление).
package http

import (
	"errors"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"log/slog"
	"warehouseHelper/internal/domain"
	msu "warehouseHelper/internal/mssuppliers/usecase"
	"warehouseHelper/internal/receiving"
)

// SuppliersListData — данные страницы списка поставщиков.
type SuppliersListData struct {
	Suppliers []domain.Supplier
	Error     string
}

// SupplierFormData — данные формы создания/редактирования поставщика.
// Текстовые поля подготовлены для показа (не копейки/массивы, а строки),
// чтобы шаблон оставался глупым.
type SupplierFormData struct {
	Supplier *domain.Supplier
	IsEdit   bool
	RawID    string // создание: исходная ссылка пользователя (uuid извлекается при сохранении)

	DecodeRulesText    string
	BoxDecodeRulesText string
	MinOrderAmountText string

	Barcodes []receiving.BarcodeRef // виджет «Внешние коды» (приёмка)

	// Prices — цены товаров поставщика (виджет «Цены товаров»): пары
	// (товар, цена). Строка модели несёт рубли строкой — шаблон глупый.
	PriceRows    []SupplierPriceRow
	ExpandPrices bool

	// ExpandBarcodes — раскрыть виджет кодов сразу при загрузке (?barcodes=1):
	// после батчевого добавления оператор возвращается ВНУТРЬ виджета, а не
	// наверх страницы. Кнопка-тоггл при этом продолжает работать.
	ExpandBarcodes bool
	Message        string // инфо-сообщение (например, результат батча кодов)

	Error string
}

// Шаблоны модуля «МойСклад», парсятся один раз при старте.
var (
	msIndexTmpl         = template.Must(template.ParseFiles("../internal/delivery/web/templates/ms_index.html", "../internal/delivery/web/templates/_nav.html"))
	msSuppliersListTmpl = template.Must(template.ParseFiles("../internal/delivery/web/templates/ms_suppliers_list.html", "../internal/delivery/web/templates/_nav.html"))
	msSupplierFormTmpl  = template.Must(template.ParseFiles("../internal/delivery/web/templates/ms_suppliers_form.html", "../internal/delivery/web/templates/_nav.html"))
)

// MsPage — GET /ms: хаб модуля «МойСклад» (кнопки подмодулей).
func (h *Handler) MsPage(w http.ResponseWriter, _ *http.Request) {
	if err := msIndexTmpl.Execute(w, nil); err != nil {
		slog.Info(fmt.Sprintf("ms_index template: %v", err))
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
		slog.Info(fmt.Sprintf("list suppliers: %v", err))
		d.Error = "не удалось загрузить список поставщиков"
	} else {
		d.Suppliers = suppliers
	}

	if err := msSuppliersListTmpl.Execute(w, d); err != nil {
		slog.Info(fmt.Sprintf("ms_suppliers_list template: %v", err))
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
		slog.Info("get supplier", "id", id, "err", err)
		h.renderSupplierForm(w, buildSupplierFormData(nil, true, "", "не удалось загрузить поставщика"))
		return
	}
	if s == nil {
		http.Redirect(w, r, "/ms/suppliers", http.StatusSeeOther)
		return
	}

	// err/msg приходят из PRG-редиректов (батч кодов, удаление кода);
	// barcodes=1 раскрывает виджет кодов сразу при заходе.
	d := buildSupplierFormData(s, true, "", r.URL.Query().Get("err"))
	d.Barcodes = h.loadSupplierBarcodes(r, s.ID)
	d.Message = r.URL.Query().Get("msg")
	d.ExpandBarcodes = r.URL.Query().Get("barcodes") == "1"
	d.PriceRows = h.loadSupplierPrices(r, s.ID, d.Barcodes)
	d.ExpandPrices = r.URL.Query().Get("prices") == "1"

	h.renderSupplierForm(w, d)
}

// SupplierPriceRow — строка виджета «Цены товаров» (рубли строкой, чтобы
// шаблон не форматировал копейки).
type SupplierPriceRow struct {
	ProductID   string
	ProductName string
	PriceText   string
}

// loadSupplierPrices собирает строки виджета «Цены товаров»: товары поставщика
// (по внешним кодам — то, что он у нас реально поставляет) плюс уже заведённые
// цены, которых в кодах нет. Ошибка чтения цен не ломает форму.
func (h *Handler) loadSupplierPrices(r *http.Request, supplierID string, barcodes []receiving.BarcodeRef) []SupplierPriceRow {
	prices, err := h.msUC.SupplierPrices(r.Context(), supplierID)
	if err != nil {
		slog.Info("list supplier prices", "supplier_id", supplierID, "err", err)
	}

	priceByProduct := make(map[string]int64, len(prices))
	nameByProduct := make(map[string]string, len(prices))
	for _, p := range prices {
		priceByProduct[p.ProductID] = p.Price
		nameByProduct[p.ProductID] = p.ProductName
	}

	order := make([]string, 0, len(barcodes)+len(prices))
	seen := make(map[string]struct{}, len(barcodes)+len(prices))
	for _, b := range barcodes {
		if _, ok := seen[b.ProductID]; ok {
			continue
		}
		seen[b.ProductID] = struct{}{}
		order = append(order, b.ProductID)
		if b.ProductName != "" {
			nameByProduct[b.ProductID] = b.ProductName
		}
	}
	for _, p := range prices {
		if _, ok := seen[p.ProductID]; ok {
			continue
		}
		seen[p.ProductID] = struct{}{}
		order = append(order, p.ProductID)
	}

	rows := make([]SupplierPriceRow, 0, len(order))
	for _, productID := range order {
		name := nameByProduct[productID]
		if name == "" {
			name = productID
		}

		var priceText string
		if price, ok := priceByProduct[productID]; ok && price > 0 {
			priceText = kopecksToRubles(price)
		}
		rows = append(rows, SupplierPriceRow{
			ProductID:   productID,
			ProductName: name,
			PriceText:   priceText,
		})
	}

	return rows
}

// SupplierPricesSave — POST /ms/suppliers/prices/save: батч цен поставщика.
// Пустое поле = цена не задана (строка удаляется), иначе пишется сумма.
func (h *Handler) SupplierPricesSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "не удалось разобрать форму", http.StatusBadRequest)
		return
	}

	supplierID := strings.TrimSpace(r.FormValue("supplier_id"))
	productIDs := r.Form["product_id"]
	prices := r.Form["price"]
	if supplierID == "" {
		http.Redirect(w, r, "/ms/suppliers", http.StatusSeeOther)
		return
	}

	saved, cleared := 0, 0
	for i, productID := range productIDs {
		productID = strings.TrimSpace(productID)
		if productID == "" {
			continue
		}

		var raw string
		if i < len(prices) {
			raw = prices[i]
		}

		kop, err := parseRublesToKopecks(raw)
		if err != nil {
			slog.Info("supplier price parse", "supplier_id", supplierID, "product_id", productID, "err", err)
			http.Redirect(w, r, "/ms/suppliers/edit?id="+supplierID+"&prices=1&err="+url.QueryEscape("цена: "+err.Error()), http.StatusSeeOther)
			return
		}

		var amount int64
		if kop != nil {
			amount = *kop
		}
		if err := h.msUC.SetSupplierPrice(r.Context(), supplierID, productID, amount); err != nil {
			slog.Info("set supplier price", "supplier_id", supplierID, "product_id", productID, "err", err)
			http.Redirect(w, r, "/ms/suppliers/edit?id="+supplierID+"&prices=1&err="+url.QueryEscape("не удалось сохранить цену"), http.StatusSeeOther)
			return
		}

		if amount > 0 {
			saved++
		} else {
			cleared++
		}
	}

	msg := fmt.Sprintf("цены сохранены: %d", saved)
	if cleared > 0 {
		msg = fmt.Sprintf("цены сохранены: %d, снято: %d", saved, cleared)
	}
	http.Redirect(w, r, "/ms/suppliers/edit?id="+supplierID+"&prices=1&msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

// loadSupplierBarcodes читает связки «внешний код → товар» для виджета;
// ошибка чтения не ломает форму поставщика (вторичные данные), в лог — да.
func (h *Handler) loadSupplierBarcodes(r *http.Request, supplierID string) []receiving.BarcodeRef {
	barcodes, err := h.msUC.ListBarcodes(r.Context(), supplierID)
	if err != nil {
		slog.Info("list supplier barcodes", "supplier_id", supplierID, "err", err)

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
			slog.Info("get supplier", "id", id, "err", err)
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
		http.Redirect(w, r, "/ms/suppliers/edit?id="+url.QueryEscape(s.ID), http.StatusSeeOther)
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
		slog.Info("delete supplier", "id", id, "err", err)
		http.Redirect(w, r, "/ms/suppliers?err="+url.QueryEscape("не удалось удалить поставщика: "+err.Error()), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/ms/suppliers", http.StatusSeeOther)
}

func (h *Handler) renderSupplierForm(w http.ResponseWriter, d *SupplierFormData) {
	if err := msSupplierFormTmpl.Execute(w, d); err != nil {
		slog.Info(fmt.Sprintf("ms_suppliers_form template: %v", err))
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
	if s.MinOrderAmount != nil {
		d.MinOrderAmountText = kopecksToRubles(*s.MinOrderAmount)
	}

	return d
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
