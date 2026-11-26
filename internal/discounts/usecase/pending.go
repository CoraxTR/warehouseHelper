package usecase

import (
	"sync"

	"warehouseHelper/internal/discounts"
)

// pendingEntries — пары, которым скидку не записали из-за закрытого входа
// (ТГ-день до 14:45) или первого прохода после старта. Они «ждут места»: вход
// считается по переходу состава окна, и без метки такая пара навсегда осталась
// бы «уже в окне» (инцидент 26.11.2026).
type pendingEntries struct {
	mu   sync.Mutex
	keys map[discounts.LotKey]struct{}
}

func (p *pendingEntries) add(keys map[discounts.LotKey]struct{}) {
	if len(keys) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.keys == nil {
		p.keys = make(map[discounts.LotKey]struct{}, len(keys))
	}
	for k := range keys {
		p.keys[k] = struct{}{}
	}
}

// take — забрать метки и очистить набор: пары входят один раз, повторно
// форсировать вход незачем.
func (p *pendingEntries) take() map[discounts.LotKey]struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.keys
	p.keys = nil
	return out
}
