package http

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"warehouseHelper/internal/complaints/photostore"
	"warehouseHelper/internal/complaints/usecase"
	"warehouseHelper/internal/domain"
)

// Модуль «Жалобы»: обработчики страниц. Шаблоны парсятся один раз при
// старте (правило проекта: не парсить шаблон на каждый запрос).

var (
	complaintsListTmpl   = template.Must(template.ParseFiles("../internal/delivery/web/templates/complaints_list.html"))
	complaintFormTmpl    = template.Must(template.ParseFiles("../internal/delivery/web/templates/complaint_form.html"))
	complaintsSearchTmpl = template.Must(template.ParseFiles("../internal/delivery/web/templates/complaints_search.html"))
	complaintsTagsTmpl   = template.Must(template.ParseFiles("../internal/delivery/web/templates/complaints_tags.html"))
)

const (
	complaintMaxBodyBytes = 1 << 30 // предохранитель размера multipart (~1 ГБ), как в qrcodes
	complaintMaxOrderLen  = 100     // максимум символов в номере заказа МС
)

// complaintPhotoNameRe — допустимое имя фото в архиве (защита от обхода пути).
var complaintPhotoNameRe = regexp.MustCompile(`^[a-f0-9]{16}\.[a-z0-9]{1,8}$`)

// ---- Данные страниц ----

// complaintProductCell — товар в списке/карточке: кликабелен при наличии id
// (товар мог быть удалён из каталога — тогда ссылка на поиск не строится).
type complaintProductCell struct {
	ProductID string
	Name      string
}

// complaintListRow — строка списка «Жалобы» / «Полный список» / результатов
// поиска. Phone — нормализованный (для ссылки поиска), PhoneDisplay —
// «+7 936 123-45-67», Status — русская подпись.
type complaintListRow struct {
	ID           int64
	OrderNumber  string
	Phone        string
	PhoneDisplay string
	CreatedAt    string
	Status       string
	Products     []complaintProductCell
}

// complaintsListData — страница со списком обращений.
type complaintsListData struct {
	Title    string
	All      bool // true — «Полный список» (включая завершённые)
	Rows     []complaintListRow
	Msg      string
	SearchTo string // возврат к параметрам поиска, если пришли из него
}

// complaintFormData — страница обращения (создание или карточка).
type complaintFormData struct {
	IsEdit       bool
	ID           string // пусто при создании
	OrderNumber  string
	Phone        string
	Description  string
	Actions      string
	Status       string // выбранный статус (код)
	Statuses     []domain.ComplaintStatus
	Deadline     string // локальное время, формат datetime-local: 2006-01-02T15:04
	Products     []complaintProductCell
	Photos       []photostore.Photo
	PhotoCount   int
	Error        string
	Msg          string
	SearchReturn string // ссылка «← к списку/поиску», если открыли из поиска
}

// complaintsTagsPageData — страница «Зарегистрировать tg-теги».
type complaintsTagsPageData struct {
	Roles []complaintRoleRow
	Msg   string
	Error string
}

type complaintRoleRow struct {
	Role  domain.ComplaintRole
	Label string
	Tag   string
}

// ---- Преобразования ----

// complaintRowFromSummary собирает строку списка из сводки обращения.
func complaintRowFromSummary(s domain.ComplaintSummary) complaintListRow {
	r := complaintListRow{
		ID:           s.ID,
		OrderNumber:  s.MSOrderNumber,
		Phone:        s.Phone,
		PhoneDisplay: domain.FormatPhone(s.Phone),
		CreatedAt:    s.CreatedAt.In(procLoc).Format("02.01.2006 15:04"),
		Status:       s.Status.StatusLabel(),
	}
	for _, it := range s.Items {
		r.Products = append(r.Products, complaintProductCell{ProductID: it.ProductID, Name: it.ProductName})
	}
	return r
}

func complaintRowsFromSummaries(list []domain.ComplaintSummary) []complaintListRow {
	rows := make([]complaintListRow, 0, len(list))
	for _, s := range list {
		rows = append(rows, complaintRowFromSummary(s))
	}
	return rows
}

func complaintFormDataFromComplaint(c *domain.Complaint, msg, searchReturn string) complaintFormData {
	d := complaintFormData{
		IsEdit:       true,
		ID:           strconv.FormatInt(c.ID, 10),
		OrderNumber:  c.MSOrderNumber,
		Phone:        domain.FormatPhone(c.Phone),
		Description:  c.Description,
		Actions:      c.Actions,
		Status:       string(c.Status),
		Statuses:     domain.ComplaintStatuses,
		Deadline:     c.Deadline.In(procLoc).Format("2006-01-02T15:04"),
		Msg:          msg,
		SearchReturn: searchReturn,
	}
	for _, it := range c.Items {
		d.Products = append(d.Products, complaintProductCell{ProductID: it.ProductID, Name: it.ProductName})
	}
	return d
}

// ---- Общие помощники ----

// procLoc — часовая зона процесса: прод-машина работает в МСК, значение
// datetime-local и даты поиска вводятся в локальном времени сервера.
var procLoc = time.Now().Location()

// errBadDeadlineFormat — значение datetime-local не разобралось.
var errBadDeadlineFormat = errors.New("дедлайн указан в неверном формате")

// closeComplaintFiles закрывает открытые загруженные файлы.
func closeComplaintFiles(files []io.Closer) {
	for _, f := range files {
		_ = f.Close()
	}
}

// parseComplaintID читает id обращения из query/формы.
func parseComplaintID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// parseDeadlineLocal разбирает значение datetime-local как локальное время
// процесса (прод-машина в МСК). Пустая строка — нулевое время.
func parseDeadlineLocal(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	return time.ParseInLocation("2006-01-02T15:04", s, procLoc)
}

// complaintFormResult — разобранный ввод формы обращения.
type complaintFormResult struct {
	in      usecase.ComplaintInput
	uploads []photostore.Upload
	opened  []io.Closer
}

// complaintFormFromRequest собирает ввод формы обращения (поля + товары +
// приложенные фото). Ошибка валидации полей возвращается второй величиной
// (телефон и дедлайн дополнительно проверяет usecase).
func complaintFormFromRequest(r *http.Request) (complaintFormResult, error) {
	var res complaintFormResult
	res.in = usecase.ComplaintInput{
		MSOrderNumber: strings.TrimSpace(r.FormValue("ms_order")),
		Phone:         strings.TrimSpace(r.FormValue("phone")),
		Description:   r.FormValue("description"),
		Actions:       r.FormValue("actions"),
		Status:        domain.ComplaintStatus(r.FormValue("status")),
	}
	if !res.in.Status.Valid() {
		res.in.Status = domain.ComplaintStatusCreated
	}
	dl, err := parseDeadlineLocal(r.FormValue("deadline"))
	if err != nil {
		return res, errBadDeadlineFormat
	}
	res.in.Deadline = dl

	// Товары: параллельные поля product_id[] / product_name[].
	ids := r.Form["product_id"]
	names := r.Form["product_name"]
	for i := 0; i < max(len(ids), len(names)); i++ {
		var id, name string
		if i < len(ids) {
			id = strings.TrimSpace(ids[i])
		}
		if i < len(names) {
			name = strings.TrimSpace(names[i])
		}
		if id == "" && name == "" {
			continue
		}
		res.in.Items = append(res.in.Items, domain.ComplaintItem{ProductID: id, ProductName: name})
	}

	// Фото из multipart (при создании; на карточке фото добавляются
	// отдельным запросом). Ошибки чтения файлов накапливаем — вернём первую.
	files := r.MultipartForm.File["photos"]
	res.opened = make([]io.Closer, 0, len(files))
	var first error
	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		res.opened = append(res.opened, f)
		id, err := newComplaintPhotoID()
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		ext := extFromContentType(fh.Header.Get("Content-Type"))
		if ext == "" {
			if first == nil {
				first = errors.New("файл «" + fh.Filename + "» не похож на изображение")
			}
			continue
		}
		res.uploads = append(res.uploads, photostore.Upload{ID: id, Ext: ext, Data: f})
	}
	return res, first
}

// newComplaintPhotoID — id фото: 16 hex-символов (8 случайных байт).
func newComplaintPhotoID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ---- Страницы ----

// ComplaintsPage — «Жалобы»: активные обращения (статус != Завершено).
func (h *Handler) ComplaintsPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	list, err := h.complaintsUC.ListActive(r.Context())
	if err != nil {
		slog.Info(fmt.Sprintf("complaints: list active: %v", err))
		http.Error(w, "Не удалось загрузить список обращений.", http.StatusInternalServerError)
		return
	}
	renderComplaintsList(w, complaintsListData{
		Title: "Жалобы",
		Rows:  complaintRowsFromSummaries(list),
		Msg:   r.URL.Query().Get("msg"),
	})
}

// ComplaintsAllPage — «Полный список»: все обращения.
func (h *Handler) ComplaintsAllPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	list, err := h.complaintsUC.ListAll(r.Context())
	if err != nil {
		slog.Info(fmt.Sprintf("complaints: list all: %v", err))
		http.Error(w, "Не удалось загрузить список обращений.", http.StatusInternalServerError)
		return
	}
	renderComplaintsList(w, complaintsListData{
		Title: "Полный список",
		All:   true,
		Rows:  complaintRowsFromSummaries(list),
		Msg:   r.URL.Query().Get("msg"),
	})
}

func renderComplaintsList(w http.ResponseWriter, data complaintsListData) {
	if err := complaintsListTmpl.Execute(w, data); err != nil {
		slog.Info(fmt.Sprintf("complaints: render list: %v", err))
	}
}

// ComplaintForm — карточка обращения (просмотр/редактирование), GET.
func (h *Handler) ComplaintForm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, ok := parseComplaintID(r)
	if !ok {
		http.Error(w, "Не указан id обращения.", http.StatusBadRequest)
		return
	}
	c, err := h.complaintsUC.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, domain.ErrComplaintNotFound) {
			http.Error(w, "Обращение не найдено.", http.StatusNotFound)
			return
		}
		slog.Info(fmt.Sprintf("complaints: get %d: %v", id, err))
		http.Error(w, "Не удалось загрузить обращение.", http.StatusInternalServerError)
		return
	}
	photos, err := h.complaintsUC.Photos(r.Context(), id)
	if err != nil {
		slog.Info(fmt.Sprintf("complaints: photos %d: %v", id, err))
		photos = nil
	}
	data := complaintFormDataFromComplaint(c, r.URL.Query().Get("msg"), complaintSearchReturn(r))
	data.Photos = photos
	data.PhotoCount = len(photos)
	if err := complaintFormTmpl.Execute(w, data); err != nil {
		slog.Info(fmt.Sprintf("complaints: render form: %v", err))
	}
}

// complaintSearchReturn — адрес возврата из карточки в результаты поиска.
// Принимаются только внутренние пути (с ведущим «/» и не «//host»):
// javascript:-схемы и внешние URL из query-параметра отбрасываются.
func complaintSearchReturn(r *http.Request) string {
	from := r.URL.Query().Get("from")
	if from == "" || from[0] != '/' || (len(from) > 1 && from[1] == '/') {
		return ""
	}
	return from
}

// ComplaintNewForm — форма создания обращения, GET.
func (h *Handler) ComplaintNewForm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	data := complaintFormData{
		Statuses: domain.ComplaintStatuses,
		Status:   string(domain.ComplaintStatusCreated),
	}
	if err := complaintFormTmpl.Execute(w, data); err != nil {
		slog.Info(fmt.Sprintf("complaints: render new form: %v", err))
	}
}

// ComplaintCreate — сохранение нового обращения, POST (multipart).
func (h *Handler) ComplaintCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, complaintMaxBodyBytes)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "Не удалось прочитать отправленные данные.", http.StatusBadRequest)
		return
	}
	renderErr := func(msg string) {
		data := complaintFormData{
			OrderNumber: strings.TrimSpace(r.FormValue("ms_order")),
			Phone:       r.FormValue("phone"),
			Description: r.FormValue("description"),
			Actions:     r.FormValue("actions"),
			Statuses:    domain.ComplaintStatuses,
			Status:      r.FormValue("status"),
			Deadline:    r.FormValue("deadline"),
			Error:       msg,
		}
		for i := 0; i < len(r.Form["product_id"]); i++ {
			data.Products = append(data.Products, complaintProductCell{ProductID: r.Form["product_id"][i], Name: formName(r, i)})
		}
		_ = complaintFormTmpl.Execute(w, data)
	}

	res, firstErr := complaintFormFromRequest(r)
	if firstErr != nil {
		closeComplaintFiles(res.opened)
		renderErr(complaintSaveError(firstErr))
		return
	}
	if len(res.uploads) > 0 {
		defer closeComplaintFiles(res.opened)
	}

	id, err := h.complaintsUC.Create(r.Context(), res.in, res.uploads)
	if err != nil {
		renderErr(complaintSaveError(err))
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/complaint?id=%d&msg=%s", id, url.QueryEscape("Обращение создано")), http.StatusSeeOther)
}

func formName(r *http.Request, i int) string {
	if i < len(r.Form["product_name"]) {
		return r.Form["product_name"][i]
	}
	return ""
}

// ComplaintSave — сохранение изменений карточки, POST.
func (h *Handler) ComplaintSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, ok := parseComplaintID(r)
	if !ok {
		http.Error(w, "Не указан id обращения.", http.StatusBadRequest)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "Не удалось прочитать отправленные данные.", http.StatusBadRequest)
		return
	}
	res, firstErr := complaintFormFromRequest(r)
	closeComplaintFiles(res.opened) // фото на карточке добавляются отдельным POST /complaint/photo/add
	if firstErr != nil {
		h.complaintFormErr(w, r, id, complaintSaveError(firstErr))
		return
	}
	if err := h.complaintsUC.Update(r.Context(), id, res.in); err != nil {
		h.complaintFormErr(w, r, id, complaintSaveError(err))
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/complaint?id=%d&msg=%s", id, url.QueryEscape("Сохранено")), http.StatusSeeOther)
}

// complaintFormErr перерисовывает карточку с ошибкой (без потери ввода):
// берёт актуальное обращение из БД и подставляет введённые значения.
func (h *Handler) complaintFormErr(w http.ResponseWriter, r *http.Request, id int64, msg string) {
	c, err := h.complaintsUC.Get(r.Context(), id)
	if err != nil {
		http.Error(w, msg, http.StatusInternalServerError)
		return
	}
	photos, _ := h.complaintsUC.Photos(r.Context(), id)
	data := complaintFormDataFromComplaint(c, "", r.FormValue("from"))
	// Введённые значения поверх данных из БД.
	if v := strings.TrimSpace(r.FormValue("order_number")); v != "" {
		data.OrderNumber = v
	}
	if v := r.FormValue("phone"); v != "" {
		data.Phone = v
	}
	if v := r.FormValue("description"); v != "" {
		data.Description = v
	}
	if v := r.FormValue("actions"); v != "" {
		data.Actions = v
	}
	if v := r.FormValue("deadline"); v != "" {
		data.Deadline = v
	}
	if v := r.FormValue("status"); v != "" {
		data.Status = v
	}
	data.Products = nil
	for i := 0; i < len(r.Form["product_id"]); i++ {
		data.Products = append(data.Products, complaintProductCell{
			ProductID: r.Form["product_id"][i],
			Name:      formName(r, i),
		})
	}
	data.Photos = photos
	data.PhotoCount = len(photos)
	data.Error = msg
	if err := complaintFormTmpl.Execute(w, data); err != nil {
		slog.Info(fmt.Sprintf("complaints: render form error: %v", err))
	}
}

// complaintSaveError превращает ошибку usecase в сообщение для формы.
func complaintSaveError(err error) string {
	switch {
	case errors.Is(err, usecase.ErrComplaintNoItems):
		return "Добавьте хотя бы один товар."
	case errors.Is(err, usecase.ErrComplaintBadPhone):
		return "Не удалось распознать номер телефона. Примеры: +79361234567, 8-936-123-45-67, 9361234567."
	case errors.Is(err, usecase.ErrComplaintDeadlinePast):
		return "Дедлайн должен быть позже текущего момента."
	case errors.Is(err, errBadDeadlineFormat):
		return "Дедлайн указан в неверном формате."
	default:
		return "Не удалось сохранить обращение. Попробуйте ещё раз."
	}
}

// ---- Поиск ----

// ComplaintsSearch — страница поиска, GET: форма + результаты (если задан
// хотя бы один фильтр).
func (h *Handler) ComplaintsSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	data := complaintsSearchData{
		OrderNumber: q.Get("ms_order"),
		Phone:       q.Get("phone"),
		ProductID:   q.Get("product_id"),
		ProductName: q.Get("product_name"),
		DateFrom:    q.Get("date_from"),
		DateTo:      q.Get("date_to"),
	}
	if !complaintHasSearchFilter(data) {
		_ = complaintsSearchTmpl.Execute(w, data)
		return
	}
	if data.ProductName != "" && data.ProductID == "" {
		// Текст в поле товара без выбора из подсказок каталога: поиск идёт
		// только по товарам базы, фильтра по «сырому» названию нет.
		data.Error = "Выберите товар из выпадающего списка, затем нажмите «Найти»."
		_ = complaintsSearchTmpl.Execute(w, data)
		return
	}
	data.Searched = true
	f := domain.ComplaintFilter{
		MSOrderNumber: data.OrderNumber,
		Phone:         data.Phone,
		ProductID:     data.ProductID,
	}
	if data.DateFrom != "" {
		from, err := time.ParseInLocation("2006-01-02", data.DateFrom, procLoc)
		if err != nil {
			data.Error = "Дата «с» указана неверно."
			_ = complaintsSearchTmpl.Execute(w, data)
			return
		}
		f.From = from
	}
	if data.DateTo != "" {
		to, err := time.ParseInLocation("2006-01-02", data.DateTo, procLoc)
		if err != nil {
			data.Error = "Дата «по» указана неверно."
			_ = complaintsSearchTmpl.Execute(w, data)
			return
		}
		f.To = to
	}
	list, err := h.complaintsUC.Search(r.Context(), f)
	if err != nil {
		data.Error = complaintSearchError(err)
	} else {
		data.Rows = complaintRowsFromSummaries(list)
	}
	_ = complaintsSearchTmpl.Execute(w, data)
}

// complaintHasSearchFilter — задан хотя бы один фильтр поиска.
func complaintHasSearchFilter(d complaintsSearchData) bool {
	return d.OrderNumber != "" || d.Phone != "" || d.ProductID != "" ||
		d.DateFrom != "" || d.DateTo != ""
}

type complaintsSearchData struct {
	OrderNumber string
	Phone       string
	ProductID   string
	ProductName string
	DateFrom    string
	DateTo      string
	Searched    bool // true — поиск выполнялся (чтобы показать «ничего не найдено»)
	Rows        []complaintListRow
	Error       string
}

func complaintSearchError(err error) string {
	switch {
	case errors.Is(err, usecase.ErrComplaintBadPhone):
		return "Не удалось распознать номер телефона. Примеры: +79361234567, 8-936-123-45-67, 9361234567."
	default:
		return "Не удалось выполнить поиск. Попробуйте ещё раз."
	}
}

// ---- Теги ролей ----

// ComplaintsTagsPage — страница регистрации tg-тегов, GET (форма) и POST
// (сохранение/удаление тега роли).
func (h *Handler) ComplaintsTags(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		roles, err := h.complaintsRoleTagsData(r.Context())
		if err != nil {
			http.Error(w, "Не удалось загрузить теги.", http.StatusInternalServerError)
			return
		}
		data := complaintsTagsPageData{Roles: roles, Msg: r.URL.Query().Get("msg")}
		_ = complaintsTagsTmpl.Execute(w, data)
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Не удалось прочитать форму.", http.StatusBadRequest)
			return
		}
		action := r.FormValue("action")
		role := domain.ComplaintRole(r.FormValue("role"))
		var err error
		switch action {
		case "save":
			err = h.complaintsUC.SetRoleTag(r.Context(), role, strings.TrimSpace(r.FormValue("tag")))
		case "delete":
			err = h.complaintsUC.DeleteRoleTag(r.Context(), role)
		default:
			err = errors.New("неизвестное действие")
		}
		if err != nil {
			roles, rerr := h.complaintsRoleTagsData(r.Context())
			if rerr != nil {
				http.Error(w, "Не удалось загрузить теги.", http.StatusInternalServerError)
				return
			}
			data := complaintsTagsPageData{Roles: roles, Error: "Не удалось сохранить тег."}
			_ = complaintsTagsTmpl.Execute(w, data)
			return
		}
		http.Redirect(w, r, "/complaints/tags?msg="+url.QueryEscape("Сохранено"), http.StatusSeeOther)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) complaintsRoleTagsData(ctx context.Context) ([]complaintRoleRow, error) {
	tags, err := h.complaintsUC.RoleTags(ctx)
	if err != nil {
		return nil, err
	}
	byRole := make(map[domain.ComplaintRole]string)
	for _, t := range tags {
		byRole[t.Role] = t.Tag
	}
	roles := []complaintRoleRow{
		{Role: domain.ComplaintRoleValidator, Label: domain.ComplaintRoleValidator.RoleLabel(), Tag: byRole[domain.ComplaintRoleValidator]},
		{Role: domain.ComplaintRoleWarehouse, Label: domain.ComplaintRoleWarehouse.RoleLabel(), Tag: byRole[domain.ComplaintRoleWarehouse]},
	}
	return roles, nil
}

// ---- Фото ----

// ComplaintPhotoAdd — добавление фото к обращению, POST (multipart,
// поле photos). Вызывается с карточки обращения.
func (h *Handler) ComplaintPhotoAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, ok := parseComplaintID(r)
	if !ok {
		http.Error(w, "Не указан id обращения.", http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, complaintMaxBodyBytes)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "Не удалось прочитать файлы.", http.StatusBadRequest)
		return
	}
	res, firstErr := complaintFormFromRequest(r)
	if firstErr != nil {
		closeComplaintFiles(res.opened)
		http.Redirect(w, r, fmt.Sprintf("/complaint?id=%d&msg=%s", id, url.QueryEscape(complaintSaveError(firstErr))), http.StatusSeeOther)
		return
	}
	if len(res.uploads) == 0 {
		closeComplaintFiles(res.opened)
		http.Redirect(w, r, fmt.Sprintf("/complaint?id=%d&msg=%s", id, url.QueryEscape("Выберите фотографии")), http.StatusSeeOther)
		return
	}
	defer closeComplaintFiles(res.opened)
	if err := h.complaintsUC.AddPhotos(r.Context(), id, res.uploads); err != nil {
		slog.Info(fmt.Sprintf("complaints: add photos to %d: %v", id, err))
		http.Redirect(w, r, fmt.Sprintf("/complaint?id=%d&msg=%s", id, url.QueryEscape("Фото не сохранились")), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/complaint?id=%d", id), http.StatusSeeOther)
}

// ComplaintPhotoDelete — удаление фото обращения, POST.
func (h *Handler) ComplaintPhotoDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, ok := parseComplaintID(r)
	if !ok {
		http.Error(w, "Не указан id обращения.", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	if !complaintPhotoNameRe.MatchString(name) {
		http.Error(w, "Неверное имя фото.", http.StatusBadRequest)
		return
	}
	if err := h.complaintsUC.DeletePhoto(r.Context(), id, name); err != nil {
		slog.Info(fmt.Sprintf("complaints: delete photo %s of %d: %v", name, id, err))
	}
	http.Redirect(w, r, fmt.Sprintf("/complaint?id=%d", id), http.StatusSeeOther)
}

// ComplaintPhotoFile раздаёт фото обращения (GET). Имя фото валидируется
// регуляркой — листинга и обхода каталогов нет.
func (h *Handler) ComplaintPhotoFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, ok := parseComplaintID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	name := r.URL.Query().Get("name")
	if !complaintPhotoNameRe.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	rc, err := h.complaintsUC.OpenPhoto(r.Context(), id, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() {
		if err := rc.Close(); err != nil {
			slog.Info(fmt.Sprintf("complaints: close photo: %v", err))
		}
	}()
	ext := name[strings.LastIndexByte(name, '.')+1:]
	if ct := mime.TypeByExtension("." + ext); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, rc); err != nil {
		slog.Info(fmt.Sprintf("complaints: serve photo: %v", err))
	}
}
