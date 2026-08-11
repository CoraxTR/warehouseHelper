package orderscache

import (
	"testing"

	"warehouseHelper/internal/domain"
)

func TestOrderCacheRemoveFromCache(t *testing.T) {
	cache := NewOrderCache()

	order := &domain.InternalOrder{}
	order.SetHREF("https://api.moysklad.ru/api/remap/1.2/entity/customerorder/1")

	cache.AddOrdersToCache([]*domain.InternalOrder{order})

	if !cache.CheckOrderInCache(order.GetHREF()) {
		t.Fatal("заказ должен быть в кеше после добавления")
	}

	cache.RemoveFromCache(order.GetHREF())

	if cache.CheckOrderInCache(order.GetHREF()) {
		t.Fatal("заказ должен быть удалён из кеша")
	}
}
