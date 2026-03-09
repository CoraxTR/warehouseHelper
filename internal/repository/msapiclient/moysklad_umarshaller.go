package msapiclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
)

func unmarshalMSFetchOrdersResponse(body []byte) (*MSFetchOrdersResponse, error) {
	response := MSFetchOrdersResponse{}

	err := json.Unmarshal(body, &response)
	if err != nil {
		log.Println(err)

		return nil, err
	}

	for i := range response.Rows {
		err = unmarshalMSOrderAttributes(&response.Rows[i])
		if err != nil {
			return nil, err
		}
	}

	return &response, nil
}

func unmarshalMSOrderAttributes(o *MSOrder) error {
	o.AttributesMap = make(map[string]any)
	for _, attribute := range o.Attributes {
		var value any

		var err error

		switch attribute.Type {
		case "string":
			var s string

			err = json.Unmarshal(attribute.Value, &s)
			if err != nil {
				return fmt.Errorf("failed to parse attribute %s: %w", attribute.Name, err)
			}

			value = s
		case "customentity":
			var ce struct {
				Name string `json:"name"`
			}

			err = json.Unmarshal(attribute.Value, &ce)
			if err != nil {
				return fmt.Errorf("failed to parse attribute %s: %w", attribute.Name, err)
			}

			value = ce.Name

		case "employee":
			var emp struct {
				Name string `json:"name"`
			}

			err = json.Unmarshal(attribute.Value, &emp)
			if err != nil {
				return fmt.Errorf("failed to parse attribute %s: %w", attribute.Name, err)
			}

			value = emp.Name
		default:
			return errors.New("error unmarshalling attribute")
		}

		o.AttributesMap[attribute.Name] = value
	}

	return nil
}

func unmarshalAgentInfo(body []byte) (*MSAgentInfo, error) {
	response := &MSAgentInfo{}

	err := json.Unmarshal(body, &response)
	if err != nil {
		return nil, err
	}

	return response, nil
}

func unmarshalPositions(body []byte) (*MSPositions, error) {
	var response *MSPositions

	err := json.Unmarshal(body, &response)
	if err != nil {
		return nil, err
	}

	return response, nil
}

func unmarshalPositionSubInfo(body []byte) (PositionSubInfo, error) {
	var response PositionSubInfo

	err := json.Unmarshal(body, &response)
	if err != nil {
		return PositionSubInfo{}, err
	}

	return response, nil
}
