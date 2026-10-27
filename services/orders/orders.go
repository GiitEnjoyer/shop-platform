// Package orders accepts orders, persists them in PostgreSQL and publishes an
// `order.created` event to RabbitMQ for downstream consumers (payments, mail).
package orders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/GiitEnjoyer/shop-platform/internal/httpx"
)

type Item struct {
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
	PriceRUB  int64  `json:"price_rub"`
}

type Order struct {
	ID         string    `json:"id"`
	CustomerID string    `json:"customer_id"`
	Items      []Item    `json:"items"`
	TotalRUB   int64     `json:"total_rub"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

var (
	ErrNotFound  = errors.New("order not found")
	ErrDuplicate = errors.New("duplicate idempotency key")
)

// Store persists orders. Implemented by PostgresStore; faked in tests.
type Store interface {
	Create(ctx context.Context, o *Order, idempotencyKey string) error
	Get(ctx context.Context, id string) (*Order, error)
}

// Publisher emits domain events. Implemented by AMQPPublisher.
type Publisher interface {
	Publish(ctx context.Context, routingKey string, body []byte) error
}

type CreateRequest struct {
	CustomerID string `json:"customer_id"`
	Items      []Item `json:"items"`
}

// Validate checks business invariants and returns the order total.
func (r CreateRequest) Validate() (int64, error) {
	if r.CustomerID == "" {
		return 0, errors.New("customer_id is required")
	}
	if len(r.Items) == 0 || len(r.Items) > 50 {
		return 0, errors.New("order must contain 1..50 items")
	}
	var total int64
	for i, it := range r.Items {
		switch {
		case it.ProductID == "":
			return 0, fmt.Errorf("items[%d].product_id is required", i)
		case it.Quantity < 1 || it.Quantity > 100:
			return 0, fmt.Errorf("items[%d].quantity must be 1..100", i)
		case it.PriceRUB <= 0:
			return 0, fmt.Errorf("items[%d].price_rub must be positive", i)
		}
		total += it.PriceRUB * int64(it.Quantity)
	}
	return total, nil
}

type Service struct {
	store     Store
	publisher Publisher
	newID     func() string
	now       func() time.Time

	created   prometheus.Counter
	revenue   prometheus.Counter
	pubErrors prometheus.Counter
}

func NewService(store Store, pub Publisher, reg prometheus.Registerer, newID func() string) *Service {
	f := promauto.With(reg)
	return &Service{
		store:     store,
		publisher: pub,
		newID:     newID,
		now:       time.Now,
		created: f.NewCounter(prometheus.CounterOpts{
			Name: "orders_created_total", Help: "Orders successfully created.",
		}),
		revenue: f.NewCounter(prometheus.CounterOpts{
			Name: "orders_revenue_rub_total", Help: "Sum of created order totals, RUB.",
		}),
		pubErrors: f.NewCounter(prometheus.CounterOpts{
			Name: "orders_event_publish_errors_total", Help: "Failed order.created publications.",
		}),
	}
}

// Register mounts order routes on mux.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/orders", s.create)
	mux.HandleFunc("GET /api/v1/orders/{id}", s.get)
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req CreateRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	total, err := req.Validate()
	if err != nil {
		httpx.Error(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}

	o := &Order{
		ID:         s.newID(),
		CustomerID: req.CustomerID,
		Items:      req.Items,
		TotalRUB:   total,
		Status:     "created",
		CreatedAt:  s.now().UTC(),
	}

	// Idempotency-Key lets clients retry safely after timeouts at the ingress.
	key := r.Header.Get("Idempotency-Key")
	if err := s.store.Create(r.Context(), o, key); err != nil {
		if errors.Is(err, ErrDuplicate) {
			httpx.Error(w, r, http.StatusConflict, "order with this Idempotency-Key already exists")
			return
		}
		httpx.Error(w, r, http.StatusInternalServerError, "could not save order")
		return
	}
	s.created.Inc()
	s.revenue.Add(float64(total))

	// The order is committed; a broker outage must not fail the request.
	// Lost events are counted and alerted on; the trade-off and the
	// transactional-outbox upgrade path are in docs/adr/0002-event-publishing.md.
	body, _ := json.Marshal(o)
	if err := s.publisher.Publish(r.Context(), "order.created", body); err != nil {
		s.pubErrors.Inc()
	}

	w.Header().Set("Location", "/api/v1/orders/"+o.ID)
	httpx.WriteJSON(w, http.StatusCreated, o)
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	o, err := s.store.Get(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "order not found")
	case err != nil:
		httpx.Error(w, r, http.StatusInternalServerError, "could not load order")
	default:
		httpx.WriteJSON(w, http.StatusOK, o)
	}
}
