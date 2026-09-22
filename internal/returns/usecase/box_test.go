package usecase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"warehouseHelper/internal/innercode"
	"warehouseHelper/internal/tempdir"
)

// ── Создание коробки («Продукция» → «Создать коробку») ─────────────────────
//
// Коробка — физическая группировка кусков одного товара с одним сроком:
// в БД и остатки ничего не уходит, итог операции — файл наклейки. Поэтому
// тесты проверяют и текст отказа, и то, что реально попало на наклейку:
// 33-значный код (товар, вес, число вложений, даты) и подпись наименования.

const (
	prodEarly = "29082026" // 29.08.2026 — ранняя выработка
	prodLate  = "01092026" // 01.09.2026 — выработка базовой etiketa()
	expMain   = "15092026" // 15.09.2026 — срок базовой etiketa()
	expOther  = "20092026" // 20.09.2026 — другой срок

	ruDate      = "02.01.2006"
	ruProdLate  = "01.09.2026"
	ruProdEarly = "29.08.2026"
	ruExpMain   = "15.09.2026"
)

// etiketaDates — этикетка куска (29 цифр) с заданными выработкой и сроком;
// базовая etiketa() фиксирует 01.09.2026 / 15.09.2026.
func etiketaDates(code string, weightG int, prod, exp string) string {
	return code + fmt.Sprintf("%05d", weightG) + prod + exp
}

// boxEtiketa — этикетка коробки (33 цифры) для проверки отказа.
func boxEtiketa(code string, weightG, qty int, prod, exp string) string {
	return code + fmt.Sprintf("%06d", weightG) + fmt.Sprintf("%03d", qty) + prod + exp
}

// manyEtiketas — n этикеток штучного товара (вес-заглушка 1 г).
func manyEtiketas(n int) []string {
	scans := make([]string, 0, n)
	for range n {
		scans = append(scans, etiketa(codeD, 1))
	}
	return scans
}

// weightOverflow — 11 сканов весового товара по 99 999 г: 1 099 989 г — больше
// предела поля веса в 33-значном коде коробки (999 999 г).
func weightOverflow() []string {
	scans := make([]string, 0, 11)
	for range 11 {
		scans = append(scans, etiketa(codeA, 99999))
	}
	return scans
}

// ensureTempDir готовит temp-директорию приложения: наклейки пишутся туда.
func ensureTempDir(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(tempdir.Dir, 0o750); err != nil {
		t.Fatalf("MkdirAll(%s): %v", tempdir.Dir, err)
	}
}

// buildBox — сборка коробки стабами модуля: путь к наклейке и предупреждения.
// Ошибка — фатально: отказы проверяет TestCreateBox_Rejects.
func buildBox(t *testing.T, scans []string) (path string, warnings []string) {
	t.Helper()
	ensureTempDir(t)
	path, warnings, err := newTestEnv(newStubRepo()).uc.CreateBox(context.Background(), scans)
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	return path, warnings
}

// readLabel открывает файл наклейки и читает подписи блока: строку 3 (отступ,
// штрих-код, цифры 33-значного кода — товар, вес, вложения, даты) и строку 4
// (наименование товара).
func readLabel(t *testing.T, path string) (parsedCode innercode.Code, productName string) {
	t.Helper()
	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatalf("открыть наклейку %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	digits, err := f.GetCellValue(f.GetSheetName(0), "B3")
	if err != nil {
		t.Fatalf("цифры кода наклейки: %v", err)
	}
	name, err := f.GetCellValue(f.GetSheetName(0), "B4")
	if err != nil {
		t.Fatalf("наименование наклейки: %v", err)
	}
	code, err := innercode.Parse(digits)
	if err != nil {
		t.Fatalf("код коробки %q с наклейки: %v", digits, err)
	}
	return code, name
}

// boxCase — кейс сборки коробки: сканы и ожидаемый результат.
type boxCase struct {
	name       string
	scans      []string
	wantErr    string // подстрока отказа; пусто — коробка собирается
	wantQty    int    // число вложений в коде наклейки
	wantWeight int    // вес в коде наклейки, г (штучному boxlabel ставит 1 г)
	wantProd   string // выработка в коде наклейки
	wantName   string // наименование товара в подписи наклейки (products.name)
}

// labelCases — батч принят: коробка собирается и её наклейку проверяем целиком.
func labelCases() []boxCase {
	return []boxCase{
		{
			name:       "дубликаты сканов — разные куски, веса суммируются",
			scans:      []string{etiketa(codeA, 500), etiketa(codeA, 500)},
			wantQty:    2,
			wantWeight: 1000,
			wantProd:   ruProdLate,
			wantName:   nameA,
		},
		{
			name:       "три куска одного товара и срока",
			scans:      []string{etiketa(codeA, 250), etiketa(codeA, 500), etiketa(codeA, 150)},
			wantQty:    3,
			wantWeight: 900,
			wantProd:   ruProdLate,
			wantName:   nameA,
		},
		{
			name:       "штучный товар — вес не считается, заглушку 1 г ставит boxlabel",
			scans:      []string{etiketa(codeD, 1), etiketa(codeD, 1), etiketa(codeD, 1)},
			wantQty:    3,
			wantWeight: 1,
			wantProd:   ruProdLate,
			wantName:   nameD,
		},
	}
}

// rejectCases — батч отклоняется целиком: файл наклейки не создаётся.
func rejectCases() []boxCase {
	return []boxCase{
		{
			name:    "товары разных позиций — отказ",
			scans:   []string{etiketa(codeA, 500), etiketa(codeD, 1)},
			wantErr: "разных позиций",
		},
		{
			name:    "разные сроки годности — отказ",
			scans:   []string{etiketa(codeA, 500), etiketaDates(codeA, 300, prodLate, expOther)},
			wantErr: "разные сроки годности",
		},
		{
			name:    "этикетка коробки (33 цифры) — отказ",
			scans:   []string{boxEtiketa(codeA, 2500, 2, prodLate, expMain)},
			wantErr: "коробка (33)",
		},
		{
			name:    "товара нет в каталоге — отказ",
			scans:   []string{etiketa("00999000", 500)},
			wantErr: "не в каталоге",
		},
		{
			name:    "не внутренний штрих-код — отказ",
			scans:   []string{"4600000000001"},
			wantErr: "неверный штрих-код",
		},
		{
			name:    "нет сканов — отказ",
			scans:   nil,
			wantErr: "нет сканов",
		},
		{
			name:    "вложений больше предела формата — отказ",
			scans:   manyEtiketas(1000),
			wantErr: "вложений 1000",
		},
		{
			name:    "общий вес весового товара больше предела формата — отказ",
			scans:   weightOverflow(),
			wantErr: "общий вес 1099989 г",
		},
	}
}

func TestCreateBox(t *testing.T) {
	for _, tt := range labelCases() {
		t.Run(tt.name, func(t *testing.T) {
			path, warnings := buildBox(t, tt.scans)

			if len(warnings) != 0 {
				t.Errorf("успешная сборка без предупреждений, got %+v", warnings)
			}
			if filepath.Ext(path) != ".xlsx" || !strings.Contains(filepath.Base(path), "box_labels_") {
				t.Fatalf("путь файла наклейки: %q", path)
			}
			checkLabel(t, path, tt)
		})
	}
}

// checkLabel — наклейка собранной коробки: код в строке цифр (товар, вес,
// вложения, даты) и наименование товара в подписи.
func checkLabel(t *testing.T, path string, tt boxCase) {
	t.Helper()
	code, productName := readLabel(t, path)

	if code.Kind != innercode.KindBox {
		t.Errorf("код наклейки = %v, want KindBox", code.Kind)
	}
	if code.InternalCode != codeA && code.InternalCode != codeD {
		t.Errorf("код склада на наклейке = %q, want %q или %q", code.InternalCode, codeA, codeD)
	}
	if code.Qty != tt.wantQty || code.WeightG != tt.wantWeight {
		t.Errorf("наклейка: вложений %d / вес %d г, want %d / %d г", code.Qty, code.WeightG, tt.wantQty, tt.wantWeight)
	}
	if code.ProdDate.Format(ruDate) != tt.wantProd {
		t.Errorf("выработка наклейки = %s, want %s", code.ProdDate.Format(ruDate), tt.wantProd)
	}
	if code.ExpDate.Format(ruDate) != ruExpMain {
		t.Errorf("срок наклейки = %s, want %s", code.ExpDate.Format(ruDate), ruExpMain)
	}
	if productName != tt.wantName {
		t.Errorf("наименование на наклейке = %q, want %q (products.name из шва каталога)", productName, tt.wantName)
	}
}

// TestCreateBox_Rejects — отказ батча целиком: ValidationError с понятным
// текстом и никакого файла наклейки.
func TestCreateBox_Rejects(t *testing.T) {
	for _, tt := range rejectCases() {
		t.Run(tt.name, func(t *testing.T) {
			ensureTempDir(t)
			path, _, err := newTestEnv(newStubRepo()).uc.CreateBox(context.Background(), tt.scans)

			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("want ValidationError, got %v", err)
			}
			if !strings.Contains(ve.Reason, tt.wantErr) {
				t.Fatalf("текст отказа = %q, want подстрока %q", ve.Reason, tt.wantErr)
			}
			if path != "" {
				t.Errorf("при отказе файл наклейки не создаётся, got %q", path)
			}
		})
	}
}

// Расхождение выработки не блокирует коробку: в код идёт самая ранняя дата, а
// оператор получает предупреждение (сколько кусков с другой выработкой).
func TestCreateBox_ProdDateMismatchWarns(t *testing.T) {
	path, warnings := buildBox(t, []string{
		etiketa(codeA, 500), // выработка 01.09.2026
		etiketaDates(codeA, 250, prodEarly, expMain),
		etiketaDates(codeA, 250, prodEarly, expMain),
	})

	if len(warnings) != 1 {
		t.Fatalf("want 1 предупреждение, got %+v", warnings)
	}
	if !strings.Contains(warnings[0], "самая ранняя") || !strings.Contains(warnings[0], ruProdEarly) {
		t.Errorf("предупреждение = %q, want «самая ранняя (%s)»", warnings[0], ruProdEarly)
	}
	if !strings.Contains(warnings[0], "1 кусок") {
		t.Errorf("предупреждение = %q, want счёт кусков с другой выработкой (1 кусок)", warnings[0])
	}

	code, productName := readLabel(t, path)
	if code.ProdDate.Format(ruDate) != ruProdEarly {
		t.Errorf("в код коробки идёт самая ранняя выработка, got %s", code.ProdDate.Format(ruDate))
	}
	if code.Qty != 3 || code.WeightG != 1000 {
		t.Errorf("наклейка: вложений %d / вес %d г, want 3 / 1000 г", code.Qty, code.WeightG)
	}
	if productName != nameA {
		t.Errorf("наименование на наклейке = %q, want %q", productName, nameA)
	}
}
