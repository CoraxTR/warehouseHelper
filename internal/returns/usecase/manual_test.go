package usecase

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ── Ручной возврат (ManualReturn) ──────────────────────────────────────────

// boxCode — этикетка коробки 33: internal_code(8)+вес(6)+кол-во(3)+даты(16).
func boxCode(code string) string {
	return code + "000400" + "003" + "01092026" + "15092026"
}

func TestManualReturn_AcceptsAndAggregates(t *testing.T) {
	env := newTestEnv(newStubRepo())
	uc, stockS := env.uc, env.stock

	// Весовой Чак ролл: два куска с разным весом (один срок) — лот qty 2;
	// штучный Соус: кусок (вес-заглушка 00001) — свой лот qty 1.
	n, err := uc.ManualReturn(context.Background(), []string{
		etiketa(codeA, 400),
		etiketa(codeA, 257),
		etiketa(codeD, 1),
	})
	if err != nil {
		t.Fatalf("ManualReturn: %v", err)
	}
	if n != 3 {
		t.Errorf("принято кусков = %d, want 3", n)
	}
	if len(stockS.accepted) != 2 {
		t.Fatalf("lots = %+v, want 2 лота (по товару)", stockS.accepted)
	}
	byID := map[string]int64{}
	for _, lot := range stockS.accepted {
		byID[lot.ProductID] += lot.Qty
	}
	if byID[prodA] != 2 || byID[prodD] != 1 {
		t.Errorf("lots по товарам = %v, want prodA 2 / prodD 1", byID)
	}
	wantBB := time.Date(2026, time.September, 15, 0, 0, 0, 0, time.UTC)
	for _, lot := range stockS.accepted {
		if !lot.BestBefore.Equal(wantBB) {
			t.Errorf("срок лота %s = %v, want %v (из этикетки)", lot.ProductID, lot.BestBefore, wantBB)
		}
	}
}

func TestManualReturn_DuplicateScanCountsAsSeparatePiece(t *testing.T) {
	env := newTestEnv(newStubRepo())
	uc, stockS := env.uc, env.stock

	// Этикетки штучного товара с одним сроком идентичны — два скана одного
	// кода легитимно значат два куска (повтор неотличим, защита невозможна).
	n, err := uc.ManualReturn(context.Background(), []string{
		etiketa(codeD, 1),
		etiketa(codeD, 1),
	})
	if err != nil {
		t.Fatalf("ManualReturn: %v", err)
	}
	if n != 2 {
		t.Errorf("принято кусков = %d, want 2", n)
	}
	if len(stockS.accepted) != 1 || stockS.accepted[0].Qty != 2 {
		t.Fatalf("lots = %+v, want один лот Соус qty 2", stockS.accepted)
	}
}

func TestManualReturn_BoxRejected(t *testing.T) {
	env := newTestEnv(newStubRepo())
	uc, stockS := env.uc, env.stock

	_, err := uc.ManualReturn(context.Background(), []string{boxCode(codeA)})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want ValidationError про коробку, got %v", err)
	}
	if len(stockS.accepted) != 0 {
		t.Fatal("коробка не должна попасть в остатки (возвращаются только куски)")
	}
}

func TestManualReturn_UnknownCodeRejectedWholeBatch(t *testing.T) {
	env := newTestEnv(newStubRepo())
	uc, stockS := env.uc, env.stock

	// Первый скан валиден, второй — товар, которого нет в каталоге:
	// батч отклоняется целиком (атомарность: ничего не записывается).
	_, err := uc.ManualReturn(context.Background(), []string{
		etiketa(codeA, 657),
		etiketa("00219999", 500),
	})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want ValidationError про неизвестный код, got %v", err)
	}
	if len(stockS.accepted) != 0 {
		t.Fatal("батч с неизвестным кодом не должен писаться частично")
	}
}

func TestManualReturn_InvalidScanRejected(t *testing.T) {
	env := newTestEnv(newStubRepo())
	uc, stockS := env.uc, env.stock

	_, err := uc.ManualReturn(context.Background(), []string{"123"})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want ValidationError, got %v", err)
	}
	if len(stockS.accepted) != 0 {
		t.Fatal("невалидный скан не пишется в остатки")
	}
}

func TestManualReturn_EmptyRejected(t *testing.T) {
	env := newTestEnv(newStubRepo())
	uc, stockS := env.uc, env.stock

	_, err := uc.ManualReturn(context.Background(), nil)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want ValidationError про пустой батч, got %v", err)
	}
	if len(stockS.accepted) != 0 {
		t.Fatal("пустой батч не пишется")
	}
}

func TestManualReturn_StockErrorPropagates(t *testing.T) {
	env := newTestEnv(newStubRepo())
	env.stock.err = errors.New("БД упала")
	uc := env.uc

	_, err := uc.ManualReturn(context.Background(), []string{etiketa(codeA, 657)})
	if err == nil || err.Error() == "" {
		t.Fatalf("ошибка AcceptStock должна вернуться наружу, got %v", err)
	}
}
