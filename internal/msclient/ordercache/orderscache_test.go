package orderscache

import (
	"testing"

	"warehouseHelper/internal/domain"
)

func TestOrderCacheRemoveFromCache(t *testing.T) {
	cache := NewOrderCache()

	order := &domain.InternalOrder{}
	order.SetID("1")

	cache.AddOrdersToCache([]*domain.InternalOrder{order})

	if !cache.CheckOrderInCache(order.GetID()) {
		t.Fatal("заказ должен быть в кеше после добавления")
	}

	cache.RemoveFromCache(order.GetID())

	if cache.CheckOrderInCache(order.GetID()) {
		t.Fatal("заказ должен быть удалён из кеша")
	}
}
