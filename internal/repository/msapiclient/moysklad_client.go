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
)

const msEncoding = "gzip"

type MoySkladAPIClient struct {
	ratelimiter moySkladOutRateLimiter
	msConfig    *config.MoySkladConfig
	rgConfig    *config.RefGoConfig
}

func NewMoySkladAPIClient(c *config.Config) *MoySkladAPIClient {
	return &MoySkladAPIClient{
		*NewMoySkladOutRateLimiter(c.RequestCap, c.TimeSpan),
		c.MoySkladConfig,
		c.RefGoConfig,
	}
}

func (msac *MoySkladAPIClient) FetchOrderAgentByHREF(parentctx context.Context, o *MSOrder) (s1, s2 string, err error) {
	ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
	defer cancel()

	baseURL := o.Agent.Meta.HREF

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, http.NoBody)
	if err != nil {
		log.Printf("Failed to create fetching request: %v", err)

		return "", "", nil
	}

	req.Header.Set("Authorization", msac.msConfig.AuthHeader+" "+msac.msConfig.APIKEY)
	req.Header.Set("Accept-Encoding", msac.msConfig.EncodeHeader)
	msac.ratelimiter.Wait()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		select {
		case <-ctx.Done():
			log.Printf("FetchOrderAgentByHREF orders timed out: %v", ctx.Err())
		default:
			log.Printf("FetchOrderAgentByHREF orders failed %v", err)
		}

		return "", "", nil
	}

	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == msEncoding {
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			log.Printf("Failed to decode: %v", err)
		}

		defer func() {
			err := gzReader.Close()
			if err != nil {
				log.Println(err)
			}
		}()

		reader = gzReader
	}

	defer func() {
		err := resp.Body.Close()
		if err != nil {
			log.Println(err)
		}
	}()

	body, err := io.ReadAll(reader)
	if err != nil {
		log.Println(err)

		return "", "", nil
	}

	log.Printf("FetchOrderAgentByHREF got a response with status %v,", resp.Status)

	agentinfo, err := unmarshalAgentInfo(body)
	if err != nil {
		return "", "", err
	}

	s1 = agentinfo.Name
	s2 = agentinfo.Phone

	return s1, s2, nil
}

func (msac *MoySkladAPIClient) FetchOrderPositionsByHREF(parentctx context.Context, o *MSOrder) ([]MSPosition, error) {
	ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
	defer cancel()

	baseURL := o.MSPositions.Meta.HREF

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, http.NoBody)
	if err != nil {
		log.Printf("Failed to create fetching request: %v", err)

		return nil, err
	}

	req.Header.Set("Authorization", msac.msConfig.AuthHeader+" "+msac.msConfig.APIKEY)
	req.Header.Set("Accept-Encoding", msac.msConfig.EncodeHeader)
	msac.ratelimiter.Wait()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		select {
		case <-ctx.Done():
			log.Printf("FetchOrderPositionsByHREF orders timed out: %v", ctx.Err())
		default:
			log.Printf("FetchOrderPositionsByHREF orders failed %v", err)
		}

		return nil, err
	}

	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == msEncoding {
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			log.Printf("Failed to decode: %v", err)
		}

		defer func() {
			err := gzReader.Close()
			if err != nil {
				log.Println(err)
			}
		}()

		reader = gzReader
	}

	defer func() {
		err := resp.Body.Close()
		if err != nil {
			log.Println(err)
		}
	}()

	body, err := io.ReadAll(reader)
	if err != nil {
		log.Println(err)

		return nil, err
	}

	log.Printf("FetchOrderPositionsByHREF got a response with status %v,", resp.Status)

	positions, err := unmarshalPositions(body)
	if err != nil {
		return nil, err
	}

	return positions.Rows, err
}

func (msac *MoySkladAPIClient) FetchPositionSubInfoByHREF(parentctx context.Context, p MSPosition) (s string, f float64, err error) {
	ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
	defer cancel()

	baseURL := p.Assortment.Meta.HREF

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, http.NoBody)
	if err != nil {
		log.Printf("Failed to create fetching request: %v", err)

		return "", 0, err
	}

	req.Header.Set("Authorization", msac.msConfig.AuthHeader+" "+msac.msConfig.APIKEY)
	req.Header.Set("Accept-Encoding", msac.msConfig.EncodeHeader)
	msac.ratelimiter.Wait()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		select {
		case <-ctx.Done():
			log.Printf("FetchPositionSubInfoByHREF timed out: %v", ctx.Err())
		default:
			log.Printf("FetchPositionSubInfoByHREF failed %v", err)
		}

		return "", 0, err
	}

	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == msEncoding {
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			log.Printf("Failed to decode: %v", err)
		}

		defer func() {
			err := gzReader.Close()
			if err != nil {
				log.Println(err)
			}
		}()

		reader = gzReader
	}

	defer func() {
		err := resp.Body.Close()
		if err != nil {
			log.Println(err)
		}
	}()

	body, err := io.ReadAll(reader)
	if err != nil {
		log.Println(err)

		return "", 0, err
	}

	log.Printf("FetchPositionSubInfoByHREF got a response with status %v,", resp.Status)

	position, err := unmarshalPositionSubInfo(body)
	if err != nil {
		return "", 0, err
	}

	return position.Code, position.Weight, nil
}

func (msac *MoySkladAPIClient) FetchDeliverableOrders(parentctx context.Context) []*MSOrder {
	ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
	defer cancel()

	now := time.Now()
	tomorrow := now.AddDate(0, 0, 1)
	tomorrowStart := ">=" + tomorrow.Format(time.DateOnly) + " 00:00:00"
	dayAfterTomorrow := tomorrow.AddDate(0, 0, 1)
	dayAfterTomorrowEnd := "<=" + dayAfterTomorrow.Format(time.DateOnly) + " 23:59:59"

	baseURL, err := url.Parse(msac.msConfig.URLstart)
	if err != nil {
		log.Printf("GetDeliverableOrders failed to create baseURL: %v", err)

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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL.String(), http.NoBody)
	if err != nil {
		log.Printf("Failed to create fetching request: %v", err)

		return nil
	}

	req.Header.Set("Authorization", msac.msConfig.AuthHeader+" "+msac.msConfig.APIKEY)
	req.Header.Set("Accept-Encoding", msac.msConfig.EncodeHeader)
	msac.ratelimiter.Wait()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		select {
		case <-ctx.Done():
			log.Printf("FetchDeliverableOrders timed out: %v", ctx.Err())
		default:
			log.Printf("FetchDeliverableOrders orders failed %v", err)
		}

		return nil
	}

	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == msEncoding {
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			log.Printf("Failed to decode: %v", err)
		}

		defer func() {
			err := gzReader.Close()
			if err != nil {
				log.Println(err)
			}
		}()

		reader = gzReader
	}

	defer func() {
		err := resp.Body.Close()
		if err != nil {
			log.Println(err)
		}
	}()

	body, err := io.ReadAll(reader)
	if err != nil {
		log.Println(err)

		return nil
	}

	log.Printf("FetchDeliverableOrders got a response with status %v,", resp.Status)

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
		s1, s2, err := msac.FetchOrderAgentByHREF(ctx, o)
		if err != nil {
			log.Println(err)
		}

		o.AgentName = s1
		o.AgentPhone = s2

		o.PositionsWInfo, err = (msac.FetchOrderPositionsByHREF(ctx, o))
		if err != nil {
			log.Println(err)
		}

		for i := range o.PositionsWInfo {
			code, weight, err := msac.FetchPositionSubInfoByHREF(ctx, o.PositionsWInfo[i])
			if err != nil {

				log.Printf("error fetching subinfo: %v", err)
			}

			o.PositionsWInfo[i].PositionCode = code
			o.PositionsWInfo[i].PositionWeight = weight

			log.Println(code, weight, o.PositionsWInfo[i].PositionCode, o.PositionsWInfo[i].PositionWeight)
		}
	}

	return msOrders
}

func (msac *MoySkladAPIClient) GetOrderByHREF(parentctx context.Context, href string) (*MSOrder, error) {
	ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, href, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", msac.msConfig.AuthHeader+" "+msac.msConfig.APIKEY)
	req.Header.Set("Accept-Encoding", msac.msConfig.EncodeHeader)
	msac.ratelimiter.Wait()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			return nil, err
		}
	}
	defer resp.Body.Close()

	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == msEncoding {
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer gzReader.Close()
		reader = gzReader
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned %s: %s", resp.Status, string(body))
	}

	var order MSOrder
	if err := json.Unmarshal(body, &order); err != nil {
		return nil, fmt.Errorf("failed to unmarshal order: %w", err)
	}

	if err := unmarshalMSOrderAttributes(&order); err != nil {
		return nil, err
	}

	agentName, agentPhone, err := msac.FetchOrderAgentByHREF(ctx, &order)
	if err != nil {
		log.Printf("failed to fetch agent for order %s: %v", order.HREF, err)
	} else {
		order.AgentName = agentName
		order.AgentPhone = agentPhone
	}

	positions, err := msac.FetchOrderPositionsByHREF(ctx, &order)
	if err != nil {
		log.Printf("failed to fetch positions for order %s: %v", order.HREF, err)
	} else {
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

	return &order, nil
}

type FullOrderUpdate struct {
	State      *State        `json:"state,omitempty"`
	Attributes []interface{} `json:"attributes,omitempty"`
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
	Href string `json:"href"`

	Type      string `json:"type"`
	MediaType string `json:"mediaType"`
}

func (msac *MoySkladAPIClient) SetOrderAsShippedToRefGo(ctx context.Context, href, refGoNumber string) error {
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

func (msac *MoySkladAPIClient) SetRefGoNumberOnly(ctx context.Context, href, refGoNumber string) error {
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

func (msac *MoySkladAPIClient) sendPutRequest(ctx context.Context, url string, body interface{}) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+msac.msConfig.APIKEY)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Content-Type", "application/json")

	msac.ratelimiter.Wait()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}

	defer func() {
		err = resp.Body.Close()
		if err != nil {
			log.Printf("Failed to close response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	return nil
}

func (msac *MoySkladAPIClient) sendPatchRequest(ctx context.Context, url string, body interface{}) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+msac.msConfig.APIKEY)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Content-Type", "application/json")

	msac.ratelimiter.Wait()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(bodyBytes))
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

func (msac *MoySkladAPIClient) FetchOrderPDF(parentctx context.Context, href string) (pdf []byte, err error) {
	ctx, cancel := context.WithTimeout(parentctx, 300*time.Second)
	defer cancel()

	url := href + "/export/"

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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+msac.msConfig.APIKEY)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Content-Type", "application/json")
	msac.ratelimiter.Wait()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to create gzip reader: %w", err)
		}
		defer gzReader.Close()
		reader = gzReader
	}

	pdf, err = io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	return pdf, nil
}
