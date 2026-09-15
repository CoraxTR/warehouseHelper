package discounts

import "testing"

//go:fix inline
func i16(v int16) *int16 { return new(v) }

func TestSourceString(t *testing.T) {
	tests := []struct {
		name string
		src  Source
		want string
	}{
		{"нет скидки", SourceNone, "none"},
		{"ручная", SourceManual, "manual"},
		{"срок", SourceExpiry, "expiry"},
		{"избыток", SourceSurplus, "surplus"},
		{"неизвестное значение", Source(99), "none"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.src.String(); got != tc.want {
				t.Errorf("Source(%d).String() = %q, want %q", tc.src, got, tc.want)
			}
		})
	}
}

// Приоритет: ручная → срок → избыток. nil и 0 равнозначны «нет».
func TestResolve(t *testing.T) {
	tests := []struct {
		name        string
		manual      *int16
		expiry      *int16
		surplus     *int16
		wantPercent *int16
		wantSource  Source
	}{
		{"ручная 30 + срок 40 → ручная", i16(30), i16(40), nil, i16(30), SourceManual},
		{"ручная 0 + срок 40 → ручная 0 % (запрет)", i16(0), i16(40), nil, i16(0), SourceManual},
		{"срок 0 + избыток 10 → избыток", nil, i16(0), i16(10), i16(10), SourceSurplus},
		{"ручная 0 + срок 0 + избыток 10 → ручная 0 %", i16(0), i16(0), i16(10), i16(0), SourceManual},
		{"всё пусто → нет скидки", nil, nil, nil, nil, SourceNone},
		{"нули у расчётных источников без ручной → нет скидки", nil, i16(0), i16(0), nil, SourceNone},
		{"все нули: ручная 0 % побеждает", i16(0), i16(0), i16(0), i16(0), SourceManual},
		{"только ручная", i16(15), nil, nil, i16(15), SourceManual},
		{"только срок", nil, i16(20), nil, i16(20), SourceExpiry},
		{"только избыток", nil, nil, i16(10), i16(10), SourceSurplus},
		{"ручная выше всех → ручная", i16(5), i16(40), i16(10), i16(5), SourceManual},
		{"отрицательная ручная → срок", i16(-5), i16(40), nil, i16(40), SourceExpiry},
		{"избыток при пустом сроке", nil, i16(0), i16(10), i16(10), SourceSurplus},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotPercent, gotSource := Resolve(tc.manual, tc.expiry, tc.surplus)
			if gotSource != tc.wantSource {
				t.Errorf("Resolve(...) source = %v, want %v", gotSource, tc.wantSource)
			}
			switch {
			case tc.wantPercent == nil && gotPercent != nil:
				t.Errorf("Resolve(...) percent = %d, want nil", *gotPercent)
			case tc.wantPercent != nil && gotPercent == nil:
				t.Errorf("Resolve(...) percent = nil, want %d", *tc.wantPercent)
			case tc.wantPercent != nil && gotPercent != nil && *gotPercent != *tc.wantPercent:
				t.Errorf("Resolve(...) percent = %d, want %d", *gotPercent, *tc.wantPercent)
			default:
				// Значения совпали (в том числе оба nil) — процент проверен.
			}
		})
	}
}
