package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"warehouseHelper/internal/msclient/client"
)

// stubShipmentClient — заглушка OrderShipmentClient для тестов.
type stubShipmentClient struct {
	state       *client.MSOrderShipmentState
	stateErr    error
	template    json.RawMessage
	templateErr error
	createErr   error

	templateCalls     int
	createCalls       int
	lastTemplateHref  string
	lastCreatePayload json.RawMessage
}

// Компиляционная проверка: стаб реализует OrderShipmentClient.
var _ OrderShipmentClient = (*stubShipmentClient)(nil)

func (s *stubShipmentClient) FetchOrderShipmentState(_ context.Context, _ string) (*client.MSOrderShipmentState, error) {
	return s.state, s.stateErr
}

func (s *stubShipmentClient) FetchDemandNewTemplate(_ context.Context, href string) (json.RawMessage, error) {
	s.templateCalls++
	s.lastTemplateHref = href

	return s.template, s.templateErr
}

func (s *stubShipmentClient) CreateDemand(_ context.Context, template json.RawMessage) error {
	s.createCalls++
	s.lastCreatePayload = template

	return s.createErr
}

// stubWarehouseNotifier — заглушка WarehouseNotifier, копит сообщения.
type stubWarehouseNotifier struct {
	messages []string
}

func (s *stubWarehouseNotifier) NotifyWarehouse(text string) error {
	s.messages = append(s.messages, text)

	return nil
}

func TestOrderShipmentEnsurer(t *testing.T) {
	template := json.RawMessage(`{"organization":{"meta":{}}}`)

	tests := []struct {
		name        string
		state       *client.MSOrderShipmentState
		stateErr    error
		templateErr error
		createErr   error

		wantErr           bool
		wantTemplateCalls int
		wantCreateCalls   int
		wantMessages      []string
	}{
		{
			name: "отгрузок нет — создаём отгрузку",
			state: &client.MSOrderShipmentState{
				HREF: "https://api.moysklad.ru/entity/customerorder/1",
				Name: "05754",
				Sum:  1772195.0,
			},
			wantTemplateCalls: 1,
			wantCreateCalls:   1,
		},
		{
			name: "отгрузок нет и шаблон упал — нотификация",
			state: &client.MSOrderShipmentState{
				Name: "05754",
				Sum:  1772195.0,
			},
			templateErr:       errors.New("timeout"),
			wantErr:           true,
			wantTemplateCalls: 1,
			wantMessages:      []string{"Не удалось создать отгрузку в заказ: 05754"},
		},
		{
			name: "отгрузок нет и создание упало — нотификация",
			state: &client.MSOrderShipmentState{
				Name: "05754",
				Sum:  1772195.0,
			},
			createErr:         errors.New("API returned 400"),
			wantErr:           true,
			wantTemplateCalls: 1,
			wantCreateCalls:   1,
			wantMessages:      []string{"Не удалось создать отгрузку в заказ: 05754"},
		},
		{
			name: "отгрузка есть и сумма совпадает — без действий",
			state: &client.MSOrderShipmentState{
				Name:       "05754",
				Sum:        1772195.0,
				ShippedSum: 1772195.0,
				Demands:    []client.MSDemandMeta{{Meta: client.MSMeta{HREF: "https://api.moysklad.ru/entity/demand/1"}}},
			},
		},
		{
			name: "отгрузка есть и сумма не совпадает — нотификация на правку",
			state: &client.MSOrderShipmentState{
				Name:       "05754",
				Sum:        1772195.0,
				ShippedSum: 1700000.0,
				Demands:    []client.MSDemandMeta{{Meta: client.MSMeta{HREF: "https://api.moysklad.ru/entity/demand/1"}}},
			},
			wantMessages: []string{"В заказе 05754 нужно поправить отгрузку"},
		},
		{
			name:     "получение состояния упало — ошибка без нотификации",
			stateErr: errors.New("network"),
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msc := &stubShipmentClient{
				state:       tt.state,
				stateErr:    tt.stateErr,
				template:    template,
				templateErr: tt.templateErr,
				createErr:   tt.createErr,
			}
			notifier := &stubWarehouseNotifier{}

			uc := NewOrderShipmentEnsurer(msc, notifier)

			err := uc.EnsureOrderShipment(context.Background(), "https://api.moysklad.ru/entity/customerorder/1")
			if (err != nil) != tt.wantErr {
				t.Fatalf("EnsureOrderShipment error = %v, wantErr %v", err, tt.wantErr)
			}

			if msc.templateCalls != tt.wantTemplateCalls {
				t.Errorf("FetchDemandNewTemplate calls = %d, want %d", msc.templateCalls, tt.wantTemplateCalls)
			}

			if msc.createCalls != tt.wantCreateCalls {
				t.Errorf("CreateDemand calls = %d, want %d", msc.createCalls, tt.wantCreateCalls)
			}

			if len(notifier.messages) != len(tt.wantMessages) {
				t.Fatalf("notifications = %v, want %v", notifier.messages, tt.wantMessages)
			}

			for i, want := range tt.wantMessages {
				if notifier.messages[i] != want {
					t.Errorf("notification[%d] = %q, want %q", i, notifier.messages[i], want)
				}
			}

			if tt.wantCreateCalls > 0 && string(msc.lastCreatePayload) != string(template) {
				t.Errorf("CreateDemand payload = %s, want template as-is", msc.lastCreatePayload)
			}
		})
	}
}
