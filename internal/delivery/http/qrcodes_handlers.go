package http

import (
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"fmt"
	"log/slog"
	"warehouseHelper/internal/domain"
	qucase "warehouseHelper/internal/qrcodes/usecase"
)

// Шаблоны модуля «Честный знак» парсятся один раз при старте (правило проекта:
// не парсить шаблон на каждый запрос).
var (
	qrAddTmpl  = template.Must(template.ParseFiles("../internal/delivery/web/templates/qrcodes_add.html"))
	qrListTmpl = template.Must(template.ParseFiles("../internal/delivery/web/templates/qrcodes_list.html"))
)

const (
	// qrMaxBodyBytes — жёсткий предохранитель размера тела запроса (~1 ГБ):
	// ограничения на КОЛИЧЕСТВО фото нет, лимит — только диск сервера.
	qrMaxBodyBytes    = 1 << 30
	qrMaxOrderNumLen  = 100 // максимум символов в номере заказа
	qrOrderNumTooLong = "Номер заказа слишком длинный: максимум 100 символов."
)

// QRAddPageData — данные формы «Добавить коды».
type QRAddPageData struct {
	OrderNumber string
	Error       string
	Msg         string
}

// QRListPageData — данные таблицы «Получить коды».
type QRListPageData struct {
	Orders []domain.QROrder
}

// QRPage — главная страница модуля «Честный знак» с кнопками
// «Добавить коды» и «Получить коды».
func (h *Handler) QRPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	http.ServeFile(w, r, "../internal/delivery/web/templates/qrcodes.html")
}

// QRAdd — форма «Введите номер заказа» + фотографии (GET) и сохранение (POST).
func (h *Handler) QRAdd(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.renderQRAdd(w, QRAddPageData{
			OrderNumber: r.URL.Query().Get("order_number"),
			Msg:         r.URL.Query().Get("msg"),
		})
	case http.MethodPost:
		h.saveQRPhotos(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) renderQRAdd(w http.ResponseWriter, data QRAddPageData) {
	if err := qrAddTmpl.Execute(w, data); err != nil {
		slog.Info(fmt.Sprintf("qrcodes: render add form: %v", err))
	}
}

func (h *Handler) saveQRPhotos(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, qrMaxBodyBytes)
	// maxMemory — порог спула multipart на диск: файлы сверх 32 МБ пишутся
	// во временные файлы, а не держатся в памяти.
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		slog.Info(fmt.Sprintf("qrcodes: parse multipart form: %v", err))
		h.renderQRAdd(w, QRAddPageData{Error: "Не удалось прочитать отправленные данные. Попробуйте ещё раз."})

		return
	}

	orderNumber := strings.TrimSpace(r.FormValue("order_number"))
	if orderNumber == "" {
		h.renderQRAdd(w, QRAddPageData{Error: "Введите номер заказа."})

		return
	}
	if len(orderNumber) > qrMaxOrderNumLen {
		h.renderQRAdd(w, QRAddPageData{OrderNumber: orderNumber, Error: qrOrderNumTooLong})

		return
	}

	files := r.MultipartForm.File["photos"]
	if len(files) == 0 {
		h.renderQRAdd(w, QRAddPageData{OrderNumber: orderNumber, Error: "Добавьте хотя бы одну фотографию."})

		return
	}

	uploads := make([]qucase.PhotoUpload, 0, len(files))
	opened := make([]io.Closer, 0, len(files))
	closeOpened := func() {
		for _, f := range opened {
			_ = f.Close()
		}
	}

	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			closeOpened()
			slog.Info(fmt.Sprintf("qrcodes: open uploaded photo: %v", err))
			h.renderQRAdd(w, QRAddPageData{OrderNumber: orderNumber, Error: "Фото не сохранилось. Переснимите фотографии и попробуйте ещё раз."})

			return
		}
		opened = append(opened, f)

		ext := extFromContentType(fh.Header.Get("Content-Type"))
		if ext == "" {
			closeOpened()
			h.renderQRAdd(w, QRAddPageData{
				OrderNumber: orderNumber,
				Error:       "Файл «" + fh.Filename + "» не похож на изображение. Сфотографируйте коды заново.",
			})

			return
		}

		uploads = append(uploads, qucase.PhotoUpload{Ext: ext, Data: f})
	}

	saved, err := h.qrUC.SavePhotos(r.Context(), orderNumber, uploads)
	closeOpened()
	if err != nil {
		slog.Info(fmt.Sprintf("qrcodes: save photos for order %q: %v", orderNumber, err))
		msg := "Фото не сохранилось. Переснимите фотографии и попробуйте ещё раз."
		if errors.Is(err, qucase.ErrEmptyOrderNumber) {
			msg = "Введите номер заказа."
		} else if errors.Is(err, qucase.ErrNoPhotos) {
			msg = "Добавьте хотя бы одну фотографию."
		}
		h.renderQRAdd(w, QRAddPageData{OrderNumber: orderNumber, Error: msg})

		return
	}

	http.Redirect(w, r, "/qrcodes/add?msg="+url.QueryEscape("Сохранено "+strconv.Itoa(saved)+" фото"), http.StatusSeeOther)
}

// QRList — таблица заказов с миниатюрами фотографий. Очистка устаревших
// фото здесь не выполняется: она привязана к сохранению новых фото (не чаще
// раза в день), чтобы просмотр списка не делал лишней работы на диске и в БД.
func (h *Handler) QRList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	orders, err := h.qrUC.ListOrders(r.Context())
	if err != nil {
		slog.Info(fmt.Sprintf("qrcodes: list orders: %v", err))
		http.Error(w, "Не удалось загрузить список. Попробуйте позже.", http.StatusInternalServerError)

		return
	}

	if err := qrListTmpl.Execute(w, QRListPageData{Orders: orders}); err != nil {
		slog.Info(fmt.Sprintf("qrcodes: render list: %v", err))
	}
}

// qrPhotoPathRe — допустимый путь к файлу фото внутри корня QRCodes:
// QRCodes/<id>.<ext> (новая схема) или QRCodes/<id>/photo.<ext> (старая
// схема) — без листинга и обхода каталогов.
var qrPhotoPathRe = regexp.MustCompile(`^([a-f0-9]{16})(?:/photo)?\.([a-z0-9]{1,8})$`)

// qrPhotosHandler раздаёт файлы фото из корня QRCodes: принимает только
// точные пути <id>.<ext> и <id>/photo.<ext>, листинг каталогов не отдаёт,
// на все ответы ставит X-Content-Type-Options: nosniff (защита от
// переинтерпретации содержимого как HTML/JS). Если файла новой схемы
// <id>.<ext> нет — пробует старую схему <id>/photo.<ext>, чтобы фото,
// сохранённые до перехода на плоские файлы, продолжали отображаться.
func qrPhotosHandler(dir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

			return
		}

		rel := strings.TrimPrefix(r.URL.Path, "/")
		m := qrPhotoPathRe.FindStringSubmatch(rel)
		if m == nil {
			http.NotFound(w, r)

			return
		}

		path := filepath.Join(dir, rel)
		if _, err := os.Stat(path); err != nil {
			// Старая схема: <id>/photo.<ext>.
			path = filepath.Join(dir, m[1], "photo."+m[2])
		}

		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFile(w, r, path)
	})
}

// extFromContentType возвращает расширение файла по Content-Type изображения;
// для неподдерживаемого типа — пустую строку.
func extFromContentType(ct string) string {
	switch strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0])) {
	case "image/jpeg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	case "image/heic":
		return "heic"
	case "image/heif":
		return "heif"
	default:
		return ""
	}
}
