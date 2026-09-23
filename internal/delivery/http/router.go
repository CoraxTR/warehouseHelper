package http

import (
	"net/http"

	"warehouseHelper/internal/metrics"
)

func NewRouter(h *Handler) *http.ServeMux {
	mux := http.NewServeMux()

	fs := http.FileServer(http.Dir("../internal/delivery/web/static"))

	mux.Handle("/static/", http.StripPrefix("/static/", fs))
	mux.HandleFunc("/", h.Home)
	mux.HandleFunc("/sync", h.Sync)                     // POST
	mux.HandleFunc("/refgo", h.RefGoPage)               // GET
	mux.HandleFunc("/refgo-check", h.RefGoCheckAgainst) // GET — страница сверки, POST — запуск сверки
	mux.HandleFunc("/order-find", h.OrderFind)          // GET — форма поиска, POST — поиск заказа
	mux.HandleFunc("/orders", h.Orders)                 // GET
	mux.HandleFunc("/orders/update", h.UpdateOrders)    // POST
	mux.HandleFunc("/export", h.ExportToExcel)
	mux.HandleFunc("/download", h.DownloadFile)
	mux.HandleFunc("/update-from-ms", h.UpdateFromMS) // POST
	mux.HandleFunc("/print-form", h.PrintForm)
	mux.HandleFunc("/print-multiple-forms", h.PrintMultipleForms)          // POST
	mux.HandleFunc("/orders/delete", h.DeleteOrder)                        // DELETE
	mux.HandleFunc("/print-barcodes", h.PrintBarcodes)                     // POST
	mux.HandleFunc("/wiki", h.WikiIndex)                                   // GET
	mux.HandleFunc("/wiki/page", h.WikiPage)                               // GET
	mux.HandleFunc("/wiki/edit", h.WikiEdit)                               // GET — форма, POST — сохранение
	mux.HandleFunc("/wiki/delete", h.WikiDelete)                           // POST
	mux.HandleFunc("/wiki/photo", h.WikiPhoto)                             // GET
	mux.HandleFunc("GET /goods", h.GoodsPage)                              // хаб «Продукция» (поиск + добавление)
	mux.HandleFunc("POST /goods/search", h.GoodsSearch)                    // поиск позиции в каталоге
	mux.HandleFunc("GET /goods/edit", h.GoodsEditPage)                     // редактирование позиции
	mux.HandleFunc("POST /goods/edit", h.GoodsEditSave)                    // сохранение ручных правок
	mux.HandleFunc("POST /goods/resync", h.GoodsResync)                    // ресинк позиции из МС
	mux.HandleFunc("GET /goods/tree", h.GoodsTreePage)                     // дерево папок для добавления
	mux.HandleFunc("POST /goods/tree", h.GoodsExport)                      // выгрузка отмеченных товаров в каталог
	mux.HandleFunc("GET /goods/search/json", h.GoodsSearchJSON)            // поиск товаров для виджета поставщика (JSON)
	mux.HandleFunc("GET /goods/availability", h.AvailabilityPage)          // календарь «Доступность товаров»
	mux.HandleFunc("POST /goods/availability/save", h.AvailabilitySave)    // сохранить доступность на даты
	mux.HandleFunc("GET /goods/stock-report", h.StockReportPage)           // «Отчёт по наличию»
	mux.HandleFunc("POST /goods/stock-report/export", h.StockReportExport) // выгрузка отчёта xlsx
	// «Возврат в продажу»: список событий (?e нет), карточка (?e=<id>),
	// приём возврата и ручное закрытие (URL-кнопка «Расформировать» в чате склада).
	mux.HandleFunc("GET /goods/return", h.ReturnsPage)                    // список / карточка события
	mux.HandleFunc("POST /goods/return/save", h.ReturnsSave)              // принять возврат (JSON-сканы)
	mux.HandleFunc("POST /goods/return/close", h.ReturnsClose)            // закрыть вручную
	mux.HandleFunc("GET /goods/return/manual", h.ReturnsManualPage)       // ручной возврат: страница
	mux.HandleFunc("POST /goods/return/manual/save", h.ReturnsManualSave) // ручной возврат: принять сканы
	// «Вывод из продажи»: зеркало ручного возврата — сканы кусков снимают их
	// со сроков (остатков), документ МС не создаётся.
	mux.HandleFunc("GET /goods/withdraw", h.GoodsWithdrawPage)       // вывод из продажи: страница
	mux.HandleFunc("POST /goods/withdraw/save", h.GoodsWithdrawSave) // вывод из продажи: списать сканы
	mux.HandleFunc("GET /goods/box", h.GoodsBoxPage)                 // «Создать коробку»: страница сканирования кусков
	mux.HandleFunc("POST /goods/box/save", h.GoodsBoxSave)           // «Создать коробку»: наклейка коробки (xlsx)
	mux.HandleFunc("/qrcodes", h.QRPage)                             // GET — модуль «Честный знак»
	mux.HandleFunc("/qrcodes/add", h.QRAdd)                          // GET — форма, POST — сохранение фото
	mux.HandleFunc("/qrcodes/list", h.QRList)                        // GET — таблица заказов с фото
	mux.Handle("/qrcodes/photos/", http.StripPrefix("/qrcodes/photos/", qrPhotosHandler(h.qrUC.PhotosDir())))

	// Модуль «МойСклад»: хаб и справочник поставщиков.
	mux.HandleFunc("/ms", h.MsPage)                              // GET — хаб
	mux.HandleFunc("/ms/suppliers", h.SuppliersList)             // GET — список поставщиков
	mux.HandleFunc("/ms/suppliers/new", h.SupplierNew)           // GET — форма создания
	mux.HandleFunc("/ms/suppliers/edit", h.SupplierEdit)         // GET — форма редактирования
	mux.HandleFunc("/ms/suppliers/save", h.SupplierSave)         // POST — создание/обновление
	mux.HandleFunc("/ms/suppliers/delete", h.SupplierDelete)     // POST — удаление
	mux.HandleFunc("GET /ms/orders", h.MSOrdersPage)             // раздел «Заказы»
	mux.HandleFunc("GET /ms/orders/pick", h.MSOrdersPickForm)    // подбор: форма/результат (?name=)
	mux.HandleFunc("POST /ms/orders/pick", h.MSOrdersPickSearch) // подбор: запуск поиска (PRG → GET ?name=)
	// Печать бланков за день: список заказов по плановой дате доставки и
	// слитый PDF по выделенным (литерал /ms/orders/forms специфичнее {id}).
	mux.HandleFunc("GET /ms/orders/forms", h.MSOrdersFormsForm)         // форма/список (?date=)
	mux.HandleFunc("POST /ms/orders/forms", h.MSOrdersFormsSearch)      // смена даты (PRG → GET ?date=)
	mux.HandleFunc("POST /ms/orders/forms/print", h.MSOrdersFormsPrint) // PDF по выделенным заказам
	// Детальная страница заказа (литерал /ms/orders/pick специфичнее {id} — конфликта нет).
	mux.HandleFunc("GET /ms/orders/{id}", h.MSOrderDetailPage)
	// Отправка подбора в МС (итерация 3): сервер пересобирает positions из кэша
	// страницы и PUT-ит заказ; 200 — смена локации на /ms/orders/pick на клиенте.
	mux.HandleFunc("POST /ms/orders/{id}/submit", h.MSOrderSubmit)
	// Ручное подтверждение подбора: оператор вводит вес/количество строки
	// вместо сканов (qty = reserve = введённое), сроки не списываются —
	// складу уходит уведомление о пересчёте остатков.
	mux.HandleFunc("POST /ms/orders/{id}/submit-manual", h.MSOrderSubmitManual)
	// Возврат в сроки при переподборе: приём вернувшихся кусков (остатки по
	// срокам этикеток) и ручное закрытие с уведомлением складу о пересчёте.
	mux.HandleFunc("POST /ms/orders/{id}/return", h.MSOrderReturnSave)
	mux.HandleFunc("POST /ms/orders/{id}/return/close", h.MSOrderReturnClose)

	// Модуль «Жалобы»: обращения клиентов с фото и статусами.
	mux.HandleFunc("GET /complaints", h.ComplaintsPage)                    // активные обращения (статус != «Завершено»)
	mux.HandleFunc("GET /complaints/all", h.ComplaintsAllPage)             // полный список
	mux.HandleFunc("GET /complaints/new", h.ComplaintNewForm)              // форма создания
	mux.HandleFunc("POST /complaints/new", h.ComplaintCreate)              // создание (multipart: поля + фото)
	mux.HandleFunc("GET /complaints/search", h.ComplaintsSearch)           // поиск обращений
	mux.HandleFunc("GET /complaints/tags", h.ComplaintsTags)               // страница tg-тегов
	mux.HandleFunc("POST /complaints/tags", h.ComplaintsTags)              // сохранить/удалить тег роли
	mux.HandleFunc("GET /complaint", h.ComplaintForm)                      // карточка обращения (просмотр/редактирование)
	mux.HandleFunc("POST /complaint/save", h.ComplaintSave)                // сохранить карточку
	mux.HandleFunc("POST /complaint/photo/add", h.ComplaintPhotoAdd)       // добавить фото (multipart)
	mux.HandleFunc("POST /complaint/photo/delete", h.ComplaintPhotoDelete) // удалить фото
	mux.HandleFunc("GET /complaint/photo", h.ComplaintPhotoFile)           // раздача фото из архива (img src)

	// Метрики приложения для Prometheus (скрейпит внешний сервер).
	mux.Handle("/metrics", metrics.Handler())

	// Приёмка: виджет «Внешние коды» на карточке поставщика.
	mux.HandleFunc("POST /ms/receive/barcodes/add", h.ReceiveBarcodesAdd)
	mux.HandleFunc("POST /ms/receive/barcodes/delete", h.ReceiveBarcodesDelete)
	mux.HandleFunc("GET /ms/receive", h.ReceivePage)                        // страница приёмки (выбор поставщика / сканирование)
	mux.HandleFunc("GET /ms/receive/cache", h.ReceiveCache)                 // кеш приёмки (JSON для резолва на клиенте)
	mux.HandleFunc("POST /ms/receive/save", h.ReceiveSave)                  // сохранить приёмку (JSON) → отчёт
	mux.HandleFunc("POST /ms/receive/labels", h.ReceiveLabels)              // этикетки принятых кусков (xlsx)
	mux.HandleFunc("POST /ms/receive/box-labels", h.ReceiveBoxLabels)       // наклейки принятых коробок (xlsx)
	mux.HandleFunc("GET /ms/receive/break-label", h.ReceiveBreakLabel)      // наклейка спец-кода 666 (xlsx)
	mux.HandleFunc("GET /ms/receive/open-box-label", h.ReceiveOpenBoxLabel) // наклейка спец-кода 555 (xlsx)

	// Модуль «Сроки» (остатки по срокам годности).
	mux.HandleFunc("GET /ms/dates", h.StockDatesPage)               // страница «Сроки»
	mux.HandleFunc("GET /ms/dates/short", h.StockShortPage)         // страница «Шорт-лист»
	mux.HandleFunc("GET /ms/dates/ws", h.StockDatesWS)              // вебсокет: снапшот и дельты
	mux.HandleFunc("POST /ms/dates/discount", h.StockDiscount)      // запись ручной скидки
	mux.HandleFunc("GET /ms/dates/update", h.StockUpdatePage)       // страница «Обновить сроки»
	mux.HandleFunc("GET /ms/discounts", h.DiscountsPage)            // страница «Скидки» (окно и допродажа)
	mux.HandleFunc("GET /ms/discounts/active", h.DiscountsActive)   // активные пары окна (JSON, для страницы «Сроки»)
	mux.HandleFunc("POST /ms/dates/update/save", h.StockUpdateSave) // применить батч сканов

	return mux
}
