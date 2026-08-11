package usecase

import (
	"context"
	"errors"
	"testing"

	"warehouseHelper/internal/msclient/client"
)

// stubShipmentClient — заглушка OrderShipmentClient для тестов.
type stubShipmentClient struct {
	state        *client.MSOrderShipmentState
	stateErr     error
	positions    []client.MSPosition
	positionsErr error
	createErr    error

	positionsCalls    int
	createCalls       int
	lastPositionsHref string
	lastOrderHref     string
	lastAgentHref     string
	lastPositions     []client.MSPosition
}

// Компиляционная проверка: стаб реализует OrderShipmentClient.
var _ OrderShipmentClient = (*stubShipmentClient)(nil)

func (s *stubShipmentClient) FetchOrderShipmentState(_ context.Context, _ string) (*client.MSOrderShipmentState, error) {
	return s.state, s.stateErr
}

func (s *stubShipmentClient) FetchOrderPositions(_ context.Context, positionsHref string) ([]client.MSPosition, error) {
	s.positionsCalls++
	s.lastPositionsHref = positionsHref

	return s.positions, s.positionsErr
}

func (s *stubShipmentClient) CreateDemand(_ context.Context, orderHref, agentHref string, positions []client.MSPosition) error {
	s.createCalls++
	s.lastOrderHref = orderHref
	s.lastAgentHref = agentHref
	s.lastPositions = positions

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

const (
	testOrderHref   = "https://api.moysklad.ru/api/remap/1.2/entity/customerorder/1"
	testAgentHref   = "https://api.moysklad.ru/api/remap/1.2/entity/counterparty/6c08d8fa"
	testOrderName   = "05754"
	testPositionsHr = testOrderHref + "/positions"
)

func TestOrderShipmentEnsurer(t *testing.T) {
	positions := []client.MSPosition{
		{Quantity: 2.005, Price: 859000.0},
	}

	tests := []struct {
		name           string
		state          *client.MSOrderShipmentState
		stateErr       error
		positionsErr   error
		createErr      error
		wantErr        bool
		wantPosCalls   int
		wantCreateCall int
		wantMessages   []string
	}{
		{
			name: "отгрузок нет — тянем позиции и создаём отгрузку",
			state: &client.MSOrderShipmentState{
				HREF:  testOrderHref,
				Name:  testOrderName,
				Sum:   1772195.0,
				Agent: client.MSAgent{Meta: client.MSMeta{HREF: testAgentHref}},
			},
			wantPosCalls:   1,
			wantCreateCall: 1,
		},
		{
			name: "отгрузок нет и позиции не пришли — нотификация",
			state: &client.MSOrderShipmentState{
				Name:  testOrderName,
				Sum:   1772195.0,
				Agent: client.MSAgent{Meta: client.MSMeta{HREF: testAgentHref}},
			},
			positionsErr: errors.New("timeout"),
			wantErr:      true,
			wantPosCalls: 1,
			wantMessages: []string{"Не удалось создать отгрузку в заказ: " + testOrderName},
		},
		{
			name: "отгрузок нет и создание упало — нотификация",
			state: &client.MSOrderShipmentState{
				Name:  testOrderName,
				Sum:   1772195.0,
				Agent: client.MSAgent{Meta: client.MSMeta{HREF: testAgentHref}},
			},
			createErr:      errors.New("API returned 400"),
			wantErr:        true,
			wantPosCalls:   1,
			wantCreateCall: 1,
			wantMessages:   []string{"Не удалось создать отгрузку в заказ: " + testOrderName},
		},
		{
			name: "отгрузка есть и сумма совпадает — без действий",
			state: &client.MSOrderShipmentState{
				Name:       testOrderName,
				Sum:        1772195.0,
				ShippedSum: 1772195.0,
				Demands:    []client.MSDemandMeta{{Meta: client.MSMeta{HREF: "https://api.moysklad.ru/api/remap/1.2/entity/demand/1"}}},
			},
		},
		{
			name: "отгрузка есть и сумма не совпадает — нотификация на правку",
			state: &client.MSOrderShipmentState{
				Name:       testOrderName,
				Sum:        1772195.0,
				ShippedSum: 1700000.0,
				Demands:    []client.MSDemandMeta{{Meta: client.MSMeta{HREF: "https://api.moysklad.ru/api/remap/1.2/entity/demand/1"}}},
			},
			wantMessages: []string{"В заказе " + testOrderName + " нужно поправить отгрузку"},
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
				state:        tt.state,
				stateErr:     tt.stateErr,
				positions:    positions,
				positionsErr: tt.positionsErr,
				createErr:    tt.createErr,
			}
			notifier := &stubWarehouseNotifier{}

			uc := NewOrderShipmentEnsurer(msc, notifier)

			err := uc.EnsureOrderShipment(context.Background(), testOrderHref)
			if (err != nil) != tt.wantErr {
				t.Fatalf("EnsureOrderShipment error = %v, wantErr %v", err, tt.wantErr)
			}

			if msc.positionsCalls != tt.wantPosCalls {
				t.Errorf("FetchOrderPositions calls = %d, want %d", msc.positionsCalls, tt.wantPosCalls)
			}

			if msc.createCalls != tt.wantCreateCall {
				t.Errorf("CreateDemand calls = %d, want %d", msc.createCalls, tt.wantCreateCall)
			}

			if len(notifier.messages) != len(tt.wantMessages) {
				t.Fatalf("notifications = %v, want %v", notifier.messages, tt.wantMessages)
			}

			for i, want := range tt.wantMessages {
				if notifier.messages[i] != want {
					t.Errorf("notification[%d] = %q, want %q", i, notifier.messages[i], want)
				}
			}

			if tt.wantCreateCall > 0 {
				if msc.lastPositionsHref != testPositionsHr {
					t.Errorf("positions href = %q, want %q", msc.lastPositionsHref, testPositionsHr)
				}

				if msc.lastOrderHref != testOrderHref {
					t.Errorf("CreateDemand order href = %q, want %q", msc.lastOrderHref, testOrderHref)
				}

				if msc.lastAgentHref != testAgentHref {
					t.Errorf("CreateDemand agent href = %q, want %q", msc.lastAgentHref, testAgentHref)
				}

				if len(msc.lastPositions) != len(positions) {
					t.Errorf("CreateDemand positions count = %d, want %d", len(msc.lastPositions), len(positions))
				}
			}
		})
	}
}
