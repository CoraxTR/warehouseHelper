package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestNormalizePath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "uuid в середине пути", in: "/ms/suppliers/edit?id=550e8400-e29b-41d4-a716-446655440000", want: "/ms/suppliers/edit?id=:id"},
		{name: "uuid как сегмент", in: "/qrcodes/photos/550e8400-e29b-41d4-a716-446655440000.jpg", want: "/qrcodes/photos/:id.jpg"},
		{name: "путь без uuid", in: "/ms/suppliers", want: "/ms/suppliers"},
		{name: "корень", in: "/", want: "/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizePath(tc.in); got != tc.want {
				t.Errorf("NormalizePath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestHandlerServesGoMetrics(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()

	Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"go_goroutines", "go_memstats_heap_alloc_bytes", "process_resident_memory_bytes"} {
		if !strings.Contains(body, want) {
			t.Errorf("тело /metrics не содержит %q", want)
		}
	}
}

func TestMiddlewareCountsRequests(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux := Middleware(inner)

	req := httptest.NewRequest(http.MethodGet, "/some/page", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("ответ = %d, want 404", rec.Code)
	}
	want := "some/page"
	if got := testutil.ToFloat64(httpRequestsTotal.WithLabelValues(http.MethodGet, "/some/page", "404")); got != 1 {
		t.Errorf("счётчик http_requests_total для %s = %v, want 1 (путь нормализован в %q)", want, got, want)
	}
}
