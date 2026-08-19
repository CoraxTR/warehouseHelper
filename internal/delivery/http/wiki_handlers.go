// Пакет http — хендлеры веб-слоя вики: список страниц, просмотр,
// редактирование (создание/сохранение), удаление и отдача фото.
package http

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/wiki/render"
)

// WikiIndexData — данные страницы списка вики-страниц.
type WikiIndexData struct {
	Query     string
	TagFilter []string
	Type      string
	Entries   []domain.WikiIndexEntry
	TagCloud  []domain.WikiTagCount
	Error     string
}

// WikiLink — ссылка на вики-страницу со страницы противоположного типа:
// поставщик на странице товара или товар на странице поставщика.
type WikiLink struct {
	Title  string
	Exists bool
}

// WikiPageData — данные страницы просмотра вики-страницы.
type WikiPageData struct {
	Page          *domain.WikiPage
	ContentHTML   template.HTML
	Backlinks     []string
	SupplierLinks []WikiLink
	ProductLinks  []WikiLink
	Error         string
}

// WikiEditData — данные формы создания/редактирования вики-страницы.
type WikiEditData struct {
	Page           *domain.WikiPage
	CurrentTitle   string
	TagsValue      string
	SupplierTitles []string
	ProductTitles  []string
	AllTags        []string
	Error          string
}

// Шаблоны вики, парсятся один раз при старте.
var (
	wikiIndexTmpl        = template.Must(template.ParseFiles("../internal/delivery/web/templates/wiki_index.html"))
	wikiSupplierTmpl     = template.Must(template.ParseFiles("../internal/delivery/web/templates/wiki_supplier.html"))
	wikiProductTmpl      = template.Must(template.ParseFiles("../internal/delivery/web/templates/wiki_product.html"))
	wikiSupplierEditTmpl = template.Must(template.ParseFiles("../internal/delivery/web/templates/wiki_supplier_edit.html"))
	wikiProductEditTmpl  = template.Must(template.ParseFiles("../internal/delivery/web/templates/wiki_product_edit.html"))
)

// WikiIndex — GET: список страниц с фильтрами по запросу, тегам и типу.
func (h *Handler) WikiIndex(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	tagFilter := r.URL.Query()["tag"]
	typ := r.URL.Query().Get("type")
	if typ != "" && !domain.ValidPageType(typ) {
		typ = ""
	}

	entries, err := h.wikiUC.ListIndex(r.Context(), q, tagFilter, domain.PageType(typ))
	if err != nil {
		http.Error(w, "Ошибка загрузки списка страниц: "+err.Error(), http.StatusInternalServerError)

		return
	}

	tagCloud, err := h.wikiUC.TagCloud(r.Context())
	if err != nil {
		http.Error(w, "Ошибка загрузки облака тегов: "+err.Error(), http.StatusInternalServerError)

		return
	}

	data := WikiIndexData{
		Query:     q,
		TagFilter: tagFilter,
		Type:      typ,
		Entries:   entries,
		TagCloud:  tagCloud,
	}

	if err = wikiIndexTmpl.Execute(w, data); err != nil {
		http.Error(w, "Ошибка рендеринга шаблона: "+err.Error(), http.StatusInternalServerError)
	}
}

// WikiPage — GET: просмотр страницы с рендером содержимого и ссылок.
func (h *Handler) WikiPage(w http.ResponseWriter, r *http.Request) {
	title := r.URL.Query().Get("title")
	if title == "" {
		http.Error(w, "Заголовок не указан", http.StatusBadRequest)

		return
	}

	page, backlinks, err := h.wikiUC.GetPageWithBacklinks(r.Context(), title)
	if err != nil {
		http.Error(w, "Ошибка загрузки страницы: "+err.Error(), http.StatusInternalServerError)

		return
	}
	if page == nil {
		http.Error(w, "Страница не найдена", http.StatusNotFound)

		return
	}

	// Цели вики-ссылок из содержимого; добавляем связанные страницы
	// противоположного типа: поставщиков у товара, продукты у поставщика.
	extracted := render.ExtractLinks(page.Content)
	if page.Type == domain.PageTypeProduct {
		extracted = append(extracted, page.Suppliers...)
	} else {
		extracted = append(extracted, page.Products...)
	}

	targets, err := h.wikiUC.ResolveLinkTargets(r.Context(), extracted)
	if err != nil {
		http.Error(w, "Ошибка разрешения ссылок: "+err.Error(), http.StatusInternalServerError)

		return
	}

	contentHTML, err := render.Render(page.Content, targets)
	if err != nil {
		http.Error(w, "Ошибка рендеринга содержимого: "+err.Error(), http.StatusInternalServerError)

		return
	}

	data := WikiPageData{
		Page:          page,
		ContentHTML:   contentHTML,
		Backlinks:     backlinks,
		SupplierLinks: make([]WikiLink, 0, len(page.Suppliers)),
		ProductLinks:  make([]WikiLink, 0, len(page.Products)),
	}
	for _, s := range page.Suppliers {
		_, exists := targets[strings.ToLower(s)]
		data.SupplierLinks = append(data.SupplierLinks, WikiLink{Title: s, Exists: exists})
	}
	for _, p := range page.Products {
		_, exists := targets[strings.ToLower(p)]
		data.ProductLinks = append(data.ProductLinks, WikiLink{Title: p, Exists: exists})
	}

	tmpl := wikiSupplierTmpl
	if page.Type == domain.PageTypeProduct {
		tmpl = wikiProductTmpl
	}

	if err = tmpl.Execute(w, data); err != nil {
		http.Error(w, "Ошибка рендеринга шаблона: "+err.Error(), http.StatusInternalServerError)
	}
}

// WikiEdit — GET: форма создания (type в query) или редактирования (title в query);
// POST: сохранение страницы.
func (h *Handler) WikiEdit(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.wikiEditForm(w, r)
	case http.MethodPost:
		h.wikiSave(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// wikiEditForm — GET: форма создания новой или редактирования существующей страницы.
func (h *Handler) wikiEditForm(w http.ResponseWriter, r *http.Request) {
	title := r.URL.Query().Get("title")

	var page *domain.WikiPage
	if title == "" {
		typ := r.URL.Query().Get("type")
		if !domain.ValidPageType(typ) {
			http.Error(w, "Укажите тип страницы", http.StatusBadRequest)

			return
		}
		page = &domain.WikiPage{Type: domain.PageType(typ)}
	} else {
		var err error
		page, err = h.wikiUC.GetPage(r.Context(), title)
		if err != nil {
			http.Error(w, "Ошибка загрузки страницы: "+err.Error(), http.StatusInternalServerError)

			return
		}
		if page == nil {
			http.Error(w, "Страница не найдена", http.StatusNotFound)

			return
		}
	}

	data := h.wikiEditData(r.Context(), page, page.Title)
	h.renderWikiEditForm(w, page.Type, data)
}

// wikiEditData собирает данные формы: текущие значения и списки-подсказки
// (заголовки поставщиков и теги). Ошибки вспомогательных выборок не критичны —
// форма отрендерится без подсказок.
func (h *Handler) wikiEditData(ctx context.Context, page *domain.WikiPage, currentTitle string) WikiEditData {
	data := WikiEditData{
		Page:         page,
		CurrentTitle: currentTitle,
		TagsValue:    strings.Join(page.Tags, ", "),
	}

	if page.Type == domain.PageTypeProduct {
		if titles, err := h.wikiUC.ListPageTitlesByType(ctx, domain.PageTypeSupplier); err == nil {
			data.SupplierTitles = titles
		}
	} else {
		if titles, err := h.wikiUC.ListPageTitlesByType(ctx, domain.PageTypeProduct); err == nil {
			data.ProductTitles = titles
		}
	}

	if tags, err := h.wikiUC.TagCloud(ctx); err == nil {
		for _, t := range tags {
			data.AllTags = append(data.AllTags, t.Name)
		}
	}

	return data
}

// renderWikiEditForm рендерит форму редактирования по типу страницы.
func (h *Handler) renderWikiEditForm(w http.ResponseWriter, typ domain.PageType, data WikiEditData) {
	tmpl := wikiSupplierEditTmpl
	if typ == domain.PageTypeProduct {
		tmpl = wikiProductEditTmpl
	}

	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, "Ошибка рендеринга шаблона: "+err.Error(), http.StatusInternalServerError)
	}
}

// wikiSave — POST: сохранение (создание или обновление) вики-страницы.
func (h *Handler) wikiSave(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "Invalid form data: "+err.Error(), http.StatusBadRequest)

		return
	}

	typ := r.FormValue("type")
	if !domain.ValidPageType(typ) {
		http.Error(w, "Укажите тип страницы", http.StatusBadRequest)

		return
	}

	page := &domain.WikiPage{
		Type:    domain.PageType(typ),
		Title:   r.FormValue("title"),
		Content: r.FormValue("content"),
		Tags:    strings.Split(r.FormValue("tags"), ","),
	}

	if page.Type == domain.PageTypeSupplier {
		// Собираем все строки контактов (до 20), пустые пропускаем —
		// серверная логика не должна полагаться на порядок строк из JS.
		for n := 0; n < 20; n++ {
			contact := domain.Contact{
				Name:  r.FormValue(fmt.Sprintf("contacts_name_%d", n)),
				Phone: r.FormValue(fmt.Sprintf("contacts_phone_%d", n)),
				Email: r.FormValue(fmt.Sprintf("contacts_email_%d", n)),
				Site:  r.FormValue(fmt.Sprintf("contacts_site_%d", n)),
			}
			if contact.Name == "" && contact.Phone == "" && contact.Email == "" && contact.Site == "" {
				continue
			}
			page.Contacts = append(page.Contacts, contact)
		}
		page.OrderDays = formDays(r.Form["order_days"])
		page.DeliveryDays = formDays(r.Form["delivery_days"])
		page.Products = r.Form["products"]
	} else {
		page.AverageWeight = r.FormValue("average_weight")
		page.Suppliers = r.Form["suppliers"]
	}

	// Фото: multipart-файл либо отсутствует.
	var photo *domain.PhotoUpload
	f, _, err := r.FormFile("photo")
	switch {
	case err == nil:
		data, readErr := io.ReadAll(io.LimitReader(f, 5<<20+1))
		_ = f.Close()
		if readErr != nil {
			http.Error(w, "Ошибка чтения фото: "+readErr.Error(), http.StatusBadRequest)

			return
		}
		if len(data) > 5<<20 {
			http.Error(w, "Фото больше 5 МБ", http.StatusBadRequest)

			return
		}
		photo = &domain.PhotoUpload{
			Data:        data,
			ContentType: http.DetectContentType(data),
		}
	case errors.Is(err, http.ErrMissingFile):
		photo = nil
	default:
		http.Error(w, "Ошибка загрузки фото: "+err.Error(), http.StatusBadRequest)

		return
	}

	currentTitle := r.FormValue("currentTitle")

	if err = h.wikiUC.SavePage(r.Context(), currentTitle, page, photo); err != nil {
		data := h.wikiEditData(r.Context(), page, currentTitle)
		data.Error = err.Error()
		h.renderWikiEditForm(w, page.Type, data)

		return
	}

	// Удаление фото по отметке, если новое фото не загружалось.
	// После успешного SavePage страница уже под page.Title (возможно, переименована).
	if r.FormValue("remove_photo") != "" && photo == nil && currentTitle != "" {
		if err = h.wikiUC.RemovePhoto(r.Context(), page.Title); err != nil {
			log.Printf("WikiSave RemovePhoto error: %v", err)
		}
	}

	http.Redirect(w, r, "/wiki/page?title="+url.QueryEscape(page.Title), http.StatusSeeOther)
}

// formDays преобразует значения полей формы (мультивыбор дней недели) в []int.
// Нечисловые значения пропускаются — валидацию диапазона делает usecase.
func formDays(values []string) []int {
	days := make([]int, 0, len(values))
	for _, v := range values {
		d, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			continue
		}
		days = append(days, d)
	}

	return days
}

// WikiDelete — POST: удаление страницы по заголовку из формы.
func (h *Handler) WikiDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data: "+err.Error(), http.StatusBadRequest)

		return
	}

	title := r.FormValue("title")
	if title == "" {
		http.Error(w, "Заголовок не указан", http.StatusBadRequest)

		return
	}

	if err := h.wikiUC.DeletePage(r.Context(), title); err != nil {
		http.Error(w, "Ошибка удаления страницы: "+err.Error(), http.StatusInternalServerError)

		return
	}

	http.Redirect(w, r, "/wiki", http.StatusSeeOther)
}

// WikiPhoto — GET: отдача фото страницы по заголовку.
func (h *Handler) WikiPhoto(w http.ResponseWriter, r *http.Request) {
	title := r.URL.Query().Get("title")
	if title == "" {
		http.Error(w, "Заголовок не указан", http.StatusBadRequest)

		return
	}

	data, contentType, err := h.wikiUC.GetPhoto(r.Context(), title)
	if err != nil {
		http.Error(w, "Ошибка загрузки фото: "+err.Error(), http.StatusInternalServerError)

		return
	}
	if data == nil {
		http.Error(w, "Фото не найдено", http.StatusNotFound)

		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}
