package catalog

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GiitEnjoyer/shop-platform/internal/httpx"
)

const fixture = `[
  {"id":"p3","name":"Keyboard","category":"electronics","price_rub":4990,"stock":10},
  {"id":"p1","name":"Mug","category":"home","price_rub":590,"stock":100},
  {"id":"p2","name":"Mouse","category":"electronics","price_rub":1990,"stock":0}
]`

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	store := NewStore()
	if err := store.Load(strings.NewReader(fixture)); err != nil {
		t.Fatal(err)
	}
	srv := httpx.NewServer("catalog", "test", ":0", slog.New(slog.NewTextHandler(io.Discard, nil)))
	Register(srv.Mux, store)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestListFiltersAndSorts(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/v1/products?category=Electronics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body struct {
		Items []Product `json:"items"`
		Total int       `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 2 || body.Items[0].ID != "p2" || body.Items[1].ID != "p3" {
		t.Fatalf("unexpected result: %+v", body)
	}
}

func TestPaginationBounds(t *testing.T) {
	s := NewStore()
	_ = s.Load(strings.NewReader(fixture))
	items, total := s.List("", 2, 10)
	if total != 3 || len(items) != 1 || items[0].ID != "p3" {
		t.Fatalf("got %v total=%d", items, total)
	}
	items, _ = s.List("", 50, 10)
	if len(items) != 0 {
		t.Fatalf("offset past end must return empty page, got %v", items)
	}
}

func TestGetNotFoundReturnsJSONError(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/v1/products/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if resp.Header.Get(httpx.RequestIDHeader) == "" {
		t.Fatal("request id header missing")
	}
}

func TestMetricsUseRoutePatternNotRawPath(t *testing.T) {
	ts := newTestServer(t)
	for _, id := range []string{"p1", "p2", "p3"} {
		r, _ := http.Get(ts.URL + "/api/v1/products/" + id)
		r.Body.Close()
	}
	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	metrics := string(b)

	want := `http_requests_total{code="200",method="GET",route="GET /api/v1/products/{id}"} 3`
	if !strings.Contains(metrics, want) {
		t.Fatalf("metric %q not found", want)
	}
	if strings.Contains(metrics, `/products/p1"`) {
		t.Fatal("raw path leaked into labels (cardinality explosion)")
	}
}

func TestLoadRejectsProductWithoutID(t *testing.T) {
	s := NewStore()
	if err := s.Load(strings.NewReader(`[{"name":"x"}]`)); err == nil {
		t.Fatal("expected error")
	}
}
