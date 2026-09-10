package usecase

import (
	"context"

	"warehouseHelper/internal/metrics"
)

// (trackPkg объявлена в первом файле пакета)

// PDFPreloader — предзагрузка бланков в фоне (модуль refgo). Перед печатью
// предзагрузка останавливается: качаем ровно то, что запросил оператор.
type PDFPreloader interface {
	StopPreloading()
}

// PDFForms — печать бланков заказов нижним слоем pdfexport (общий с msorders):
// бланк одного заказа и пачка бланков слитым файлом. GetMultipleOrdersPDF
// вторым значением отдаёт id бланков, которые получить не удалось.
type PDFForms interface {
	GetOrderPDF(ctx context.Context, id string) (string, error)
	GetMultipleOrdersPDF(ctx context.Context, ids []string) (string, []string, error)
}

// ExportOrderPDFUseCase — печать бланков заказов РефГо. Сам экспорт/слияние
// живёт в нижнем слое pdfexport (общий с модулем msorders); здесь остаются
// метрики и остановка фоновой предзагрузки.
type ExportOrderPDFUseCase struct {
	forms     PDFForms
	preloader PDFPreloader
}

func NewExportOrderPDFUseCase(forms PDFForms, preloader PDFPreloader) *ExportOrderPDFUseCase {
	return &ExportOrderPDFUseCase{
		forms:     forms,
		preloader: preloader,
	}
}

// GetOrderPDF — бланк одного заказа (кэш либо скачивание с повторами).
func (uc *ExportOrderPDFUseCase) GetOrderPDF(ctx context.Context, id string) (string, error) {
	done := metrics.Track(trackPkg, "GetOrderPDF")
	defer done()

	uc.preloader.StopPreloading()

	return uc.forms.GetOrderPDF(ctx, id)
}

// GetMultipleOrdersPDF — пачка бланков одним PDF (порядок ids = порядок
// страниц). Второе значение — id бланков, которые скачать не удалось: они
// пропущены в файле (провалы в логе), хендлер отдаёт их клиенту заголовком,
// чтобы оператор не остался в неведении.
func (uc *ExportOrderPDFUseCase) GetMultipleOrdersPDF(ctx context.Context, ids []string) (string, []string, error) {
	done := metrics.Track(trackPkg, "GetMultipleOrdersPDF")
	defer done()

	uc.preloader.StopPreloading()

	return uc.forms.GetMultipleOrdersPDF(ctx, ids)
}
