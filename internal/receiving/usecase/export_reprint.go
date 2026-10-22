package usecase

import (
	"context"
	"fmt"
	"strings"

	"warehouseHelper/internal/innercode"
	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/receiving"
)

// ReprintVariant — каким видом наклейки перепечатать отсканированный код.
type ReprintVariant string

const (
	// ReprintLong — «длинная» этикетка: одна на печатную страницу, с
	// наименованием, сроком и весом.
	ReprintLong ReprintVariant = "long"
	// ReprintShort — «короткая» QR-наклейка: две в ряд, только код и QR.
	ReprintShort ReprintVariant = "short"
	// ReprintBox — наклейка коробки (75×120 мм): QR, код, наименование,
	// вес с числом вложений и обе даты.
	ReprintBox ReprintVariant = "box"
)

// ExportReprint формирует xlsx-файл наклеек по сканам с рабочего места
// (страница «Перепечатать наклейку», хаб «Продукция»).
//
// Данные наклейки целиком лежат в самом коде: 29 цифр — кусок (код товара,
// вес, выработка, срок), 33 — коробка (плюс общий вес и число вложений).
// Поэтому ни остатки, ни МойСклад не читаются и не пишутся, а каталог нужен
// только ради наименования и весовости товара. Код, которого нет в каталоге,
// отклоняет весь батч — наклейку без наименования не печатаем. Дубли не
// схлопываются: сколько раз отсканировали, столько наклеек и в файле.
//
// Сканы другого вида не ошибка: страница шлёт всё, что на ней насканировано,
// а в файл идут только коды под выбранный вид наклейки.
func (uc *ReceivingUseCase) ExportReprint(ctx context.Context, scans []string, variant ReprintVariant) (string, error) {
	done := metrics.Track(trackPkg, "ExportReprint")
	defer done()

	codes, err := reprintCodes(scans, variant)
	if err != nil {
		return "", err
	}

	refs, err := uc.reprintCatalogRefs(ctx, codes)
	if err != nil {
		return "", err
	}

	switch variant {
	case ReprintLong:
		return uc.ExportLabels(reprintUnits(codes, refs))
	case ReprintShort:
		return uc.ExportQRLabels(reprintUnits(codes, refs))
	case ReprintBox:
		return uc.ExportBoxLabels(reprintBoxes(codes, refs))
	default:
		// Вид уже проверен reprintCodes — сюда попадать нечему.
		return "", fmt.Errorf("неизвестный вид наклейки %q", variant)
	}
}

// reprintCodes разбирает сканы и оставляет коды нужного вида: куски (29) для
// «длинной» и «короткой» наклейки, коробки (33) — для наклейки коробки.
func reprintCodes(scans []string, variant ReprintVariant) ([]innercode.Code, error) {
	want, err := variant.kind()
	if err != nil {
		return nil, err
	}

	out := make([]innercode.Code, 0, len(scans))
	for _, raw := range scans {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		code, err := innercode.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("скан %q не разобран: %w", raw, err)
		}
		if code.Kind != want {
			continue
		}
		out = append(out, code)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("в сканах нет %s — печатать нечего", variant.subject())
	}

	return out, nil
}

// kind — вид внутреннего кода, который печатает наклейка этого вида.
func (v ReprintVariant) kind() (innercode.Kind, error) {
	switch v {
	case ReprintLong, ReprintShort:
		return innercode.KindItem, nil
	case ReprintBox:
		return innercode.KindBox, nil
	default:
		return 0, fmt.Errorf("неизвестный вид наклейки %q", v)
	}
}

// subject — что сканировать для этого вида наклейки (текст отказа).
func (v ReprintVariant) subject() string {
	if v == ReprintBox {
		return "коробок (33 цифры)"
	}

	return "кусков (29 цифр)"
}

// reprintCatalogRefs — товары каталога по кодам сканов (один запрос на батч).
// Код, которого нет в каталоге, отклоняет батч: наименование на наклейке
// взять неоткуда.
func (uc *ReceivingUseCase) reprintCatalogRefs(ctx context.Context, codes []innercode.Code) (map[string]receiving.ProductRef, error) {
	uniq := make([]string, 0, len(codes))
	seen := make(map[string]bool, len(codes))
	for _, c := range codes {
		if seen[c.InternalCode] {
			continue
		}
		seen[c.InternalCode] = true
		uniq = append(uniq, c.InternalCode)
	}

	refs, err := uc.repo.LoadCatalogProductsByCodes(ctx, uniq)
	if err != nil {
		return nil, fmt.Errorf("товары каталога по кодам: %w", err)
	}

	missing := make([]string, 0)
	for _, code := range uniq {
		if _, ok := refs[code]; !ok {
			missing = append(missing, code)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("нет в каталоге: %s — наклейки не печатаем", strings.Join(missing, ", "))
	}

	return refs, nil
}

// reprintUnits — куски для этикетки и QR-наклейки: код, вес и даты из скана,
// наименование и весовость — из каталога. Вес уходит в код тот же (у штучного
// та же заглушка 1 г), поэтому перепечатанный код совпадает со сканом.
func reprintUnits(codes []innercode.Code, refs map[string]receiving.ProductRef) []receiving.Unit {
	out := make([]receiving.Unit, 0, len(codes))
	for _, c := range codes {
		ref := refs[c.InternalCode]
		produced, bestBefore := c.ProdDate, c.ExpDate
		out = append(out, receiving.Unit{
			ProductID:    ref.ProductID,
			InternalCode: c.InternalCode,
			ProductName:  ref.Name,
			Weighted:     ref.Weighted,
			WeightG:      int64(c.WeightG),
			ProducedOn:   &produced,
			BestBefore:   bestBefore,
		})
	}

	return out
}

// reprintBoxes — коробки для наклейки коробки: всё, кроме наименования и
// весовости, берётся из 33-значного кода.
func reprintBoxes(codes []innercode.Code, refs map[string]receiving.ProductRef) []receiving.Box {
	out := make([]receiving.Box, 0, len(codes))
	for _, c := range codes {
		ref := refs[c.InternalCode]
		produced, bestBefore := c.ProdDate, c.ExpDate
		out = append(out, receiving.Box{
			ProductID:    ref.ProductID,
			InternalCode: c.InternalCode,
			ProductName:  ref.Name,
			Weighted:     ref.Weighted,
			WeightG:      int64(c.WeightG),
			Qty:          int64(c.Qty),
			ProducedOn:   &produced,
			BestBefore:   &bestBefore,
		})
	}

	return out
}
