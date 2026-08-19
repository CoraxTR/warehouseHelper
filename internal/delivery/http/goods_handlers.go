package http

import (
	"html/template"
	"log"
	"net/http"

	gucase "warehouseHelper/internal/goods/usecase"
)

var goodsTmpl = template.Must(template.ParseFiles("../internal/delivery/web/templates/goods.html"))

// GoodsPageData — данные страницы «Продукция».
type GoodsPageData struct {
	// Tree — дерево папок товаров (nil, пока не выполнена выгрузка).
	Tree []*gucase.FolderNode
	// Error — общее сообщение об ошибке (детали только в лог).
	Error string
}

// GoodsPage — GET: пустая страница «Продукция»; POST: выгрузка дерева папок
// товаров из МойСклад и его отображение. POST идемпотентен (fetch + рендер).
func (h *Handler) GoodsPage(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		renderGoodsPage(w, GoodsPageData{})
	case http.MethodPost:
		tree, err := h.goodsUC.LoadFolderTree(r.Context())
		if err != nil {
			log.Printf("goods: не удалось выгрузить папки из МС: %v", err)
			renderGoodsPage(w, GoodsPageData{Error: "Не удалось выгрузить папки из МойСклад. Попробуйте ещё раз."})

			return
		}

		renderGoodsPage(w, GoodsPageData{Tree: tree})
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func renderGoodsPage(w http.ResponseWriter, data GoodsPageData) {
	if err := goodsTmpl.Execute(w, data); err != nil {
		log.Printf("goods: ошибка рендера шаблона: %v", err)
	}
}
