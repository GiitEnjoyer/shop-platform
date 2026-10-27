// Package chaos injects controlled failures to demonstrate that the canary
// analysis in CI catches a bad release and rolls it back automatically.
// Enabled only when FAULT_RATE / FAULT_LATENCY are set (never in prod values).
package chaos

import (
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
)

type Config struct {
	ErrorRate float64       // share of requests answered with 500, 0..1
	Latency   time.Duration // extra latency added to every request
}

func (c Config) Enabled() bool { return c.ErrorRate > 0 || c.Latency > 0 }

// Middleware applies faults to API routes only; probes stay healthy so the
// failure is visible in metrics, not hidden by Kubernetes restarting pods.
// The route pattern is resolved up front so injected errors are attributed to
// the real route in metrics, exactly like genuine application errors.
func Middleware(c Config, mux *http.ServeMux) http.Handler {
	if !c.Enabled() {
		return mux
	}
	next := http.Handler(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			_, r.Pattern = mux.Handler(r)
			if c.Latency > 0 {
				time.Sleep(c.Latency)
			}
			if c.ErrorRate > 0 && rand.Float64() < c.ErrorRate {
				http.Error(w, `{"error":"injected fault"}`, http.StatusInternalServerError)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
