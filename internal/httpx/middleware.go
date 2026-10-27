// Package httpx contains the HTTP plumbing shared by every service:
// request IDs, structured access logs, RED metrics, panic recovery and
// graceful shutdown. Services only register handlers.
package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type ctxKey struct{}

// RequestIDHeader is propagated from ingress-nginx ($req_id) or generated here.
const RequestIDHeader = "X-Request-ID"

// Metrics holds RED metrics (Rate, Errors, Duration) for one service.
// Label `route` is the mux pattern, not the raw path, to keep cardinality bounded.
type Metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	inflight prometheus.Gauge
}

func NewMetrics(reg prometheus.Registerer) *Metrics {
	f := promauto.With(reg)
	return &Metrics{
		requests: f.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "HTTP requests by route, method and status code.",
		}, []string{"route", "method", "code"}),
		duration: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency by route.",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
		}, []string{"route", "method"}),
		inflight: f.NewGauge(prometheus.GaugeOpts{
			Name: "http_requests_in_flight",
			Help: "Requests currently being served.",
		}),
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// RequestID returns the request ID stored in ctx, if any.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Instrument wraps a mux with request ID, recovery, metrics and access log.
// Probe and metrics endpoints are excluded from logs to avoid noise.
func Instrument(next http.Handler, m *Metrics, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if id == "" {
			id = newID()
		}
		w.Header().Set(RequestIDHeader, id)
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, id))

		rec := &statusRecorder{ResponseWriter: w}
		start := time.Now()
		m.inflight.Inc()

		defer func() {
			m.inflight.Dec()
			if p := recover(); p != nil {
				log.Error("panic", "request_id", id, "panic", p)
				if rec.status == 0 {
					rec.WriteHeader(http.StatusInternalServerError)
				}
			}
			if rec.status == 0 {
				rec.status = http.StatusOK
			}

			route := r.Pattern // set by http.ServeMux (Go 1.23+)
			if route == "" {
				route = "unmatched"
			}
			elapsed := time.Since(start)
			m.requests.WithLabelValues(route, r.Method, strconv.Itoa(rec.status)).Inc()
			m.duration.WithLabelValues(route, r.Method).Observe(elapsed.Seconds())

			if isInternal(r.URL.Path) {
				return
			}
			level := slog.LevelInfo
			if rec.status >= 500 {
				level = slog.LevelError
			}
			log.LogAttrs(r.Context(), level, "request",
				slog.String("request_id", id),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("route", route),
				slog.Int("status", rec.status),
				slog.Int("bytes", rec.bytes),
				slog.Duration("duration", elapsed),
			)
		}()

		next.ServeHTTP(rec, r)
	})
}

func isInternal(path string) bool {
	switch path {
	case "/healthz", "/readyz", "/metrics":
		return true
	}
	return false
}
