package client

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

func unmarshalMSFetchOrdersResponse(body []byte) (*MSFetchOrdersResponse, error) {
	response := MSFetchOrdersResponse{}

	err := json.Unmarshal(body, &response)
	if err != nil {
		slog.Info(fmt.Sprintln(err))

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

// unmarshalMSOrderAttributes раскладывает атрибуты строки заказа в
// AttributesMap: string — плоская строка, customentity/employee — name объекта
// справочника.
//
// Атрибут НЕИЗВЕСТНОГО типа пропускается (WARN: заказ, имя, тип), а не роняет
// разбор. В базе МС у заказа живут типы long/text/time («Бонусы», «Кол-во
// гостей», «Дата МК», «Скидки по бонусам», «Стоимость яндекс доставки» —
// проверено по /entity/customerorder/metadata/attributes 21.09.2026), и раньше
// один заполненный такой атрибут убивал разбор ВСЕЙ страницы: слепли
// reservewatch и импорт заказов (FetchDeliverableOrders), хотя ни одному
// потребителю эти атрибуты не нужны.
//
// Битое значение у ЗНАКОМОГО типа — по-прежнему ошибка: ломается формат того,
// что модули читают (регион, интервал, коробки), и терять это поле молча нельзя.
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
		case MSCustomEntityType:
			var ce struct {
				Name string `json:"name"`
			}

			err = json.Unmarshal(attribute.Value, &ce)
			if err != nil {
				return fmt.Errorf("failed to parse attribute %s: %w", attribute.Name, err)
			}

			value = ce.Name

		case MSEmployeeType:
			var emp struct {
				Name string `json:"name"`
			}

			err = json.Unmarshal(attribute.Value, &emp)
			if err != nil {
				return fmt.Errorf("failed to parse attribute %s: %w", attribute.Name, err)
			}

			value = emp.Name
		default:
			// Тип, которого нет в разборе: атрибут не наш, отдаём его в лог и
			// идём дальше — из-за него страница заказов падать не должна.
			slog.Warn("msclient: атрибут заказа с неподдерживаемым типом пропущен",
				"заказ", o.Name, "атрибут", attribute.Name, "тип", attribute.Type)

			continue
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

// msPositionsFetch — позиции заказа и их сырые JSON-строки из одного тела
// ответа (сырьё уходит эхом в PUT при отправке подбора, см. msorders Submit).
type msPositionsFetch struct {
	positions []MSPosition
	rawRows   []json.RawMessage
}

// unmarshalPositionRows разбирает тело ответа {rows:[...]} в модели + сырьё.
func unmarshalPositionRows(body []byte) (*msPositionsFetch, error) {
	var env struct {
		Rows []json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("failed to unmarshal positions: %w", err)
	}

	fetch := &msPositionsFetch{
		positions: make([]MSPosition, 0, len(env.Rows)),
		rawRows:   env.Rows,
	}
	for _, raw := range env.Rows {
		var p MSPosition
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("failed to unmarshal position row: %w", err)
		}
		fetch.positions = append(fetch.positions, p)
	}

	return fetch, nil
}

func unmarshalPositionSubInfo(body []byte) (PositionSubInfo, error) {
	var response PositionSubInfo

	err := json.Unmarshal(body, &response)
	if err != nil {
		return PositionSubInfo{}, err
	}

	return response, nil
}
