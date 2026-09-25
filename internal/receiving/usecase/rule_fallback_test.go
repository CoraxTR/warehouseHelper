package usecase

import (
	"context"
	"strings"
	"testing"

	"warehouseHelper/internal/receiving"
)

// --- правила одной длины: перебор (условие владельца 23.09.2026) ---
//
// У поставщика могут стоять несколько правил одной длины, и рабочее — не первым.
// Случай владельца: ШК 4610030753485, первое правило вычитывает код «10030»
// (которого у поставщика нет), второе — заведённый «753485».

const (
	ruleFirstWrong13 = "13-3-5-8-5- -0- -0"        // код с поз. 3 → «10030»
	ruleWorking13    = "13-8-6- -0- -0- -0"        // код с поз. 8 → «753485»
	ruleBrokenDate13 = "13-8-6- -0-7-6- -0-ггммдд" // код тот же, «дата» из середины кода: «075348» — месяца 53 нет
	ownerBarcode13   = "4610030753485"
	ownerExtCode     = "753485"
	junkExtCode      = "10030"
	ownerIntCode     = "00210050"
	ownerProductID   = "p-owner13"

	// Правила коробок той же длины: у коробки в правиле пять полей (код, вес,
	// кол-во вложений, выработка, срок), поэтому пустых пар на одну больше.
	boxRuleFirstWrong13 = "13-3-5-8-5- -0- -0- -0"
	boxRuleWorking13    = "13-8-6- -0- -0- -0- -0"
)

// addOwnerProduct заводит у поставщика товар с ШК владельца: правило веса не
// вычитывает (поля нет) — вес вводится вручную.
func addOwnerProduct(repo *stubReceiveRepo) {
	repo.barcodes = append(repo.barcodes, receiving.BarcodeRef{
		ExternalCode: ownerExtCode, ProductID: ownerProductID, ProductName: "Говядина б/к",
		InternalCode: ownerIntCode, Weighted: true,
	})
	repo.catalog[ownerIntCode] = receiving.ProductRef{
		ProductID: ownerProductID, InternalCode: ownerIntCode, Name: "Говядина б/к", Weighted: true,
	}
}

// ownerCache собирает кеш приёмки по правилам, заявленным в репозитории.
func ownerCache(t *testing.T, repo *stubReceiveRepo) *receiving.Cache {
	t.Helper()
	uc := NewReceivingUseCase(repo, &stubStockAccepter{}, &stubWeightRecorder{})
	cache, err := uc.GetCache(context.Background(), "sup-1")
	if err != nil {
		t.Fatalf("GetCache: %v", err)
	}
	return cache
}

// Правило одной длины товар не нашло (код не заведён у поставщика) — приёмка
// пробует следующее правило той же длины: рабочее может стоять не первым.
func TestResolveTriesNextRuleOfSameLength(t *testing.T) {
	uc, _ := newTestReceive()
	repo, ok := uc.repo.(*stubReceiveRepo)
	if !ok {
		t.Fatal("ожидался stubReceiveRepo")
	}
	addOwnerProduct(repo)
	repo.supplier.DecodeRules = []string{ruleFirstWrong13, ruleWorking13}
	cache := ownerCache(t, repo)

	s, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{Raw: ownerBarcode13})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if s.ProductID != ownerProductID || s.InternalCode != ownerIntCode || s.Kind != receiving.KindItem {
		t.Fatalf("скан: %+v", s)
	}
	// Вес правилом не вычитывается (поля нет) — вес вводится вручную.
	if s.WeightG != nil || s.Qty != 1 {
		t.Fatalf("вес/кол-во: %+v", s)
	}
}

// Оба кода заведены — решает порядок правил: первое правило в приоритете, и
// «подходящее дальше» его не перебивает. Вес читается полем первого правила.
func TestResolveFirstRuleWinsWhenBothCodesKnown(t *testing.T) {
	uc, _ := newTestReceive()
	repo, ok := uc.repo.(*stubReceiveRepo)
	if !ok {
		t.Fatal("ожидался stubReceiveRepo")
	}
	addOwnerProduct(repo)
	// Код первого правила заведён — на другой товар (p1 из фикстуры).
	repo.barcodes = append(repo.barcodes, receiving.BarcodeRef{
		ExternalCode: junkExtCode, ProductID: "p1", ProductName: "Говядина охл.",
		InternalCode: intCode, Weighted: true,
	})
	repo.supplier.DecodeRules = []string{ruleFirstWrong13, ruleWorking13}
	cache := ownerCache(t, repo)

	s, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{Raw: ownerBarcode13})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if s.ProductID != "p1" || !eqInt64Ptr(s.WeightG, new(int64(75348))) {
		t.Fatalf("скан: %+v (ждали товар первого правила и вес 75348)", s)
	}
}

// Ни одно правило не дало заведённого кода — в отказе перечислены коды ВСЕХ
// правил одной длины: по первому оператор завёл бы не то, что читает рабочее.
func TestResolveUnknownCodesListAllRules(t *testing.T) {
	uc, _ := newTestReceive()
	repo, ok := uc.repo.(*stubReceiveRepo)
	if !ok {
		t.Fatal("ожидался stubReceiveRepo")
	}
	repo.supplier.DecodeRules = []string{ruleFirstWrong13, ruleWorking13}
	cache := ownerCache(t, repo)

	_, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{Raw: ownerBarcode13})
	if err == nil {
		t.Fatal("ожидался отказ скана")
	}
	for _, want := range []string{junkExtCode, ownerExtCode, "не заведены"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("в отказе нет %q: %v", want, err)
		}
	}
}

// Явный отказ правила (нераспознанная дата) перебором не лечится: следующее
// правило не подхватывает — иначе ошибка настроек молча меняла бы чтение полей.
func TestResolveBrokenRuleDoesNotFallThrough(t *testing.T) {
	uc, _ := newTestReceive()
	repo, ok := uc.repo.(*stubReceiveRepo)
	if !ok {
		t.Fatal("ожидался stubReceiveRepo")
	}
	addOwnerProduct(repo)
	repo.supplier.DecodeRules = []string{ruleBrokenDate13, ruleWorking13}
	cache := ownerCache(t, repo)

	_, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{Raw: ownerBarcode13})
	if err == nil || !strings.Contains(err.Error(), "дата выработки") {
		t.Fatalf("ждали отказ по дате выработки, получили %v", err)
	}
}

// То же для правил коробок: код первого правила не заведён — работает второе.
func TestResolveBoxTriesNextRuleOfSameLength(t *testing.T) {
	uc, _ := newTestReceive()
	repo, ok := uc.repo.(*stubReceiveRepo)
	if !ok {
		t.Fatal("ожидался stubReceiveRepo")
	}
	addOwnerProduct(repo)
	repo.supplier.BoxDecodeRules = []string{boxRuleFirstWrong13, boxRuleWorking13}
	cache := ownerCache(t, repo)

	s, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{
		Raw:      ownerBarcode13,
		Children: []receiving.ScanEntry{{Raw: extBarcode}},
	})
	if err != nil {
		t.Fatalf("Resolve коробки: %v", err)
	}
	if s.Kind != receiving.KindBox || s.ProductID != ownerProductID {
		t.Fatalf("коробка: %+v", s)
	}
}
