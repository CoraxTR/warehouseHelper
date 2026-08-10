package orderscache

import (
	"sync"
	"time"
	"warehouseHelper/internal/domain"
)

type OrderCache struct {
	data   map[string]struct{}
	OCmu   sync.RWMutex
	ticker *time.Ticker
}

func NewOrderCache() *OrderCache {
	return &OrderCache{
		data:   make(map[string]struct{}),
		ticker: time.NewTicker(12 * time.Hour),
	}
}

func (o *OrderCache) AddOrdersToCache(orders []*domain.InternalOrder) {
	o.OCmu.Lock()
	defer o.OCmu.Unlock()

	for _, order := range orders {
		o.data[order.GetHREF()] = struct{}{}
	}
}

func (o *OrderCache) CheckOrderInCache(s string) bool {
	o.OCmu.RLock()
	defer o.OCmu.RUnlock()

	_, ok := o.data[s]
	if !ok {
		return false
	}

	return true
}

func (o *OrderCache) StartCacheFlusher() {
	go func() {
		for {
			now := time.Now()

			next := time.Date(now.Year(), now.Month(), now.Day(), 22, 0, 0, 0, now.Location())
			if now.After(next) {
				next = next.Add(24 * time.Hour)
			}

			duration := time.Until(next)
			<-time.After(duration)
			o.Flush()
		}
	}()
}

func (o *OrderCache) Flush() {
	o.OCmu.Lock()
	defer o.OCmu.Unlock()

	o.data = make(map[string]struct{})
}
