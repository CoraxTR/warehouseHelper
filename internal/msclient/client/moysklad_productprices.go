package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Синк цен и НДС товаров: выборка по пачке id (батч) и инкремент по updated.
// Оба запроса — ТОЛЬКО ЧТЕНИЕ, поэтому идут через SubmitOther (воркерпул сам
// повторяет временные сбои и держит рейт-лимит 5 req/s); своих retry-циклов и
// NoRetry здесь нет.

// productPricesPageLimit — размер страницы выборки цен (как profitPageLimit).
const productPricesPageLimit = 1000

// MSProductPrice — цена и НДС товара для синка прайса. BuyPrice/SalePrice —
// КОПЕЙКИ (округление math.Round); nil — МС поле не отдала (у товара нет
// цены). EffectiveVat/EffectiveVatEnabled — СЫРЫЕ значения МС: nil — полей в
// ответе нет (useParentVat=true, НДС наследуется от группы). EffectiveVat:
// (0,false) — «без НДС», (0,true) — 0%; живые ставки 10/22.
type MSProductPrice struct {
	ID                  string
	BuyPrice            *int64 // копейки; nil — МС не отдала
	SalePrice           *int64 // первый (единственный) элемент salePrices; nil — не отдала
	EffectiveVat        *int   // сырое значение МС
	EffectiveVatEnabled *bool  // nil — поля нет в ответе
	UseParentVat        bool
}

// FetchProductPricesByIDs — цены и НДС пачки товаров по их id.
//
// Фильтр — ПОВТОРЯЮЩИЕСЯ условия "id=<uuid>" через ';' (ИЛИ по докам МС);
// список через запятую МС отвергает (400/1014). Значение отдаём
// url.Values.Encode() — он закодирует как id%3D...%3B.... Свой заголовок
// Accept не шлём: транспорт уже проставляет Content-Type/Accept-Encoding,
// а лишний Accept МС отвергает (400/1062).
//
// Пустой список id — сразу nil без запроса. Пагинация: limit=1000 и добор
// offset'ами, пока offset+len(page) < meta.size (страховка на будущее).
func (msac *MSAPIClient) FetchProductPricesByIDs(parentctx context.Context, ids []string) ([]MSProductPrice, error) {
	// Пустой список — заведомо нечего фильтровать: не тратим запрос.
	if len(ids) == 0 {
		return nil, nil
	}

	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
		defer cancel()

		rows, err := msac.fetchProductPrices(ctx, apiKey, func(offset, limit int) (string, error) {
			return msac.productPricesByIDsEndpoint(ids, offset, limit)
		})
		if err != nil {
			return nil, err
		}

		return rows, nil
	}

	return msac.submitProductPrices(parentctx, job, "FetchProductPricesByIDs")
}

// FetchProductPricesSince — инкремент цен и НДС: товары, изменённые не раньше
// since (filter "updated>=<момент>"). Момент форматируется в TZ учётки (МСК):
// учётка МС живёт в МСК, сервер — в UTC. Пагинация та же, что в батче.
func (msac *MSAPIClient) FetchProductPricesSince(parentctx context.Context, since time.Time) ([]MSProductPrice, error) {
	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
		defer cancel()

		rows, err := msac.fetchProductPrices(ctx, apiKey, func(offset, limit int) (string, error) {
			return msac.productPricesSinceEndpoint(since, offset, limit)
		})
		if err != nil {
			return nil, err
		}

		return rows, nil
	}

	return msac.submitProductPrices(parentctx, job, "FetchProductPricesSince")
}

// submitProductPrices — общая обвязка обоих методов: воркерпул + ожидание
// результата + приведение типа. Прямых запросов к МС здесь нет.
func (msac *MSAPIClient) submitProductPrices(
	parentctx context.Context,
	job func(apiKey string) (any, error),
	op string,
) ([]MSProductPrice, error) {
	resCh := msac.workerpool.SubmitOther(job)

	select {
	case res := <-resCh:
		if res.Err != nil {
			return nil, fmt.Errorf("%s failed: %w", op, res.Err)
		}

		rows, ok := res.Value.([]MSProductPrice)
		if !ok {
			return nil, errors.New(op + " failed: unexpected value type")
		}

		return rows, nil
	case <-parentctx.Done():
		return nil, parentctx.Err()
	}
}

// fetchProductPrices — пагинация выборки цен: endpointFor(offset, limit)
// строит URL страницы, цикл идёт до offset+len(page) >= meta.size.
// Body закрываем явно в цикле: defer накопил бы открытые соединения.
func (msac *MSAPIClient) fetchProductPrices(
	ctx context.Context,
	apiKey string,
	endpointFor func(offset, limit int) (string, error),
) ([]MSProductPrice, error) {
	var prices []MSProductPrice

	for offset := 0; ; offset += productPricesPageLimit {
		// Отмена контекста между страницами: не тратим запрос, если родитель уже отменил.
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		endpoint, err := endpointFor(offset, productPricesPageLimit)
		if err != nil {
			return nil, err
		}

		body, resp, err := msac.httpRequest(ctx, http.MethodGet, endpoint, apiKey, http.NoBody)
		if err != nil {
			return nil, err
		}

		if err := resp.Body.Close(); err != nil {
			slog.Error(fmt.Sprintf("failed to close response body: %v", err))
		}

		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			return nil, msAPIError(resp.Status, body)
		}

		page, size, err := unmarshalProductPriceRows(body)
		if err != nil {
			return nil, err
		}

		prices = append(prices, page...)

		if offset+len(page) >= size || len(page) == 0 {
			break
		}
	}

	return prices, nil
}

// productPricesByIDsEndpoint — URL выборки товаров пачкой id. filter —
// повторяющиеся условия "id=<uuid>" через ';' (ИЛИ по докам МС); Encode()
// отдаст их как id%3D...%3B... (запятая между id запрещена — МС 400/1014).
func (msac *MSAPIClient) productPricesByIDsEndpoint(ids []string, offset, limit int) (string, error) {
	endpoint, err := msac.entityEndpoint("product")
	if err != nil {
		return "", err
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("failed to parse product endpoint: %w", err)
	}

	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, "id="+id)
	}

	q := u.Query()
	q.Set("filter", strings.Join(parts, ";"))
	q.Set("limit", strconv.Itoa(limit))
	q.Set("offset", strconv.Itoa(offset))
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// productPricesSinceEndpoint — URL инкремента: filter "updated>=<момент>"
// (одиночное условие), момент — в МСК. limit/offset — пагинация.
func (msac *MSAPIClient) productPricesSinceEndpoint(since time.Time, offset, limit int) (string, error) {
	endpoint, err := msac.entityEndpoint("product")
	if err != nil {
		return "", err
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("failed to parse product endpoint: %w", err)
	}

	q := u.Query()
	q.Set("filter", "updated>="+momentMSK(since))
	q.Set("limit", strconv.Itoa(limit))
	q.Set("offset", strconv.Itoa(offset))
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// momentMSK — момент в TZ учётки МС (МСК) для фильтров: учётка МС живёт в МСК,
// сервер — в UTC. Формат "2006-01-02 15:04:05" (time.DateTime).
// auditLoc — общий TZ учётки (см. moysklad_audit.go).
func momentMSK(t time.Time) string {
	return t.In(auditLoc).Format(time.DateTime)
}

// unmarshalProductPriceRows — тело ответа {meta:{size},rows:[...]} в модели цен.
// Строка разбирается через parseMSProductPrice (одна точка правды по полям).
func unmarshalProductPriceRows(body []byte) (prices []MSProductPrice, size int, err error) {
	var env struct {
		Meta struct {
			Size int `json:"size"`
		} `json:"meta"`
		Rows []json.RawMessage `json:"rows"`
	}

	if err := json.Unmarshal(body, &env); err != nil {
		return nil, 0, fmt.Errorf("failed to unmarshal product prices: %w", err)
	}

	for _, row := range env.Rows {
		p, err := parseMSProductPrice(row)
		if err != nil {
			return nil, 0, err
		}

		prices = append(prices, p)
	}

	return prices, env.Meta.Size, nil
}

// parseMSProductPrice — разбор одной строки товара в цену для синка: id,
// buyPrice.value (копейки, math.Round), salePrices[0].value (первый/единственный
// тип), effectiveVat/effectiveVatEnabled/useParentVat. buyPrice и salePrices
// могут отсутствовать, salePrices может быть пустым — тогда соответствующая
// цена nil (без паники). Поля НДС — указатели: отсутствие ключа даёт nil.
func parseMSProductPrice(row json.RawMessage) (MSProductPrice, error) {
	var p MSProduct

	if err := json.Unmarshal(row, &p); err != nil {
		return MSProductPrice{}, fmt.Errorf("failed to unmarshal product row: %w", err)
	}

	return ProductPriceFrom(p), nil
}

// ProductPriceFrom переносит модель товара МС в плоскую цену для синка: одно
// правило «где в карточке лежат цены и НДС» на все пути — им пользуется и
// фоновый обновитель, и выгрузка дерева/ресинк каталога (goods). Экспортирована
// намеренно: дубликат правила в двух пакетах молча разъехался бы.
func ProductPriceFrom(p MSProduct) MSProductPrice {
	price := MSProductPrice{
		ID:                  p.ID,
		EffectiveVat:        p.EffectiveVat,
		EffectiveVatEnabled: p.EffectiveVatEnabled,
		UseParentVat:        p.UseParentVat,
	}

	if p.BuyPrice != nil {
		v := int64(math.Round(p.BuyPrice.Value))
		price.BuyPrice = &v
	}

	// Цена продажи у нас ровно одна — берём первый элемент массива.
	if len(p.SalePrices) > 0 {
		v := int64(math.Round(p.SalePrices[0].Value))
		price.SalePrice = &v
	}

	return price
}
