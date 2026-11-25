package usecase

import (
	"context"
	"testing"
	"warehouseHelper/internal/discounts"
)

// Пара с постоянной скидкой и ступенью по сроку выше неё — ОДНА позиция на
// сайте и одно место: ступень меняет только значение, слот ёмкости она не
// занимает (решение владельца 03.11.2026, признак — наличие постоянной у пары,
// а не метка победившего источника). Раньше такая пара держала слот фантомом:
// значение на сайт не попадало (место занято, подъём вне КТ-дня запрещён), а
// следующая по приоритету пара с пустым местом вылетала в «допродажу»
// (25.11.2026: Ти-Бон 5 % против ступени 10 % — 11 активных при ёмкости 12).
func TestPermanentRowKeepsSlotFree(t *testing.T) {
	bb := day(12)
	perm := PairState{
		ProductID: "perm", Name: "Ти-Бон", BestBefore: bb, Qty: 16, DaysLeft: 12,
		Permanent: new(int16(5)), Expiry: new(int16(10)),
	}
	near := PairState{
		ProductID: "near", Name: "Сирлойн флэп", BestBefore: bb, Qty: 5, DaysLeft: 12,
		Expiry: new(int16(10)),
	}
	far := PairState{
		ProductID: "far", Name: "Вырезка Frigolar", BestBefore: bb.AddDate(0, 0, 10),
		Qty: 6, DaysLeft: 22, Expiry: new(int16(10)),
	}

	d := discounts.BuildDigest([]discounts.Row{perm.Row(), near.Row(), far.Row()}, 1)

	// Единственный слот достался паре с пустым местом, а не паре с постоянной.
	if len(d.Discounts) != 2 {
		t.Fatalf("активных строк %d, want 2 (слот + постоянная): %+v", len(d.Discounts), d.Discounts)
	}
	if d.Discounts[0].ProductID != "near" {
		t.Errorf("слот достался %q, want near: постоянная место не занимает", d.Discounts[0].ProductID)
	}
	if len(d.Surplus) != 1 || d.Surplus[0].ProductID != "far" {
		t.Errorf("допродажа %+v, want только far", d.Surplus)
	}

	// Значение постоянной пары — победившая ступень, и она не потерялась.
	for _, row := range d.Discounts {
		if row.ProductID != "perm" {
			continue
		}
		if !row.Permanent {
			t.Errorf("строка %q слотовой быть не должна", row.ProductID)
		}
		if row.Percent != 10 {
			t.Errorf("значение постоянной пары %d, want 10 (ступень выше постоянной)", row.Percent)
		}
	}
}

// Ручная скидка сайта на паре с постоянной — исключение: её ставит человек, и
// место в окне она занимает (иначе слот «терялся» бы у позиции, которая реально
// стоит на сайте под ручным значением).
func TestManualOnPermanentPairTakesSlot(t *testing.T) {
	bb := day(12)
	perm := PairState{
		ProductID: "perm", Name: "Ти-Бон", BestBefore: bb, Qty: 16, DaysLeft: 12,
		Permanent: new(int16(5)), Manual: new(int16(30)),
	}
	next := PairState{
		ProductID: "next", Name: "Следующая", BestBefore: bb.AddDate(0, 0, 1), Qty: 5, DaysLeft: 13,
		Expiry: new(int16(10)),
	}

	row := perm.Row()
	if row.Permanent {
		t.Fatalf("ручная на паре с постоянной слот не занимает: %+v", row)
	}
	if row.Percent != 30 {
		t.Errorf("значение ручной %d, want 30", row.Percent)
	}

	d := discounts.BuildDigest([]discounts.Row{row, next.Row()}, 1)
	if len(d.Discounts) != 1 || d.Discounts[0].ProductID != "perm" {
		t.Fatalf("активные %+v, want только perm: ручная занимает слот", d.Discounts)
	}
	if len(d.Surplus) != 1 || d.Surplus[0].ProductID != "next" {
		t.Errorf("допродажа %+v, want next", d.Surplus)
	}
}

// Конец цепочки: освободившееся место реально работает — следующая по
// приоритету пара входит в окно и получает свою ступень на пустое место.
func TestPermanentPairFreesWindowSlotForNextPair(t *testing.T) {
	now := recalcNow(5) // суббота: КТ-день, ступень пишется
	h := newRecalcHarness(now,
		lotInput("perm", "Постоянная", day(9), 5, shelfLifeInput(26),
			permanentInput(5, now.AddDate(0, 0, 30))),
		lotInput("next", "Следующая", day(11), 5, shelfLifeInput(26)),
	)
	h.uc.SetWindowCap(1)

	if err := h.uc.RecalcExpiry(context.Background(), now); err != nil {
		t.Fatalf("пересчёт по сроку: %v", err)
	}

	got := make(map[string]bool)
	for _, w := range flatWrites(h.batches()) {
		got[w.ProductID] = true
	}
	if !got["perm"] {
		t.Errorf("постоянная пара не получила ступень: %+v", flatWrites(h.batches()))
	}
	if !got["next"] {
		t.Errorf("следующая пара не вошла в окно: %+v — слот занят постоянной", flatWrites(h.batches()))
	}

	if keys := h.uc.ActiveLots(1); len(keys) != 2 {
		t.Errorf("пар окна %d, want 2 (постоянная + одна слотовая): %+v", len(keys), keys)
	}
}
