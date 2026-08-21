package http

import (
	"html/template"
	"log"
	"net/http"

	gucase "warehouseHelper/internal/goods/usecase"
)

// folderCtxData — контекст рекурсивного рендера узла дерева: сам узел и
// выбранные id (нужны для проставления checked у листьев). Внутри вызова
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
// галочками листьев дерева: они сохраняются на странице и передаются дальше
// (следующий шаг модуля) как r.Form["folders"].
type GoodsPageData struct {
	Tree     []*gucase.FolderNode
	Selected map[string]bool
	Error    string
}

func (h *Handler) GoodsPage(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		renderGoodsPage(w, r, GoodsPageData{})
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			log.Printf("goods: ошибка разбора формы: %v", err)
			renderGoodsPage(w, r, GoodsPageData{Error: "Не удалось прочитать данные формы."})

			return
		}

		// Галочки снимаются/ставятся на странице и переживают повторные выгрузки.
		selected := make(map[string]bool, len(r.Form["folders"]))
		for _, href := range r.Form["folders"] {
			selected[href] = true
		}

		tree, err := h.goodsUC.LoadFolderTree(r.Context())
		if err != nil {
			log.Printf("goods: не удалось выгрузить папки из МС: %v", err)
			renderGoodsPage(w, r, GoodsPageData{Selected: selected, Error: "Не удалось выгрузить папки из МойСклад. Попробуйте ещё раз."})

			return
		}

		renderGoodsPage(w, r, GoodsPageData{Tree: tree, Selected: selected})
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func renderGoodsPage(w http.ResponseWriter, r *http.Request, data GoodsPageData) {
	if err := goodsTmpl.Execute(w, data); err != nil {
		log.Printf("goods: ошибка рендера шаблона: %v", err)
		http.Error(w, "Failed to execute template: "+err.Error(), http.StatusInternalServerError)
	}
}
