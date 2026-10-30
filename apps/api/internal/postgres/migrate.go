package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationLock is an arbitrary application-wide key for pg_advisory_lock.
const migrationLock = 7_468_216_301

type migration struct {
	version int
	name    string
	sql     string
}

// Migrate applies every migration in migrations/ that has not run yet, in
// version order. Each runs in its own transaction together with its
// bookkeeping row, and the whole run holds an advisory lock, so API instances
// starting at the same time cannot apply a migration twice.
func (db *DB) Migrate(ctx context.Context) error {
	pending, err := loadMigrations()
	if err != nil {
		return err
	}

	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return classify(fmt.Errorf("migrate: %w", err))
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLock); err != nil {
		return classify(fmt.Errorf("migrate: lock: %w", err))
	}
	defer func() {
		// The lock is per session; releasing it explicitly matters because
		// the pool reuses the connection.
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLock)
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    integer     PRIMARY KEY,
			name       text        NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return classify(fmt.Errorf("migrate: %w", err))
	}

	applied := map[int]bool{}
	rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return classify(fmt.Errorf("migrate: %w", err))
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return classify(fmt.Errorf("migrate: %w", err))
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return classify(fmt.Errorf("migrate: %w", err))
	}

	for _, m := range pending {
		if applied[m.version] {
			continue
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return classify(fmt.Errorf("migrate %s: %w", m.name, err))
		}
		// Without arguments pgx uses the simple protocol, which allows the
		// several statements a migration file usually contains.
		if _, err := tx.Exec(ctx, m.sql); err != nil {
			_ = tx.Rollback(ctx)
			return classify(fmt.Errorf("migrate %s: %w", m.name, err))
		}
		if _, err := tx.Exec(ctx,
			"INSERT INTO schema_migrations (version, name) VALUES ($1, $2)", m.version, m.name); err != nil {
			_ = tx.Rollback(ctx)
			return classify(fmt.Errorf("migrate %s: %w", m.name, err))
		}
		if err := tx.Commit(ctx); err != nil {
			return classify(fmt.Errorf("migrate %s: %w", m.name, err))
		}
	}
	return nil
}

// loadMigrations reads migrations/NNNN_description.sql in version order.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	seen := map[int]string{}
	for _, e := range entries {
		name := e.Name()
		prefix, _, ok := strings.Cut(name, "_")
		version, err := strconv.Atoi(prefix)
		if !ok || err != nil || !strings.HasSuffix(name, ".sql") {
			return nil, fmt.Errorf("migration %q: want NNNN_description.sql", name)
		}
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf("migrations %q and %q share version %d", other, name, version)
		}
		seen[version] = name
		body, err := fs.ReadFile(migrationFiles, "migrations/"+name)
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: version, name: name, sql: string(body)})
	}
	slices.SortFunc(out, func(a, b migration) int { return a.version - b.version })
	return out, nil
}
