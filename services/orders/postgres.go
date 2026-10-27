package orders

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
)

//go:embed migrations/001_init.sql
var migrationInit string

type PostgresStore struct {
	db *sql.DB
}

// OpenPostgres opens a pool sized for PgBouncer in transaction mode
// (Managed PostgreSQL exposes it on port 6432).
func OpenPostgres(dsn string) (*PostgresStore, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)
	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
func (s *PostgresStore) Close() error                   { return s.db.Close() }

// Migrate is executed by the Helm pre-upgrade hook Job (`orders -migrate`),
// never by application pods on start: N replicas must not race on DDL.
func (s *PostgresStore) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, migrationInit)
	return err
}

func (s *PostgresStore) Create(ctx context.Context, o *Order, key string) error {
	items, err := json.Marshal(o.Items)
	if err != nil {
		return err
	}
	var nullableKey sql.NullString
	if key != "" {
		nullableKey = sql.NullString{String: key, Valid: true}
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO orders (id, customer_id, items, total_rub, status, created_at, idempotency_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		o.ID, o.CustomerID, items, o.TotalRUB, o.Status, o.CreatedAt, nullableKey)

	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" && pqErr.Constraint == "orders_idempotency_key_key" {
		return ErrDuplicate
	}
	if err != nil {
		return fmt.Errorf("insert order: %w", err)
	}
	return nil
}

func (s *PostgresStore) Get(ctx context.Context, id string) (*Order, error) {
	var (
		o     Order
		items []byte
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, customer_id, items, total_rub, status, created_at
		FROM orders WHERE id = $1`, id).
		Scan(&o.ID, &o.CustomerID, &items, &o.TotalRUB, &o.Status, &o.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(items, &o.Items); err != nil {
		return nil, err
	}
	o.CreatedAt = o.CreatedAt.UTC()
	return &o, nil
}
