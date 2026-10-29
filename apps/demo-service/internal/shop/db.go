package shop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/multitracer"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a connection pool whose statements are traced as OpenTelemetry
// spans (otelpgx) and timed by a SlowQueryTracer. otelpgx runs first, so the
// slow-query log is written in the context of the database span.
func Connect(ctx context.Context, databaseURL string, slowQuery time.Duration, logger *slog.Logger) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// The parse error would echo the URL, password included.
		return nil, errors.New("DEMO_DATABASE_URL is not a valid PostgreSQL connection URL")
	}
	cfg.ConnConfig.Tracer = multitracer.New(
		otelpgx.NewTracer(),
		&SlowQueryTracer{Threshold: slowQuery, Logger: logger},
	)

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres ping: %w", err)
	}
	if err := otelpgx.RecordStats(pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres pool metrics: %w", err)
	}
	return pool, nil
}

// SlowQueryTracer logs statements slower than Threshold at WARN. Each record
// is logged with the statement's context, so it carries the trace and span
// IDs of the database span: an investigation can go from a slow span to its
// log line and back (docs/demo-script.md, step 4).
type SlowQueryTracer struct {
	Threshold time.Duration
	Logger    *slog.Logger
}

type queryStartKey struct{}

type queryStart struct {
	at  time.Time
	sql string
}

// TraceQueryStart implements pgx.QueryTracer.
func (t *SlowQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, queryStartKey{}, queryStart{at: time.Now(), sql: data.SQL})
}

// TraceQueryEnd implements pgx.QueryTracer.
func (t *SlowQueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	start, ok := ctx.Value(queryStartKey{}).(queryStart)
	if !ok {
		return
	}
	elapsed := time.Since(start.at)
	if elapsed < t.Threshold {
		return
	}
	t.Logger.WarnContext(ctx, "slow query",
		slog.Int64("duration_ms", elapsed.Milliseconds()),
		slog.String("db.query.text", start.sql),
		slog.Bool("failed", data.Err != nil),
	)
}
