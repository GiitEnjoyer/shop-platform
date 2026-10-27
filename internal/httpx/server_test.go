package httpx

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s := NewServer("svc", "1.2.3", ":0", slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.Mux.HandleFunc("GET /api/panic", func(http.ResponseWriter, *http.Request) { panic("boom") })
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func status(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestReadinessReflectsDependencies(t *testing.T) {
	s, ts := newTestServer(t)
	if got := status(t, ts.URL+"/readyz"); got != http.StatusOK {
		t.Fatalf("no checks: %d", got)
	}
	s.AddCheck("db", func(context.Context) error { return errors.New("down") })
	if got := status(t, ts.URL+"/readyz"); got != http.StatusServiceUnavailable {
		t.Fatalf("failing check: %d", got)
	}
	// Liveness must not depend on dependencies: a DB outage must not restart pods.
	if got := status(t, ts.URL+"/healthz"); got != http.StatusOK {
		t.Fatalf("liveness: %d", got)
	}
}

func TestDrainingFailsReadiness(t *testing.T) {
	s, ts := newTestServer(t)
	s.draining.Store(true)
	if got := status(t, ts.URL+"/readyz"); got != http.StatusServiceUnavailable {
		t.Fatalf("draining: %d", got)
	}
}

func TestPanicIsRecoveredAs500(t *testing.T) {
	_, ts := newTestServer(t)
	if got := status(t, ts.URL+"/api/panic"); got != http.StatusInternalServerError {
		t.Fatalf("panic: %d", got)
	}
	// server still alive
	if got := status(t, ts.URL+"/healthz"); got != http.StatusOK {
		t.Fatalf("after panic: %d", got)
	}
}

func TestRequestIDPropagated(t *testing.T) {
	_, ts := newTestServer(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/version", nil)
	req.Header.Set(RequestIDHeader, "abc-123")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get(RequestIDHeader); got != "abc-123" {
		t.Fatalf("request id = %q", got)
	}
}
