package postgres

import (
	"reflect"
	"testing"
)

func TestDeliveryDateRange(t *testing.T) {
	tests := []struct {
		name    string
		from    string
		to      string
		want    []string
		wantErr bool
	}{
		{
			name: "single day",
			from: "05.08.2026",
			to:   "05.08.2026",
			want: []string{"05.08.2026"},
		},
		{
			name: "multi day",
			from: "03.08.2026",
			to:   "05.08.2026",
			want: []string{"03.08.2026", "04.08.2026", "05.08.2026"},
		},
		{
			name: "cross month",
			from: "30.06.2026",
			to:   "02.07.2026",
			want: []string{"30.06.2026", "01.07.2026", "02.07.2026"},
		},
		{
			name:    "bad from",
			from:    "05.13.2026",
			to:      "05.08.2026",
			wantErr: true,
		},
		{
			name:    "bad to",
			from:    "05.08.2026",
			to:      "abc",
			wantErr: true,
		},
		{
			name:    "inverted",
			from:    "05.08.2026",
			to:      "03.08.2026",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := deliveryDateRange(tt.from, tt.to)
			if (err != nil) != tt.wantErr {
				t.Fatalf("deliveryDateRange(%q, %q) error = %v, wantErr %v", tt.from, tt.to, err, tt.wantErr)
			}

			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("deliveryDateRange(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
			}
		})
	}
}
