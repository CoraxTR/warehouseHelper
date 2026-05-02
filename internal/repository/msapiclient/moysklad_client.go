package msapiclient

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"time"
	"warehouseHelper/internal/config"
	"warehouseHelper/internal/msratelimiter"
)

const msEncoding = "gzip"

type MSAPIClient struct {
	ratelimiter *msratelimiter.MoySkladOutRateLimiter
	msConfig    *config.MoySkladConfig
	rgConfig    *config.RefGoConfig
}

func NewMSAPIClient(c *config.Config, rl *msratelimiter.MoySkladOutRateLimiter) *MSAPIClient {
	return &MSAPIClient{
		ratelimiter: rl,
		msConfig:    c.MoySkladConfig,
		rgConfig:    c.RefGoConfig,
	}
}

func (msac *MSAPIClient) FetchOrderAgentByHREF(parentctx context.Context, o *MSOrder) (name, phone string, err error) {
	ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
	defer cancel()

	body, resp, err := msac.doRequest(ctx, http.MethodGet, o.Agent.Meta.HREF, http.NoBody)
	if err != nil {
		select {
		case <-ctx.Done():
			log.Printf("FetchOrderAgentByHREF timed out: %v", ctx.Err())
		default:
			log.Printf("FetchOrderAgentByHREF failed: %v", err)
		}

		return "", "", nil
	}

	defer func() {
		err = resp.Body.Close()
		if err != nil {
			log.Printf("failed to close response body: %v", err)
		}
	}()

	log.Printf("FetchOrderAgentByHREF got a response with status %v", resp.Status)

	agentinfo, err := unmarshalAgentInfo(body)
	if err != nil {
		return "", "", err
	}

	return agentinfo.Name, agentinfo.Phone, nil
}

func (msac *MSAPIClient) FetchOrderPositionsByHREF(parentctx context.Context, o *MSOrder) ([]MSPosition, error) {
	ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
	defer cancel()

	body, resp, err := msac.doRequest(ctx, http.MethodGet, o.MSPositions.Meta.HREF, http.NoBody)
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

func (msac *MSAPIClient) FetchPositionSubInfoByHREF(parentctx context.Context, p MSPosition) (code string, weight float64, err error) {
	ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
	defer cancel()

	body, resp, err := msac.doRequest(ctx, http.MethodGet, p.Assortment.Meta.HREF, http.NoBody)
	if err != nil {
		select {
		case <-ctx.Done():
			log.Printf("FetchPositionSubInfoByHREF timed out: %v", ctx.Err())
		default:
			log.Printf("FetchPositionSubInfoByHREF failed: %v", err)
		}

		return "", 0, err
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
		return "", 0, err
	}

	return position.Code, position.Weight, nil
}

func (msac *MSAPIClient) FetchDeliverableOrders(parentctx context.Context) []*MSOrder {
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

		return nil
	}

	baseURL.Path = path.Join(baseURL.Path, "customerorder")

	filterValue := fmt.Sprintf("deliveryPlannedMoment%s;deliveryPlannedMoment%s;state=%s",
		tomorrowStart, dayAfterTomorrowEnd, msac.msConfig.Hrefs.Readystatehref)

	log.Println(tomorrowStart)
	log.Println(dayAfterTomorrowEnd)

	q := baseURL.Query()
	q.Set("filter", filterValue)
	baseURL.RawQuery = q.Encode()

	body, resp, err := msac.doRequest(ctx, http.MethodGet, baseURL.String(), http.NoBody)
	if err != nil {
		select {
		case <-ctx.Done():
			log.Printf("FetchDeliverableOrders timed out: %v", ctx.Err())
		default:
			log.Printf("FetchDeliverableOrders failed: %v", err)
		}

		return nil
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
	}

	log.Printf("FetchDeliverableOrders fetched %v orders", len(unmFOR.Rows))

	msOrders := make([]*MSOrder, len(unmFOR.Rows))
	for k := range unmFOR.Rows {
		msOrders[k] = &unmFOR.Rows[k]
	}

	for _, o := range msOrders {
		msac.enrichOrder(ctx, o)
	}

	return msOrders
}

func (msac *MSAPIClient) GetOrderByHREF(parentctx context.Context, href string) (*MSOrder, error) {
	ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
	defer cancel()

	body, resp, err := msac.doRequest(ctx, http.MethodGet, href, http.NoBody)
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

func (msac *MSAPIClient) SetOrderAsShippedToRefGo(ctx context.Context, href string) error {
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

	return msac.sendPutRequest(ctx, href, update)
}

func (msac *MSAPIClient) SetRefGoNumberOnly(ctx context.Context, href, refGoNumber string) error {
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

	return msac.sendPutRequest(ctx, href, update)
}

func (msac *MSAPIClient) sendPutRequest(ctx context.Context, url string, body interface{}) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal request body: %w", err)
	}

	respBody, resp, err := msac.doRequest(ctx, http.MethodPut, url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}

	defer func() {
		err = resp.Body.Close()
		if err != nil {
			log.Printf("failed to close response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

func (msac *MSAPIClient) sendPatchRequest(ctx context.Context, url string, body interface{}) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal request body: %w", err)
	}

	respBody, resp, err := msac.doRequest(ctx, http.MethodPatch, url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}

	defer func() {
		err = resp.Body.Close()
		if err != nil {
			log.Printf("failed to close response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(respBody))
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

	body, resp, err := msac.doRequest(ctx, http.MethodPost, exportURL, bytes.NewReader(jsonBody))
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

func (msac *MSAPIClient) doRequest(ctx context.Context, method, url string, body io.Reader) ([]byte, *http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", msac.msConfig.AuthHeader+" "+msac.msConfig.APIKEY)
	req.Header.Set("Accept-Encoding", msEncoding)
	req.Header.Set("Content-Type", "application/json")

	msac.ratelimiter.Wait()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}

	defer func() {
		err := resp.Body.Close()
		if err != nil {
			log.Println(err)
		}
	}()

	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == msEncoding {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, resp, fmt.Errorf("failed to create gzip reader: %w", err)
		}

		defer func() {
			err = gz.Close()
			if err != nil {
				log.Println(err)
			}
		}()

		reader = gz
	}

	respBody, err := io.ReadAll(reader)

	return respBody, resp, err
}
