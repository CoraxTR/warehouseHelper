package workerpool_test

import (
	"testing"

	"warehouseHelper/internal/msclient/client"
)

// *client.MSAPIError обязан удовлетворять контракту, по которому воркерпул
// решает, повторять запрос или нет (permanentError в ms_workerpool.go).
var _ interface{ Permanent() bool } = &client.MSAPIError{}

// TestMSAPIErrorPermanentContract прибивает классификацию ошибок МС, на которой
// стоит ретрай воркерпула. Тесты самого воркерпула используют свой тип ошибки
// (цикл импорта не даёт взять client.MSAPIError), поэтому соответствие
// проверяется здесь — на настоящем типе.
func TestMSAPIErrorPermanentContract(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		code      int
		permanent bool
	}{
		"400 — постоянная":                {code: 400, permanent: true},
		"404 — постоянная":                {code: 404, permanent: true},
		"408 таймаут — временная":         {code: 408, permanent: false},
		"429 рейт-лимит — временная":      {code: 429, permanent: false},
		"500 — временная":                 {code: 500, permanent: false},
		"503 — временная":                 {code: 503, permanent: false},
		"код не разобран (0) — временная": {code: 0, permanent: false},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := &client.MSAPIError{Code: c.code}
			if got := err.Permanent(); got != c.permanent {
				t.Errorf("Permanent() для кода %d = %v, ожидали %v", c.code, got, c.permanent)
			}
		})
	}
}
