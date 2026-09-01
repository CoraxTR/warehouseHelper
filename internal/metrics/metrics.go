// Пакет metrics — метрики приложения для Prometheus (нижний общий слой).
//
// Отдаёт через /metrics: счётчики HTTP-запросов (http_requests_total),
// гистограммы длительности (http_request_duration_seconds) и стандартные
// метрики Go-рантайма и процесса (go_*, process_*), которые регистрирует
// default-registry client_golang.
package metrics

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	httpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Общее количество HTTP-запросов (по методу, пути и статусу).",
		},
		[]string{"method", "path", "status"},
	)

	httpRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "Длительность HTTP-запросов в секундах.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)
)

// uuidPattern — UUID из 8-4-4-4-12 hex-символов; в путях заменяется на ":id",
// чтобы кардинальность метрик не росла с каждым новым id.
var uuidPattern = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// NormalizePath приводит путь к виду с постоянной кардинальностью:
// идентификаторы (uuid) заменяются на ":id".
func NormalizePath(p string) string {
	return uuidPattern.ReplaceAllString(p, ":id")
}

// tableSizesBytes — размер таблиц БД в байтах (заполняет фоновый опрос
// в app: периодический запрос pg_total_relation_size по pg_tables).
var tableSizesBytes = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "pg_table_sizes_bytes",
		Help: "Общий размер таблиц PostgreSQL в байтах (включая индексы и TOAST), по схеме и таблице.",
	},
	[]string{"schema", "table"},
)

// SetTableSizes обновляет gauge размеров таблиц. Вызывается фоновым
// опросом БД; ключ — "schema.table".
func SetTableSizes(sizes map[string]int64) {
	tableSizesBytes.Reset()
	for k, v := range sizes {
		schema, table, ok := strings.Cut(k, ".")
		if !ok {
			continue
		}
		tableSizesBytes.WithLabelValues(schema, table).Set(float64(v))
	}
}

// msRequestsTotal — исходящие запросы к МойСклад (по нормализованному
// эндпоинту и статусу; статус "network_error" — запрос не ушёл: таймаут,
// соединение отказано и т.п.).
var msRequestsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "ms_requests_total",
		Help: "Исходящие запросы к API МойСклад (эндпоинт, статус ответа или network_error).",
	},
	[]string{"endpoint", "status"},
)

// msRequestDuration — длительность исходящих запросов к МойСклад.
var msRequestDuration = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "ms_request_duration_seconds",
		Help:    "Длительность запросов к API МойСклад в секундах.",
		Buckets: prometheus.DefBuckets,
	},
	[]string{"endpoint"},
)

// msURLPrefix — префикс пути API МойСклад, который отрезается в лейбле
// endpoint, чтобы не раздувать кардинальность (остаётся "entity/customerorder/:id").
const msURLPrefix = "/api/remap/1.2/"

// MSEndpoint нормализует URL запроса к МС до вида "entity/customerorder/:id":
// отрезает префикс хоста/версии и заменяет uuid на ":id". Непарсящийся URL —
// лейбл "unknown".
func MSEndpoint(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "unknown"
	}
	return NormalizePath(strings.TrimPrefix(u.Path, msURLPrefix))
}

// msRateLimitedTotal — счётчик 429 от МС (рейт-лимит; 100×429/5мин = бан).
var msRateLimitedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "ms_rate_limited_total",
		Help: "Ответы МойСклад 429 (превышен лимит запросов) — риск бана API.",
	},
	[]string{"endpoint"},
)

// ObserveMSRateLimited учитывает один ответ 429 от МС.
func ObserveMSRateLimited(endpoint string) {
	msRateLimitedTotal.WithLabelValues(endpoint).Inc()
}

// ObserveMSRequest учитывает один исходящий запрос к МС: счётчик по
// эндпоинту и статусу (или "network_error", если запрос не ушёл) и
// длительность в гистограмму. Вызывается из msclient.
func ObserveMSRequest(endpoint, status string, d time.Duration) {
	msRequestsTotal.WithLabelValues(endpoint, status).Inc()
	msRequestDuration.WithLabelValues(endpoint).Observe(d.Seconds())
}

// funcCallsTotal — количество вызовов функций приложения (по пакету и имени).
// Инструментируются входные точки usecase-слоя модулей: это «функции в
// пакетах», по которым видно, что вызывается чаще всего (см. Track).
var funcCallsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "func_calls_total",
		Help: "Количество вызовов функций приложения (пакет, функция).",
	},
	[]string{"package", "function"},
)

// funcCallDuration — длительность вызовов функций приложения.
var funcCallDuration = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "func_call_duration_seconds",
		Help:    "Длительность вызовов функций приложения в секундах.",
		Buckets: prometheus.DefBuckets,
	},
	[]string{"package", "function"},
)

// Track возвращает замыкание для замера одного вызова функции:
//
//	defer metrics.Track("stock", "SetManualDiscount")()
//
// Учитывает вызов в func_calls_total и длительность в func_call_duration_seconds.
// Считает только те функции, где вызывается, — полный охват не обязателен,
// на дашборде видно по лейблу package, какой модуль покрыт.
func Track(pkg, fn string) func() {
	start := time.Now()
	return func() {
		funcCallsTotal.WithLabelValues(pkg, fn).Inc()
		funcCallDuration.WithLabelValues(pkg, fn).Observe(time.Since(start).Seconds())
	}
}

// Handler возвращает HTTP-обработчик /metrics (стандартные go_*, process_*
// метрики плюс зарегистрированные выше счётчики).
func Handler() http.Handler {
	return promhttp.Handler()
}

// statusRecorder запоминает код ответа, чтобы middleware мог пометить
// запрос статусом (для http_requests_total), и пробрасывает hijack
// нижележащему writer (без этого вебсокеты падают с 500).
type statusRecorder struct {
	http.ResponseWriter

	status   int
	hijacked bool
}

// WriteHeader фиксирует код ответа до передачи нижележащему writer.
// После hijack (вебсокет) ответ уже пишется напрямую в соединение — не трогаем.
func (r *statusRecorder) WriteHeader(code int) {
	if r.hijacked {
		return
	}
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Hijack отдаёт соединение нижележащему writer (http.Hijacker). Без этого
// обёрнутый middleware ResponseWriter не реализует http.Hijacker, и
// gorilla/websocket отвечает 500 «response does not implement http.Hijacker».
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response does not implement http.Hijacker")
	}
	r.hijacked = true
	return h.Hijack()
}

// Компиляционная проверка: обёртка обязана сохранять hijack-возможность
// нижележащего ResponseWriter (иначе регрессия ломает вебсокеты молча).
var _ http.Hijacker = (*statusRecorder)(nil)

// Middleware оборачивает весь роутер: считает запросы и длительность
// обработки по методу и нормализованному пути.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, req)
		if rec.hijacked {
			rec.status = http.StatusSwitchingProtocols
		}

		path := NormalizePath(req.URL.Path)
		httpRequestsTotal.WithLabelValues(req.Method, path, strconv.Itoa(rec.status)).Inc()
		httpRequestDuration.WithLabelValues(req.Method, path).Observe(time.Since(start).Seconds())
	})
}
