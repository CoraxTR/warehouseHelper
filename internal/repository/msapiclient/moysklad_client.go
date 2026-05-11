package msapiclient

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
	"time"
	"warehouseHelper/internal/config"
	"warehouseHelper/internal/ms_workerpool"
)

const msEncoding = "gzip"

type MSAPIClient struct {
	workerpool *ms_workerpool.MSWorkerPool
	msConfig   *config.MSConfig
	rgConfig   *config.RefGoConfig
}

func NewMSAPIClient(c *config.Config, wp *ms_workerpool.MSWorkerPool) *MSAPIClient {
	return &MSAPIClient{
		workerpool: wp,
		msConfig:   c.MSConfig,
		rgConfig:   c.RefGoConfig,
	}
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

		log.Printf("FetchOrderPositionsByHREF got a response with status %v", resp.Status)

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

		log.Printf("FetchPositionSubInfoByHREF got a response with status %v", resp.Status)

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
			tomorrowStart, dayAfterTomorrowEnd, msac.msConfig.Hrefs.Readystatehref)

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

		log.Printf("FetchDeliverableOrders got a response with status %v", resp.Status)

		unmFOR, err := unmarshalMSFetchOrdersResponse(body)
		if err != nil {
			log.Println(err)

			return nil, err
		}

		log.Printf("FetchDeliverableOrders fetched %v orders", len(unmFOR.Rows))

		msOrders := make([]*MSOrder, len(unmFOR.Rows))
		for k := range unmFOR.Rows {
			msOrders[k] = &unmFOR.Rows[k]
		}

		for _, o := range msOrders {
			msac.enrichOrder(ctx, o)
		}

		return msOrders, nil
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

func (msac *MSAPIClient) GetOrderByHREF(parentctx context.Context, href string) (*MSOrder, error) {
	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
		defer cancel()

		body, resp, err := msac.httpRequest(ctx, http.MethodGet, href, apiKey, http.NoBody)
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

func (msac *MSAPIClient) SetOrderAsShippedToRefGo(parentctx context.Context, href string) error {
	update := FullOrderUpdate{
		// Статус
		State: &State{
			Meta: Meta{
				Href:      msac.msConfig.Hrefs.Shipedstatehref,
				Type:      "state",
				MediaType: "application/json",
			},
		},
		// Атрибуты
		Attributes: []interface{}{
			// 1. Вид продажи = "Прочие"
			Attribute{
				Meta: Meta{
					Href:      msac.msConfig.Hrefs.SellTypehref,
					Type:      "attributemetadata",
					MediaType: "application/json",
				},
				ID:   msac.msConfig.SellTypeID,
				Name: "Вид продажи",
				Type: "customentity",
				Value: Value{
					Meta: Meta{
						Href:      msac.msConfig.Hrefs.SellTypeOtherhref,
						Type:      "customentity",
						MediaType: "application/json",
					},
					Name: "Прочие",
				},
			},
			// 3. Курьер = "РефГо"
			Attribute{
				Meta: Meta{
					Href:      msac.msConfig.Hrefs.Courierhref,
					Type:      "attributemetadata",
					MediaType: "application/json",
				},
				ID:   msac.msConfig.CourierID,
				Name: "Курьер",
				Type: "employee",
				Value: Value{
					Meta: Meta{
						Href:      msac.msConfig.Hrefs.RefGoCourierhref,
						Type:      "employee",
						MediaType: "application/json",
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

		respBody, resp, err := msac.httpRequest(ctx, http.MethodPut, href, apiKey, bytes.NewBuffer(jsonBody))
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

func (msac *MSAPIClient) SetRefGoNumberOnly(parentctx context.Context, href, refGoNumber string) error {
	update := struct {
		Attributes []StringedAttribute `json:"attributes"`
	}{
		Attributes: []StringedAttribute{
			{
				Meta: Meta{
					Href:      msac.msConfig.Hrefs.RefGoNumberhref,
					Type:      "attributemetadata",
					MediaType: "application/json",
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

		respBody, resp, err := msac.httpRequest(ctx, http.MethodPut, href, apiKey, bytes.NewBuffer(jsonBody))
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

func (msac *MSAPIClient) FetchOrderPDF(parentctx context.Context, href string) ([]byte, error) {
	job := func(apiKey string) (any, error) {
		ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
		defer cancel()

		exportURL := href + "/export/"

		reqBody := PDFExportRequest{
			Template: exportTemplate{
				Meta: Meta{
					Href:      msac.msConfig.Hrefs.Printtemplatehref,
					Type:      "customtemplate",
					MediaType: "application/json",
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
		log.Printf("FetchOrderPDF timed out: %v", parentctx.Err())

		return nil, nil
	}
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
		log.Printf("failed to fetch agent for order %s: %v", order.HREF, err)
	} else {
		order.AgentName = name
		order.AgentPhone = phone
	}

	positions, err := msac.FetchOrderPositionsByHREF(ctx, order)
	if err != nil {
		log.Printf("failed to fetch positions for order %s: %v", order.HREF, err)

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
