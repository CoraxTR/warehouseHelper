package pdf

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

type PDFExporter struct{}

func NewPDFExporter() *PDFExporter {
	return &PDFExporter{}
}

func (e *PDFExporter) ExportOrderPDF(data []byte) (string, error) {
	conf := model.NewDefaultConfiguration()

	validated, err := api.ReadAndValidate(bytes.NewReader(data), conf)
	if err != nil {
		log.Printf("couldn't validate, %v", err)
	}

	outFile, err := os.Create("exported.pdf")
	if err != nil {
		return "", err
	}

	defer func() {
		err := outFile.Close()
		if err != nil {
			log.Printf("Failed to close file: %v", err)
		}
	}()

	err = api.Write(validated, outFile, conf)
	if err != nil {
		return "", err
	}
	// На данный момент сохраняем в cmd, позже переделаем в отдельную папку
	return outFile.Name(), nil
}

func (e *PDFExporter) ExportMergedPDF(data [][]byte) (string, error) {
	conf := model.NewDefaultConfiguration()

	readers := make([]io.ReadSeeker, len(data))
	for i, b := range data {
		readers[i] = bytes.NewReader(b)
	}

	timestamp := time.Now().Format("20060102_150405")
	filename := fmt.Sprintf("merged_%s.pdf", timestamp)
	fullpath := "../" + filename

	file, err := os.Create(fullpath)
	if err != nil {
		return "", err
	}

	defer func() {
		err := file.Close()
		if err != nil {
			log.Printf("Failed to close file: %v", err)
		}
	}()

	err = api.MergeRaw(readers, file, false, conf)
	if err != nil {
		return "", err
	}

	return fullpath, nil
}
