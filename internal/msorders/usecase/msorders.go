// Пакет usecase — сценарии раздела «Заказы» МойСклад (internal/msorders):
// поиск заказа покупателя по номеру для подбора. Своей схемы БД у модуля
// нет — данные живут в МС, usecase проксирует клиент и готовит строки
// для отображения (тримминг моментов до даты, заглушки пустых значений).
package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"warehouseHelper/internal/metrics"
	"warehouseHelper/internal/msclient/client"
)

const trackPkg = "msorders"

// dash — заглушка пустого значения в строке результата.
const dash = "—"

// ErrEmptyName — пустое поле поиска (после обрезки пробелов).
var ErrEmptyName = errors.New("введите номер заказа")

// OrderSearchClient — контракт поиска заказов в МойСклад, реализуется
// *client.MSAPIClient.
type OrderSearchClient interface {
	SearchCustomerOrdersByName(ctx context.Context, name string) ([]client.MSOrder, error)
	// FetchOrderAgentByHREF — имя контрагента по agent.meta.href строки.
	// МС игнорирует expand=agent при filter=name (проверено контрольными
	// запросами), поэтому имя дотягивается отдельным запросом на строку.
	FetchOrderAgentByHREF(ctx context.Context, o *client.MSOrder) (string, string, error)
}

// OrderRow — строка результата поиска, готовая для показа в таблице:
// даты уже приведены к ДД.ММ.ГГГГ, пустые значения — «—».
// Href — полный href заказа из meta ответа (данные МС, не хранится в БД):
// заготовка для будущего перехода «внутрь» заказа.
type OrderRow struct {
	Name         string // номер заказа (name)
	Created      string // moment, ДД.ММ.ГГГГ
	AgentName    string // контрагент (agent.name при expand=agent)
	DeliveryDate string // deliveryPlannedMoment, ДД.ММ.ГГГГ
	Href         string // meta.href заказа
}

// UseCase — поиск заказов МС.
type UseCase struct {
	ms OrderSearchClient
}

// NewUseCase создаёт сценарий с переданным клиентом МС.
func NewUseCase(ms OrderSearchClient) *UseCase {
	return &UseCase{ms: ms}
}

// Search ищет заказы по точному номеру (name). Пустое поле — ErrEmptyName;
// «ничего не найдено» — пустой слайс без ошибки.
func (uc *UseCase) Search(ctx context.Context, name string) ([]OrderRow, error) {
	done := metrics.Track(trackPkg, "Search")
	defer done()

	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrEmptyName
	}

	orders, err := uc.ms.SearchCustomerOrdersByName(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("search orders: %w", err)
	}

	rows := make([]OrderRow, 0, len(orders))
	for i := range orders {
		o := &orders[i]
		rows = append(rows, OrderRow{
			Name:         o.Name,
			Created:      shortDate(o.Moment),
			AgentName:    orDash(uc.agentName(ctx, o)),
			DeliveryDate: shortDate(o.DeliveryPlannedMoment),
			Href:         o.Meta.HREF,
		})
	}

	return rows, nil
}

// agentName возвращает имя контрагента заказа. Если имя не приехало в строке
// (expand=agent игнорируется МС при filter=name) — дотягивает по
// agent.meta.href отдельным запросом (воркерпул рейт-лимитит). Ошибка хопа
// не роняет поиск: клиент уже залогировал её, имя остаётся пустым — в строке
// таблицы будет «—» (вторичные данные, как barcodes на форме поставщика).
func (uc *UseCase) agentName(ctx context.Context, o *client.MSOrder) string {
	if name := strings.TrimSpace(o.Agent.Name); name != "" {
		return name
	}
	if o.Agent.Meta.HREF == "" {
		return ""
	}
	name, _, err := uc.ms.FetchOrderAgentByHREF(ctx, o)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(name)
}

// shortDate режет момент МС («2006-01-02 15:04:05.000») до даты ДД.ММ.ГГГГ.
// Пустая или непарсящаяся строка — «—».
func shortDate(msMoment string) string {
	datePart, _, _ := strings.Cut(msMoment, " ")
	if datePart == "" {
		return dash
	}
	t, err := time.Parse(time.DateOnly, datePart)
	if err != nil {
		return dash
	}
	return t.Format("02.01.2006")
}

// orDash возвращает «—» для пустой строки.
func orDash(s string) string {
	if s = strings.TrimSpace(s); s == "" {
		return dash
	}
	return s
}
