package usecase

import (
	"context"
	"fmt"
	"time"

	"warehouseHelper/internal/boxlabel"
	"warehouseHelper/internal/innercode"
	"warehouseHelper/internal/returns"
)

// Метод страницы «Создать коробку» («Продукция»). Коробка — физическая
// группировка кусков одного товара с одним сроком годности: ускоряет пересчёт
// сроков и инвентаризацию. Состав коробки в БД не хранится, в остатки/МойСклад
// при её создании не пишется ничего — итог операции один: файл наклейки,
// который печатают и клеят на коробку.

// Пределы полей 33-значного кода коробки (innercode.EncodeBox): число вложений
// и общий вес весового товара. Выше — код коробки не собрать, поэтому батч
// отклоняется до печати (иначе наклейка молча вышла бы без коробки).
const (
	maxBoxQty     = 999
	maxBoxWeightG = 999999
)

// boxData — состав коробки, собранный из сканов и каталога: опорный товар
// (он же подпись наклейки), число вложений, общий вес и даты. Товар и обе
// даты задаёт первый скан, поэтому они берутся у него.
type boxData struct {
	product    returns.CatalogProduct
	qty        int
	weightG    int64
	producedOn time.Time
	bestBefore time.Time
}

// CreateBox — собрать коробку из отсканированных ярлыков кусков (29 цифр) и
// отдать файл наклейки. Состав не сохраняется: коробка — физическая
// группировка, в БД уходят только куски (их не трогаем вовсе).
// Возвращает путь к xlsx-файлу наклейки.
//
// Содержимое коробки задаёт первый скан: товар и обе даты (выработка и срок).
// Каждый следующий скан обязан совпадать с ним — расхождение отклоняет батч
// целиком (ValidationError, на странице — 400 с текстом). Дубликаты сканов —
// разные куски (этикетки штучных товаров с одним сроком идентичны, повтор
// неотличим от второго куска), дедупликации нет.
func (uc *UseCase) CreateBox(ctx context.Context, scans []string) (path string, err error) {
	parsed, codes, err := parseBoxScans(scans)
	if err != nil {
		return "", err
	}

	products, err := uc.catalog.ProductsByInternalCodes(ctx, codes)
	if err != nil {
		return "", err
	}

	box, err := collectBox(parsed, products)
	if err != nil {
		return "", err
	}
	if err := checkBoxLimits(box); err != nil {
		return "", err
	}

	weight := box.weightG
	if !box.product.Weighted {
		weight = 0 // штучному товару вес не считаем: заглушку 1 г подставит boxlabel
	}

	// Наименование товара приходит из шва каталога (products.name) и печатается
	// на наклейке между цифрами ШК и строкой веса с числом вложений.
	path, err = boxlabel.Export([]boxlabel.Box{{
		InternalCode: box.product.InternalCode,
		ProductName:  box.product.Name,
		Weighted:     box.product.Weighted,
		WeightG:      weight,
		Qty:          box.qty,
		ProducedOn:   box.producedOn,
		BestBefore:   box.bestBefore,
	}})
	if err != nil {
		return "", fmt.Errorf("наклейка коробки: %w", err)
	}

	return path, nil
}

// parseBoxScans — разбор и проверка сканов батча: в коробку идут только куски
// (29 цифр), любой другой штрих-код отклоняет батч целиком. Возвращает
// разобранные коды и уникальные коды склада — для шва каталога.
func parseBoxScans(scans []string) ([]innercode.Code, []string, error) {
	if len(scans) == 0 {
		return nil, nil, &ValidationError{Reason: noScansReason}
	}

	parsed := make([]innercode.Code, 0, len(scans))
	codes := make([]string, 0, len(scans)) // коды склада (уникальные) — для каталога
	seen := make(map[string]struct{}, len(scans))
	for _, raw := range scans {
		code, err := innercode.Parse(raw)
		if err != nil {
			return nil, nil, &ValidationError{Reason: fmt.Sprintf("неверный штрих-код %q: %v", raw, err)}
		}
		if code.Kind != innercode.KindItem {
			return nil, nil, &ValidationError{Reason: fmt.Sprintf("штрих-код %q — коробка (33): в коробку собирают только куски (29)", raw)}
		}
		parsed = append(parsed, code)
		if _, ok := seen[code.InternalCode]; !ok {
			seen[code.InternalCode] = struct{}{}
			codes = append(codes, code.InternalCode)
		}
	}
	return parsed, codes, nil
}

// collectBox — состав коробки по сканам и каталогу: товар и обе даты задаёт
// первый скан (у коробки одна позиция, одна выработка и один срок). Каждый
// следующий скан обязан совпадать с ним: расхождение (чужой товар, другие
// даты, товар вне каталога) отклоняет батч — ValidationError.
func collectBox(parsed []innercode.Code, products map[string]returns.CatalogProduct) (boxData, error) {
	first := parsed[0]
	firstProduct, err := catalogProduct(products, first.InternalCode)
	if err != nil {
		return boxData{}, err
	}

	box := boxData{product: firstProduct, producedOn: first.ProdDate, bestBefore: first.ExpDate}
	for _, code := range parsed {
		p, err := catalogProduct(products, code.InternalCode)
		if err != nil {
			return boxData{}, err
		}
		if p.ProductID != firstProduct.ProductID {
			return boxData{}, &ValidationError{Reason: "в коробке товары разных позиций — коробка собирается из одного товара"}
		}
		if !code.ExpDate.Equal(first.ExpDate) {
			return boxData{}, &ValidationError{Reason: fmt.Sprintf("в коробке разные сроки годности (%s и %s) — коробка собирается с одним сроком",
				hintDate(first.ExpDate), hintDate(code.ExpDate))}
		}
		if !code.ProdDate.Equal(first.ProdDate) {
			return boxData{}, &ValidationError{Reason: fmt.Sprintf("в коробке разные даты выработки (%s и %s) — первый скан задаёт даты коробки",
				hintDate(first.ProdDate), hintDate(code.ProdDate))}
		}
		box.qty++
		box.weightG += int64(code.WeightG)
	}
	return box, nil
}

// catalogProduct — товар шва каталога по коду склада из этикетки: пусто или без
// uuid товара (нет кода склада) — отказ, коробку из такого куска не собрать.
func catalogProduct(products map[string]returns.CatalogProduct, internalCode string) (returns.CatalogProduct, error) {
	p, ok := products[internalCode]
	if !ok || p.ProductID == "" {
		return returns.CatalogProduct{}, &ValidationError{Reason: fmt.Sprintf("товар с кодом %s не в каталоге — коробку из него не собрать", internalCode)}
	}
	return p, nil
}

// checkBoxLimits — пределы полей 33-значного кода коробки: больше вложений или
// больше общего веса в код не влезет (boxlabel такую коробку молча пропустил бы),
// поэтому батч отклоняется до печати с понятным оператору текстом.
func checkBoxLimits(box boxData) error {
	if box.qty > maxBoxQty {
		return &ValidationError{Reason: fmt.Sprintf("вложений %d — больше %d: столько в код коробки не влезает, разбейте на две коробки", box.qty, maxBoxQty)}
	}
	// Вес проверяем только весовому товару: у штучного веса нет, в код коробки
	// boxlabel подставит заглушку 1 г.
	if box.product.Weighted && box.weightG > maxBoxWeightG {
		return &ValidationError{Reason: fmt.Sprintf("общий вес %d г — больше %d г: столько в код коробки не влезает, разбейте на две коробки", box.weightG, maxBoxWeightG)}
	}
	return nil
}

// hintDate — дата в текстах страницы: 29.08.2026.
func hintDate(t time.Time) string {
	return t.Format("02.01.2006")
}
