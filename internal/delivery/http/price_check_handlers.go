package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"warehouseHelper/internal/domain"
	gucase "warehouseHelper/internal/goods/usecase"
)

var priceCheckTmpl = template.Must(template.ParseFiles(
	"../internal/delivery/web/templates/price_check.html",
	"../internal/delivery/web/templates/_nav.html",
))

// priceCheckPage — данные страницы «Проверка цен»: строки товаров в порядке
// каталога (group_name, name) — шаблон режет их на группы, как «Сроки».
type priceCheckPage struct {
	Rows []priceCheckRow
}

// priceCheckValues — четыре значения расчёта в виде строк формы: цены в рублях
// («450.00»), проценты целые. Пустая строка = «не задано» (HTML это пустое поле).
type priceCheckValues struct {
	Sale   string
	Buy    string
	VatOut string
	VatIn  string
}

// priceCheckRow — строка товара: ручное поле входящего НДС, предзаполнение
// песочницы и тексты наценок (по базе и по снапшоту).
type priceCheckRow struct {
	ProductID    string
	Name         string
	InternalCode string
	GroupName    string
	// VatIn — products.vat_incoming (поле строки, Enter пишет в каталог).
	VatIn string
	// SBox — предзаполнение полей песочницы: снапшот товара, а если его нет —
	// значения из базы (решение владельца: «по дефолту вставлены значения базы»).
	SBox priceCheckValues
	// Markup/SBoxMarkup — «53.33» (пусто, если данных не хватает) и
	// Missing/SBoxMissing — «цена продажи, входящий НДС» (пусто, если хватает).
	Markup      string
	Missing     string
	SBoxMarkup  string
	SBoxMissing string
}

// PriceCheckPage — GET /goods/price-check: страница «Проверка цен».
func (h *Handler) PriceCheckPage(w http.ResponseWriter, r *http.Request) {
	items, err := h.goodsUC.PriceCheckList(r.Context())
	if err != nil {
		slog.Info(fmt.Sprintf("price check page: %v", err))
		http.Error(w, "не удалось собрать проверку цен", http.StatusInternalServerError)

		return
	}

	page := priceCheckPage{Rows: make([]priceCheckRow, 0, len(items))}
	for _, it := range items {
		page.Rows = append(page.Rows, toPriceCheckRow(it))
	}

	if err := priceCheckTmpl.Execute(w, page); err != nil {
		slog.Error(fmt.Sprintf("price_check template: %v", err))
	}
}

// toPriceCheckRow переводит строку юзкейса в данные шаблона.
func toPriceCheckRow(it gucase.PriceCheckItem) priceCheckRow {
	row := priceCheckRow{
		ProductID:    it.ID,
		Name:         it.Name,
		InternalCode: it.InternalCode,
		GroupName:    it.GroupName,
		VatIn:        pcPercent(it.VATIncoming),
		SBox: priceCheckValues{
			Sale:   pcRubles(it.SalePrice),
			Buy:    pcRubles(it.BuyPrice),
			VatOut: pcCatalogVat(it.EffectiveVat),
			VatIn:  pcPercent(it.VATIncoming),
		},
		Markup:  pcMarkup(it.Markup),
		Missing: strings.Join(it.Markup.Missing, ", "),
	}
	// Снапшот песочницы есть — поля и наценка берутся из него, а не из базы.
	row.SBoxMarkup, row.SBoxMissing = row.Markup, row.Missing
	if sb := it.Sandbox; sb != nil {
		row.SBox = priceCheckValues{
			Sale:   pcRubles(sb.SalePrice),
			Buy:    pcRubles(sb.BuyPrice),
			VatOut: pcCatalogVat(sb.EffectiveVat),
			VatIn:  pcPercent(sb.VATIncoming),
		}
		row.SBoxMarkup = pcMarkup(it.SandboxMarkup)
		row.SBoxMissing = strings.Join(it.SandboxMarkup.Missing, ", ")
	}

	return row
}

// pcRubles — копейки в рубли строкой («450.00»); nil — пусто («не задано»).
func pcRubles(kop *int64) string {
	if kop == nil {
		return ""
	}

	return strconv.FormatFloat(float64(*kop)/100, 'f', 2, 64)
}

// pcPercent — процент целым числом строкой; nil — пусто.
func pcPercent(pct *int16) string {
	if pct == nil {
		return ""
	}

	return strconv.Itoa(int(*pct))
}

// pcCatalogVat — наш НДС для поля песочницы: -1 («без НДС») читается как 0 %
// (в песочницу отрицательное значение не попадает — она принимает 0..100).
func pcCatalogVat(pct *int16) string {
	if pct == nil {
		return ""
	}
	if *pct < 0 {
		return "0"
	}

	return strconv.Itoa(int(*pct))
}

// pcMarkup — наценка числом строкой («53.33»); неполные данные дают пустую
// строку — страница показывает рядом список того, чего не хватает.
func pcMarkup(res gucase.MarkupResult) string {
	if len(res.Missing) > 0 {
		return ""
	}

	return strconv.FormatFloat(res.Percent, 'f', 2, 64)
}

// priceCheckVATReq — запись входящего НДС товара (поле строки страницы).
type priceCheckVATReq struct {
	ProductID   string `json:"product_id"`
	VATIncoming *int16 `json:"vat_incoming"`
}

// PriceCheckVATSave — POST /goods/price-check/vat: записать входящий НДС товара
// в каталог (products.vat_incoming) и вернуть НЕМЕДЛЕННО пересчитанную наценку
// по значениям базы — страница подменяет ею текст в строке.
func (h *Handler) PriceCheckVATSave(w http.ResponseWriter, r *http.Request) {
	var req priceCheckVATReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "некорректный JSON", http.StatusBadRequest)

		return
	}
	if req.ProductID == "" {
		http.Error(w, "product_id обязателен", http.StatusBadRequest)

		return
	}

	if err := h.goodsUC.SetIncomingVAT(r.Context(), req.ProductID, req.VATIncoming); err != nil {
		h.priceCheckError(w, "сохранение входящего НДС", err)

		return
	}
	res, err := h.goodsUC.PriceCheckMarkup(r.Context(), req.ProductID)
	if err != nil {
		h.priceCheckError(w, "пересчёт наценки", err)

		return
	}

	writePriceCheckMarkup(w, res)
}

// priceCheckSandboxReq — снапшот песочницы товара: все поля необязательны
// (пустое поле отправляется как null и стирает прежнее значение снапшота).
type priceCheckSandboxReq struct {
	ProductID    string `json:"product_id"`
	SalePrice    *int64 `json:"sale_price"`
	BuyPrice     *int64 `json:"buy_price"`
	EffectiveVat *int16 `json:"effective_vat"`
	VATIncoming  *int16 `json:"vat_incoming"`
}

// PriceCheckSandboxSave — POST /goods/price-check/sandbox: сохранить снапшот
// песочницы товара (products НЕ трогает — это «пофантазировать») и вернуть
// наценку по этим значениям.
func (h *Handler) PriceCheckSandboxSave(w http.ResponseWriter, r *http.Request) {
	var req priceCheckSandboxReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "некорректный JSON", http.StatusBadRequest)

		return
	}
	if req.ProductID == "" {
		http.Error(w, "product_id обязателен", http.StatusBadRequest)

		return
	}

	res, err := h.goodsUC.SavePriceSandbox(r.Context(), domain.PriceSandbox{
		ProductID:    req.ProductID,
		SalePrice:    req.SalePrice,
		BuyPrice:     req.BuyPrice,
		EffectiveVat: req.EffectiveVat,
		VATIncoming:  req.VATIncoming,
	})
	if err != nil {
		h.priceCheckError(w, "сохранение песочницы", err)

		return
	}

	writePriceCheckMarkup(w, res)
}

// priceCheckMarkupResp — ответ расчёта: наценка строкой («53.33») либо список
// того, чего не хватает для формулы.
type priceCheckMarkupResp struct {
	Markup  string `json:"markup"`
	Missing string `json:"missing"`
}

// writePriceCheckMarkup — JSON с наценкой или с перечнем недостающих данных.
func writePriceCheckMarkup(w http.ResponseWriter, res gucase.MarkupResult) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	resp := priceCheckMarkupResp{
		Markup:  pcMarkup(res),
		Missing: strings.Join(res.Missing, ", "),
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Info(fmt.Sprintf("price check markup: encode: %v", err))
	}
}

// priceCheckError — разбор ошибок записи страницы «Проверка цен»: значения вне
// диапазона → 400, товара нет в каталоге → 404, остальное → 500.
func (h *Handler) priceCheckError(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, gucase.ErrVATOutOfRange), errors.Is(err, gucase.ErrPriceNegative):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, domain.ErrProductNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	default:
		slog.Info(fmt.Sprintf("price check %s: %v", what, err))
		http.Error(w, "не удалось сохранить", http.StatusInternalServerError)
	}
}
