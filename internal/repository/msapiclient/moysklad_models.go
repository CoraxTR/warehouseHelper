package msapiclient

import (
	"encoding/json"
	"strings"
	"time"
)

type MSFetchOrdersResponse struct {
	Meta struct {
		Size int `json:"size"`
	} `json:"meta"`
	Rows []MSOrder `json:"rows"`
}

type MSMeta struct {
	Size int    `json:"size"`
	HREF string `json:"href"`
	Type string `json:"type"`
}

type MSAgent struct {
	Meta MSMeta `json:"meta"`
}

type MSAgentInfo struct {
	Name  string `json:"name"`
	Phone string `json:"phone"`
}

type MSAttributes struct {
	Name  string          `json:"name"`
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

type MSPositions struct {
	Meta MSMeta       `json:"meta"`
	Rows []MSPosition `json:"rows"`
}

type MSPosition struct {
	Meta struct {
		HREF string `json:"href"`
	} `json:"meta"`
	Quantity float64 `json:"quantity"`
	// Price float64 in copecks
	Price      float64 `json:"price"`
	Assortment struct {
		Meta MSMeta `json:"meta"`
	} `json:"assortment"`
	PositionType string `json:"type"`

	PositionCode   string  `json:"-"`
	PositionWeight float64 `json:"-"`
}

type PositionSubInfo struct {
	Code   string  `json:"code"`
	Weight float64 `json:"weight"`
}

type MSOrder struct {
	HREF                  string         `json:"href"`
	Meta                  MSMeta         `json:"meta"`
	Name                  string         `json:"name"`
	Sum                   float64        `json:"sum"`
	Agent                 MSAgent        `json:"agent"`
	Attributes            []MSAttributes `json:"attributes"`
	Description           string         `json:"description"`
	MSPositions           MSPositions    `json:"positions"`
	DeliveryPlannedMoment string         `json:"deliveryPlannedMoment"`
	ShipmentAddress       string         `json:"shipmentAddress"`

	AttributesMap  map[string]any `json:"-"`
	AgentName      string         `json:"-"`
	AgentPhone     string         `json:"-"`
	PositionsWInfo []MSPosition   `json:"-"`
}

func (o *MSOrder) SetAgentNameAndPhone(s1, s2 string) {
	o.AgentName = s1
	o.AgentPhone = s2
}

func (o *MSOrder) SuitableForDelivery() bool {
	dayAftertomorrow := time.Now().AddDate(0, 0, 2).Format(time.DateOnly)

	deliveryDate := strings.Split(o.DeliveryPlannedMoment, " ")[0]
	if deliveryDate == dayAftertomorrow && o.AttributesMap["Регион доставки"] != "СПБ" {
		return false
	}

	return true
}
