-- Idempotent: safe to re-run on every deploy.
CREATE TABLE IF NOT EXISTS orders (
    id              text        PRIMARY KEY,
    customer_id     text        NOT NULL,
    items           jsonb       NOT NULL,
    total_rub       bigint      NOT NULL CHECK (total_rub > 0),
    status          text        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    idempotency_key text        UNIQUE
);

CREATE INDEX IF NOT EXISTS orders_customer_created_idx
    ON orders (customer_id, created_at DESC);
