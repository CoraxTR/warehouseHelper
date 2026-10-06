package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// MSInventoryPosition — позиция создаваемой инвентаризации.
type MSInventoryPosition struct {
	AssortmentID string  // uuid товара (href собирается здесь: assortment не пересекает границы слоёв)
	Quantity     float64 // фактическое количество: кг для весовых, штуки для штучных
	PriceKop     int64   // закупочная цена, копейки (0 — цена не задана)
}

// MSInventoryDocument — созданный документ инвентаризации.
type MSInventoryDocument struct {
	ID   string // uuid документа
	Name string // «Инвентаризация № …» — как отдал МС
	URL  string // ссылка на документ в МС (meta.uuidHref) для оператора
}

// errInventoryStoreNotConfigured — склад МС не задан в конфиге (MSAPI_STORE_ID):
// документ инвентаризации без склада создать нельзя.
var errInventoryStoreNotConfigured = errors.New("инвентаризация недоступна: не задан MSAPI_STORE_ID")

// InventoryStoreConfigured — задан ли склад МС для складских документов
// (проверка для страницы: предупредить оператора до нажатия «Создать»).
func (msac *MSAPIClient) InventoryStoreConfigured() bool {
	return msac.msConfig.Refs != nil && msac.msConfig.Refs.StoreID != ""
}

// msInventoryMeta — meta-ссылка сущности МС в теле запроса.
type msInventoryMeta struct {
	HREF      string `json:"href"`
	Type      string `json:"type"`
	MediaType string `json:"mediaType"`
}

// msInventoryMetaRef — обёртка meta-ссылки ({meta:{href,…}}).
type msInventoryMetaRef struct {
	Meta msInventoryMeta `json:"meta"`
}

// msInventoryPositionBody — позиция документа инвентаризации в теле запроса.
// price передаётся ВСЕГДА, в том числе нулём (товары без закупочной цены идут
// в документ с ценой 0, а не пропускаются — решение владельца 06.10.2026).
type msInventoryPositionBody struct {
	Assortment msInventoryMetaRef `json:"assortment"`
	Quantity   float64            `json:"quantity"`
	Price      int64              `json:"price"`
}

// msInventoryBody — тело создания документа «Инвентаризация».
type msInventoryBody struct {
	Organization msInventoryMetaRef        `json:"organization"`
	Store        msInventoryMetaRef        `json:"store"`
	Positions    []msInventoryPositionBody `json:"positions"`
}

// msInventoryResponse — разбор 2xx-ответа создания инвентаризации.
// uuidHref может прийти пустым — это не ошибка (URL останется пустым).
type msInventoryResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Meta struct {
		UUIDHref string `json:"uuidHref"`
	} `json:"meta"`
}

// msMetaRef собирает meta-ссылку сущности МС: href + тип + mediaType.
func msMetaRef(href, entityType string) msInventoryMetaRef {
	return msInventoryMetaRef{Meta: msInventoryMeta{
		HREF:      href,
		Type:      entityType,
		MediaType: MSApplicationJSON,
	}}
}

// CreateInventory — создание документа «Инвентаризация» (POST /entity/inventory)
// с позициями: фактическое количество (quantity), закупочная цена в копейках (price).
// Организация и склад — из конфига (Refs.OrgID / Refs.StoreID); склад не задан →
// ошибка «инвентаризация недоступна: не задан MSAPI_STORE_ID», запрос в МС не уходит.
//
// Ключи — складской пул (SubmitWarehouseNoRetry): создание документа неидемпотентно,
// повтор при таймауте/5xx создал бы вторую инвентаризацию в учёте; лучше вернуть
// ошибку оператору (как CreateDemand).
func (msac *MSAPIClient) CreateInventory(
	parentctx context.Context,
	positions []MSInventoryPosition,
) (MSInventoryDocument, error) {
	var empty MSInventoryDocument

	if !msac.InventoryStoreConfigured() {
		return empty, errInventoryStoreNotConfigured
	}

	body := msInventoryBody{
		Organization: msMetaRef(msac.refHref("organization", msac.msConfig.Refs.OrgID), "organization"),
		Store:        msMetaRef(msac.refHref("store", msac.msConfig.Refs.StoreID), "store"),
		Positions:    make([]msInventoryPositionBody, 0, len(positions)),
	}
	for _, p := range positions {
		body.Positions = append(body.Positions, msInventoryPositionBody{
			Assortment: msMetaRef(msac.refHref("product", p.AssortmentID), "product"),
			Quantity:   p.Quantity,
			Price:      p.PriceKop,
		})
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return empty, fmt.Errorf("CreateInventory failed: %w", err)
	}

	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
		defer cancel()

		endpoint, err := msac.entityEndpoint("inventory")
		if err != nil {
			return nil, err
		}

		respBody, resp, err := msac.httpRequest(ctx, http.MethodPost, endpoint, apiKey, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}

		defer func() {
			closeErr := resp.Body.Close()
			if closeErr != nil {
				slog.Error(fmt.Sprintf("failed to close response body: %v", closeErr))
			}
		}()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, msAPIError(resp.Status, respBody)
		}

		var parsed msInventoryResponse
		if err := json.Unmarshal(respBody, &parsed); err != nil {
			return nil, fmt.Errorf("failed to unmarshal inventory response: %w", err)
		}

		return MSInventoryDocument{
			ID:   parsed.ID,
			Name: parsed.Name,
			URL:  parsed.Meta.UUIDHref,
		}, nil
	}

	// БЕЗ повторов (NoRetry): POST создания инвентаризации неидемпотентен — при
	// 5xx или таймауте МС мог успеть создать документ и потерять ответ.
	resCh := msac.workerpool.SubmitWarehouseNoRetry(job)

	select {
	case res := <-resCh:
		if res.Err != nil {
			return empty, fmt.Errorf("CreateInventory failed: %w", res.Err)
		}

		doc, ok := res.Value.(MSInventoryDocument)
		if !ok {
			return empty, errors.New("CreateInventory failed: unexpected value type")
		}

		return doc, nil
	case <-parentctx.Done():
		return empty, parentctx.Err()
	}
}
