// Package catalog is a read-heavy product catalog API. Data is held in memory
// and reloaded from a JSON snapshot, which makes the service stateless and
// horizontally scalable: the hot path never touches the database.
package catalog

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/GiitEnjoyer/shop-platform/internal/httpx"
)

type Product struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	PriceRUB int64  `json:"price_rub"`
	Stock    int    `json:"stock"`
}

type Store struct {
	mu       sync.RWMutex
	products map[string]Product
}

func NewStore() *Store {
	return &Store{products: map[string]Product{}}
}

// Load replaces the catalog atomically from a JSON array.
func (s *Store) Load(r io.Reader) error {
	var items []Product
	if err := json.NewDecoder(r).Decode(&items); err != nil {
		return fmt.Errorf("decode catalog: %w", err)
	}
	next := make(map[string]Product, len(items))
	for _, p := range items {
		if p.ID == "" {
			return fmt.Errorf("product without id: %+v", p)
		}
		next[p.ID] = p
	}
	s.mu.Lock()
	s.products = next
	s.mu.Unlock()
	return nil
}

func (s *Store) Get(id string) (Product, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.products[id]
	return p, ok
}

// List returns products filtered by category, sorted by ID, paginated.
func (s *Store) List(category string, offset, limit int) ([]Product, int) {
	s.mu.RLock()
	out := make([]Product, 0, len(s.products))
	for _, p := range s.products {
		if category == "" || strings.EqualFold(p.Category, category) {
			out = append(out, p)
		}
	}
	s.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	total := len(out)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return out[offset:end], total
}

func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.products)
}

// Register mounts catalog routes on mux.
func Register(mux *http.ServeMux, s *Store) {
	mux.HandleFunc("GET /api/v1/products", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		offset := intParam(q.Get("offset"), 0, 0, 1_000_000)
		limit := intParam(q.Get("limit"), 20, 1, 100)
		items, total := s.List(q.Get("category"), offset, limit)
		w.Header().Set("Cache-Control", "public, max-age=30")
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"items":  items,
			"total":  total,
			"offset": offset,
			"limit":  limit,
		})
	})

	mux.HandleFunc("GET /api/v1/products/{id}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.Get(r.PathValue("id"))
		if !ok {
			httpx.Error(w, r, http.StatusNotFound, "product not found")
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=30")
		httpx.WriteJSON(w, http.StatusOK, p)
	})
}

func intParam(raw string, def, min, max int) int {
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
