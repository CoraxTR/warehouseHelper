package discounts

import "testing"

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
		{"ручная 30 + срок 40 → ручная", new(int16(30)), new(int16(40)), nil, new(int16(30)), SourceManual},
		{"ручная 0 + срок 40 → ручная 0 % (запрет)", new(int16(0)), new(int16(40)), nil, new(int16(0)), SourceManual},
		{"срок 0 + избыток 10 → избыток", nil, new(int16(0)), new(int16(10)), new(int16(10)), SourceSurplus},
		{"ручная 0 + срок 0 + избыток 10 → ручная 0 %", new(int16(0)), new(int16(0)), new(int16(10)), new(int16(0)), SourceManual},
		{"всё пусто → нет скидки", nil, nil, nil, nil, SourceNone},
		{"нули у расчётных источников без ручной → нет скидки", nil, new(int16(0)), new(int16(0)), nil, SourceNone},
		{"все нули: ручная 0 % побеждает", new(int16(0)), new(int16(0)), new(int16(0)), new(int16(0)), SourceManual},
		{"только ручная", new(int16(15)), nil, nil, new(int16(15)), SourceManual},
		{"только срок", nil, new(int16(20)), nil, new(int16(20)), SourceExpiry},
		{"только избыток", nil, nil, new(int16(10)), new(int16(10)), SourceSurplus},
		{"ручная выше всех → ручная", new(int16(5)), new(int16(40)), new(int16(10)), new(int16(5)), SourceManual},
		{"отрицательная ручная → срок", new(int16(-5)), new(int16(40)), nil, new(int16(40)), SourceExpiry},
		{"избыток при пустом сроке", nil, new(int16(0)), new(int16(10)), new(int16(10)), SourceSurplus},
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
