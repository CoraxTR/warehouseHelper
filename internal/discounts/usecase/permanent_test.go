package usecase

import (
	"testing"
	"time"

	"warehouseHelper/internal/discounts"
)

// pairPermanent — пара товара с постоянной скидкой карточки.
func pairPermanent(bb time.Time, qty int64, percent *int16) PairState {
	return PairState{
		Key:        lotKey("p1", bb),
		ProductID:  "p1",
		BestBefore: bb,
		Qty:        qty,
		Permanent:  percent,
	}
}

// Постоянная скидка пишется в канал сайта своим владельцем: по владельцу
// движок снимает её, когда основание ушло, и не трогает чужие значения
// (решение владельца 03.11.2026).
func TestPermanentWrites(t *testing.T) {
	bb := day(9)

	t.Run("ставит значение с владельцем permanent", func(t *testing.T) {
		w := permanentWrites([]PairState{pairPermanent(bb, 5, new(int16(15)))}, nil)
		if len(w) != 1 {
			t.Fatalf("записей %d, want одну: %+v", len(w), w)
		}
		if w[0].General == nil || *w[0].General != 15 {
			t.Fatalf("general = %v, want 15", w[0].General)
		}
		if w[0].GeneralOwner != discounts.OwnerPermanent.String() {
			t.Errorf("владелец %q, want permanent", w[0].GeneralOwner)
		}
		if w[0].Source != discounts.SourcePermanent.String() {
			t.Errorf("метка источника %q, want permanent", w[0].Source)
		}
	})

	t.Run("нет остатка — скидки нет", func(t *testing.T) {
		p := pairPermanent(bb, 0, new(int16(15)))
		if w := permanentWrites([]PairState{p}, nil); len(w) != 0 {
			t.Fatalf("записи при нулевом остатке: %+v", w)
		}
	})

	t.Run("дата вышла — снимает своё значение", func(t *testing.T) {
		p := pairPermanent(bb, 5, nil)
		p.GeneralOwner = discounts.OwnerPermanent
		w := permanentWrites([]PairState{p}, nil)
		if len(w) != 1 || w[0].General != nil {
			t.Fatalf("снятие не записано: %+v", w)
		}
	})

	t.Run("дата вышла, но значение чужое — не трогает", func(t *testing.T) {
		p := pairPermanent(bb, 5, nil)
		p.GeneralOwner = discounts.OwnerExpiry
		if w := permanentWrites([]PairState{p}, nil); len(w) != 0 {
			t.Fatalf("чужое значение тронуто: %+v", w)
		}
	})

	t.Run("ступень глубже — постоянная не понижает", func(t *testing.T) {
		p := pairPermanent(bb, 5, new(int16(15)))
		already := []discounts.DiscountWrite{{
			ProductID: "p1", BestBefore: bb, General: new(int16(30)),
			Source: discounts.SourceExpiry.String(), GeneralOwner: discounts.OwnerExpiry.String(),
		}}
		if w := permanentWrites([]PairState{p}, already); len(w) != 0 {
			t.Fatalf("постоянная перебила ступень: %+v", w)
		}
	})

	t.Run("ручная выше — постоянная не пишется", func(t *testing.T) {
		p := pairPermanent(bb, 5, new(int16(15)))
		p.Manual = new(int16(25))
		p.Applied = new(int16(25))
		if w := permanentWrites([]PairState{p}, nil); len(w) != 0 {
			t.Fatalf("постоянная перебила ручную: %+v", w)
		}
	})

	t.Run("ручной 0 (бан) — пара не трогается", func(t *testing.T) {
		p := pairPermanent(bb, 5, new(int16(15)))
		p.Manual = new(int16(0))
		if w := permanentWrites([]PairState{p}, nil); len(w) != 0 {
			t.Fatalf("забаненная пара получила скидку: %+v", w)
		}
	})
}
