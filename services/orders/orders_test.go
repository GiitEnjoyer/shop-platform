package orders

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/GiitEnjoyer/shop-platform/internal/httpx"
)

type memStore struct {
	mu     sync.Mutex
	orders map[string]*Order
	keys   map[string]bool
	fail   bool
}

func (m *memStore) Create(_ context.Context, o *Order, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("db down")
	}
	if key != "" && m.keys[key] {
		return ErrDuplicate
	}
	m.orders[o.ID] = o
	if key != "" {
		m.keys[key] = true
	}
	return nil
}

func (m *memStore) Get(_ context.Context, id string) (*Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.orders[id]
	if !ok {
		return nil, ErrNotFound
	}
	return o, nil
}

type fakePublisher struct {
	mu   sync.Mutex
	sent []string
	fail bool
}

func (f *fakePublisher) Publish(_ context.Context, key string, _ []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("broker down")
	}
	f.sent = append(f.sent, key)
	return nil
}

func setup(t *testing.T) (*httptest.Server, *memStore, *fakePublisher) {
	t.Helper()
	store := &memStore{orders: map[string]*Order{}, keys: map[string]bool{}}
	pub := &fakePublisher{}
	srv := httpx.NewServer("orders", "test", ":0", slog.New(slog.NewTextHandler(io.Discard, nil)))
	n := 0
	NewService(store, pub, srv.Registry, func() string { n++; return "ord-" + string(rune('0'+n)) }).Register(srv.Mux)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, store, pub
}

func post(t *testing.T, url, body, key string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url+"/api/v1/orders", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func get(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

const validOrder = `{"customer_id":"c1","items":[{"product_id":"p1","quantity":2,"price_rub":590},{"product_id":"p3","quantity":1,"price_rub":4990}]}`

func TestCreateOrderComputesTotalAndPublishesEvent(t *testing.T) {
	ts, store, pub := setup(t)
	resp := post(t, ts.URL, validOrder, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var o Order
	_ = json.NewDecoder(resp.Body).Decode(&o)
	if o.TotalRUB != 2*590+4990 {
		t.Fatalf("total = %d", o.TotalRUB)
	}
	if resp.Header.Get("Location") != "/api/v1/orders/"+o.ID {
		t.Fatalf("location = %q", resp.Header.Get("Location"))
	}
	if len(store.orders) != 1 || len(pub.sent) != 1 || pub.sent[0] != "order.created" {
		t.Fatalf("store=%d events=%v", len(store.orders), pub.sent)
	}
}

func TestValidation(t *testing.T) {
	ts, _, _ := setup(t)
	cases := map[string]string{
		"no customer":   `{"items":[{"product_id":"p1","quantity":1,"price_rub":1}]}`,
		"no items":      `{"customer_id":"c1","items":[]}`,
		"zero quantity": `{"customer_id":"c1","items":[{"product_id":"p1","quantity":0,"price_rub":1}]}`,
		"neg price":     `{"customer_id":"c1","items":[{"product_id":"p1","quantity":1,"price_rub":-5}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if code := post(t, ts.URL, body, "").StatusCode; code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d", code)
			}
		})
	}
}

func TestUnknownFieldsRejected(t *testing.T) {
	ts, _, _ := setup(t)
	body := strings.Replace(validOrder, `"customer_id"`, `"admin":true,"customer_id"`, 1)
	if code := post(t, ts.URL, body, "").StatusCode; code != http.StatusBadRequest {
		t.Fatalf("status = %d", code)
	}
}

func TestIdempotencyKey(t *testing.T) {
	ts, store, _ := setup(t)
	if code := post(t, ts.URL, validOrder, "k-1").StatusCode; code != http.StatusCreated {
		t.Fatalf("first = %d", code)
	}
	if code := post(t, ts.URL, validOrder, "k-1").StatusCode; code != http.StatusConflict {
		t.Fatalf("retry = %d", code)
	}
	if len(store.orders) != 1 {
		t.Fatalf("orders = %d", len(store.orders))
	}
}

func TestBrokerOutageDoesNotFailCommittedOrder(t *testing.T) {
	ts, store, pub := setup(t)
	pub.fail = true
	if code := post(t, ts.URL, validOrder, "").StatusCode; code != http.StatusCreated {
		t.Fatalf("status = %d", code)
	}
	if len(store.orders) != 1 {
		t.Fatal("order must be stored")
	}

	resp := get(t, ts.URL+"/metrics")
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "orders_event_publish_errors_total 1") {
		t.Fatal("publish error must be counted")
	}
}

func TestDatabaseErrorReturns500(t *testing.T) {
	ts, store, _ := setup(t)
	store.fail = true
	if code := post(t, ts.URL, validOrder, "").StatusCode; code != http.StatusInternalServerError {
		t.Fatalf("status = %d", code)
	}
}

func TestGetOrder(t *testing.T) {
	ts, _, _ := setup(t)
	var created Order
	_ = json.NewDecoder(post(t, ts.URL, validOrder, "").Body).Decode(&created)

	if resp := get(t, ts.URL+"/api/v1/orders/"+created.ID); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if resp2 := get(t, ts.URL+"/api/v1/orders/missing"); resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", resp2.StatusCode)
	}
}
