// Пакет metrics — метрики приложения для Prometheus (нижний общий слой).
//
// Отдаёт через /metrics: счётчики HTTP-запросов (http_requests_total),
// гистограммы длительности (http_request_duration_seconds) и стандартные
// метрики Go-рантайма и процесса (go_*, process_*), которые регистрирует
// default-registry client_golang.
package metrics

import (
	"net/http"
	"regexp"
	"strconv"
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

// Handler возвращает HTTP-обработчик /metrics (стандартные go_*, process_*
// метрики плюс зарегистрированные выше счётчики).
func Handler() http.Handler {
	return promhttp.Handler()
}

// statusRecorder запоминает код ответа, чтобы middleware мог пометить
// запрос статусом (для http_requests_total).
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader фиксирует код ответа до передачи нижележащему writer.
func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Middleware оборачивает весь роутер: считает запросы и длительность
// обработки по методу и нормализованному пути.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, req)

		path := NormalizePath(req.URL.Path)
		httpRequestsTotal.WithLabelValues(req.Method, path, strconv.Itoa(rec.status)).Inc()
		httpRequestDuration.WithLabelValues(req.Method, path).Observe(time.Since(start).Seconds())
	})
}
