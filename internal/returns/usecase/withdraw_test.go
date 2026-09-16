package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"warehouseHelper/internal/innercode"
)

// ── Вывод из продажи (WithdrawFromSale) ────────────────────────────────────

func TestWithdrawFromSale_SinglePiece(t *testing.T) {
	env := newTestEnv(newStubRepo())
	uc, stockS := env.uc, env.stock

	n, err := uc.WithdrawFromSale(context.Background(), []string{etiketa(codeA, 657)})
	if err != nil {
		t.Fatalf("WithdrawFromSale: %v", err)
	}
	if n != 1 {
		t.Errorf("списано кусков = %d, want 1", n)
	}
	if len(stockS.picked) != 1 {
		t.Fatalf("picked = %+v, want одно списание", stockS.picked)
	}
	lot := stockS.picked[0]
	wantBB := time.Date(2026, time.September, 15, 0, 0, 0, 0, time.UTC)
	if lot.ProductID != prodA || lot.Qty != 1 || !lot.BestBefore.Equal(wantBB) {
		t.Errorf("списание = %+v, want prodA qty 1 на %v (срок из этикетки)", lot, wantBB)
	}
	if len(stockS.accepted) != 0 {
		t.Errorf("accepted = %+v, want пусто: вывод только списывает остатки", stockS.accepted)
	}
}

func TestWithdrawFromSale_GroupsLotsByProductAndExpiry(t *testing.T) {
	env := newTestEnv(newStubRepo())
	uc, stockS := env.uc, env.stock

	// Два куска Чак ролла с одним сроком (лот qty 2), один Соус (qty 1) и
	// отдельный кусок Чак ролла с другим сроком (свой лот qty 1): ключ
	// списания — пара (товар, срок из этикетки). Второй срок собирает
	// владелец формата (innercode.EncodeItem).
	other, err := innercode.EncodeItem(codeA, 400,
		time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.September, 20, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("EncodeItem: %v", err)
	}

	n, err := uc.WithdrawFromSale(context.Background(), []string{
		etiketa(codeA, 400),
		etiketa(codeA, 257),
		etiketa(codeD, 1),
		other,
	})
	if err != nil {
		t.Fatalf("WithdrawFromSale: %v", err)
	}
	if n != 4 {
		t.Errorf("списано кусков = %d, want 4", n)
	}
	if len(stockS.picked) != 3 {
		t.Fatalf("picked = %+v, want 3 лота (товар+срок)", stockS.picked)
	}

	type key struct {
		productID string
		bb        string
	}
	got := make(map[key]int64, len(stockS.picked))
	for _, lot := range stockS.picked {
		got[key{productID: lot.ProductID, bb: lot.BestBefore.Format(time.DateOnly)}] += lot.Qty
	}
	want := map[key]int64{
		{productID: prodA, bb: "2026-09-15"}: 2,
		{productID: prodA, bb: "2026-09-20"}: 1,
		{productID: prodD, bb: "2026-09-15"}: 1,
	}
	if len(got) != len(want) {
		t.Fatalf("лоты = %v, want %v", got, want)
	}
	for k, qty := range want {
		if got[k] != qty {
			t.Errorf("лот %+v qty = %d, want %d", k, got[k], qty)
		}
	}
}

func TestWithdrawFromSale_DuplicateScanCountsAsSeparatePiece(t *testing.T) {
	env := newTestEnv(newStubRepo())
	uc, stockS := env.uc, env.stock

	// Этикетки штучного товара с одним сроком идентичны — два скана одного
	// кода легитимно значат два куска (повтор неотличим, защита невозможна).
	n, err := uc.WithdrawFromSale(context.Background(), []string{
		etiketa(codeD, 1),
		etiketa(codeD, 1),
	})
	if err != nil {
		t.Fatalf("WithdrawFromSale: %v", err)
	}
	if n != 2 {
		t.Errorf("списано кусков = %d, want 2", n)
	}
	if len(stockS.picked) != 1 || stockS.picked[0].Qty != 2 {
		t.Fatalf("picked = %+v, want один лот Соус qty 2", stockS.picked)
	}
}

func TestWithdrawFromSale_BoxRejected(t *testing.T) {
	env := newTestEnv(newStubRepo())
	uc, stockS := env.uc, env.stock

	_, err := uc.WithdrawFromSale(context.Background(), []string{boxCode(codeA)})
	if _, ok := errors.AsType[*ValidationError](err); !ok {
		t.Fatalf("want ValidationError про коробку, got %v", err)
	}
	if len(stockS.picked) != 0 {
		t.Fatal("коробка не должна ничего списывать (снимаются только куски)")
	}
}

func TestWithdrawFromSale_UnknownCodeRejectedWholeBatch(t *testing.T) {
	env := newTestEnv(newStubRepo())
	uc, stockS := env.uc, env.stock

	// Первый скан валиден, второй — товар, которого нет в каталоге:
	// батч отклоняется целиком (атомарность: ничего не списывается).
	_, err := uc.WithdrawFromSale(context.Background(), []string{
		etiketa(codeA, 657),
		etiketa("00219999", 500),
	})
	if _, ok := errors.AsType[*ValidationError](err); !ok {
		t.Fatalf("want ValidationError про неизвестный код, got %v", err)
	}
	if len(stockS.picked) != 0 {
		t.Fatal("батч с неизвестным кодом не должен списываться частично")
	}
}

func TestWithdrawFromSale_InvalidScanRejected(t *testing.T) {
	env := newTestEnv(newStubRepo())
	uc, stockS := env.uc, env.stock

	_, err := uc.WithdrawFromSale(context.Background(), []string{"123"})
	if _, ok := errors.AsType[*ValidationError](err); !ok {
		t.Fatalf("want ValidationError, got %v", err)
	}
	if len(stockS.picked) != 0 {
		t.Fatal("невалидный скан ничего не списывает")
	}
}

func TestWithdrawFromSale_EmptyRejected(t *testing.T) {
	env := newTestEnv(newStubRepo())
	uc, stockS := env.uc, env.stock

	_, err := uc.WithdrawFromSale(context.Background(), nil)
	if _, ok := errors.AsType[*ValidationError](err); !ok {
		t.Fatalf("want ValidationError про пустой батч, got %v", err)
	}
	if len(stockS.picked) != 0 {
		t.Fatal("пустой батч ничего не списывает")
	}
}

// TestWithdrawFromSale_DeficitDelegatedToStock — сканов больше, чем есть в
// лоте: квоту режет владелец остатков, а не эта страница. Свой слой ничего не
// проверяет и не урезает (в PickStock уходит полное количество сканов), а
// поведение дефицита — списание до нуля и уведомление складу «Необходимо
// обновить сроки по …», отрицательных остатков не бывает — уже закреплено
// тестами stock: TestPickStockDeficitNotifiesGroup / TestPickStockMissingLotDeficit.
func TestWithdrawFromSale_DeficitDelegatedToStock(t *testing.T) {
	env := newTestEnv(newStubRepo())
	uc, stockS := env.uc, env.stock

	n, err := uc.WithdrawFromSale(context.Background(), []string{
		etiketa(codeA, 657),
		etiketa(codeA, 657),
		etiketa(codeA, 657),
	})
	if err != nil {
		t.Fatalf("WithdrawFromSale: %v", err)
	}
	if n != 3 {
		t.Errorf("списано кусков = %d, want 3", n)
	}
	if len(stockS.picked) != 1 || stockS.picked[0].Qty != 3 {
		t.Fatalf("picked = %+v, want один лот prodA qty 3 (без локального урезания)", stockS.picked)
	}
}

// TestWithdrawFromSale_StockErrorPropagates — сбой записи остатков: наружу
// уходит ошибка шва stock (HTTP 500), а не ValidationError (HTTP 400).
func TestWithdrawFromSale_StockErrorPropagates(t *testing.T) {
	env := newTestEnv(newStubRepo())
	env.stock.err = errors.New("БД упала")
	uc := env.uc

	_, err := uc.WithdrawFromSale(context.Background(), []string{etiketa(codeA, 657)})
	if err == nil {
		t.Fatal("ошибка PickStock должна вернуться наружу, got nil")
	}
	if _, ok := errors.AsType[*ValidationError](err); ok {
		t.Fatalf("сбой остатков не должен выглядеть как ValidationError (400): %v", err)
	}
	if !errors.Is(err, env.stock.err) {
		t.Errorf("ошибка = %v, want обёртку над %v", err, env.stock.err)
	}
}
