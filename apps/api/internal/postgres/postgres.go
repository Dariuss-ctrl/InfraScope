// Package postgres stores InfraScope's own state in PostgreSQL: users,
// sessions and DeployRadar's deployments now, alert rules from Phase 5.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"infrascope/api/internal/auth"
	"infrascope/api/internal/backend"
)

// DB is the API's PostgreSQL database.
type DB struct {
	pool *pgxpool.Pool
}

// Options configure Open.
type Options struct {
	// ConnectTimeout bounds each connection attempt, so an unreachable
	// database fails fast instead of hanging requests.
	ConnectTimeout time.Duration
	// Tracer, when set, traces every statement (for example otelpgx).
	Tracer pgx.QueryTracer
}

// Open connects to the database at url and checks that it answers.
func Open(ctx context.Context, url string, opts Options) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		// The parse error would echo the URL, password included.
		return nil, errors.New("postgres: invalid connection URL")
	}
	if opts.ConnectTimeout > 0 {
		cfg.ConnConfig.ConnectTimeout = opts.ConnectTimeout
	}
	cfg.ConnConfig.Tracer = opts.Tracer

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, classify(fmt.Errorf("postgres: %w", err))
	}
	db := &DB{pool: pool}
	if err := db.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return db, nil
}

// Close releases every connection.
func (db *DB) Close() {
	db.pool.Close()
}

// Ping checks that the database answers.
func (db *DB) Ping(ctx context.Context) error {
	return classify(db.pool.Ping(ctx))
}

// Pool exposes the connection pool, for instrumentation.
func (db *DB) Pool() *pgxpool.Pool {
	return db.pool
}

// UserByName implements auth.Store.
func (db *DB) UserByName(ctx context.Context, username string) (auth.User, error) {
	var u auth.User
	err := db.pool.QueryRow(ctx,
		`SELECT id, username, password_hash FROM users WHERE username = $1`, username,
	).Scan(&u.ID, &u.Username, &u.PasswordHash)
	return u, classify(err)
}

// CreateUser implements auth.Store.
func (db *DB) CreateUser(ctx context.Context, username, passwordHash string) error {
	_, err := db.pool.Exec(ctx,
		`INSERT INTO users (username, password_hash) VALUES ($1, $2)`, username, passwordHash)
	return classify(err)
}

// ReplacePassword implements auth.Store: the new hash and the revocation of
// every existing session commit together.
func (db *DB) ReplacePassword(ctx context.Context, userID int64, passwordHash string) error {
	return classify(pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`, userID, passwordHash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		_, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
		return err
	}))
}

// DeleteUsersExcept implements auth.Store. Sessions go with their user
// (ON DELETE CASCADE).
func (db *DB) DeleteUsersExcept(ctx context.Context, username string) ([]string, error) {
	rows, err := db.pool.Query(ctx, `DELETE FROM users WHERE username <> $1 RETURNING username`, username)
	if err != nil {
		return nil, classify(err)
	}
	removed, err := pgx.CollectRows(rows, pgx.RowTo[string])
	return removed, classify(err)
}

// CreateSession implements auth.Store.
func (db *DB) CreateSession(ctx context.Context, tokenHash []byte, userID int64, expiresAt time.Time) error {
	_, err := db.pool.Exec(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		tokenHash, userID, expiresAt)
	return classify(err)
}

// Session implements auth.Store.
func (db *DB) Session(ctx context.Context, tokenHash []byte, now time.Time) (auth.Session, error) {
	var s auth.Session
	err := db.pool.QueryRow(ctx, `
		SELECT u.username, s.expires_at
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > $2`, tokenHash, now,
	).Scan(&s.Username, &s.ExpiresAt)
	return s, classify(err)
}

// DeleteSession implements auth.Store.
func (db *DB) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := db.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return classify(err)
}

// DeleteExpiredSessions implements auth.Store.
func (db *DB) DeleteExpiredSessions(ctx context.Context, now time.Time) error {
	_, err := db.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= $1`, now)
	return classify(err)
}

// classify wraps driver errors in the backend failure kinds.
func classify(err error) error {
	var (
		connectErr *pgconn.ConnectError
		netErr     net.Error
		pgErr      *pgconn.PgError
	)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("%w: %w", backend.ErrNotFound, err)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%w: postgres: %w", backend.ErrTimeout, err)
	case errors.As(err, &pgErr) && serverUnavailable(pgErr.Code),
		errors.As(err, &connectErr), errors.As(err, &netErr), pgconn.SafeToRetry(err),
		// The server closed a connection that was in use, for example while
		// shutting down or restarting.
		errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, net.ErrClosed):
		return fmt.Errorf("%w: postgres: %w", backend.ErrUnavailable, err)
	}
	return err
}

// serverUnavailable reports SQLSTATEs that mean the server cannot serve
// right now, rather than that the statement was wrong.
func serverUnavailable(code string) bool {
	switch code {
	case "57P01", "57P02", "57P03", // admin shutdown, crash shutdown, cannot connect now
		"53300": // too many connections
		return true
	}
	return strings.HasPrefix(code, "08") // connection exception class
}
