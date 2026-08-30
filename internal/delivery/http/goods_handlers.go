// Пакет http — хендлеры веб-слоя модуля «Продукция»: хаб (поиск позиции
// в каталоге / добавление через дерево), страница редактирования позиции
// с ресинком из МС, дерево папок с выгрузкой отмеченного.
package http

import (
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"warehouseHelper/internal/domain"
	gucase "warehouseHelper/internal/goods/usecase"
)

// folderCtxData — контекст рекурсивного рендера узла дерева: сам узел и
// выбранные id (нужны для проставления checked). Внутри вызова
// {{template}} $ сбрасывается на аргумент вызова, поэтому корневой контекст
// передаётся в узел явно через folderCtx.
type folderCtxData struct {
	Node     *gucase.FolderNode
	Selected map[string]bool
}

func folderCtx(node *gucase.FolderNode, selected map[string]bool) folderCtxData {
	return folderCtxData{Node: node, Selected: selected}
}

var goodsHubTmpl = template.Must(template.ParseFiles("../internal/delivery/web/templates/goods.html"))

var goodsTreeTmpl = template.Must(
	template.New("goods_tree.html").
		Funcs(template.FuncMap{"folderCtx": folderCtx}).
		ParseFiles("../internal/delivery/web/templates/goods_tree.html"),
)

var goodsEditTmpl = template.Must(template.ParseFiles("../internal/delivery/web/templates/goods_edit.html"))

// GoodsHubData — данные хаба «Продукция».
type GoodsHubData struct {
	Error   string
	Query   string
	Matches []domain.Product
}

// GoodsEditData — данные страницы редактирования позиции.
type GoodsEditData struct {
	Product *domain.Product
	OK      string
	Error   string
}

// GoodsTreeData — данные страницы дерева.
type GoodsTreeData struct {
	Tree     []*gucase.FolderNode
	Selected map[string]bool
	Errors   []gucase.ProductExportError
	Error    string
}

// GoodsPage — GET /goods: хаб «Продукция» (поиск позиции + добавление).
func (h *Handler) GoodsPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	if err := goodsHubTmpl.Execute(w, GoodsHubData{}); err != nil {
		log.Printf("goods: ошибка рендера хаба: %v", err)
		http.Error(w, "Failed to execute template: "+err.Error(), http.StatusInternalServerError)
	}
}

// GoodsSearch — POST /goods/search: поиск позиции в каталоге по коду
// (точное совпадение) или имени (подстрока). Одна позиция — на
// редактирование; несколько — список на выбор; ноль — ошибка.
func (h *Handler) GoodsSearch(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		log.Printf("goods: ошибка разбора формы поиска: %v", err)
		h.renderGoodsHub(w, GoodsHubData{Error: "Не удалось прочитать запрос."})

		return
	}

	query := strings.TrimSpace(r.FormValue("q"))
	if query == "" {
		h.renderGoodsHub(w, GoodsHubData{Error: "Введите код или название товара."})

		return
	}

	matches, err := h.goodsUC.SearchProducts(r.Context(), query)
	if err != nil {
		log.Printf("goods: ошибка поиска: %v", err)
		h.renderGoodsHub(w, GoodsHubData{Error: "Не удалось выполнить поиск: " + err.Error(), Query: query})

		return
	}

	switch len(matches) {
	case 0:
		h.renderGoodsHub(w, GoodsHubData{Error: "В каталоге ничего не найдено.", Query: query})
	case 1:
		http.Redirect(w, r, "/goods/edit?id="+url.QueryEscape(matches[0].ID), http.StatusSeeOther)
	default:
		h.renderGoodsHub(w, GoodsHubData{Matches: matches, Query: query})
	}
}

func (h *Handler) renderGoodsHub(w http.ResponseWriter, data GoodsHubData) {
	if err := goodsHubTmpl.Execute(w, data); err != nil {
		log.Printf("goods: ошибка рендера хаба: %v", err)
		http.Error(w, "Failed to execute template: "+err.Error(), http.StatusInternalServerError)
	}
}

// GoodsEditPage — GET /goods/edit?id=...: страница редактирования позиции.
func (h *Handler) GoodsEditPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	id := r.URL.Query().Get("id")
	product, err := h.goodsUC.GetProduct(r.Context(), id)
	if err != nil {
		log.Printf("goods: не удалось получить позицию %s: %v", id, err)
		http.Redirect(w, r, "/goods?err="+url.QueryEscape("позиция не найдена в каталоге"), http.StatusSeeOther)

		return
	}

	h.renderGoodsEdit(w, GoodsEditData{
		Product: product,
		OK:      r.URL.Query().Get("ok"),
		Error:   r.URL.Query().Get("err"),
	})
}

func (h *Handler) renderGoodsEdit(w http.ResponseWriter, data GoodsEditData) {
	if err := goodsEditTmpl.Execute(w, data); err != nil {
		log.Printf("goods: ошибка рендера редактирования: %v", err)
		http.Error(w, "Failed to execute template: "+err.Error(), http.StatusInternalServerError)
	}
}

// GoodsEditSave — POST /goods/edit: сохранение ручных правок позиции
// (upsert по id).
func (h *Handler) GoodsEditSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		log.Printf("goods: ошибка разбора формы позиции: %v", err)
		http.Redirect(w, r, "/goods?err="+url.QueryEscape("не удалось прочитать форму"), http.StatusSeeOther)

		return
	}

	p, err := productFromForm(r)
	if err != nil {
		log.Printf("goods: неверные данные формы позиции: %v", err)
		http.Redirect(w, r, "/goods/edit?id="+url.QueryEscape(r.FormValue("id"))+"&err="+url.QueryEscape(err.Error()), http.StatusSeeOther)

		return
	}

	if err := h.goodsUC.SaveProduct(r.Context(), p); err != nil {
		log.Printf("goods: не удалось сохранить позицию %s: %v", p.ID, err)
		http.Redirect(w, r, "/goods/edit?id="+url.QueryEscape(p.ID)+"&err="+url.QueryEscape("не удалось сохранить: "+err.Error()), http.StatusSeeOther)

		return
	}

	http.Redirect(w, r, "/goods/edit?id="+url.QueryEscape(p.ID)+"&ok="+url.QueryEscape("позиция сохранена"), http.StatusSeeOther)
}

// GoodsResync — POST /goods/resync?id=...: ресинк позиции из МойСклад
// (все поля заново из МС, group_name сохраняется).
func (h *Handler) GoodsResync(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Redirect(w, r, "/goods?err="+url.QueryEscape("не указан id позиции"), http.StatusSeeOther)

		return
	}

	_, err := h.goodsUC.ResyncProduct(r.Context(), id)
	if err != nil {
		log.Printf("goods: не удалось ресинкнуть позицию %s: %v", id, err)
		http.Redirect(w, r, "/goods/edit?id="+url.QueryEscape(id)+"&err="+url.QueryEscape("не удалось обновить из МС: "+err.Error()), http.StatusSeeOther)

		return
	}

	http.Redirect(w, r, "/goods/edit?id="+url.QueryEscape(id)+"&ok="+url.QueryEscape("позиция обновлена из МойСклад"), http.StatusSeeOther)
}

// GoodsTreePage — GET /goods/tree: дерево папок МС с товарами-листьями.
func (h *Handler) GoodsTreePage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	h.renderGoodsTree(w, r, GoodsTreeData{})
}

// GoodsExport — POST /goods/tree: выгрузка отмеченных товаров в каталог.
func (h *Handler) GoodsExport(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		log.Printf("goods: ошибка разбора формы: %v", err)
		h.renderGoodsTree(w, r, GoodsTreeData{Error: "Не удалось прочитать данные формы."})

		return
	}

	// Состояние галочек после выгрузки: папки и товары.
	selected := make(map[string]bool)
	for _, id := range r.Form["folders"] {
		selected[id] = true
	}

	// Отмеченные товары: id + путь группы (скрытое поле path_<id>).
	items := make([]gucase.ExportItem, 0, len(r.Form["product"]))
	for _, id := range r.Form["product"] {
		selected[id] = true
		items = append(items, gucase.ExportItem{
			ProductID: id,
			GroupPath: r.FormValue("path_" + id),
		})
	}

	if len(items) == 0 {
		h.renderGoodsTree(w, r, GoodsTreeData{Selected: selected, Error: "Ничего не выбрано — отметьте товары или группы."})

		return
	}

	errs, err := h.goodsUC.ExportProducts(r.Context(), items)
	if err != nil {
		log.Printf("goods: ошибка выгрузки каталога: %v", err)
		h.renderGoodsTree(w, r, GoodsTreeData{Selected: selected, Error: "Не удалось выгрузить каталог: " + err.Error()})

		return
	}

	h.renderGoodsTree(w, r, GoodsTreeData{Selected: selected, Errors: errs})
}

// renderGoodsTree грузит дерево (папки + товары) и рендерит страницу.
// Ошибка загрузки дерева не обнуляет остальные данные страницы.
func (h *Handler) renderGoodsTree(w http.ResponseWriter, r *http.Request, data GoodsTreeData) {
	tree, err := h.goodsUC.LoadTreeWithProducts(r.Context())
	if err != nil {
		log.Printf("goods: не удалось выгрузить дерево из МС: %v", err)
		data.Error = "Не удалось выгрузить дерево из МойСклад: " + err.Error()
	} else {
		data.Tree = tree
	}

	if err := goodsTreeTmpl.Execute(w, data); err != nil {
		log.Printf("goods: ошибка рендера шаблона дерева: %v", err)
		http.Error(w, "Failed to execute template: "+err.Error(), http.StatusInternalServerError)
	}
}

// productFromForm собирает domain.Product из полей формы редактирования.
func productFromForm(r *http.Request) (*domain.Product, error) {
	p := &domain.Product{
		ID:            strings.TrimSpace(r.FormValue("id")),
		InternalCode:  strings.TrimSpace(r.FormValue("internal_code")),
		Name:          strings.TrimSpace(r.FormValue("name")),
		UOM:           strings.TrimSpace(r.FormValue("uom")),
		GroupName:     strings.TrimSpace(r.FormValue("group_name")),
		InventoryType: strings.TrimSpace(r.FormValue("inventory_type")),
		ShortList:     r.FormValue("short_list") == "on",
		TrackWeekly:   r.FormValue("track_weekly") == "on",
	}

	if p.ID == "" || p.InternalCode == "" || p.Name == "" || p.UOM == "" || p.InventoryType == "" {
		return nil, errors.New("обязательные поля: id, внутренний код, название, единица измерения, вид инвентаризации")
	}

	parseNullableFloat := func(v string) (*float64, error) {
		v = strings.TrimSpace(v)
		if v == "" {
			//nolint:nilnil // контракт: пустая строка = отсутствующее значение
			return nil, nil
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, err
		}
		return &f, nil
	}
	parseNullableInt := func(v string) (*int16, error) {
		v = strings.TrimSpace(v)
		if v == "" {
			//nolint:nilnil // контракт: пустая строка = отсутствующее значение
			return nil, nil
		}
		n, err := strconv.ParseInt(v, 10, 16)
		if err != nil {
			return nil, err
		}
		s := int16(n)
		return &s, nil
	}

	var err error
	if p.AverageWeight, err = parseNullableFloat(r.FormValue("average_weight")); err != nil {
		return nil, errors.New("средний вес — не число")
	}
	if p.ShelfLife, err = parseNullableInt(r.FormValue("shelf_life")); err != nil {
		return nil, errors.New("общий срок годности — не число")
	}
	if p.PackSize, err = parseNullableInt(r.FormValue("pack_size")); err != nil {
		return nil, errors.New("кол-во в упаковке — не число")
	}

	return p, nil
}

// GoodsSearchJSON — GET /goods/search/json?q=...: поиск товаров каталога
// для виджета «Внешние коды» на карточке поставщика (JSON, не страница).
// Ответ: [{id, name, internal_code, uom}].
func (h *Handler) GoodsSearchJSON(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		http.Error(w, "пустой запрос", http.StatusBadRequest)

		return
	}

	products, err := h.goodsUC.SearchProducts(r.Context(), q)
	if err != nil {
		log.Printf("goods search json: %v", err)
		http.Error(w, "не удалось выполнить поиск", http.StatusInternalServerError)

		return
	}

	type item struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		InternalCode string `json:"internal_code"`
		UOM          string `json:"uom"`
	}

	out := make([]item, 0, len(products))
	for _, p := range products {
		out = append(out, item{ID: p.ID, Name: p.Name, InternalCode: p.InternalCode, UOM: p.UOM})
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		log.Printf("goods search json: encode: %v", err)
	}
}
