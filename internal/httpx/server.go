package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Check is a readiness dependency (database, broker...).
type Check func(ctx context.Context) error

// Server bundles an HTTP server with probes, metrics and graceful shutdown.
type Server struct {
	Name     string
	Version  string
	Addr     string
	Log      *slog.Logger
	Registry *prometheus.Registry
	Mux      *http.ServeMux

	// Wrap optionally decorates the mux (e.g. fault injection) inside the
	// instrumentation layer, so its effects are visible in metrics and logs.
	Wrap func(*http.ServeMux) http.Handler

	// DrainDelay keeps serving after SIGTERM while readiness reports failure,
	// so kube-proxy / ingress remove the pod from endpoints before connections close.
	DrainDelay time.Duration

	mu       sync.Mutex
	checks   map[string]Check
	draining atomic.Bool
}

func NewServer(name, version, addr string, log *slog.Logger) *Server {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	build := prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "build_info",
		Help:        "Build information; value is always 1.",
		ConstLabels: prometheus.Labels{"service": name, "version": version},
	})
	build.Set(1)
	reg.MustRegister(build)

	return &Server{
		Name:       name,
		Version:    version,
		Addr:       addr,
		Log:        log,
		Registry:   reg,
		Mux:        http.NewServeMux(),
		DrainDelay: 10 * time.Second,
		checks:     map[string]Check{},
	}
}

// AddCheck registers a readiness check.
func (s *Server) AddCheck(name string, c Check) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checks[name] = c
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if s.draining.Load() {
		http.Error(w, "draining", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	s.mu.Lock()
	checks := make(map[string]Check, len(s.checks))
	for k, v := range s.checks {
		checks[k] = v
	}
	s.mu.Unlock()

	result := map[string]string{}
	status := http.StatusOK
	for name, c := range checks {
		if err := c(ctx); err != nil {
			result[name] = err.Error()
			status = http.StatusServiceUnavailable
		} else {
			result[name] = "ok"
		}
	}
	WriteJSON(w, status, result)
}

// Handler returns the full instrumented handler (useful in tests).
func (s *Server) Handler() http.Handler {
	s.Mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	s.Mux.HandleFunc("GET /readyz", s.handleReady)
	s.Mux.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"service": s.Name, "version": s.Version})
	})
	s.Mux.Handle("GET /metrics", promhttp.HandlerFor(s.Registry, promhttp.HandlerOpts{}))
	var h http.Handler = s.Mux
	if s.Wrap != nil {
		h = s.Wrap(s.Mux)
	}
	return Instrument(h, NewMetrics(s.Registry), s.Log)
}

// Run serves until SIGTERM/SIGINT, then drains and shuts down gracefully.
func (s *Server) Run() error {
	srv := &http.Server{
		Addr:              s.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		s.Log.Info("listening", "addr", s.Addr, "version", s.Version)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	s.Log.Info("shutdown signal received, draining", "delay", s.DrainDelay)
	s.draining.Store(true)
	time.Sleep(s.DrainDelay)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	s.Log.Info("stopped")
	return nil
}

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes a JSON error body.
func Error(w http.ResponseWriter, r *http.Request, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg, "request_id": RequestID(r.Context())})
}
