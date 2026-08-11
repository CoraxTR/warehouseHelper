package client

import (
	"testing"

	"warehouseHelper/internal/config"
)

func TestEntityEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		urlStart string
		parts    []string
		want     string
	}{
		{
			name:     "создание отгрузки",
			urlStart: "https://api.moysklad.ru/api/remap/1.2/entity/",
			parts:    []string{"demand"},
			want:     "https://api.moysklad.ru/api/remap/1.2/entity/demand",
		},
		{
			name:     "запрос заказа",
			urlStart: "https://api.moysklad.ru/api/remap/1.2/entity/",
			parts:    []string{"customerorder"},
			want:     "https://api.moysklad.ru/api/remap/1.2/entity/customerorder",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msac := &MSAPIClient{msConfig: &config.MSConfig{URLstart: tt.urlStart}}

			got, err := msac.entityEndpoint(tt.parts...)
			if err != nil {
				t.Fatalf("entityEndpoint() error: %v", err)
			}

			if got != tt.want {
				t.Errorf("entityEndpoint() = %q, want %q", got, tt.want)
			}
		})
	}
}
