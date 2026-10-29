// Package shop is the demo service's domain: products and orders, their
// storage and their HTTP API. The code is deliberately ordinary; what matters
// is the telemetry it produces and the failure scenarios it can reproduce.
package shop

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

// ErrNotFound is returned when a product or order does not exist.
var ErrNotFound = errors.New("not found")

// Product is an item for sale.
type Product struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	PriceCents int64  `json:"price_cents"`
}

// Order is one purchase of a product.
type Order struct {
	ID            int64     `json:"id"`
	CustomerEmail string    `json:"customer_email"`
	ProductID     int64     `json:"product_id"`
	Quantity      int32     `json:"quantity"`
	TotalCents    int64     `json:"total_cents"`
	CreatedAt     time.Time `json:"created_at"`
}

// SeedSize sets how much demo data Seed creates.
type SeedSize struct {
	Products, Customers, Orders int
}

// Store reads and writes the shop database.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore returns a Store backed by pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Migrate creates the schema if it does not exist yet.
func (s *Store) Migrate(ctx context.Context) error {
	// Without arguments pgx uses the simple protocol, which runs the whole
	// multi-statement file in one round trip.
	if _, err := s.pool.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Seed fills an empty database with deterministic demo data: the same sizes
// always produce the same rows, so benchmark runs are comparable. It reports
// whether it inserted anything; it does nothing when products already exist.
func (s *Store) Seed(ctx context.Context, size SeedSize) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	// A no-op after Commit; otherwise it undoes a partial seed.
	defer func() { _ = tx.Rollback(ctx) }()

	// Serialise concurrent first starts; the second one then sees the data.
	if _, err := tx.Exec(ctx, "LOCK TABLE products IN SHARE ROW EXCLUSIVE MODE"); err != nil {
		return false, err
	}
	var existing int64
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM products").Scan(&existing); err != nil {
		return false, err
	}
	if existing > 0 {
		return false, nil
	}

	// Prices and order details are derived from the row number with fixed
	// multipliers instead of random(), so every run produces identical data.
	if _, err := tx.Exec(ctx, `
		INSERT INTO products (id, name, price_cents)
		SELECT i, 'Product ' || i, 100 + (i::bigint * 7919) % 99900
		FROM generate_series(1, $1::int) AS i`, size.Products); err != nil {
		return false, fmt.Errorf("seed products: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO orders (customer_email, product_id, quantity, total_cents, created_at)
		SELECT 'customer' || (g.i % $2::int) || '@example.com',
		       p.id,
		       q.quantity,
		       q.quantity * p.price_cents,
		       timestamptz '2026-01-01 00:00:00+00' - g.i * interval '37 seconds'
		FROM generate_series(1, $1::int) AS g(i)
		CROSS JOIN LATERAL (SELECT 1 + g.i % 5 AS quantity) AS q
		JOIN products p ON p.id = 1 + (g.i::bigint * 7919) % $3::int`,
		size.Orders, size.Customers, size.Products); err != nil {
		return false, fmt.Errorf("seed orders: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}

	// Fresh statistics, so the planner's choices reflect the data from the start.
	if _, err := s.pool.Exec(ctx, "ANALYZE products, orders"); err != nil {
		return true, fmt.Errorf("analyze: %w", err)
	}
	return true, nil
}

// ListProducts returns one page of products in ID order.
func (s *Store) ListProducts(ctx context.Context, limit, offset int) ([]Product, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, price_cents FROM products ORDER BY id LIMIT $1 OFFSET $2`,
		limit, offset)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Product])
}

// GetProduct returns one product or ErrNotFound.
func (s *Store) GetProduct(ctx context.Context, id int64) (Product, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, price_cents FROM products WHERE id = $1`, id)
	if err != nil {
		return Product{}, err
	}
	return oneRow(pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Product]))
}

// OrdersByCustomer returns a customer's most recent orders. The orders table
// ships without an index on customer_email (scenario S1), so until
// `make demo-fix` adds one, every call scans the whole table.
func (s *Store) OrdersByCustomer(ctx context.Context, email string, limit int) ([]Order, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, customer_email, product_id, quantity, total_cents, created_at
		FROM orders
		WHERE customer_email = $1
		ORDER BY created_at DESC
		LIMIT $2`, email, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Order])
}

// CreateOrder records a purchase priced from the product table. It returns
// ErrNotFound when the product does not exist.
func (s *Store) CreateOrder(ctx context.Context, email string, productID int64, quantity int32) (Order, error) {
	rows, err := s.pool.Query(ctx, `
		INSERT INTO orders (customer_email, product_id, quantity, total_cents)
		SELECT $1::text, p.id, $3::int, $3::int * p.price_cents
		FROM products p
		WHERE p.id = $2
		RETURNING id, customer_email, product_id, quantity, total_cents, created_at`,
		email, productID, quantity)
	if err != nil {
		return Order{}, err
	}
	return oneRow(pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Order]))
}

// GetOrder returns one order or ErrNotFound.
func (s *Store) GetOrder(ctx context.Context, id int64) (Order, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, customer_email, product_id, quantity, total_cents, created_at
		FROM orders
		WHERE id = $1`, id)
	if err != nil {
		return Order{}, err
	}
	return oneRow(pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Order]))
}

func oneRow[T any](v T, err error) (T, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}
