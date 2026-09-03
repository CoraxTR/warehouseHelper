package domain

import "testing"

func TestNormalizePhone(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"+79361234567", "79361234567"},     // как в ТЗ: +7936...
		{"+7-936-123-45-67", "79361234567"}, // с дефисами
		{"89361234567", "79361234567"},      // 8 вместо 7
		{"9361234567", "79361234567"},       // без кода страны
		{"79361234567", "79361234567"},      // уже нормализованный
		{"+7 (936) 123-45-67", "79361234567"},
		{"7 936 123 45 67", "79361234567"},
	}
	for _, tt := range tests {
		got, err := NormalizePhone(tt.in)
		if err != nil {
			t.Errorf("NormalizePhone(%q) error: %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("NormalizePhone(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNormalizePhoneErrors(t *testing.T) {
	bad := []string{"", "abc", "+7", "12345", "99361234567", "7936123456", "793612345678"}
	for _, in := range bad {
		if _, err := NormalizePhone(in); err == nil {
			t.Errorf("NormalizePhone(%q): error = nil, want error", in)
		}
	}
}

func TestFormatPhone(t *testing.T) {
	if got := FormatPhone("79361234567"); got != "+7 936 123-45-67" {
		t.Errorf("FormatPhone = %q, want «+7 936 123-45-67»", got)
	}
	// Невалидные значения возвращаются как есть (без паники).
	for _, in := range []string{"", "9361234567", "abc", "7936123456"} {
		if got := FormatPhone(in); got != in {
			t.Errorf("FormatPhone(%q) = %q, want %q", in, got, in)
		}
	}
}

func TestStatusLabel(t *testing.T) {
	labels := map[ComplaintStatus]string{
		ComplaintStatusCreated:   "Создано",
		ComplaintStatusReviewing: "На рассмотрении",
		ComplaintStatusWarehouse: "Склад",
		ComplaintStatusSupplier:  "Поставщик",
		ComplaintStatusCompleted: "Завершено",
		ComplaintStatusClient:    "Клиент",
	}
	for s, want := range labels {
		if got := s.StatusLabel(); got != want {
			t.Errorf("%s label = %q, want %q", s, got, want)
		}
	}
}

func TestTagRoleForStatus(t *testing.T) {
	tests := []struct {
		status   ComplaintStatus
		wantRole ComplaintRole
		wantOK   bool
	}{
		{ComplaintStatusReviewing, ComplaintRoleValidator, true},
		{ComplaintStatusCompleted, ComplaintRoleValidator, true},
		{ComplaintStatusWarehouse, ComplaintRoleWarehouse, true},
		{ComplaintStatusSupplier, ComplaintRoleWarehouse, true}, // по ТЗ — тег склада
		{ComplaintStatusCreated, "", false},
		{ComplaintStatusClient, "", false},
	}
	for _, tt := range tests {
		role, ok := TagRoleForStatus(tt.status)
		if role != tt.wantRole || ok != tt.wantOK {
			t.Errorf("TagRoleForStatus(%s) = (%q, %v), want (%q, %v)",
				tt.status, role, ok, tt.wantRole, tt.wantOK)
		}
	}
}
