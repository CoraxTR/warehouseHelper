package client

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"warehouseHelper/internal/config"
	"warehouseHelper/internal/domain"
	"warehouseHelper/internal/msclient/workerpool"
)

const msEncoding = "gzip"

type OrderCache interface {
	AddOrdersToCache(orders []*domain.InternalOrder)
	CheckOrderInCache(s string) bool
	RemoveFromCache(s string)
}

type MSAPIClient struct {
	workerpool *workerpool.MSWorkerPool
	msConfig   *config.MSConfig
	rgConfig   *config.RefGoConfig
	Cache      OrderCache
}

func NewMSAPIClient(c *config.Config, wp *workerpool.MSWorkerPool, cache OrderCache) *MSAPIClient {
	return &MSAPIClient{
		workerpool: wp,
		msConfig:   c.MSConfig,
		rgConfig:   c.RefGoConfig,
		Cache:      cache,
	}
}

// RemoveOrderFromCache удаляет заказ из кеша обработанных заказов.
func (msac *MSAPIClient) RemoveOrderFromCache(id string) {
	msac.Cache.RemoveFromCache(id)
}

func (msac *MSAPIClient) FetchOrderAgentByHREF(parentCtx context.Context, o *MSOrder) (name, phone string, err error) {
	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentCtx, 300*time.Second)
		defer cancel()

		body, resp, err := msac.httpRequest(ctx, http.MethodGet, o.Agent.Meta.HREF, apiKey, nil)
		if err != nil {
			return nil, err
		}

		defer func() {
			err = resp.Body.Close()
			if err != nil {
				log.Printf("failed to close response body: %v", err)
			}
		}()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("API returned %s", resp.Status)
		}

		agentInfo, err := unmarshalAgentInfo(body)
		if err != nil {
			return nil, err
		}

		return agentInfo, nil
	}

	resultCh := msac.workerpool.SubmitOther(job)

	select {
	case res := <-resultCh:
		if res.Err != nil {
			log.Printf("FetchOrderAgentByHREF failed: %v", res.Err)

			return "", "", res.Err
		}

		info, ok := res.Value.(*MSAgentInfo)
		if !ok {
			return "", "", errors.New("FetchOrderAgentByHREF failed: unexpected value type")
		}

		return info.Name, info.Phone, nil
	case <-parentCtx.Done():
		log.Printf("FetchOrderAgentByHREF timed out: %v", parentCtx.Err())

		return "", "", nil
	}
}

func (msac *MSAPIClient) FetchOrderPositionsByHREF(parentCtx context.Context, o *MSOrder) ([]MSPosition, error) {
	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentCtx, 300*time.Second)
		defer cancel()

		body, resp, err := msac.httpRequest(ctx, http.MethodGet, o.MSPositions.Meta.HREF, apiKey, nil)
		if err != nil {
			select {
			case <-ctx.Done():
				log.Printf("FetchOrderPositionsByHREF timed out: %v", ctx.Err())
			default:
				log.Printf("FetchOrderPositionsByHREF failed: %v", err)
			}

			return nil, err
		}

		defer func() {
			err = resp.Body.Close()
			if err != nil {
				log.Printf("failed to close response body: %v", err)
			}
		}()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("API returned %s", resp.Status)
		}

		positions, err := unmarshalPositions(body)
		if err != nil {
			return nil, err
		}

		return positions.Rows, nil
	}

	resultCh := msac.workerpool.SubmitOther(job)

	select {
	case res := <-resultCh:
		if res.Err != nil {
			return nil, fmt.Errorf("FetchOrderPositionsByHREF failed: %w", res.Err)
		}

		positions, ok := res.Value.([]MSPosition)
		if !ok {
			return nil, errors.New("FetchOrderPositionsByHREF failed: unexpected value type")
		}

		return positions, nil
	case <-parentCtx.Done():
		log.Printf("FetchOrderPositionsByHREF timed out: %v", parentCtx.Err())

		return nil, nil
	}
}

func (msac *MSAPIClient) FetchPositionSubInfoByHREF(parentctx context.Context, p MSPosition) (code string, weight float64, err error) {
	type positionSubInfo struct {
		Code   string
		Weight float64
	}

	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
		defer cancel()

		body, resp, err := msac.httpRequest(ctx, http.MethodGet, p.Assortment.Meta.HREF, apiKey, http.NoBody)
		if err != nil {
			select {
			case <-ctx.Done():
				log.Printf("FetchPositionSubInfoByHREF timed out: %v", ctx.Err())
			default:
				log.Printf("FetchPositionSubInfoByHREF failed: %v", err)
			}

			return nil, err
		}

		defer func() {
			err = resp.Body.Close()
			if err != nil {
				log.Printf("failed to close response body: %v", err)
			}
		}()

		position, err := unmarshalPositionSubInfo(body)
		if err != nil {
			return nil, err
		}

		return &positionSubInfo{
			Code:   position.Code,
			Weight: position.Weight,
		}, nil
	}

	resultCh := msac.workerpool.SubmitOther(job)
	select {
	case res := <-resultCh:
		if res.Err != nil {
			return "", 0, res.Err
		}

		positionSubInfo, ok := res.Value.(*positionSubInfo)
		if !ok {
			log.Print("FetchPositionSubInfoByHREF failed: unexpected value type")

			return "", 0, res.Err
		}

		return positionSubInfo.Code, positionSubInfo.Weight, nil
	case <-parentctx.Done():
		log.Printf("FetchPositionSubInfoByHREF timed out: %v", parentctx.Err())

		return "", 0, nil
	}
}

func (msac *MSAPIClient) FetchDeliverableOrders(parentctx context.Context) ([]*MSOrder, error) {
	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
		defer cancel()

		now := time.Now()
		tomorrow := now.AddDate(0, 0, 1)
		tomorrowStart := ">=" + tomorrow.Format(time.DateOnly) + " 00:00:00"
		dayAfterTomorrow := tomorrow.AddDate(0, 0, 1)
		dayAfterTomorrowEnd := "<=" + dayAfterTomorrow.Format(time.DateOnly) + " 23:59:59"

		baseURL, err := url.Parse(msac.msConfig.URLstart)
		if err != nil {
			log.Printf("FetchDeliverableOrders failed to parse baseURL: %v", err)

			return nil, err
		}

		baseURL.Path = path.Join(baseURL.Path, "customerorder")

		filterValue := fmt.Sprintf("deliveryPlannedMoment%s;deliveryPlannedMoment%s;state=%s",
			tomorrowStart, dayAfterTomorrowEnd, msac.refHref("customerorder/metadata/states", msac.msConfig.Refs.ReadystateID))

		log.Println(tomorrowStart)
		log.Println(dayAfterTomorrowEnd)

		q := baseURL.Query()
		q.Set("filter", filterValue)
		baseURL.RawQuery = q.Encode()

		body, resp, err := msac.httpRequest(ctx, http.MethodGet, baseURL.String(), apiKey, http.NoBody)
		if err != nil {
			select {
			case <-ctx.Done():
				log.Printf("FetchDeliverableOrders timed out: %v", ctx.Err())
			default:
				log.Printf("FetchDeliverableOrders failed: %v", err)
			}

			return nil, err
		}

		defer func() {
			err = resp.Body.Close()
			if err != nil {
				log.Printf("failed to close response body: %v", err)
			}
		}()

		unmFOR, err := unmarshalMSFetchOrdersResponse(body)
		if err != nil {
			log.Println(err)

			return nil, err
		}

		log.Printf("FetchDeliverableOrders fetched %v orders", len(unmFOR.Rows))

		newOrders := make([]*MSOrder, 0, len(unmFOR.Rows)/2)
		for _, o := range unmFOR.Rows {
			if !msac.Cache.CheckOrderInCache(o.ID) {
				newOrders = append(newOrders, &o)
			}
		}

		for _, o := range newOrders {
			log.Printf("Enriching order with ID: %s", o.Name)
			msac.enrichOrder(ctx, o)
		}

		return newOrders, nil
	}

	resCh := msac.workerpool.SubmitOther(job)
	select {
	case res := <-resCh:
		if res.Err != nil {
			log.Printf("FetchDeliverableOrders failed: %v", res.Err)

			return nil, res.Err
		}

		orders, ok := res.Value.([]*MSOrder)
		if !ok {
			log.Print("FetchDeliverableOrders failed: unexpected value type")

			return nil, res.Err
		}

		return orders, nil
	case <-parentctx.Done():
		log.Printf("FetchDeliverableOrders timed out: %v", parentctx.Err())

		return nil, nil
	}
}

func (msac *MSAPIClient) GetOrderByID(parentctx context.Context, id string) (*MSOrder, error) {
	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
		defer cancel()

		endpoint, err := msac.entityEndpoint("customerorder", id)
		if err != nil {
			return nil, err
		}

		body, resp, err := msac.httpRequest(ctx, http.MethodGet, endpoint, apiKey, http.NoBody)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
				return nil, err
			}
		}

		defer func() {
			err = resp.Body.Close()
			if err != nil {
				log.Printf("failed to close response body: %v", err)
			}
		}()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("API returned %s: %s", resp.Status, string(body))
		}

		var order MSOrder

		err = json.Unmarshal(body, &order)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal order: %w", err)
		}

		err = unmarshalMSOrderAttributes(&order)
		if err != nil {
			return nil, err
		}

		msac.enrichOrder(ctx, &order)

		return &order, nil
	}

	resCh := msac.workerpool.SubmitOther(job)
	select {
	case res := <-resCh:
		if res.Err != nil {
			return nil, res.Err
		}

		order, ok := res.Value.(*MSOrder)
		if !ok {
			return nil, errors.New("unexpected value type")
		}

		return order, nil
	case <-parentctx.Done():
		return nil, parentctx.Err()
	}
}

type FullOrderUpdate struct {
	State      *State `json:"state,omitempty"`
	Attributes []any  `json:"attributes,omitempty"`
}

type Attribute struct {
	Meta  Meta   `json:"meta"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value Value  `json:"value"`
}

type StringedAttribute struct {
	Meta  Meta   `json:"meta"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

type Value struct {
	Meta Meta   `json:"meta"`
	Name string `json:"name"`
}

type State struct {
	Meta Meta `json:"meta"`
}

type Meta struct {
	Href      string `json:"href"`
	Type      string `json:"type"`
	MediaType string `json:"mediaType"`
}

func (msac *MSAPIClient) SetOrderAsShippedToRefGo(parentctx context.Context, id string) error {
	update := FullOrderUpdate{
		// Статус
		State: &State{
			Meta: Meta{
				Href:      msac.refHref("customerorder/metadata/states", msac.msConfig.Refs.ShipedstateID),
				Type:      "state",
				MediaType: MSApplicationJSON,
			},
		},
		// Атрибуты
		Attributes: []any{
			// 1. Вид продажи = "Прочие"
			Attribute{
				Meta: Meta{
					Href:      msac.refHref("customerorder/metadata/attributes", msac.msConfig.SellTypeID),
					Type:      MSAttributeMetaData,
					MediaType: MSApplicationJSON,
				},
				ID:   msac.msConfig.SellTypeID,
				Name: "Вид продажи",
				Type: MSCustomEntityType,
				Value: Value{
					Meta: Meta{
						Href:      msac.refHref("customentity/"+msac.msConfig.Refs.SellTypeOtherType, msac.msConfig.Refs.SellTypeOtherID),
						Type:      MSCustomEntityType,
						MediaType: MSApplicationJSON,
					},
					Name: "Прочие",
				},
			},
			// 3. Курьер = "РефГо"
			Attribute{
				Meta: Meta{
					Href:      msac.refHref("customerorder/metadata/attributes", msac.msConfig.CourierID),
					Type:      MSAttributeMetaData,
					MediaType: MSApplicationJSON,
				},
				ID:   msac.msConfig.CourierID,
				Name: "Курьер",
				Type: MSEmployeeType,
				Value: Value{
					Meta: Meta{
						Href:      msac.refHref("employee", msac.msConfig.Refs.RefGoCourierID),
						Type:      MSEmployeeType,
						MediaType: MSApplicationJSON,
					},
					Name: "РефГо",
				},
			},
		},
	}

	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
		defer cancel()

		jsonBody, err := json.Marshal(update)
		if err != nil {
			return nil, err
		}

		endpoint, err := msac.entityEndpoint("customerorder", id)
		if err != nil {
			return nil, err
		}

		respBody, resp, err := msac.httpRequest(ctx, http.MethodPut, endpoint, apiKey, bytes.NewBuffer(jsonBody))
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}

		defer func() {
			err = resp.Body.Close()
			if err != nil {
				log.Printf("failed to close response body: %v", err)
			}
		}()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(respBody))
		}

		return nil, err
	}

	resultCh := msac.workerpool.SubmitWarehouse(job)

	select {
	case res := <-resultCh:
		if res.Err != nil {
			return res.Err
		}
	case <-parentctx.Done():
		return parentctx.Err()
	}

	return nil
}

func (msac *MSAPIClient) SetRefGoNumberOnly(parentctx context.Context, id, refGoNumber string) error {
	update := struct {
		Attributes []StringedAttribute `json:"attributes"`
	}{
		Attributes: []StringedAttribute{
			{
				Meta: Meta{
					Href:      msac.refHref("customerorder/metadata/attributes", msac.msConfig.RefGoNumberID),
					Type:      MSAttributeMetaData,
					MediaType: MSApplicationJSON,
				},
				ID:    msac.msConfig.RefGoNumberID,
				Name:  "Номер в РЕФ",
				Type:  "string",
				Value: refGoNumber,
			},
		},
	}

	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
		defer cancel()

		jsonBody, err := json.Marshal(update)
		if err != nil {
			return nil, err
		}

		endpoint, err := msac.entityEndpoint("customerorder", id)
		if err != nil {
			return nil, err
		}

		respBody, resp, err := msac.httpRequest(ctx, http.MethodPut, endpoint, apiKey, bytes.NewBuffer(jsonBody))
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}

		defer func() {
			err = resp.Body.Close()
			if err != nil {
				log.Printf("failed to close response body: %v", err)
			}
		}()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(respBody))
		}

		return nil, err
	}

	resultCh := msac.workerpool.SubmitWarehouse(job)

	select {
	case res := <-resultCh:
		if res.Err != nil {
			return res.Err
		}
	case <-parentctx.Done():
		return parentctx.Err()
	}

	return nil
}

type PDFExportRequest struct {
	Template  exportTemplate `json:"template"`
	Extension string         `json:"extension"`
}

type exportTemplate struct {
	Meta Meta `json:"meta"`
}

func (msac *MSAPIClient) FetchOrderPDF(parentctx context.Context, id string) ([]byte, error) {
	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
		defer cancel()

		endpoint, err := msac.entityEndpoint("customerorder", id)
		if err != nil {
			return nil, err
		}

		exportURL := endpoint + "/export/"

		reqBody := PDFExportRequest{
			Template: exportTemplate{
				Meta: Meta{
					Href:      msac.refHref("customtemplate", msac.msConfig.Refs.PrinttemplateID),
					Type:      "customtemplate",
					MediaType: MSApplicationJSON,
				},
			},
			Extension: "pdf",
		}

		jsonBody, err := json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request: %w", err)
		}

		body, resp, err := msac.httpRequest(ctx, http.MethodPost, exportURL, apiKey, bytes.NewReader(jsonBody))
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}

		defer func() {
			err = resp.Body.Close()
			if err != nil {
				log.Printf("failed to close response body: %v", err)
			}
		}()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
		}

		return body, nil
	}

	resCh := msac.workerpool.SubmitOther(job)
	select {
	case res := <-resCh:
		if res.Err != nil {
			return nil, fmt.Errorf("FetchOrderPDF failed: %w", res.Err)
		}

		pdfData, ok := res.Value.([]byte)
		if !ok {
			return nil, errors.New("FetchOrderPDF failed: unexpected value type")
		}

		return pdfData, nil
	case <-parentctx.Done():
		log.Printf("FetchOrderPDF aborted: %v", parentctx.Err())

		return nil, parentctx.Err()
	}
}

// MSAPIError — ошибка API МойСклад: HTTP-статус и тексты из errors[].error.
type MSAPIError struct {
	Status string
	Errors []string
}

func (e *MSAPIError) Error() string {
	if len(e.Errors) == 0 {
		return fmt.Sprintf("API returned %s", e.Status)
	}

	return fmt.Sprintf("API returned %s: %s", e.Status, strings.Join(e.Errors, "; "))
}

// msAPIError формирует MSAPIError из тела ответа МС (errors[].error).
func msAPIError(status string, body []byte) *MSAPIError {
	e := &MSAPIError{Status: status}

	var parsed struct {
		Errors []struct {
			Error string `json:"error"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		for _, item := range parsed.Errors {
			if item.Error != "" {
				e.Errors = append(e.Errors, item.Error)
			}
		}
	}

	return e
}

func (msac *MSAPIClient) httpRequest(ctx context.Context, method, url, apikey string, body io.Reader) ([]byte, *http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", msac.msConfig.AuthHeader+" "+apikey)
	req.Header.Set("Accept-Encoding", msEncoding)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}

	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == msEncoding {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			err = resp.Body.Close()
			if err != nil {
				log.Printf("failed to close response body: %v", err)
			}

			return nil, nil, fmt.Errorf("failed to create gzip reader: %w", err)
		}

		defer func() {
			err = gz.Close()
			if err != nil {
				log.Printf("failed to close gzip reader: %v", err)
			}
		}()

		reader = gz
	}

	bodyBytes, err := io.ReadAll(reader)
	if err != nil {
		err = resp.Body.Close()
		if err != nil {
			log.Printf("failed to close response body: %v", err)
		}

		return nil, nil, err
	}

	return bodyBytes, resp, nil
}

func (msac *MSAPIClient) enrichOrder(ctx context.Context, order *MSOrder) {
	name, phone, err := msac.FetchOrderAgentByHREF(ctx, order)
	if err != nil {
		log.Printf("failed to fetch agent for order %s: %v", order.ID, err)
	} else {
		order.AgentName = name
		order.AgentPhone = phone
	}

	positions, err := msac.FetchOrderPositionsByHREF(ctx, order)
	if err != nil {
		log.Printf("failed to fetch positions for order %s: %v", order.ID, err)

		return
	}

	order.PositionsWInfo = positions

	for i := range order.PositionsWInfo {
		code, weight, err := msac.FetchPositionSubInfoByHREF(ctx, order.PositionsWInfo[i])
		if err != nil {
			log.Printf("failed to fetch subinfo for position %v: %v", order.PositionsWInfo[i].Assortment.Meta.HREF, err)

			continue
		}

		order.PositionsWInfo[i].PositionCode = code
		order.PositionsWInfo[i].PositionWeight = weight
	}
}

// MSMetaRef — ссылка на сущность МойСклад (meta) в теле запроса.
type MSMetaRef struct {
	Meta Meta `json:"meta"`
}

// MSDemandMeta — ссылка на отгрузку (demand) заказа.
type MSDemandMeta struct {
	Meta MSMeta `json:"meta"`
}

// MSOrderShipmentState — «снимок» состояния отгрузки заказа: сумма заказа,
// сумма отгрузок и ссылки на отгрузки. Поле demands отсутствует в JSON,
// если отгрузок нет (nil в Go).
type MSOrderShipmentState struct {
	HREF       string         `json:"href"`
	Name       string         `json:"name"`
	Sum        float64        `json:"sum"`        // копейки
	ShippedSum float64        `json:"shippedSum"` // копейки; 0 = отгрузок нет
	Demands    []MSDemandMeta `json:"demands"`
}

// FetchOrderShipmentState — проверка состояния отгрузки заказа по href.
// Лёгкий запрос: без enrichOrder (агент/позиции/субинфо не тянутся).
func (msac *MSAPIClient) FetchOrderShipmentState(parentctx context.Context, id string) (*MSOrderShipmentState, error) {
	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
		defer cancel()

		endpoint, err := msac.entityEndpoint("customerorder", id)
		if err != nil {
			return nil, err
		}

		body, resp, err := msac.httpRequest(ctx, http.MethodGet, endpoint, apiKey, http.NoBody)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
				return nil, err
			}
		}

		defer func() {
			err = resp.Body.Close()
			if err != nil {
				log.Printf("failed to close response body: %v", err)
			}
		}()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("API returned %s: %s", resp.Status, string(body))
		}

		var state MSOrderShipmentState
		if err := json.Unmarshal(body, &state); err != nil {
			return nil, fmt.Errorf("failed to unmarshal order shipment state: %w", err)
		}

		return &state, nil
	}

	resCh := msac.workerpool.SubmitOther(job)

	select {
	case res := <-resCh:
		if res.Err != nil {
			return nil, fmt.Errorf("FetchOrderShipmentState failed: %w", res.Err)
		}

		state, ok := res.Value.(*MSOrderShipmentState)
		if !ok {
			return nil, errors.New("FetchOrderShipmentState failed: unexpected value type")
		}

		return state, nil
	case <-parentctx.Done():
		return nil, parentctx.Err()
	}
}

// demandNewRequest — тело запроса шаблона отгрузки (PUT /entity/demand/new).
type demandNewRequest struct {
	CustomerOrder MSMetaRef `json:"customerOrder"`
}

// FetchDemandNewTemplate — получение шаблона создания отгрузки для заказа
// (PUT /entity/demand/new с ссылкой на заказ). Возвращает тело шаблона
// как есть — оно отправляется в CreateDemand без изменений.
// ВАЖНО: эндпоинт принимает только PUT (POST → 404 «Неопознанный путь»).
func (msac *MSAPIClient) FetchDemandNewTemplate(parentctx context.Context, id string) (json.RawMessage, error) {
	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
		defer cancel()

		// PUT /entity/demand/new — эндпоинт шаблона отгрузки.
		endpoint, err := msac.entityEndpoint("demand", "new")
		if err != nil {
			return nil, err
		}

		// href заказа, на который создаётся отгрузка (идёт в теле шаблона).
		orderHref, err := msac.entityEndpoint("customerorder", id)
		if err != nil {
			return nil, err
		}

		reqBody, err := json.Marshal(demandNewRequest{
			CustomerOrder: MSMetaRef{
				Meta: Meta{
					Href:      orderHref,
					Type:      "customerorder",
					MediaType: MSApplicationJSON,
				},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to marshal demand template request: %w", err)
		}

		body, resp, err := msac.httpRequest(ctx, http.MethodPut, endpoint, apiKey, bytes.NewReader(reqBody))
		if err != nil {
			return nil, err
		}

		defer func() {
			err = resp.Body.Close()
			if err != nil {
				log.Printf("failed to close response body: %v", err)
			}
		}()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, msAPIError(resp.Status, body)
		}

		return json.RawMessage(body), nil
	}

	resCh := msac.workerpool.SubmitOther(job)

	select {
	case res := <-resCh:
		if res.Err != nil {
			return nil, fmt.Errorf("FetchDemandNewTemplate failed: %w", res.Err)
		}

		template, ok := res.Value.(json.RawMessage)
		if !ok {
			return nil, errors.New("FetchDemandNewTemplate failed: unexpected value type")
		}

		return template, nil
	case <-parentctx.Done():
		return nil, parentctx.Err()
	}
}

// CreateDemand — создание отгрузки (POST /entity/demand) из шаблона,
// полученного через FetchDemandNewTemplate. Шаблон отправляется как есть.
func (msac *MSAPIClient) CreateDemand(parentctx context.Context, template json.RawMessage) error {
	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
		defer cancel()

		endpoint, err := msac.entityEndpoint("demand")
		if err != nil {
			return nil, err
		}

		body, resp, err := msac.httpRequest(ctx, http.MethodPost, endpoint, apiKey, bytes.NewReader(template))
		if err != nil {
			return nil, err
		}

		defer func() {
			err = resp.Body.Close()
			if err != nil {
				log.Printf("failed to close response body: %v", err)
			}
		}()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, msAPIError(resp.Status, body)
		}

		return nil, nil
	}

	resCh := msac.workerpool.SubmitOther(job)

	select {
	case res := <-resCh:
		if res.Err != nil {
			return fmt.Errorf("CreateDemand failed: %w", res.Err)
		}

		return nil
	case <-parentctx.Done():
		return parentctx.Err()
	}
}

// entityEndpoint — URL эндпоинта МойСклад: URLstart (заканчивается на /entity/)
// + <parts...>. Сущность добавлять НЕ нужно: она уже в URLstart (см. .env.example).
// refHref собирает href сущности МС из пути и id: URLstart + path + id.
// Ошибка парсинга URLstart невозможна (конфиг валидируется при старте),
// поэтому при сбое возвращается пустая строка.
func (msac *MSAPIClient) refHref(entityPath, id string) string {
	href, err := msac.entityEndpoint(entityPath, id)
	if err != nil {
		return ""
	}

	return href
}

func (msac *MSAPIClient) entityEndpoint(parts ...string) (string, error) {
	base, err := url.Parse(msac.msConfig.URLstart)
	if err != nil {
		return "", fmt.Errorf("failed to parse MS API base URL: %w", err)
	}

	base.Path = path.Join(append([]string{base.Path}, parts...)...)

	return base.String(), nil
}

// productFolderPageLimit — размер страницы при пагинации папок товаров.
const productFolderPageLimit = 1000

// FetchProductFolders — получение всех папок товаров МойСклад
// (GET /entity/productfolder). Пагинация по offset: запросы повторяются
// с шагом limit=1000, пока не получены все meta.size папок.
func (msac *MSAPIClient) FetchProductFolders(parentctx context.Context) ([]MSProductFolder, error) {
	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
		defer cancel()

		var folders []MSProductFolder

		for offset := 0; ; offset += productFolderPageLimit {
			// Отмена контекста между страницами: не тратим запрос, если родитель уже отменил.
			if err := parentctx.Err(); err != nil {
				return nil, err
			}

			endpoint, err := msac.productFolderListEndpoint(offset, productFolderPageLimit)
			if err != nil {
				return nil, err
			}

			body, resp, err := msac.httpRequest(ctx, http.MethodGet, endpoint, apiKey, http.NoBody)
			if err != nil {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				default:
					return nil, err
				}
			}

			err = resp.Body.Close()
			if err != nil {
				log.Printf("failed to close response body: %v", err)
			}

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return nil, msAPIError(resp.Status, body)
			}

			var list MSProductFolderList
			if err := json.Unmarshal(body, &list); err != nil {
				return nil, fmt.Errorf("failed to unmarshal product folders: %w", err)
			}

			folders = append(folders, list.Rows...)

			if offset+productFolderPageLimit >= list.Meta.Size {
				break
			}
		}

		return folders, nil
	}

	resCh := msac.workerpool.SubmitOther(job)

	select {
	case res := <-resCh:
		if res.Err != nil {
			return nil, fmt.Errorf("FetchProductFolders failed: %w", res.Err)
		}

		folders, ok := res.Value.([]MSProductFolder)
		if !ok {
			return nil, errors.New("FetchProductFolders failed: unexpected value type")
		}

		return folders, nil
	case <-parentctx.Done():
		return nil, parentctx.Err()
	}
}

// productFolderListEndpoint — URL списка папок товаров с пагинацией.
// Query-строка добавляется после entityEndpoint: path.Join не переваривает '?'.
func (msac *MSAPIClient) productFolderListEndpoint(offset, limit int) (string, error) {
	endpoint, err := msac.entityEndpoint("productfolder")
	if err != nil {
		return "", err
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("failed to parse product folder endpoint: %w", err)
	}

	q := u.Query()
	q.Set("limit", strconv.Itoa(limit))
	q.Set("offset", strconv.Itoa(offset))
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// FetchCounterpartyName — получение названия контрагента МойСклад
// (GET /entity/counterparty/{id}). Используется справочником поставщиков
// при сохранении: имя всегда берётся из МС, а не из формы.
func (msac *MSAPIClient) FetchCounterpartyName(parentctx context.Context, id string) (string, error) {
	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentctx, 30*time.Second)
		defer cancel()

		endpoint, err := msac.entityEndpoint("counterparty", id)
		if err != nil {
			return nil, err
		}

		body, resp, err := msac.httpRequest(ctx, http.MethodGet, endpoint, apiKey, http.NoBody)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
				return nil, err
			}
		}

		err = resp.Body.Close()
		if err != nil {
			log.Printf("failed to close response body: %v", err)
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, msAPIError(resp.Status, body)
		}

		var c MSCounterparty
		if err := json.Unmarshal(body, &c); err != nil {
			return nil, fmt.Errorf("failed to unmarshal counterparty: %w", err)
		}

		return c.Name, nil
	}

	resCh := msac.workerpool.SubmitOther(job)

	select {
	case res := <-resCh:
		if res.Err != nil {
			return "", fmt.Errorf("FetchCounterpartyName failed: %w", res.Err)
		}

		name, ok := res.Value.(string)
		if !ok {
			return "", errors.New("FetchCounterpartyName failed: unexpected value type")
		}

		return name, nil
	case <-parentctx.Done():
		return "", parentctx.Err()
	}
}

// productPageLimit — размер страницы при пагинации товаров.
const productPageLimit = 1000

// FetchProductsByPathName — товары папки МойСклад по её полному пути
// (GET /entity/product?filter=pathName=<путь>). Пагинация по offset,
// как в FetchProductFolders.
func (msac *MSAPIClient) FetchProductsByPathName(parentctx context.Context, pathName string) ([]MSProduct, error) {
	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
		defer cancel()

		var products []MSProduct

		for offset := 0; ; offset += productPageLimit {
			if err := parentctx.Err(); err != nil {
				return nil, err
			}

			endpoint, err := msac.productListEndpoint(pathName, offset, productPageLimit)
			if err != nil {
				return nil, err
			}

			body, resp, err := msac.httpRequest(ctx, http.MethodGet, endpoint, apiKey, http.NoBody)
			if err != nil {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				default:
					return nil, err
				}
			}

			err = resp.Body.Close()
			if err != nil {
				log.Printf("failed to close response body: %v", err)
			}

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return nil, msAPIError(resp.Status, body)
			}

			var list MSProductList
			if err := json.Unmarshal(body, &list); err != nil {
				return nil, fmt.Errorf("failed to unmarshal products: %w", err)
			}

			products = append(products, list.Rows...)

			if offset+productPageLimit >= list.Meta.Size {
				break
			}
		}

		return products, nil
	}

	resCh := msac.workerpool.SubmitOther(job)

	select {
	case res := <-resCh:
		if res.Err != nil {
			return nil, fmt.Errorf("FetchProductsByPathName failed: %w", res.Err)
		}

		products, ok := res.Value.([]MSProduct)
		if !ok {
			return nil, errors.New("FetchProductsByPathName failed: unexpected value type")
		}

		return products, nil
	case <-parentctx.Done():
		return nil, parentctx.Err()
	}
}

// productListEndpoint — URL списка товаров папки с фильтром pathName
// и пагинацией.
func (msac *MSAPIClient) productListEndpoint(pathName string, offset, limit int) (string, error) {
	endpoint, err := msac.entityEndpoint("product")
	if err != nil {
		return "", err
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("failed to parse product endpoint: %w", err)
	}

	q := u.Query()
	if pathName != "" {
		q.Set("filter", "pathName="+pathName)
	}
	q.Set("limit", strconv.Itoa(limit))
	q.Set("offset", strconv.Itoa(offset))
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// FetchUOMName — название единицы измерения по href из uom.meta.href товара
// (GET /entity/uom/{id}). id вытаскивается из href.
func (msac *MSAPIClient) FetchUOMName(parentctx context.Context, href string) (string, error) {
	u, err := url.Parse(href)
	if err != nil {
		return "", fmt.Errorf("FetchUOMName: failed to parse uom href: %w", err)
	}
	id := path.Base(u.Path)
	if id == "" || id == "/" {
		return "", errors.New("FetchUOMName: empty uom id in href " + href)
	}

	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentctx, 30*time.Second)
		defer cancel()

		endpoint, err := msac.entityEndpoint("uom", id)
		if err != nil {
			return nil, err
		}

		body, resp, err := msac.httpRequest(ctx, http.MethodGet, endpoint, apiKey, http.NoBody)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
				return nil, err
			}
		}

		err = resp.Body.Close()
		if err != nil {
			log.Printf("failed to close response body: %v", err)
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, msAPIError(resp.Status, body)
		}

		var uom MSUOM
		if err := json.Unmarshal(body, &uom); err != nil {
			return nil, fmt.Errorf("failed to unmarshal uom: %w", err)
		}

		return uom.Name, nil
	}

	resCh := msac.workerpool.SubmitOther(job)

	select {
	case res := <-resCh:
		if res.Err != nil {
			return "", fmt.Errorf("FetchUOMName failed: %w", res.Err)
		}

		name, ok := res.Value.(string)
		if !ok {
			return "", errors.New("FetchUOMName failed: unexpected value type")
		}

		return name, nil
	case <-parentctx.Done():
		return "", parentctx.Err()
	}
}

// FetchProductByID — товар МойСклад по id (GET /entity/product/{id}).
// Используется для ресинка конкретной позиции каталога.
func (msac *MSAPIClient) FetchProductByID(parentctx context.Context, id string) (MSProduct, error) {
	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentctx, 30*time.Second)
		defer cancel()

		endpoint, err := msac.entityEndpoint("product", id)
		if err != nil {
			return nil, err
		}

		body, resp, err := msac.httpRequest(ctx, http.MethodGet, endpoint, apiKey, http.NoBody)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
				return nil, err
			}
		}

		err = resp.Body.Close()
		if err != nil {
			log.Printf("failed to close response body: %v", err)
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, msAPIError(resp.Status, body)
		}

		var product MSProduct
		if err := json.Unmarshal(body, &product); err != nil {
			return nil, fmt.Errorf("failed to unmarshal product: %w", err)
		}

		return product, nil
	}

	resCh := msac.workerpool.SubmitOther(job)

	select {
	case res := <-resCh:
		if res.Err != nil {
			return MSProduct{}, fmt.Errorf("FetchProductByID failed: %w", res.Err)
		}

		product, ok := res.Value.(MSProduct)
		if !ok {
			return MSProduct{}, errors.New("FetchProductByID failed: unexpected value type")
		}

		return product, nil
	case <-parentctx.Done():
		return MSProduct{}, parentctx.Err()
	}
}
