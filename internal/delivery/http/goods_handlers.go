// Пакет http — хендлеры веб-слоя модуля «Продукция»: дерево папок товаров
// МойСклад с товарами-листьями и выгрузка отмеченного в каталог (products).
package http

import (
	"html/template"
	"log"
	"net/http"

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

var goodsTmpl = template.Must(
	template.New("goods.html").
		Funcs(template.FuncMap{"folderCtx": folderCtx}).
		ParseFiles("../internal/delivery/web/templates/goods.html"),
)

// GoodsPageData — данные страницы «Продукция». Selected — id отмеченных
// папок и товаров (сохраняются на странице после выгрузки), Errors —
// ошибки выгрузки по конкретным товарам.
type GoodsPageData struct {
	Tree     []*gucase.FolderNode
	Selected map[string]bool
	Errors   []gucase.ProductExportError
	Error    string
}

func (h *Handler) GoodsPage(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.renderGoodsPage(w, r, GoodsPageData{})
	case http.MethodPost:
		h.exportGoods(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// renderGoodsPage грузит дерево (папки + товары) и рендерит страницу.
// Ошибка загрузки дерева не обнуляет остальные данные страницы.
func (h *Handler) renderGoodsPage(w http.ResponseWriter, r *http.Request, data GoodsPageData) {
	tree, err := h.goodsUC.LoadTreeWithProducts(r.Context())
	if err != nil {
		log.Printf("goods: не удалось выгрузить дерево из МС: %v", err)
		data.Error = "Не удалось выгрузить дерево из МойСклад: " + err.Error()
	} else {
		data.Tree = tree
	}

	if err := goodsTmpl.Execute(w, data); err != nil {
		log.Printf("goods: ошибка рендера шаблона: %v", err)
		http.Error(w, "Failed to execute template: "+err.Error(), http.StatusInternalServerError)
	}
}

// exportGoods — POST /goods: выгрузка отмеченных товаров в каталог.
// Ошибки по отдельным товарам не прерывают остальных — они показываются
// на странице отчётом (по названию товара).
func (h *Handler) exportGoods(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		log.Printf("goods: ошибка разбора формы: %v", err)
		h.renderGoodsPage(w, r, GoodsPageData{Error: "Не удалось прочитать данные формы."})

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
		h.renderGoodsPage(w, r, GoodsPageData{Selected: selected, Error: "Ничего не выбрано — отметьте товары или группы."})

		return
	}

	errs, err := h.goodsUC.ExportProducts(r.Context(), items)
	if err != nil {
		log.Printf("goods: ошибка выгрузки каталога: %v", err)
		h.renderGoodsPage(w, r, GoodsPageData{Selected: selected, Error: "Не удалось выгрузить каталог: " + err.Error()})

		return
	}

	h.renderGoodsPage(w, r, GoodsPageData{Selected: selected, Errors: errs})
}
