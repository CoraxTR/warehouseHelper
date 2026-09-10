// Package pdfexport — нижний слой работы с PDF бланков заказов: валидация и
// запись одиночного бланка (pdfcpu) и слияние пачки бланков в один файл.
// Собственной привязки к модулю нет: источник бланков передаётся интерфейсом
// Fetcher (реализация — msclient), поэтому пакет импортируют и refgo, и msorders.
package pdfexport

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"warehouseHelper/internal/tempdir"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// Exporter — запись PDF-файлов в temp-директорию приложения.
type Exporter struct{}

// NewExporter создаёт экспортёр PDF.
func NewExporter() *Exporter {
	return &Exporter{}
}

// ExportOrderPDF валидирует бланк и пишет его файлом в temp-директорию,
// возвращая путь к файлу.
func (e *Exporter) ExportOrderPDF(data []byte) (string, error) {
	conf := model.NewDefaultConfiguration()

	validated, err := api.ReadAndValidate(bytes.NewReader(data), conf)
	if err != nil {
		slog.Info(fmt.Sprintf("couldn't validate, %v", err))
	}

	outFile, err := os.Create(filepath.Join(tempdir.Dir, "exported.pdf"))
	if err != nil {
		return "", err
	}

	defer func() {
		err := outFile.Close()
		if err != nil {
			slog.Error(fmt.Sprintf("Failed to close file: %v", err))
		}
	}()

	err = api.Write(validated, outFile, conf)
	if err != nil {
		return "", err
	}
	// Файлы сохраняются во временную директорию (tempdir.Dir)
	return outFile.Name(), nil
}

// ExportMergedPDF сливает бланки в один файл merged_<дата_время>.pdf
// в temp-директории и возвращает путь к нему.
func (e *Exporter) ExportMergedPDF(data [][]byte) (string, error) {
	conf := model.NewDefaultConfiguration()

	readers := make([]io.ReadSeeker, len(data))
	for i, b := range data {
		readers[i] = bytes.NewReader(b)
	}

	timestamp := time.Now().Format("20060102_150405")
	filename := fmt.Sprintf("merged_%s.pdf", timestamp)
	fullpath := filepath.Join(tempdir.Dir, filename)

	file, err := os.Create(fullpath)
	if err != nil {
		return "", err
	}

	defer func() {
		err := file.Close()
		if err != nil {
			slog.Error(fmt.Sprintf("Failed to close file: %v", err))
		}
	}()

	err = api.MergeRaw(readers, file, false, conf)
	if err != nil {
		return "", err
	}

	return fullpath, nil
}
