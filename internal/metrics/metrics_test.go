package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
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
	req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
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

func TestMSEndpoint(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://api.moysklad.ru/api/remap/1.2/entity/customerorder/12345678-1234-1234-1234-123456789012", "entity/customerorder/:id"},
		{"https://api.moysklad.ru/api/remap/1.2/report/profit/byproduct?interval=month", "report/profit/byproduct"},
		{"https://api.moysklad.ru/api/remap/1.2/entity/counterparty", "entity/counterparty"},
		{"не-url", "unknown"},
	}
	for _, tc := range cases {
		if got := MSEndpoint(tc.in); got != tc.want {
			t.Errorf("MSEndpoint(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestObserveMSRequest(t *testing.T) {
	ObserveMSRequest("entity/customerorder", "200", 10*time.Millisecond)
	defer msRequestsTotal.Reset()
	defer msRequestDuration.Reset()

	if got := testutil.ToFloat64(msRequestsTotal.WithLabelValues("entity/customerorder", "200")); got != 1 {
		t.Errorf("ms_requests_total = %v, want 1", got)
	}
}

func TestObserveMSRateLimited(t *testing.T) {
	ObserveMSRateLimited("entity/customerorder")
	defer msRateLimitedTotal.Reset()

	if got := testutil.ToFloat64(msRateLimitedTotal.WithLabelValues("entity/customerorder")); got != 1 {
		t.Errorf("ms_rate_limited_total = %v, want 1", got)
	}
}

func TestTrack(t *testing.T) {
	done := Track("test", "F")
	done()
	defer funcCallsTotal.Reset()
	defer funcCallDuration.Reset()

	if got := testutil.ToFloat64(funcCallsTotal.WithLabelValues("test", "F")); got != 1 {
		t.Errorf("func_calls_total = %v, want 1", got)
	}
}

func TestSetTableSizes(t *testing.T) {
	SetTableSizes(map[string]int64{"public.products": 12345, "public.orders": 67890, "no_schema": 1})
	defer tableSizesBytes.Reset()

	if got := testutil.ToFloat64(tableSizesBytes.WithLabelValues("public", "products")); got != 12345 {
		t.Errorf("public.products = %v, want 12345", got)
	}
	if got := testutil.ToFloat64(tableSizesBytes.WithLabelValues("public", "orders")); got != 67890 {
		t.Errorf("public.orders = %v, want 67890", got)
	}
	// Ключ без точки пропускается (метрика не создаётся).
	if got := testutil.ToFloat64(tableSizesBytes.WithLabelValues("", "no_schema")); got != 0 {
		t.Errorf("no_schema = %v, want 0 (пропущен)", got)
	}
}

func TestMiddlewareCountsRequests(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux := Middleware(inner)

	req := httptest.NewRequest(http.MethodGet, "/some/page", http.NoBody)
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

func TestMiddlewareKeepsWebSocketUpgrade(t *testing.T) {
	// Регрессия (31.08.2026): statusRecorder не реализовывал http.Hijacker,
	// и каждый ws-апгрейд через Middleware падал с 500
	// «response does not implement http.Hijacker» — страницы «Сроки» навсегда
	// зависали в «переподключение…». Диал через middleware обязан работать.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return // Upgrade сам написал 500
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteMessage(websocket.TextMessage, []byte("pong"))
	})
	srv := httptest.NewServer(Middleware(inner))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial ws через middleware: %v", err)
	}
	if resp != nil {
		_ = resp.Body.Close()
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))

	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read ws: %v", err)
	}
	if string(data) != "pong" {
		t.Errorf("данные ws = %q, want pong", data)
	}

	// Успешный апгрейд учитывается как 101 Switching Protocols.
	if got := testutil.ToFloat64(httpRequestsTotal.WithLabelValues(http.MethodGet, "/ws", "101")); got != 1 {
		t.Errorf("http_requests_total для ws = %v, want 1 (101)", got)
	}
	defer httpRequestsTotal.Reset()
	defer httpRequestDuration.Reset()
}
