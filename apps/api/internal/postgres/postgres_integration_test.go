//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"infrascope/api/internal/auth"
	"infrascope/api/internal/backend"
	"infrascope/api/internal/testenv"
)

var pg *testenv.Postgres

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()
	var err error
	if pg, err = testenv.StartPostgres(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "start postgres:", err)
		return 1
	}
	defer func() { _ = pg.Container.Terminate(ctx) }()
	return m.Run()
}

func open(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), pg.URL, Options{ConnectTimeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

func TestMigrationsAreIdempotentAndSafeToRunConcurrently(t *testing.T) {
	ctx := context.Background()
	dbs := []*DB{open(t), open(t), open(t)}

	var wg sync.WaitGroup
	errs := make([]error, len(dbs))
	for i, db := range dbs {
		wg.Go(func() { errs[i] = db.Migrate(ctx) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent Migrate %d: %v", i, err)
		}
	}
	if err := dbs[0].Migrate(ctx); err != nil {
		t.Fatalf("repeat Migrate: %v", err)
	}

	want, _ := loadMigrations()
	var applied int
	if err := dbs[0].pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != len(want) {
		t.Errorf("schema_migrations has %d rows, want %d", applied, len(want))
	}
}

func TestUsersAndSessions(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateUser(ctx, "store-user", "hash-1"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := db.CreateUser(ctx, "store-user", "hash-2"); err == nil || errors.Is(err, backend.ErrUnavailable) {
		t.Errorf("duplicate username: err = %v, want a constraint error (not an outage)", err)
	}
	u, err := db.UserByName(ctx, "store-user")
	if err != nil || u.PasswordHash != "hash-1" {
		t.Fatalf("UserByName = %+v, %v", u, err)
	}
	if _, err := db.UserByName(ctx, "nobody"); !errors.Is(err, backend.ErrNotFound) {
		t.Errorf("unknown user: err = %v, want ErrNotFound", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	live, expired := hash("live"), hash("expired")
	if err := db.CreateSession(ctx, live, u.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(ctx, expired, u.ID, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if s, err := db.Session(ctx, live, now); err != nil || s.Username != "store-user" || !s.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Errorf("Session(live) = %+v, %v", s, err)
	}
	if _, err := db.Session(ctx, expired, now); !errors.Is(err, backend.ErrNotFound) {
		t.Errorf("Session(expired): err = %v, want ErrNotFound", err)
	}

	if err := db.DeleteExpiredSessions(ctx, now); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := db.pool.QueryRow(ctx, "SELECT count(*) FROM sessions WHERE user_id = $1", u.ID).Scan(&left); err != nil || left != 1 {
		t.Errorf("after pruning, %d sessions left (%v), want only the live one", left, err)
	}

	if err := db.ReplacePassword(ctx, u.ID, "hash-3"); err != nil {
		t.Fatalf("ReplacePassword: %v", err)
	}
	if _, err := db.Session(ctx, live, now); !errors.Is(err, backend.ErrNotFound) {
		t.Errorf("session survived a password change: err = %v", err)
	}
	if err := db.ReplacePassword(ctx, 999_999, "x"); !errors.Is(err, backend.ErrNotFound) {
		t.Errorf("ReplacePassword(unknown user): err = %v, want ErrNotFound", err)
	}
}

func TestAuthServiceAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	db := open(t)
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	svc, err := auth.NewService(db, time.Second, bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Bootstrap(ctx, "service-admin", "correct-horse"); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	token, _, err := svc.Login(ctx, "service-admin", "correct-horse")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if s, err := svc.Authenticate(ctx, token); err != nil || s.Username != "service-admin" {
		t.Fatalf("Authenticate = %+v, %v", s, err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, auth.ErrInvalidSession) {
		t.Errorf("expired session: err = %v, want ErrInvalidSession", err)
	}
}

func TestUnreachableDatabaseIsUnavailable(t *testing.T) {
	ctx := context.Background()
	start := time.Now()
	_, err := Open(ctx, "postgres://u:p@127.0.0.1:1/x?sslmode=disable", Options{ConnectTimeout: 2 * time.Second})
	if !errors.Is(err, backend.ErrUnavailable) {
		t.Errorf("closed port: err = %v, want ErrUnavailable", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("connection failure took %v; ConnectTimeout should bound it", elapsed)
	}

	// A server that goes away while connected.
	stopped, err := testenv.StartPostgres(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stopped.Container.Terminate(ctx) })
	db, err := Open(ctx, stopped.URL, Options{ConnectTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	timeout := 5 * time.Second
	if err := stopped.Container.Stop(ctx, &timeout); err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(ctx); !errors.Is(err, backend.ErrUnavailable) {
		t.Errorf("stopped server: err = %v, want ErrUnavailable", err)
	}
}

func hash(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}
