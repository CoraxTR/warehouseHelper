package pdf

import (
	"bytes"
	"log"
	"os"

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
	defer outFile.Close()

	err = api.Write(validated, outFile, conf)
	if err != nil {
		return "", err
	}
	return outFile.Name(), nil // путь относительный
}
