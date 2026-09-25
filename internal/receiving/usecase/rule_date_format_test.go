package usecase

import (
	"context"
	"strings"
	"testing"
	"time"

	"warehouseHelper/internal/receiving"
)

// --- формат дат в правиле: ггммдд/ддммгг (правка 25.09.2026) ---
//
// ШК поставщика владельца, 50 цифр: (01)2300000001504 (3103)000670 (17)270705
// (11)260710 (30)01 (91)031 — даты ГГММДД: срок 05.07.2027, выработка
// 10.07.2026. Позиции: 01 1-2, ГТИН 3-15, 3103 16-19, вес 20-25, 17 26-27,
// срок 28-33, 11 34-35, выработка 36-41, 30 42-43, 01 44-45, 91 46-47, 031
// 48-50. Код собирается конкатенацией констант: потерянная цифра меняет длину
// и валит разбор (питфолл фикстур).
//
// ВАЖНО: длина правила обязана совпасть с тем, что отдаёт сканер. В (01) по
// GS1 ожидается 14 цифр, а у владельца в сообщении их 13 — если сканер отдаёт
// 51 цифру (ГТИН-14), все позиции после ГТИН сдвигаются на +1:
// "51-3-14-21-6-37-6-29-6-ггммдд". Обе строки можно держать на карточке сразу:
// правила перебираются по длине скана.
const (
	gs1ExtCode = "2300000001504" // ГТИН из (01) — внешний код товара
	gs1Weight  = "000670"        // (3103) 0.670 кг → 670 г
	gs1Prefix  = "01" + gs1ExtCode + "3103"
	gs1Tail    = "3001" + "91031" // (30)01 и (91)031 правилами не вычитываются

	gs1BarcodeYMD = gs1Prefix + gs1Weight + "17" + "270705" + "11" + "260710" + gs1Tail // 50, даты ггммдд
	gs1BarcodeDMY = gs1Prefix + gs1Weight + "17" + "050727" + "11" + "100726" + gs1Tail // 50, даты ддммгг

	// код 3-15, вес 20-25, выработка 36-41, срок 28-33.
	gs1RuleYMD = "50-3-13-20-6-36-6-28-6-ггммдд"
	gs1RuleDMY = "50-3-13-20-6-36-6-28-6-ддммгг"

	// Правило коробки того же кода: кол-во вложений — из (30) 44-45.
	gs1BoxRuleYMD = "50-3-13-20-6-44-2-36-6-28-6-ггммдд"
)

// gs1NoCodeRule — ШК без артикула (реальный кейс владельца): 13 цифр, только вес.
// Товар сканам задаёт оператор: правило несёт один вес, кода товара не вычитывает.
const gs1NoCodeRule = "13- -0-8-5- -0- -0"

// addGS1Product заводит у поставщика товар с внешним кодом-ГТИН.
func addGS1Product(repo *stubReceiveRepo) {
	repo.barcodes = append(repo.barcodes, receiving.BarcodeRef{
		ExternalCode: gs1ExtCode, ProductID: "p-gs1", ProductName: "Курица охл.",
		InternalCode: "00210060", Weighted: true,
	})
}

// gs1Cache собирает кеш приёмки по правилам, заявленным в репозитории, и
// заводит товар с кодом владельца.
func gs1Cache(t *testing.T, itemRules, boxRules []string) (*ReceivingUseCase, *receiving.Cache) {
	t.Helper()
	repo := testCacheRepo()
	repo.supplier.DecodeRules = itemRules
	repo.supplier.BoxDecodeRules = boxRules
	addGS1Product(repo)
	uc := NewReceivingUseCase(repo, &stubStockAccepter{}, &stubWeightRecorder{})
	cache, err := uc.GetCache(context.Background(), "sup-1")
	if err != nil {
		t.Fatalf("GetCache: %v", err)
	}
	return uc, cache
}

// checkScanDates сверяет даты скана с ожидаемыми.
func checkScanDates(t *testing.T, s *receiving.DecodedScan, produced, best time.Time) {
	t.Helper()
	if s.ProducedOn == nil || !s.ProducedOn.Equal(produced) {
		t.Errorf("выработка: %v, want %v", s.ProducedOn, produced)
	}
	if s.BestBefore == nil || !s.BestBefore.Equal(best) {
		t.Errorf("срок годности: %v, want %v", s.BestBefore, best)
	}
}

// ггммдд: даты ШК владельца — выработка 10.07.2026, срок 05.07.2027.
func TestResolveRuleDatesYMD(t *testing.T) {
	uc, cache := gs1Cache(t, []string{gs1RuleYMD}, nil)

	s, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{Raw: gs1BarcodeYMD})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if s.ProductID != "p-gs1" || s.InternalCode != "00210060" {
		t.Fatalf("товар: %+v", s)
	}
	if s.WeightG == nil || *s.WeightG != 670 {
		t.Fatalf("вес: %v", s.WeightG)
	}
	checkScanDates(t, s,
		time.Date(2026, time.July, 10, 0, 0, 0, 0, time.UTC),
		time.Date(2027, time.July, 5, 0, 0, 0, 0, time.UTC))
}

// ддммгг: тот же код с датами, записанными день-месяц-год, — те же даты.
func TestResolveRuleDatesDMY(t *testing.T) {
	uc, cache := gs1Cache(t, []string{gs1RuleDMY}, nil)

	s, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{Raw: gs1BarcodeDMY})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	checkScanDates(t, s,
		time.Date(2026, time.July, 10, 0, 0, 0, 0, time.UTC),
		time.Date(2027, time.July, 5, 0, 0, 0, 0, time.UTC))
}

// Без токена формата даты читаются как ДДММГГГГ — историческое поведение.
func TestResolveRuleDatesDefaultFormat(t *testing.T) {
	uc, _ := newTestReceive()
	cache, err := uc.GetCache(context.Background(), "sup-1")
	if err != nil {
		t.Fatalf("GetCache: %v", err)
	}

	s, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{Raw: extBarcode})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	checkScanDates(t, s, d(time.August, 29), d(time.September, 29))
}

// Коробка с 6-значными датами: поля коробки (выработка — 4-е, срок — 5-е)
// читаются форматом правила, кол-во вложений — из своего поля.
func TestResolveBoxDatesYMD(t *testing.T) {
	uc, cache := gs1Cache(t, nil, []string{gs1BoxRuleYMD})

	s, matched, err := uc.resolveByRules(cache, cache.BoxRules, gs1BarcodeYMD,
		receiving.ScanEntry{Raw: gs1BarcodeYMD}, receiving.KindBox)
	if err != nil {
		t.Fatalf("resolveByRules: %v", err)
	}
	if !matched {
		t.Fatal("правило коробки не подошло к коду")
	}
	if s.Kind != receiving.KindBox || s.Qty != 1 {
		t.Fatalf("коробка: kind=%q qty=%d", s.Kind, s.Qty)
	}
	checkScanDates(t, s,
		time.Date(2026, time.July, 10, 0, 0, 0, 0, time.UTC),
		time.Date(2027, time.July, 5, 0, 0, 0, 0, time.UTC))
}

// Кеш отдаёт формат на страницу приёмки: JS читает даты тем же форматом, что и
// сервер (иначе клиент показал бы одну дату, а сервер записал другую).
func TestGetCacheRuleDateFormat(t *testing.T) {
	_, cache := gs1Cache(t, []string{gs1RuleYMD, itemRule}, nil)

	byLength := make(map[int]string, len(cache.ItemRules))
	for _, r := range cache.ItemRules {
		byLength[r.Length] = r.DateFormat
	}
	if got := byLength[50]; got != "ггммдд" {
		t.Errorf("формат правила длиной 50 = %q, want %q", got, "ггммдд")
	}
	if got := byLength[28]; got != "" {
		t.Errorf("формат правила без токена = %q, want пусто (ДДММГГГГ)", got)
	}
}

// Дата, не подходящая формату, — по-прежнему отказ скана с понятным текстом:
// 6 цифр поля не гарантируют календарно верную дату (32-е число).
func TestResolveRuleDateOutOfCalendar(t *testing.T) {
	uc, cache := gs1Cache(t, []string{gs1RuleYMD}, nil)

	// (17)990732 — 32 июля в календаре нет.
	raw := gs1Prefix + gs1Weight + "17" + "990732" + "11" + "260710" + gs1Tail
	if _, err := uc.Resolve(context.Background(), cache, receiving.ScanEntry{Raw: raw}); err == nil {
		t.Fatal("ожидалась ошибка о нераспознанной дате")
	}
}

// --- товар задаёт оператор: правила без кода товара (решение владельца 25.09.2026) ---

// NoItemCode — ни одно товарное правило не вычитывает код товара: скан сам товар
// не определит, страница показывает выбор товара в шапке партии (а не в строке).
func TestGetCacheNoItemCode(t *testing.T) {
	cases := []struct {
		name  string
		rules []string
		want  bool
	}{
		{"правило без кода товара", []string{gs1NoCodeRule}, true},
		{"правило с кодом товара", []string{gs1RuleYMD}, false},
		{"смешанные правила", []string{gs1NoCodeRule, gs1RuleYMD}, false},
		{"правил нет", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := testCacheRepo()
			repo.supplier.DecodeRules = c.rules
			uc := NewReceivingUseCase(repo, &stubStockAccepter{}, &stubWeightRecorder{})
			cache, err := uc.GetCache(context.Background(), "sup-1")
			if err != nil {
				t.Fatalf("GetCache: %v", err)
			}
			if cache.NoItemCode != c.want {
				t.Fatalf("NoItemCode = %v, want %v", cache.NoItemCode, c.want)
			}
		})
	}
}

// Даты кода сверяются с введёнными руками (партия/строка): совпали — скан принят,
// разошлись — отказ, а не молчаливая подмена срока.
func TestResolveDateMismatchRejected(t *testing.T) {
	uc, _ := newTestReceive()
	cache, _ := uc.GetCache(context.Background(), "sup-1")

	// extBarcode несёт выработку 29.08.2026 и срок 29.09.2026 (правило itemRule).
	same := d(time.August, 29)
	s, err := uc.Resolve(context.Background(), cache,
		receiving.ScanEntry{Raw: extBarcode, ManualProducedOn: &same})
	if err != nil {
		t.Fatalf("совпадающая выработка: %v", err)
	}
	if s.ProducedOn == nil || !s.ProducedOn.Equal(same) {
		t.Fatalf("выработка: %v, want %v", s.ProducedOn, same)
	}

	other := d(time.August, 28)
	_, err = uc.Resolve(context.Background(), cache,
		receiving.ScanEntry{Raw: extBarcode, ManualProducedOn: &other})
	if err == nil || !strings.Contains(err.Error(), "не совпадает") {
		t.Fatalf("расхождение выработки: ожидался отказ, получили %v", err)
	}

	bbOther := d(time.September, 30)
	_, err = uc.Resolve(context.Background(), cache,
		receiving.ScanEntry{Raw: extBarcode, ManualBestBefore: &bbOther})
	if err == nil || !strings.Contains(err.Error(), "срок годности") {
		t.Fatalf("расхождение срока: ожидался отказ, получили %v", err)
	}
}
