package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"infrascope/api/internal/backend"
)

// memStore is an in-memory Store.
type memStore struct {
	users    map[string]User
	sessions map[string]storedSession
	err      error // returned by every method when set
}

type storedSession struct {
	userID    int64
	expiresAt time.Time
}

func newMemStore() *memStore {
	return &memStore{users: map[string]User{}, sessions: map[string]storedSession{}}
}

func (m *memStore) UserByName(_ context.Context, username string) (User, error) {
	if m.err != nil {
		return User{}, m.err
	}
	u, ok := m.users[username]
	if !ok {
		return User{}, backend.ErrNotFound
	}
	return u, nil
}

func (m *memStore) CreateUser(_ context.Context, username, hash string) error {
	m.users[username] = User{ID: int64(len(m.users) + 1), Username: username, PasswordHash: hash}
	return m.err
}

func (m *memStore) ReplacePassword(_ context.Context, userID int64, hash string) error {
	for name, u := range m.users {
		if u.ID == userID {
			u.PasswordHash = hash
			m.users[name] = u
		}
	}
	for token, s := range m.sessions {
		if s.userID == userID {
			delete(m.sessions, token)
		}
	}
	return m.err
}

func (m *memStore) CreateSession(_ context.Context, tokenHash []byte, userID int64, expiresAt time.Time) error {
	m.sessions[string(tokenHash)] = storedSession{userID: userID, expiresAt: expiresAt}
	return m.err
}

func (m *memStore) Session(_ context.Context, tokenHash []byte, now time.Time) (Session, error) {
	if m.err != nil {
		return Session{}, m.err
	}
	s, ok := m.sessions[string(tokenHash)]
	if !ok || !s.expiresAt.After(now) {
		return Session{}, backend.ErrNotFound
	}
	for _, u := range m.users {
		if u.ID == s.userID {
			return Session{Username: u.Username, ExpiresAt: s.expiresAt}, nil
		}
	}
	return Session{}, backend.ErrNotFound
}

func (m *memStore) DeleteSession(_ context.Context, tokenHash []byte) error {
	delete(m.sessions, string(tokenHash))
	return m.err
}

func (m *memStore) DeleteExpiredSessions(_ context.Context, now time.Time) error {
	for token, s := range m.sessions {
		if !s.expiresAt.After(now) {
			delete(m.sessions, token)
		}
	}
	return m.err
}

func newService(t *testing.T, store Store) *Service {
	t.Helper()
	s, err := NewService(store, time.Hour, bcrypt.MinCost)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return s
}

func TestBootstrap(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	svc := newService(t, store)

	if got, err := svc.Bootstrap(ctx, "admin", "first-password"); err != nil || got != AdminCreated {
		t.Fatalf("first Bootstrap = %q, %v; want %q", got, err, AdminCreated)
	}
	if got, err := svc.Bootstrap(ctx, "admin", "first-password"); err != nil || got != AdminUnchanged {
		t.Fatalf("repeat Bootstrap = %q, %v; want %q", got, err, AdminUnchanged)
	}

	token, _, err := svc.Login(ctx, "admin", "first-password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got, err := svc.Bootstrap(ctx, "admin", "second-password"); err != nil || got != AdminPasswordChanged {
		t.Fatalf("Bootstrap with a new password = %q, %v; want %q", got, err, AdminPasswordChanged)
	}
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, ErrInvalidSession) {
		t.Errorf("session survived a password change: err = %v", err)
	}
	if _, _, err := svc.Login(ctx, "admin", "first-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("old password still works: err = %v", err)
	}
	if _, _, err := svc.Login(ctx, "admin", "second-password"); err != nil {
		t.Errorf("new password rejected: %v", err)
	}
}

func TestLoginAndSessions(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	svc := newService(t, store)
	if _, err := svc.Bootstrap(ctx, "admin", "correct-horse"); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	token, expires, err := svc.Login(ctx, "admin", "correct-horse")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !expires.Equal(now.Add(time.Hour)) {
		t.Errorf("expires = %v, want %v", expires, now.Add(time.Hour))
	}
	if len(token) != 43 {
		t.Errorf("token %q has %d characters, want 43 (256 bits, base64url)", token, len(token))
	}
	if _, stored := store.sessions[token]; stored {
		t.Error("the raw token was stored; only its hash may be")
	}

	s, err := svc.Authenticate(ctx, token)
	if err != nil || s.Username != "admin" {
		t.Fatalf("Authenticate = %+v, %v", s, err)
	}

	svc.now = func() time.Time { return now.Add(time.Hour) }
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, ErrInvalidSession) {
		t.Errorf("expired session accepted: err = %v", err)
	}
}

func TestLoginRejectsBadCredentialsAlike(t *testing.T) {
	ctx := context.Background()
	svc := newService(t, newMemStore())
	if _, err := svc.Bootstrap(ctx, "admin", "correct-horse"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ user, password string }{
		{"admin", "wrong"},
		{"nobody", "correct-horse"},
		{"admin", ""},
	} {
		if _, _, err := svc.Login(ctx, c.user, c.password); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("Login(%q, %q) err = %v, want ErrInvalidCredentials", c.user, c.password, err)
		}
	}
}

func TestAuthenticateRejectsMalformedTokens(t *testing.T) {
	svc := newService(t, newMemStore())
	for _, token := range []string{"", "short", "not base64 !!!", "AAAA"} {
		if _, err := svc.Authenticate(context.Background(), token); !errors.Is(err, ErrInvalidSession) {
			t.Errorf("Authenticate(%q) err = %v, want ErrInvalidSession", token, err)
		}
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	ctx := context.Background()
	svc := newService(t, newMemStore())
	if _, err := svc.Bootstrap(ctx, "admin", "correct-horse"); err != nil {
		t.Fatal(err)
	}
	token, _, err := svc.Login(ctx, "admin", "correct-horse")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Logout(ctx, token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, ErrInvalidSession) {
		t.Errorf("session still valid after logout: err = %v", err)
	}
}

func TestDatabaseFailuresAreNotReportedAsBadCredentials(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	svc := newService(t, store)
	if _, err := svc.Bootstrap(ctx, "admin", "correct-horse"); err != nil {
		t.Fatal(err)
	}
	token, _, err := svc.Login(ctx, "admin", "correct-horse")
	if err != nil {
		t.Fatal(err)
	}

	store.err = fmt.Errorf("%w: connection refused", backend.ErrUnavailable)
	if _, _, err := svc.Login(ctx, "admin", "correct-horse"); !errors.Is(err, backend.ErrUnavailable) {
		t.Errorf("Login during an outage: err = %v, want ErrUnavailable", err)
	}
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, backend.ErrUnavailable) {
		t.Errorf("Authenticate during an outage: err = %v, want ErrUnavailable (not an invalid session)", err)
	}
}
